package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBuildSearchSQLGuards(t *testing.T) {
	q := SearchQuery{ProjectID: "p1", QueryEmbedding: []float32{0.1, 0.2, 0.3}}
	sql, args := BuildSearchSQL(q)
	for _, want := range []string{
		"embedding <=> $2",     // pgvector cosine operator, plan §1.5
		"status = 'CONFIRMED'", // lifecycle guard
		"confidence > 0.3",     // staleness guard
		"project_id = $1",      // scope guard
		"ORDER BY embedding <=> $2",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q:\n%s", want, sql)
		}
	}
	if len(args) != 3 { // project, embedding, limit
		t.Fatalf("expected 3 args, got %d (%v)", len(args), args)
	}
	if args[0] != "p1" {
		t.Errorf("arg $1 = %v, want project id", args[0])
	}
	emb, ok := args[1].(string)
	if !ok || emb != "[0.1,0.2,0.3]" {
		t.Errorf("arg $2 = %v, want pgvector literal [0.1,0.2,0.3]", args[1])
	}
	if args[2] != DefaultSearchLimit {
		t.Errorf("arg $3 (LIMIT) = %v, want %d", args[2], DefaultSearchLimit)
	}
}

func TestBuildSearchSQLFilters(t *testing.T) {
	q := SearchQuery{
		ProjectID:      "p1",
		QueryEmbedding: []float32{1},
		Tags:           []string{"auth", "jwt"},
		Key:            "security/auth",
		Level:          LevelProject,
		Limit:          5,
	}
	sql, args := BuildSearchSQL(q)
	for _, want := range []string{"tags && $3", "key = $4", "level = $5", "LIMIT $6"} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL missing %q:\n%s", want, sql)
		}
	}
	if len(args) != 6 {
		t.Fatalf("expected 6 args, got %d (%v)", len(args), args)
	}
	if args[5] != 5 {
		t.Errorf("LIMIT arg = %v, want 5", args[5])
	}
}

func TestBuildSearchSQLLimitClamp(t *testing.T) {
	if (SearchQuery{Limit: 0}).LimitOrDefault() != DefaultSearchLimit {
		t.Error("zero limit should default")
	}
	if (SearchQuery{Limit: 10000}).LimitOrDefault() != MaxSearchLimit {
		t.Error("huge limit should cap")
	}
}

func TestFormatEmbedding(t *testing.T) {
	if got := FormatEmbedding(nil); got != "[]" {
		t.Errorf("empty vec = %q, want []", got)
	}
	if got := FormatEmbedding([]float32{1, 0.5}); got != "[1,0.5]" {
		t.Errorf("vec = %q, want [1,0.5]", got)
	}
}

func TestCosineSimilarity(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", []float32{1, 2, 3}, []float32{1, 2, 3}, 1},
		{"scaled", []float32{1, 0}, []float32{5, 0}, 1}, // magnitude-invariant
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1},
		{"empty", nil, []float32{1}, 0},
		{"mismatched", []float32{1, 2}, []float32{1}, 0},
		{"zero-norm", []float32{0, 0}, []float32{1, 2}, 0},
	}
	for _, c := range cases {
		if got := CosineSimilarity(c.a, c.b); abs(got-c.want) > 1e-9 {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTagMatchScore(t *testing.T) {
	if got := TagMatchScore([]string{"a"}, []string{"a", "b"}); abs(got-0.5) > 1e-9 {
		t.Errorf("partial = %v, want 0.5", got)
	}
	if got := TagMatchScore([]string{"a"}, nil); got != 0 {
		t.Errorf("no query tags = %v, want 0", got)
	}
	if got := TagMatchScore(nil, []string{"a"}); got != 0 {
		t.Errorf("no item tags = %v, want 0", got)
	}
}

func TestRecencyScoreAnchors(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if got := RecencyScore(now, now); abs(got-1) > 1e-9 {
		t.Errorf("fresh = %v, want 1", got)
	}
	if got := RecencyScore(now.AddDate(0, 0, -90), now); abs(got-0.857375) > 1e-9 {
		t.Errorf("90d = %v, want ~0.857", got)
	}
	if got := RecencyScore(now.AddDate(0, 0, -180), now); abs(got-0.735091890625) > 1e-9 {
		t.Errorf("180d = %v, want ~0.735", got)
	}
	if got := RecencyScore(time.Time{}, now); got != 1 {
		t.Errorf("zero time = %v, want 1 (no penalty)", got)
	}
	if got := RecencyScore(now.Add(time.Hour), now); got != 1 {
		t.Errorf("future = %v, want 1 (clamped)", got)
	}
}

func TestRerankScoreWeights(t *testing.T) {
	if got := RerankScore(1, 1, 1); abs(got-1) > 1e-9 {
		t.Errorf("perfect = %v, want 1", got)
	}
	if got := RerankScore(0.8, 0.5, 0.9); abs(got-(0.8*0.7+0.5*0.2+0.9*0.1)) > 1e-9 {
		t.Errorf("blend = %v, want formula value", got)
	}
}

// Semantic rank: pure cosine order dominates when tags/recency tie.
func TestRankSemanticOrder(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	mk := func(key string) MemoryItem {
		return MemoryItem{Key: key, LastUsedAt: now}
	}
	items := []MemoryItem{mk("low"), mk("high"), mk("mid")}
	ranked := Rank(items, []float64{0.2, 0.95, 0.6}, nil, now)
	if ranked[0].Item.Key != "high" || ranked[1].Item.Key != "mid" || ranked[2].Item.Key != "low" {
		t.Fatalf("wrong order: %v %v %v", ranked[0].Item.Key, ranked[1].Item.Key, ranked[2].Item.Key)
	}
}

// Rerank upset: strong tag + recency signal beats raw similarity,
// proving the 0.7/0.2/0.1 blend is applied (plan §1.5).
func TestRankTagBoostUpset(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	plain := MemoryItem{Key: "plain", LastUsedAt: now}
	boosted := MemoryItem{Key: "boosted", Tags: []string{"auth", "jwt"}, LastUsedAt: now}
	// plain: 0.9*0.7+0+0.1 = 0.73 ; boosted: 0.8*0.7+1*0.2+0.1 = 0.86
	ranked := Rank([]MemoryItem{plain, boosted}, []float64{0.9, 0.8}, []string{"auth", "jwt"}, now)
	if ranked[0].Item.Key != "boosted" {
		t.Fatalf("expected tag-boosted upset, got %q first (scores %.3f vs %.3f)",
			ranked[0].Item.Key, ranked[0].Score, ranked[1].Score)
	}
	if abs(ranked[0].Score-0.86) > 1e-9 || abs(ranked[1].Score-0.73) > 1e-9 {
		t.Errorf("scores = %.4f, %.4f; want 0.86, 0.73", ranked[0].Score, ranked[1].Score)
	}
}

func TestRankRecencyTiebreak(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fresh := MemoryItem{Key: "fresh", LastUsedAt: now}
	stale := MemoryItem{Key: "stale", LastUsedAt: now.AddDate(0, 0, -180)}
	ranked := Rank([]MemoryItem{stale, fresh}, []float64{0.5, 0.5}, nil, now)
	if ranked[0].Item.Key != "fresh" {
		t.Errorf("expected fresh first on similarity tie, got %q", ranked[0].Item.Key)
	}
}

// fakeRows/fakeQuerier exercise Search end-to-end without a database.
type fakeRows struct {
	cols [][]any
	pos  int
}

func (f *fakeRows) Next() bool { f.pos++; return f.pos <= len(f.cols) }
func (f *fakeRows) Err() error { return nil }
func (f *fakeRows) Close()     {}
func (f *fakeRows) Scan(dest ...any) error {
	row := f.cols[f.pos-1]
	if len(dest) != len(row) {
		return errors.New("fakeRows: arity mismatch")
	}
	for i := range row {
		switch ptr := dest[i].(type) {
		case *string:
			*ptr = row[i].(string)
		case *float64:
			*ptr = row[i].(float64)
		case *[]string:
			*ptr = append([]string(nil), row[i].([]string)...)
		case *time.Time:
			*ptr = row[i].(time.Time)
		default:
			return errors.New("fakeRows: unsupported dest")
		}
	}
	return nil
}

type fakeQuerier struct {
	rows     *fakeRows
	gotSQL   string
	gotArgs  []any
	queryErr error
}

func (f *fakeQuerier) Query(_ context.Context, sql string, args ...any) (Rows, error) {
	f.gotSQL, f.gotArgs = sql, args
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return f.rows, nil
}

func TestSearchEndToEnd(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fq := &fakeQuerier{rows: &fakeRows{cols: [][]any{
		{"id1", "k1", "auth uses jwt", "project", "decision", 0.95, []string{"auth"}, "ctx", now, now, 0.9},
		{"id2", "k2", "unrelated fact", "project", "fact", 0.8, []string{}, "ctx", now.AddDate(0, 0, -180), now.AddDate(0, 0, -180), 0.85},
	}}}
	ranked, err := Search(context.Background(), fq, SearchQuery{
		ProjectID:      "p1",
		QueryEmbedding: []float32{0.1},
		Tags:           []string{"auth"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fq.gotSQL, "<=>") {
		t.Errorf("executed SQL missing <=>:\n%s", fq.gotSQL)
	}
	if len(ranked) != 2 {
		t.Fatalf("got %d rows, want 2", len(ranked))
	}
	// k1: 0.9*0.7+1*0.2+rec(0d)≈0.1 → ~0.93 ; k2: 0.85*0.7+0+rec(180d)*0.1 → ~0.669
	if ranked[0].Item.Key != "k1" || ranked[1].Item.Key != "k2" {
		t.Fatalf("wrong rank: %q then %q", ranked[0].Item.Key, ranked[1].Item.Key)
	}
	if ranked[0].Item.ProjectID != "p1" {
		t.Errorf("ProjectID not backfilled: %q", ranked[0].Item.ProjectID)
	}
}

func TestSearchQueryError(t *testing.T) {
	fq := &fakeQuerier{queryErr: errors.New("boom")}
	if _, err := Search(context.Background(), fq, SearchQuery{}); err == nil {
		t.Error("expected query error, got nil")
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
