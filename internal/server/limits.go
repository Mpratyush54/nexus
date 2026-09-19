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
	"time"

	"central-memory/internal/governance"
	"central-memory/internal/security"
)

// Search pagination bounds (issue #94). Library browsing needs more than 100.
const (
	DefaultSearchLimit = 20
	MaxSearchLimit     = 500
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

// clampSearchOffset parses ?offset= (default 0). Negative/invalid → 400.
func clampSearchOffset(raw string, w http.ResponseWriter) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
		return -1
	}
	if n > 100_000 {
		writeError(w, http.StatusBadRequest, "offset too large")
		return -1
	}
	return n
}

// rateGate holds per-scope token buckets. Lazily created per key so tests
// and single-project dev servers share one bucket.
//
// Bound (issue #134): keys are attacker-controlled (project IDs, IPs), so
// an unbounded map is a memory-exhaustion vector. Past maxRateGateBuckets
// entries the least-recently-used bucket is evicted.
const maxRateGateBuckets = 4096

type rateGate struct {
	mu       sync.Mutex
	buckets  map[string]*security.Limiter
	lastSeen map[string]time.Time
	rps      int
	burst    int
}

func newRateGate(rps, burst int) *rateGate {
	return &rateGate{buckets: make(map[string]*security.Limiter), lastSeen: make(map[string]time.Time), rps: rps, burst: burst}
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
		if len(g.buckets) > maxRateGateBuckets {
			var oldestKey string
			var oldest time.Time
			first := true
			for k, t := range g.lastSeen {
				if first || t.Before(oldest) {
					oldest, oldestKey, first = t, k, false
				}
			}
			if oldestKey != "" {
				delete(g.buckets, oldestKey)
				delete(g.lastSeen, oldestKey)
			}
		}
	}
	g.lastSeen[key] = time.Now().UTC()
	g.mu.Unlock()
	return b.Allow()
}

// quotaGate wraps governance quota enforcement for ingest paths.
type quotaGate struct {
	mu      sync.Mutex
	tracker *governance.CostTracker
	enforce governance.QuotaEnforcer
	usage   governance.QuotaUsage
	// Window tracking (issue #134): without day/month rollover the
	// counters grow forever and one tenant permanently consumes the
	// process-wide quota until restart.
	windowDay   time.Time // date of usage.Day* counters
	windowMonth time.Time // month of usage.Month* counters
}

func newQuotaGate() *quotaGate {
	return &quotaGate{
		tracker: governance.NewCostTracker(nil),
		enforce: governance.NewQuotaEnforcer(),
	}
}

// allowN checks whether n tokens fit inside quota, recording spend on success.
// Returns false + reason when the batch would exceed a cap. Day/month
// windows roll over automatically (issue #134).
func (q *quotaGate) allowN(tokens int) (bool, string) {
	if q == nil {
		return true, ""
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now().UTC()
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	thisMonth := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	if q.windowDay.IsZero() {
		q.windowDay, q.windowMonth = today, thisMonth
	}
	if !today.Equal(q.windowDay) {
		q.windowDay = today
		q.usage.DayTokens, q.usage.DayUSD = 0, 0
	}
	if !thisMonth.Equal(q.windowMonth) {
		q.windowMonth = thisMonth
		q.usage.MonthTokens, q.usage.MonthUSD = 0, 0
	}
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
