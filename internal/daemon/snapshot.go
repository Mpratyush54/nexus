package daemon

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"central-memory/internal/security"
	"central-memory/internal/store"
)

const maxSnapshotDiffBytes = 512 * 1024
const maxUntrackedFileBytes = 100 * 1024

// CollectSnapshot builds a SessionSnapshot for a conversation across harnesses.
func CollectSnapshot(root, conversationID, harness, machineID string) (*store.SessionSnapshot, error) {
	harness = normalizeHarness(harness)
	layout, err := ResolveSnapshotLayout(harness, conversationID, root)
	if err != nil {
		return nil, err
	}
	if layout.TranscriptFile == "" && layout.ArtifactDir == "" {
		return nil, fmt.Errorf("snapshot: harness %q has no layout for %s", harness, conversationID)
	}

	// Streamed line by line: a live chat's transcript can be tens of
	// megabytes and is re-packed every few minutes (snapshot_stream.go).
	src, err := openSnapshotTranscript(layout, root)
	if err != nil {
		return nil, fmt.Errorf("snapshot: read transcript: %w", err)
	}
	gzTranscript, turnCount, err := gzipSnapshotTranscript(src)
	_ = src.Close()
	if err != nil {
		return nil, fmt.Errorf("snapshot: read transcript: %w", err)
	}

	artifacts, err := tarGzipArtifacts(layout)
	if err != nil {
		log.Printf("daemon: snapshot artifacts: %v", err)
		artifacts = nil
	}

	branch, commit, dirty, _, _ := GitStatusCached(root)
	diff, err := snapshotDiff(root)
	if err != nil {
		return nil, err
	}
	gzDiff, diffSize, diffTrunc := diff.gz, diff.size, diff.trunc

	return &store.SessionSnapshot{
		Harness:           harness,
		ConversationID:    conversationID,
		TurnCount:         turnCount,
		GitBranch:         branch,
		GitCommit:         commit,
		GitDirty:          dirty,
		UncommittedDiff:   gzDiff,
		DiffSizeBytes:     diffSize,
		DiffTruncated:     diffTrunc,
		TranscriptPayload: gzTranscript,
		ArtifactsBundle:   artifacts,
		SourceMachineID:   machineID,
	}, nil
}

// snapshotTranscriptStamp identifies the transcript a snapshot of
// conversationID would pack (size and modification time), or "" when it
// cannot tell (no transcript file, SQLite-only harnesses).
func snapshotTranscriptStamp(root, conversationID, harness string) string {
	layout, err := ResolveSnapshotLayout(normalizeHarness(harness), conversationID, root)
	if err != nil || layout.TranscriptFile == "" || layout.SQLiteSource != "" {
		return ""
	}
	path := layout.TranscriptFile
	if layout.Harness == "antigravity" {
		if full := filepath.Join(filepath.Dir(path), "transcript_full.jsonl"); isRegularFile(full) {
			path = full
		}
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return ""
	}
	return path + "\x00" + strconv.FormatInt(st.Size(), 10) + ":" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
}

// snapshotDiffBundle is the redacted, gzipped uncommitted diff of a snapshot.
type snapshotDiffBundle struct {
	gz    []byte
	size  int
	trunc bool
}

// snapshotDiff builds the uncommitted-diff bundle, reusing the previous one
// while the workspace's git state and sources are unchanged
// (git_state_cache.go): `git diff`, `git ls-files` and the untracked-file
// reads run only after files changed.
func snapshotDiff(root string) (snapshotDiffBundle, error) {
	return cachedGit(root, "snapshot-diff", func() (snapshotDiffBundle, error) {
		diffText, diffTrunc, err := collectUncommittedDiff(root)
		if err != nil {
			log.Printf("daemon: snapshot git diff: %v", err)
		}
		if security.ContainsSecret([]byte(diffText)) {
			if redacted, ok := RedactSecrets(diffText); ok {
				diffText = redacted
			}
		}
		gz, err := gzipBytes([]byte(diffText))
		if err != nil {
			return snapshotDiffBundle{}, err
		}
		return snapshotDiffBundle{gz: gz, size: len(diffText), trunc: diffTrunc}, nil
	})
}

func collectUncommittedDiff(root string) (string, bool, error) {
	if strings.TrimSpace(root) == "" {
		return "", false, nil
	}
	diff, err := runGit(root, "diff", "HEAD", "--", ".", ":!vendor", ":!node_modules", ":!*.lock", ":!dist")
	if err != nil {
		// empty repo / no commits
		diff = ""
	}
	var b strings.Builder
	b.WriteString(diff)
	untracked, _ := runGit(root, "ls-files", "--others", "--exclude-standard")
	for _, rel := range strings.Split(untracked, "\n") {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		full := filepath.Join(root, rel)
		st, err := os.Stat(full)
		if err != nil || st.IsDir() || st.Size() > maxUntrackedFileBytes {
			continue
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		b.WriteString("\n--- /dev/null\n+++ b/")
		b.WriteString(filepath.ToSlash(rel))
		b.WriteByte('\n')
		for _, line := range strings.Split(string(raw), "\n") {
			b.WriteString("+")
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	out := b.String()
	trunc := false
	if len(out) > maxSnapshotDiffBytes {
		out = out[:maxSnapshotDiffBytes]
		trunc = true
	}
	return out, trunc, nil
}

func gzipBytes(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// tarGzipArtifacts packs harness brain/custom state. Transcripts are stored
// separately in TranscriptPayload — skip them and ephemeral dirs.
func tarGzipArtifacts(layout SnapshotLayout) ([]byte, error) {
	brainDir := layout.ArtifactDir
	if brainDir == "" {
		return nil, nil // e.g. a sub-agent run: no artifacts of its own
	}
	st, err := os.Stat(brainDir)
	if err != nil || !st.IsDir() {
		return nil, err
	}
	skipPrefixes := artifactSkipPrefixes(layout)
	transcriptRel := ""
	if layout.TranscriptFile != "" {
		if rel, err := filepath.Rel(brainDir, layout.TranscriptFile); err == nil && !strings.HasPrefix(rel, "..") {
			transcriptRel = filepath.ToSlash(rel)
		}
	}

	// Pass 1: list the files to pack from directory entries only (no reads).
	type artifactFile struct {
		path, rel string
		size      int64
		mod       int64
	}
	var files []artifactFile
	var fp strings.Builder
	_ = filepath.WalkDir(brainDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(brainDir, path)
		if err != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if relSlash == "." {
			return nil
		}
		for _, skip := range skipPrefixes {
			if relSlash == skip || strings.HasPrefix(relSlash, skip+"/") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if transcriptRel != "" && relSlash == transcriptRel {
			return nil
		}
		if otherClaudeSession(layout, relSlash) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".md", ".txt", ".json", ".jsonl", ".sh", ".py", ".go", ".ts", ".tsx", ".js", ".yaml", ".yml", ".toml":
		default:
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		// Cap individual artifact files (avoid packing huge jsonl dumps twice).
		if info.Size() > maxUntrackedFileBytes*20 {
			return nil
		}
		f := artifactFile{path: path, rel: relSlash, size: info.Size(), mod: info.ModTime().UnixNano()}
		files = append(files, f)
		fmt.Fprintf(&fp, "%s\x00%d\x00%d\n", f.rel, f.size, f.mod)
		return nil
	})
	if p := strings.TrimSpace(layout.SQLiteSource); p != "" {
		fmt.Fprintf(&fp, "sqlite\x00%s\x00%s\n", p, statStamp(p))
	}
	cacheKey := layout.Harness + "\x00" + layout.ConversationID + "\x00" + brainDir
	fingerprint := fp.String()
	if cached, ok := artifactCacheGet(cacheKey, fingerprint); ok {
		return cached, nil
	}

	// Pass 2: pack them.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		raw, rerr := os.ReadFile(f.path)
		if rerr != nil {
			continue
		}
		hdr := &tar.Header{Name: f.rel, Mode: 0o644, Size: int64(len(raw))}
		if err = tw.WriteHeader(hdr); err != nil {
			break
		}
		if _, err = tw.Write(raw); err != nil {
			break
		}
	}
	if err == nil {
		if aerr := appendSmallSQLiteArtifact(tw, layout, brainDir); aerr != nil {
			err = aerr
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	if err != nil {
		return nil, err
	}
	out := buf.Bytes()
	artifactCachePut(cacheKey, fingerprint, out)
	return out, nil
}

// otherClaudeSession reports whether relSlash (relative to a Claude project
// folder) belongs to a different conversation: <uuid>.jsonl transcripts and
// <uuid>/ sub-agent and tool-result folders of other chats. They are not
// part of this chat's state; packing them read and gzipped every chat of the
// project on each snapshot, and a restore would write them all back.
func otherClaudeSession(layout SnapshotLayout, relSlash string) bool {
	if layout.Harness != "claude" {
		return false
	}
	first := relSlash
	if i := strings.IndexByte(first, '/'); i >= 0 {
		first = first[:i]
	}
	stem := strings.TrimSuffix(first, ".jsonl")
	return looksLikeUUID(stem) && !strings.EqualFold(stem, layout.ConversationID)
}

// artifactCache keeps the last artifacts bundle per conversation, reused
// while the packed files' names, sizes and modification times are unchanged.
var artifactCache = struct {
	sync.Mutex
	m     map[string]artifactCacheEntry
	bytes int
}{m: map[string]artifactCacheEntry{}}

type artifactCacheEntry struct {
	fingerprint string
	bundle      []byte
}

// The cache is bounded by count and by bytes: a bundle can be megabytes
// (sub-agent transcripts), and 32 of them stayed on the heap for good.
const (
	artifactCacheMax       = 32
	artifactCacheMaxBytes  = 16 << 20
	artifactCacheItemBytes = 4 << 20
)

func artifactCacheGet(key, fingerprint string) ([]byte, bool) {
	artifactCache.Lock()
	defer artifactCache.Unlock()
	e, ok := artifactCache.m[key]
	if !ok || e.fingerprint != fingerprint {
		return nil, false
	}
	return e.bundle, true
}

func artifactCachePut(key, fingerprint string, bundle []byte) {
	artifactCache.Lock()
	defer artifactCache.Unlock()
	if old, ok := artifactCache.m[key]; ok {
		artifactCache.bytes -= len(old.bundle)
		delete(artifactCache.m, key)
	}
	if len(bundle) > artifactCacheItemBytes {
		return
	}
	if len(artifactCache.m) >= artifactCacheMax || artifactCache.bytes+len(bundle) > artifactCacheMaxBytes {
		artifactCache.m = map[string]artifactCacheEntry{}
		artifactCache.bytes = 0
	}
	artifactCache.m[key] = artifactCacheEntry{fingerprint: fingerprint, bundle: bundle}
	artifactCache.bytes += len(bundle)
}

func artifactSkipPrefixes(layout SnapshotLayout) []string {
	switch layout.Harness {
	case "antigravity":
		return []string{".system_generated"}
	case "cursor":
		// Transcripts packed separately; terminals are ephemeral local state.
		return []string{"agent-transcripts", "terminals"}
	case "opencode":
		// The global DB is read separately. Its snapshots and tool outputs can
		// contain gigabytes of unrelated projects and must never enter a chat.
		return []string{"snapshot", "tool-output", "log", "storage", "repos"}
	default:
		return nil
	}
}

// ungzipBytes is used by restore.
func ungzipBytes(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}
