package context

import (
	"math"
	"testing"
	"time"

	"central-memory/internal/store"
)

func auditCand(key string, emb []float32, tags []string, conf float32, status string, last time.Time) *store.MemoryItem {
	return &store.MemoryItem{
		Key: key, Content: "content for " + key, Level: "project",
		Status: status, Confidence: conf, Embedding: emb,
		Tags: tags, LastUsedAt: last,
	}
}

func TestAuditWeightConstants(t *testing.T) {
	if WeightVector != 0.7 || WeightTagKey != 0.2 || WeightRecency != 0.1 {
		t.Fatalf("weights = %v/%v/%v, want 0.7/0.2/0.1", WeightVector, WeightTagKey, WeightRecency)
	}
	if s := WeightVector + WeightTagKey + WeightRecency; math.Abs(s-1.0) > 1e-9 {
		t.Fatalf("weights sum = %v, want 1.0", s)
	}
	// Pin the exact formula on a crafted item.
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	item := auditCand("k", []float32{1, 0}, []string{"auth"}, 1.0, "CONFIRMED", now)
	got := HybridSearch([]float32{1, 0}, []string{"auth"}, "", "", []*store.MemoryItem{item}, now, 0)
	if len(got) != 1 {
		t.Fatalf("got %d results", len(got))
	}
	// sim=1, boost=1, recency=1 -> 0.7+0.2+0.1 = 1.0
	if math.Abs(got[0].Score-1.0) > 1e-9 {
		t.Errorf("score = %v, want 1.0 (0.7*1+0.2*1+0.1*1)", got[0].Score)
	}
	if math.Abs(got[0].Similarity-1.0) > 1e-9 || math.Abs(got[0].TagKeyBoost-1.0) > 1e-9 || math.Abs(got[0].Recency-1.0) > 1e-9 {
		t.Errorf("signals = sim %v boost %v rec %v, want all 1.0", got[0].Similarity, got[0].TagKeyBoost, got[0].Recency)
	}
}

func TestAuditCosineEdgeCases(t *testing.T) {
	if got := CosineSimilarity(nil, nil); got != 0 {
		t.Errorf("nil,nil = %v, want 0", got)
	}
	if got := CosineSimilarity([]float32{}, []float32{1}); got != 0 {
		t.Errorf("empty = %v, want 0", got)
	}
	if got := CosineSimilarity([]float32{0, 0, 0}, []float32{1, 2, 3}); got != 0 {
		t.Errorf("zero-magnitude = %v, want 0", got)
	}
	// FIXED (#105): mismatched dims score strict 0, matching pgvector's
	// reject-on-mismatch. E.g. [1,0] vs [1,0,99] is 0, not 1.
	if got := CosineSimilarity([]float32{1, 0}, []float32{1, 0, 99}); got != 0 {
		t.Errorf("dim mismatch = %v, want 0 (strict, issue #105)", got)
	}
	if got := CosineSimilarity([]float32{1}, []float32{0, 1}); got != 0 {
		t.Errorf("dim mismatch = %v, want 0 (strict, issue #105)", got)
	}
	// Raw cosine preserves sign; HybridSearch clamps negatives to 0.
	if got := CosineSimilarity([]float32{1, 0}, []float32{-1, 0}); math.Abs(got+1) > 1e-6 {
		t.Errorf("opposite = %v, want -1", got)
	}
	now := time.Now().UTC()
	neg := auditCand("neg", []float32{-1, 0}, nil, 1.0, "CONFIRMED", now)
	got := HybridSearch([]float32{1, 0}, nil, "", "", []*store.MemoryItem{neg}, now, 0)
	if len(got) != 1 || got[0].Similarity != 0 {
		t.Errorf("HybridSearch must clamp negative cosine to 0: %+v", got)
	}
}

func TestAuditHybridSearchStatusFilterAbsence(t *testing.T) {
	now := time.Now().UTC()
	emb := []float32{1, 0}
	cands := []*store.MemoryItem{
		auditCand("prop", emb, nil, 1.0, "PROPOSED", now),
		auditCand("conf", emb, nil, 1.0, "CONFIRMED", now),
		auditCand("rej", emb, nil, 1.0, "REJECTED", now),
		auditCand("sup", emb, nil, 1.0, "SUPERSEDED", now),
	}
	got := HybridSearch(emb, nil, "", "", cands, now, 0)
	keys := map[string]bool{}
	for _, s := range got {
		keys[s.Item.Key] = true
	}
	if keys["rej"] || keys["sup"] {
		t.Error("REJECTED/SUPERSEDED must never be served")
	}
	// Contract parity with store.SearchMemoryVector (issue #139):
	// CONFIRMED-only — PROPOSED rows are unreviewed and stay out.
	if keys["prop"] {
		t.Error("PROPOSED must not be served by HybridSearch")
	}
	if !keys["conf"] {
		t.Error("CONFIRMED must be served by HybridSearch")
	}
}

func TestAuditHybridSearchConfidenceFloorAbsence(t *testing.T) {
	now := time.Now().UTC()
	emb := []float32{1, 0}
	weak := auditCand("weak", emb, nil, 0.05, "CONFIRMED", now)
	strong := auditCand("strong", emb, nil, 0.9, "CONFIRMED", now)
	got := HybridSearch(emb, nil, "", "", []*store.MemoryItem{weak, strong, nil}, now, 0)
	// Contract parity with store.SearchMemoryVector (issue #139):
	// confidence <= 0.3 is below the floor; nils still skipped.
	if len(got) != 1 || got[0].Item.Key != "strong" {
		t.Fatalf("want only the above-floor item, got %+v", got)
	}
}

func TestAuditHybridSearchRankingTiesAndLimit(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	emb := []float32{1, 0}
	lo := auditCand("b-key", emb, nil, 0.5, "CONFIRMED", now)
	hi := auditCand("a-key", emb, nil, 0.9, "CONFIRMED", now)
	got := HybridSearch(emb, nil, "", "", []*store.MemoryItem{lo, hi}, now, 0)
	if len(got) != 2 {
		t.Fatalf("got %d", len(got))
	}
	// Same score: higher confidence wins regardless of key.
	if got[0].Item.Key != "a-key" {
		t.Fatalf("tie-break confidence failed: %q first", got[0].Item.Key)
	}
	// Full tie: lexicographically smaller key wins.
	x := auditCand("zzz", emb, nil, 1.0, "CONFIRMED", now)
	y := auditCand("aaa", emb, nil, 1.0, "CONFIRMED", now)
	got = HybridSearch(emb, nil, "", "", []*store.MemoryItem{x, y}, now, 0)
	if got[0].Item.Key != "aaa" {
		t.Fatalf("tie-break key failed: %q first", got[0].Item.Key)
	}
	// Positive limit caps; limit <= 0 returns all.
	if got := HybridSearch(emb, nil, "", "", []*store.MemoryItem{x, y}, now, 1); len(got) != 1 {
		t.Errorf("limit=1 returned %d", len(got))
	}
	if got := HybridSearch(emb, nil, "", "", []*store.MemoryItem{x, y}, now, 0); len(got) != 2 {
		t.Errorf("limit<=0 should return all, got %d", len(got))
	}
	if got := HybridSearch(emb, nil, "", "", []*store.MemoryItem{x, y}, now, -5); len(got) != 2 {
		t.Errorf("negative limit should return all, got %d", len(got))
	}
}

func TestAuditTagKeyRecencyScores(t *testing.T) {
	if got := TagMatchScore(nil, []string{"a"}); got != 0 {
		t.Errorf("no query tags = %v, want 0", got)
	}
	if got := TagMatchScore([]string{"A", "b"}, []string{"a", "B", "c"}); math.Abs(got-1.0) > 1e-9 {
		t.Errorf("case-insensitive full match = %v, want 1", got)
	}
	if got := TagMatchScore([]string{"a", "zzz"}, []string{"a"}); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("partial match = %v, want 0.5", got)
	}
	if got := KeyMatchScore("", "k"); got != 0 {
		t.Errorf("empty query key = %v, want 0", got)
	}
	if got := KeyMatchScore("auth/jwt", "auth/jwt"); got != 1.0 {
		t.Errorf("exact key = %v, want 1", got)
	}
	if got := KeyMatchScore("auth", "auth/jwt"); got != 0.5 {
		t.Errorf("substring key = %v, want 0.5", got)
	}
	if got := KeyMatchScore("zzz", "auth/jwt"); got != 0 {
		t.Errorf("no match = %v, want 0", got)
	}
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if got := RecencyScore(time.Time{}, now); got != 0 {
		t.Errorf("zero time recency = %v, want 0", got)
	}
	if got := RecencyScore(now.Add(24*time.Hour), now); math.Abs(got-1.0) > 1e-9 {
		t.Errorf("future recency = %v, want 1 (clamped)", got)
	}
	if got := RecencyScore(now, now); math.Abs(got-1.0) > 1e-9 {
		t.Errorf("fresh recency = %v, want 1", got)
	}
	want := math.Exp(-90.0 / RecencyDecayDays)
	if got := RecencyScore(now.Add(-90*24*time.Hour), now); math.Abs(got-want) > 1e-9 {
		t.Errorf("90d recency = %v, want %v", got, want)
	}
	// max(tag,key) boost: key match beats tag miss.
	now2 := time.Now().UTC()
	it := &store.MemoryItem{Key: "auth/jwt", Content: "x", Level: "project", Status: "CONFIRMED", Confidence: 1.0, LastUsedAt: now2}
	got := HybridSearch(nil, []string{"nope"}, "auth", "", []*store.MemoryItem{it}, now2, 0)
	if len(got) != 1 || math.Abs(got[0].TagKeyBoost-0.5) > 1e-9 {
		t.Errorf("boost should be max(tag,key)=0.5: %+v", got)
	}
}
