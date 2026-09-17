// Package store provides Postgres data access for central-memory.
//
// memory.go implements issue #6 (plan §§1.5–1.7): the memory-item search
// seam — types mirroring the memory_items table, a pgvector cosine-search
// SQL builder, and pure DB-free rerank math:
//
//	final = similarity*0.7 + tag_match*0.2 + recency*0.1
//
// DB OWNERSHIP NOTE (parallel-agent constraint): internal/store/db.go,
// projects.go and workspaces.go are owned by issues #1/#2 and are NOT
// touched here. This file defines the minimal Querier/Rows interfaces it
// needs; the pool owner wires *pgxpool.Pool behind Querier with a thin
// adapter (pgx rows already satisfy Rows). See ADR-006.
package store

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Memory levels (CHECK constraint in migrations/001, plan §1.1).
const (
	LevelOrganization = "organization"
	LevelProject      = "project"
	LevelPersonal     = "personal"
	LevelSession      = "session"
)

// Lifecycle states (CHECK constraint in migrations/001, plan §1.1).
const (
	StatusProposed   = "PROPOSED"
	StatusConfirmed  = "CONFIRMED"
	StatusRejected   = "REJECTED"
	StatusSuperseded = "SUPERSEDED"
)

// Search guards from plan §1.5: only confirmed, non-stale memories compete.
const (
	MinSearchConfidence = 0.3
	DefaultSearchLimit  = 20
	MaxSearchLimit      = 100
)

// Rerank weights from plan §1.5.
const (
	WeightSimilarity = 0.7
	WeightTagMatch   = 0.2
	WeightRecency    = 0.1
)

// DecayBase/DecayWindowDays mirror the confidence-decay curve in plan §1.7
// so recency scoring and decay share one time constant (see ADR-006).
const (
	DecayBase       = 0.95
	DecayWindowDays = 30.0
)

// MemoryItem mirrors a memory_items row (plan §1.1). Embedding is
// intentionally NOT selected: search returns the similarity scalar, so this
// file stays stdlib-only (no pgvector-go) and cannot break `go build` for
// the agents owning db.go/projects.go/workspaces.go.
type MemoryItem struct {
	ID             string
	ProjectID      string
	UserID         string
	SessionID      string
	OrgID          string
	Key            string
	Content        string
	ContextSnippet string
	Level          string
	Scope          string
	Tags           []string
	Confidence     float64
	Status         string
	Source         string
	UseCount       int
	LastUsedAt     time.Time // COALESCE(last_used_at, created_at), see BuildSearchSQL
	CreatedAt      time.Time
}

// SearchQuery scopes one semantic search (plan §1.5).
type SearchQuery struct {
	ProjectID      string
	QueryEmbedding []float32
	Tags           []string // optional: exact tag boost/filter
	Key            string   // optional: exact key filter
	Level          string   // optional: level filter
	Limit          int      // optional: defaults to DefaultSearchLimit, capped at MaxSearchLimit
}

// RankedMemory pairs an item with its interpretable search scores.
type RankedMemory struct {
	Item       MemoryItem
	Similarity float64 // cosine similarity of query embedding vs item
	TagScore   float64 // fraction of query tags present on the item
	Recency    float64 // 0..1 freshness of LastUsedAt
	Score      float64 // final rerank score
}

// LimitOrDefault clamps Limit into [1, MaxSearchLimit].
func (q SearchQuery) LimitOrDefault() int {
	if q.Limit <= 0 {
		return DefaultSearchLimit
	}
	if q.Limit > MaxSearchLimit {
		return MaxSearchLimit
	}
	return q.Limit
}

// FormatEmbedding renders a vector in pgvector text-input format
// ("[0.1,0.2,...]") so it can be passed as a plain query arg — no
// pgvector-go dependency required to run a search.
func FormatEmbedding(vec []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(v), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// BuildSearchSQL renders the plan §1.5 vector search: cosine ordering via
// `embedding <=> $2`, hard guards `status = 'CONFIRMED'` and
// `confidence > 0.3`, optional tag/key/level filters, LIMIT clamp.
// Placeholders are numbered sequentially ($1..$N); the embedding arg is the
// FormatEmbedding string so any driver works.
func BuildSearchSQL(q SearchQuery) (string, []any) {
	args := []any{q.ProjectID, FormatEmbedding(q.QueryEmbedding)}
	var b strings.Builder
	b.WriteString(`SELECT id, key, content, level, scope, confidence, ` +
		`COALESCE(tags, '{}') AS tags, COALESCE(context_snippet, '') AS context_snippet, ` +
		`COALESCE(last_used_at, created_at) AS effective_used, created_at, ` +
		`1 - (embedding <=> $2) AS similarity ` +
		`FROM memory_items ` +
		`WHERE project_id = $1 ` +
		`AND status = 'CONFIRMED' ` +
		`AND confidence > 0.3`)
	next := 3
	if len(q.Tags) > 0 {
		fmt.Fprintf(&b, ` AND tags && $%d`, next)
		args = append(args, q.Tags)
		next++
	}
	if q.Key != "" {
		fmt.Fprintf(&b, ` AND key = $%d`, next)
		args = append(args, q.Key)
		next++
	}
	if q.Level != "" {
		fmt.Fprintf(&b, ` AND level = $%d`, next)
		args = append(args, q.Level)
		next++
	}
	fmt.Fprintf(&b, ` ORDER BY embedding <=> $2 LIMIT $%d`, next)
	args = append(args, q.LimitOrDefault())
	return b.String(), args
}

// CosineSimilarity is cosine similarity in [-1, 1]. It returns 0 for empty,
// mismatched, or zero-norm inputs (never NaN) so Rank stays total.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// TagMatchScore is |itemTags ∩ queryTags| / |queryTags|, 0 when the query
// carries no tags (no boost without signal).
func TagMatchScore(itemTags, queryTags []string) float64 {
	if len(queryTags) == 0 {
		return 0
	}
	set := make(map[string]struct{}, len(itemTags))
	for _, t := range itemTags {
		set[t] = struct{}{}
	}
	hits := 0
	for _, t := range queryTags {
		if _, ok := set[t]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(queryTags))
}

// RecencyScore maps freshness to 0..1 on the same decay curve as plan §1.7
// (0.95^(days/30)): fresh ≈ 1, 90d ≈ 0.86, 180d ≈ 0.74. A zero timestamp
// means "unknown age" and scores 1 so fixtures/legacy rows are not
// penalized (Search always COALESCEs to created_at, so this only triggers
// for hand-built items).
func RecencyScore(lastUsed, now time.Time) float64 {
	if lastUsed.IsZero() {
		return 1
	}
	days := now.Sub(lastUsed).Hours() / 24
	if days < 0 {
		days = 0
	}
	return math.Pow(DecayBase, days/DecayWindowDays)
}

// RerankScore is the plan §1.5 blend: similarity*0.7 + tag*0.2 + recency*0.1.
func RerankScore(similarity, tagMatch, recency float64) float64 {
	return similarity*WeightSimilarity + tagMatch*WeightTagMatch + recency*WeightRecency
}

// Rank is the pure, DB-free rerank: it scores every item and sorts
// descending by Score (ties broken by Key for determinism). similarities[i]
// aligns with items[i]; short slices read as 0. now anchors recency — pass
// a fixed clock in tests.
func Rank(items []MemoryItem, similarities []float64, queryTags []string, now time.Time) []RankedMemory {
	out := make([]RankedMemory, len(items))
	for i, it := range items {
		sim := 0.0
		if i < len(similarities) {
			sim = similarities[i]
		}
		tag := TagMatchScore(it.Tags, queryTags)
		rec := RecencyScore(it.LastUsedAt, now)
		out[i] = RankedMemory{
			Item:       it,
			Similarity: sim,
			TagScore:   tag,
			Recency:    rec,
			Score:      RerankScore(sim, tag, rec),
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Score == out[b].Score {
			return out[a].Item.Key < out[b].Item.Key
		}
		return out[a].Score > out[b].Score
	})
	return out
}

// Rows is the minimal result-set surface Search needs. pgx rows satisfy it
// method-for-method, so the pool owner (issue #2) needs no adapter here.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

// Querier is the minimal query surface Search needs (see Rows). The pool
// owner exposes Query on their pool/transaction type; Search never imports
// a driver, keeping this file stdlib-only.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
}

// Search runs the plan §1.5 vector search through db and reranks in memory.
// The SQL pre-filters (CONFIRMED, confidence > 0.3, top-LIMIT by cosine
// distance); Rank then applies the 0.7/0.2/0.1 blend. Stored-base
// confidence is filtered in SQL; time decay is applied at serve time by
// context.EffectiveConfidence (issue #6 split; archival flagging is a
// follow-up for the Memory Processor, issue #10).
func Search(ctx context.Context, db Querier, q SearchQuery) ([]RankedMemory, error) {
	query, args := BuildSearchSQL(q)
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: memory search query: %w", err)
	}
	defer rows.Close()
	var items []MemoryItem
	var sims []float64
	for rows.Next() {
		var it MemoryItem
		var sim float64
		if err := rows.Scan(
			&it.ID, &it.Key, &it.Content, &it.Level, &it.Scope,
			&it.Confidence, &it.Tags, &it.ContextSnippet,
			&it.LastUsedAt, &it.CreatedAt, &sim,
		); err != nil {
			return nil, fmt.Errorf("store: memory search scan: %w", err)
		}
		it.ProjectID = q.ProjectID
		items = append(items, it)
		sims = append(sims, sim)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: memory search rows: %w", err)
	}
	return Rank(items, sims, q.Tags, time.Now()), nil
}
