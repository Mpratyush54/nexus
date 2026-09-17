// Package context implements the hybrid vector+text retrieval engine and
// inference-ready context assembly for Central Memory.
//
// Retrieval mirrors the Postgres production query (see implementation-plan.md
// Phase 1.5): pgvector cosine distance (`embedding <=> $1`) is the primary
// signal, exact tag/key matches provide a secondary boost, and recency is a
// small tie-breaker. Final ranking:
//
//	score = 0.7*similarity + 0.2*max(tagMatch, keyMatch) + 0.1*recency
//
// All scores are in [0,1]. Embeddings are accepted precomputed ([]float32);
// cosine similarity is computed locally with the standard library only, so
// this package never calls an embeddings API.
package context

import (
	"math"
	"sort"
	"strings"
	"time"

	"central-memory/internal/store"
)

// Re-rank weights (see implementation-plan.md Phase 1.5).
const (
	WeightVector  = 0.7
	WeightTagKey  = 0.2
	WeightRecency = 0.1
)

// RecencyHalfLifeDays controls how fast the recency term decays:
// recency = exp(-daysSinceActive / RecencyDecayDays).
const RecencyDecayDays = 90.0

// CosineSimilarity returns the cosine similarity of a and b in [-1, 1].
// Vectors of unequal length are compared over their shared prefix (min
// length); empty or zero-magnitude vectors score 0.
func CosineSimilarity(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if n == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
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

// TagMatchScore is the fraction of query tags present on the item
// (case-insensitive). It is 0 when the query carries no tags.
func TagMatchScore(queryTags, itemTags []string) float64 {
	if len(queryTags) == 0 {
		return 0
	}
	set := make(map[string]struct{}, len(itemTags))
	for _, t := range itemTags {
		set[strings.ToLower(strings.TrimSpace(t))] = struct{}{}
	}
	matched := 0
	for _, q := range queryTags {
		if _, ok := set[strings.ToLower(strings.TrimSpace(q))]; ok {
			matched++
		}
	}
	return float64(matched) / float64(len(queryTags))
}

// KeyMatchScore boosts machine-key alignment: 1.0 on exact match,
// 0.5 on case-insensitive substring match, 0 otherwise. Empty query key
// never matches.
func KeyMatchScore(queryKey, itemKey string) float64 {
	if queryKey == "" || itemKey == "" {
		return 0
	}
	if itemKey == queryKey {
		return 1
	}
	q, k := strings.ToLower(queryKey), strings.ToLower(itemKey)
	if strings.Contains(k, q) || strings.Contains(q, k) {
		return 0.5
	}
	return 0
}

// RecencyScore maps days since last activity to (0, 1] via exponential
// decay: exp(-days/90). Future timestamps clamp to 1; the zero time
// (unknown activity) scores 0 so it never outranks known-fresh items.
func RecencyScore(lastActive, now time.Time) float64 {
	if lastActive.IsZero() {
		return 0
	}
	days := now.Sub(lastActive).Hours() / 24
	if days < 0 {
		days = 0
	}
	return math.Exp(-days / RecencyDecayDays)
}

// lastActiveAt prefers LastUsedAt, then UpdatedAt, then CreatedAt — the
// best available freshness signal for ranking.
func lastActiveAt(item *store.MemoryItem) time.Time {
	if !item.LastUsedAt.IsZero() {
		return item.LastUsedAt
	}
	if !item.UpdatedAt.IsZero() {
		return item.UpdatedAt
	}
	return item.CreatedAt
}

// textSimilarity is the stdlib fallback used when no query embedding is
// supplied: fraction of distinct query tokens found in the item's
// key+content+tags. It substitutes for the vector term.
func textSimilarity(queryText string, item *store.MemoryItem) float64 {
	tokens := strings.Fields(strings.ToLower(queryText))
	if len(tokens) == 0 {
		return 0
	}
	hay := strings.ToLower(item.Key + " " + item.Content + " " + strings.Join(item.Tags, " "))
	seen := make(map[string]struct{}, len(tokens))
	hit := 0
	for _, t := range tokens {
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		if strings.Contains(hay, t) {
			hit++
		}
	}
	return float64(hit) / float64(len(seen))
}

// ScoredItem pairs a memory with its retrieval signals and final score.
type ScoredItem struct {
	Item       *store.MemoryItem
	Similarity float64
	TagKeyBoost float64
	Recency    float64
	Score      float64
}

// HybridSearch ranks candidates with vector similarity as the primary
// signal, tag/key boosts second, and recency as tie-breaker:
//
//	score = 0.7*similarity + 0.2*max(tagMatch, keyMatch) + 0.1*recency
//
// Cosine similarities are clamped to [0,1] (negative cosine means
// "opposite", which is no match for retrieval). Items with status
// REJECTED or SUPERSEDED are never served. Results sort by descending
// score (ties: higher confidence, then key). A positive limit caps the
// results; limit <= 0 returns all ranked items.
func HybridSearch(queryEmb []float32, queryTags []string, queryKey, queryText string, candidates []*store.MemoryItem, now time.Time, limit int) []ScoredItem {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	out := make([]ScoredItem, 0, len(candidates))
	for _, item := range candidates {
		if item == nil {
			continue
		}
		if item.Status == "REJECTED" || item.Status == "SUPERSEDED" {
			continue
		}
		var sim float64
		if len(queryEmb) > 0 && len(item.Embedding) > 0 {
			sim = CosineSimilarity(queryEmb, item.Embedding)
			if sim < 0 {
				sim = 0
			} else if sim > 1 {
				sim = 1
			}
		} else if queryText != "" {
			sim = textSimilarity(queryText, item)
		}
		boost := TagMatchScore(queryTags, item.Tags)
		if k := KeyMatchScore(queryKey, item.Key); k > boost {
			boost = k
		}
		rec := RecencyScore(lastActiveAt(item), now)
		score := WeightVector*sim + WeightTagKey*boost + WeightRecency*rec
		out = append(out, ScoredItem{
			Item:        item,
			Similarity:  sim,
			TagKeyBoost: boost,
			Recency:     rec,
			Score:       score,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Item.Confidence != out[j].Item.Confidence {
			return out[i].Item.Confidence > out[j].Item.Confidence
		}
		return out[i].Item.Key < out[j].Item.Key
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
