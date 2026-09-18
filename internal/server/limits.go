package server

// limits.go — search pagination clamps + ingest rate-limit/quota gates
// (issues #93, #94, #100).
//
//   - clampSearchLimit enforces default-20/max-100 on ?limit= (issue #94).
//   - Event/file-op rate limiting reuses internal/security Limiter presets
//     (100 events/s, 50 file-ops/s) scoped per Server (issues #93/#100).
//   - Quota gates reuse internal/governance QuotaEnforcer + CostTracker so
//     token spend halts extraction/ingest instead of running unbounded.

import (
	"net/http"
	"strconv"
	"strings"
	"sync"

	"central-memory/internal/governance"
	"central-memory/internal/security"
)

// Search pagination bounds (issue #94).
const (
	DefaultSearchLimit = 20
	MaxSearchLimit     = 100
)

// clampSearchLimit parses raw ?limit=: empty means DefaultSearchLimit,
// valid positives clamp to MaxSearchLimit, anything else writes 400 and
// returns -1.
func clampSearchLimit(raw string, w http.ResponseWriter) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultSearchLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		writeError(w, http.StatusBadRequest, "limit must be a positive integer")
		return -1
	}
	if n > MaxSearchLimit {
		n = MaxSearchLimit
	}
	return n
}

// rateGate holds per-scope token buckets. Lazily created per key so tests
// and single-project dev servers share one bucket.
type rateGate struct {
	mu      sync.Mutex
	buckets map[string]*security.Limiter
	rps     int
	burst   int
}

func newRateGate(rps, burst int) *rateGate {
	return &rateGate{buckets: make(map[string]*security.Limiter), rps: rps, burst: burst}
}

func (g *rateGate) allow(key string) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	b, ok := g.buckets[key]
	if !ok {
		if g.rps > 0 {
			b = security.NewLimiter(g.rps, g.burst)
		} else {
			b = security.NewEventLimiter()
		}
		g.buckets[key] = b
	}
	g.mu.Unlock()
	return b.Allow()
}

// quotaGate wraps governance quota enforcement for ingest paths.
type quotaGate struct {
	mu      sync.Mutex
	tracker *governance.CostTracker
	enforce governance.QuotaEnforcer
	usage   governance.QuotaUsage
}

func newQuotaGate() *quotaGate {
	return &quotaGate{
		tracker: governance.NewCostTracker(nil),
		enforce: governance.NewQuotaEnforcer(),
	}
}

// allowN checks whether n tokens fit inside quota, recording spend on success.
// Returns false + reason when the batch would exceed a cap.
func (q *quotaGate) allowN(tokens int) (bool, string) {
	if q == nil {
		return true, ""
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if ok, reason := q.enforce.AllowsBatch(q.usage, tokens, 0); !ok {
		return false, reason
	}
	q.usage.DayTokens += tokens
	q.usage.MonthTokens += tokens
	return true, ""
}

// estimateTokens proxies the shared chars/4 estimate.
func estimateIngestTokens(chars int) int {
	return governance.CharsToTokens(chars)
}
