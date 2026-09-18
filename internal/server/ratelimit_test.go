package server

// Ingest rate-limit and quota enforcement tests (nexus issues #93/#100).
//
// The security Limiter presets (100 events/s) and governance QuotaEnforcer
// must gate real ingest paths — not just exist as constructors. These tests
// drive the actual HTTP handlers and WS hub with shrunken gates and assert
// deterministic 429 / rate-limit responses.

import (
	"encoding/json"
	"net/http"
	"testing"

	"central-memory/internal/governance"
)

// Shrinking the gate to 1 token proves the middleware path trips: first
// ingest passes, the immediate second is rejected before store work.
func TestServerIngestRateLimit429(t *testing.T) {
	s := newTestServer()
	s.events = newRateGate(1, 1)
	token := loginAs(t, s, "flood")
	projectID := resolveTestProject(t, s, token, "flood-proj")

	mem := map[string]any{
		"project_id": projectID,
		"key":        "flood/item",
		"content":    "The team uses pytest with fixture-based setup for integration tests.",
		"level":      "project",
	}
	if rec := doJSON(t, s, http.MethodPost, "/memory", token, mem); rec.Code != http.StatusCreated {
		t.Fatalf("first memory create = %d, want 201", rec.Code)
	}
	rec := doJSON(t, s, http.MethodPost, "/memory", token, mem)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second memory create = %d, want 429", rec.Code)
	}

	ep := map[string]any{
		"project_id": projectID, "title": "flood episode", "episode_type": "bug_fix",
	}
	// Fresh gate for the episode path (separate per-scope bucket).
	s.events = newRateGate(1, 1)
	if rec := doJSON(t, s, http.MethodPost, "/episodes", token, ep); rec.Code != http.StatusCreated {
		t.Fatalf("first episode create = %d, want 201", rec.Code)
	}
	if rec := doJSON(t, s, http.MethodPost, "/episodes", token, ep); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second episode create = %d, want 429", rec.Code)
	}
}

// The WS hub throttle is wired to the same server gate (AttachHub): actions
// past the burst get a rate-limit error reply, not fan-out.
func TestHubActionThrottleReply(t *testing.T) {
	s := newTestServer()
	h := NewHub()
	s.AttachHub(h)
	s.events = newRateGate(1, 1)
	h.SetThrottle(func(_, projectID string) bool {
		if projectID == "" {
			projectID = "global"
		}
		return s.eventAllowed(projectID)
	})
	token := loginAs(t, s, "ws-flood-2")
	projectID := resolveTestProject(t, s, token, "ws-flood-proj-2")

	c := newHubClient(h, "ws-flood-2", "", "")
	raw, _ := json.Marshal(WSMessage{Type: WSMsgSubscribe, ProjectID: projectID})
	if err := h.HandleClientMessage(c.ID, raw); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if ack := readMsg(t, c); ack.Type != WSMsgSubscribed {
		t.Fatalf("want ack, got %+v", ack)
	}

	act, _ := json.Marshal(WSMessage{Type: WSMsgAction, EventType: "PING"})
	if err := h.HandleClientMessage(c.ID, act); err != nil {
		t.Fatalf("first action: %v", err)
	}
	if err := h.HandleClientMessage(c.ID, act); err != errWSRateLimited {
		t.Fatalf("second action err = %v, want errWSRateLimited", err)
	}
	reply := readMsg(t, c)
	if reply.Type != WSMsgError || reply.Message != "rate limit exceeded" {
		t.Fatalf("want rate-limit error reply, got %+v", reply)
	}
}

// Quota gates halt ingest spend: a batch that would cross a cap is refused
// with its reason, and spend accrues on success.
func TestQuotaGateTrips(t *testing.T) {
	q := &quotaGate{enforce: governance.QuotaEnforcer{DailyTokenCap: 10}}
	if ok, _ := q.allowN(6); !ok {
		t.Fatal("first batch must fit")
	}
	if ok, reason := q.allowN(6); ok || reason == "" {
		t.Fatalf("over-cap batch must fail with reason, got ok=%v reason=%q", ok, reason)
	}
	var nilQ *quotaGate
	if ok, _ := nilQ.allowN(1 << 30); !ok {
		t.Fatal("nil gate must allow (degraded open, never fail-closed on accounting)")
	}
}

// Memory create enforces the quota gate through estimateIngestTokens: the
// suite default caps (100k/day) never trip on a single write, but the gate
// is consulted — proven by swapping a tiny enforcer in-package.
func TestServerMemoryCreateQuotaGate(t *testing.T) {
	s := newTestServer()
	s.quota = &quotaGate{enforce: governance.QuotaEnforcer{DailyTokenCap: 1}}
	token := loginAs(t, s, "quota")
	projectID := resolveTestProject(t, s, token, "quota-proj")
	rec := doJSON(t, s, http.MethodPost, "/memory", token, map[string]any{
		"project_id": projectID,
		"key":        "quota/item",
		"content":    "The team uses pytest with fixture-based setup for integration tests.",
		"level":      "project",
	})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-quota create = %d, want 429", rec.Code)
	}
}
