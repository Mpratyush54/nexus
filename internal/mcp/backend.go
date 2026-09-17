// Backends for the MCP server (issue #7).
//
// This file holds the default backend implementations behind the narrow
// interfaces in server.go. All are stdlib-only plus reuse of internal/store
// (types + rerank math) and internal/daemon (sandbox + git helpers) —
// nothing here edits those packages:
//
//   - HashEmbed: deterministic bag-of-words embedding, dim 1536 to match the
//     memory_items vector(1536) column, so it can flow through
//     store.FormatEmbedding/SQL unchanged until real LLM embeddings land
//     (issues #8/#10).
//   - InMemoryMemoryStore: MemoryBackend + MemoryWriter test double that
//     mirrors the SQL guards (CONFIRMED, confidence > 0.3) and reranks with
//     store.Rank (0.7/0.2/0.1 blend).
//   - InMemoryEpisodeStore: EpisodeBackend test double with plan §2.4
//     filter semantics (error pattern, file involvement, status, narrative
//     substring).
//   - StaticWorkspaceProvider: canned workspace_info for tests.
//   - DaemonWorkspaceProvider: live branch/commit/dirty via daemon.Git*.
//   - DaemonFileProxy: file_read/file_write via
//     daemon.ReadFileSandboxed/WriteFileSandboxed, so traversal, secret and
//     1MB enforcement is reused, never reimplemented.
package mcp

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"sync"
	"time"

	"central-memory/internal/daemon"
	"central-memory/internal/store"
)

// EmbedDim matches the memory_items embedding vector(1536) column (plan
// §1.1) so default embeddings are SQL-compatible from day one.
const EmbedDim = 1536

// HashEmbed is a deterministic stdlib-only bag-of-words embedding:
// lowercase alphanumeric tokens are hashed (FNV-1a) into 1536 buckets and
// L2-normalized. It gives meaningful cosine ordering for offline/local use
// and keeps the SQL path type-correct; replace with LLM embeddings via
// Config.Embed when the server/processor wiring lands (issues #8/#10).
func HashEmbed(text string) []float32 {
	vec := make([]float32, EmbedDim)
	for _, tok := range tokenize(text) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tok))
		vec[h.Sum32()%EmbedDim]++
	}
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return vec
	}
	for i := range vec {
		vec[i] /= float32(norm)
	}
	return vec
}

func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
}

// ---------------------------------------------------------------------------
// In-memory memory store
// ---------------------------------------------------------------------------

// InMemoryMemoryStore is a MemoryBackend + MemoryWriter test double. Writes
// land as PROPOSED (mirroring the MCP contract); SearchMemories mirrors the
// SQL guards (CONFIRMED, confidence > 0.3) and reranks with store.Rank.
// Tests promote items with Confirm, exercising the real lifecycle.
type InMemoryMemoryStore struct {
	mu    sync.Mutex
	items []store.MemoryItem
	next  int64
}

// NewInMemoryMemoryStore builds an empty store.
func NewInMemoryMemoryStore() *InMemoryMemoryStore { return &InMemoryMemoryStore{} }

// WriteMemory implements MemoryWriter: validates (20–2000 chars, same rules
// as the handler) and stores the item as PROPOSED.
func (m *InMemoryMemoryStore) WriteMemory(_ context.Context, in MemoryWriteInput) (WrittenMemory, error) {
	if err := ValidateMemoryWrite(in); err != nil {
		return WrittenMemory{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	now := time.Now()
	it := store.MemoryItem{
		ID:             fmt.Sprintf("mem-%d", m.next),
		Key:            in.Key,
		Content:        in.Content,
		ContextSnippet: in.ContextSnippet,
		Level:          in.Level,
		Scope:          in.Scope,
		Tags:           append([]string(nil), in.Tags...),
		Confidence:     1.0,
		Status:         store.StatusProposed,
		Source:         "mcp",
		LastUsedAt:     now,
		CreatedAt:      now,
	}
	m.items = append(m.items, it)
	return WrittenMemory{ID: it.ID, Status: it.Status}, nil
}

// Confirm flips an item to CONFIRMED (the auto-confirm timers of issue #10
// collapsed into one test helper).
func (m *InMemoryMemoryStore) Confirm(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.items {
		if m.items[i].ID == id {
			m.items[i].Status = store.StatusConfirmed
			return true
		}
	}
	return false
}

// SearchMemories implements MemoryBackend with the plan §1.5 guards
// (CONFIRMED, confidence > 0.3, tag/key/level filters, LIMIT clamp) and the
// 0.7/0.2/0.1 rerank via store.Rank. Similarities are tag/recency-driven
// (this double stores no vectors); the SQL path supplies cosine similarity.
func (m *InMemoryMemoryStore) SearchMemories(_ context.Context, q store.SearchQuery) ([]store.RankedMemory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var items []store.MemoryItem
	for _, it := range m.items {
		if it.Status != store.StatusConfirmed || it.Confidence <= store.MinSearchConfidence {
			continue
		}
		if len(q.Tags) > 0 && store.TagMatchScore(it.Tags, q.Tags) == 0 {
			continue
		}
		if q.Key != "" && it.Key != q.Key {
			continue
		}
		if q.Level != "" && it.Level != q.Level {
			continue
		}
		items = append(items, it)
	}
	ranked := store.Rank(items, nil, q.Tags, time.Now())
	if n := q.LimitOrDefault(); len(ranked) > n {
		ranked = ranked[:n]
	}
	return ranked, nil
}

// ---------------------------------------------------------------------------
// In-memory episode store
// ---------------------------------------------------------------------------

// InMemoryEpisodeStore is an EpisodeBackend test double with plan §2.4
// filter semantics.
type InMemoryEpisodeStore struct {
	mu       sync.Mutex
	episodes []Episode
	next     int64
}

// NewInMemoryEpisodeStore builds an empty store.
func NewInMemoryEpisodeStore() *InMemoryEpisodeStore { return &InMemoryEpisodeStore{} }

// ReportEpisode implements EpisodeBackend: validates and opens the episode
// with status OPEN.
func (e *InMemoryEpisodeStore) ReportEpisode(ctx context.Context, in EpisodeReportInput) (Episode, error) {
	_ = ctx
	if err := ValidateEpisodeReport(in); err != nil {
		return Episode{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.next++
	ep := Episode{
		ID:      fmt.Sprintf("ep-%d", e.next),
		Title:   in.Title,
		Type:    in.Type,
		Trigger: in.Trigger,
		Status:  "OPEN",
		Tags:    append([]string(nil), in.Tags...),
	}
	e.episodes = append(e.episodes, ep)
	return ep, nil
}

// SearchEpisodes implements EpisodeBackend: every non-empty filter field
// must match (AND). Query matches narrative substrings, error_pattern
// matches ErrorPatterns entries or the trigger text, file matches
// FilesInvolved exactly, status matches exactly. All text matching is
// case-insensitive.
func (e *InMemoryEpisodeStore) SearchEpisodes(_ context.Context, f EpisodeFilter) ([]Episode, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []Episode
	for _, ep := range e.episodes {
		if f.Status != "" && ep.Status != f.Status {
			continue
		}
		if f.ErrorPattern != "" && !matchErrorPattern(ep, f.ErrorPattern) {
			continue
		}
		if f.File != "" && !matchFile(ep, f.File) {
			continue
		}
		if f.Query != "" && !matchNarrative(ep, f.Query) {
			continue
		}
		out = append(out, ep)
	}
	return out, nil
}

func matchErrorPattern(ep Episode, pat string) bool {
	pat = strings.ToLower(pat)
	for _, p := range ep.ErrorPatterns {
		if strings.Contains(strings.ToLower(p), pat) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(ep.Trigger), pat)
}

func matchFile(ep Episode, file string) bool {
	for _, f := range ep.FilesInvolved {
		if f == file {
			return true
		}
	}
	return false
}

func matchNarrative(ep Episode, q string) bool {
	hay := strings.ToLower(strings.Join([]string{
		ep.Title, ep.Trigger, ep.Investigation, ep.RootCause, ep.Resolution, ep.Verification,
	}, "\n"))
	for _, tok := range tokenize(q) {
		if strings.Contains(hay, tok) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Workspace providers
// ---------------------------------------------------------------------------

// StaticWorkspaceProvider returns canned workspace_info (tests, local mode).
type StaticWorkspaceProvider struct {
	Info WorkspaceInfo
}

// WorkspaceInfo implements WorkspaceProvider.
func (p StaticWorkspaceProvider) WorkspaceInfo(_ context.Context) (WorkspaceInfo, error) {
	return p.Info, nil
}

// DaemonWorkspaceProvider derives branch/commit/dirty live from the
// workspace root via the daemon's git helpers (reused, not reimplemented).
// Git failures degrade to empty strings / false — heartbeat/register treat
// them as unknown, never fatal (same contract as daemon.go).
type DaemonWorkspaceProvider struct {
	Root    string
	Project string
}

// WorkspaceInfo implements WorkspaceProvider.
func (p DaemonWorkspaceProvider) WorkspaceInfo(_ context.Context) (WorkspaceInfo, error) {
	branch, _ := daemon.GitBranch(p.Root)
	commit, _ := daemon.GitCommit(p.Root)
	dirty, _ := daemon.GitDirty(p.Root)
	return WorkspaceInfo{
		Project: p.Project,
		Branch:  branch,
		Commit:  commit,
		IsDirty: dirty,
		Path:    p.Root,
	}, nil
}

// ---------------------------------------------------------------------------
// File proxy
// ---------------------------------------------------------------------------

// DaemonFileProxy routes MCP file_read/file_write through the daemon's
// sandboxed helpers, so traversal escapes, secret-pattern hits and the 1MB
// read cap are enforced by the exact code the daemon serves — the MCP layer
// adds no parallel sandbox of its own. Daemon sentinel errors
// (ErrTraversal, ErrSecretHit) propagate unwrapped so Server.Call can map
// them with errors.Is.
type DaemonFileProxy struct {
	Root string
}

// ReadFile implements FileBackend.
func (p DaemonFileProxy) ReadFile(_ context.Context, path string) (string, error) {
	data, err := daemon.ReadFileSandboxed(p.Root, path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WriteFile implements FileBackend.
func (p DaemonFileProxy) WriteFile(_ context.Context, path, content string) error {
	return daemon.WriteFileSandboxed(p.Root, path, []byte(content))
}
