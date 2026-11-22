package server

import (
	"net/http"
	"testing"
	"time"

	"central-memory/internal/store"
	"central-memory/internal/usage"
)

func TestUsageRoutesRecordDedupeAndAggregate(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	proj, err := mem.ResolveProject(ctx, "", "", "usage-proj")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ClaimProject(ctx, proj.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	ownerTok := loginAs(t, s, "owner")
	strangerTok := loginAs(t, s, "stranger")

	newSession := func(harness, native string) string {
		rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions", ownerTok, map[string]any{
			"project_id": proj.ID, "harness": harness, "native_id": native,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
		}
		var row store.AgentSession
		decodeBody(t, rec, &row)
		return row.ID
	}
	claude, codex := newSession("claude", "c1"), newSession("codex", "x1")
	at := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)

	// Usage on a live-chat turn (core continue.record path).
	rec := doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+claude+"/turns/batch", ownerTok, map[string]any{
		"turns": []map[string]any{
			{"idx": 1, "role": "user", "text_preview": "go", "source_ts": at},
			{"idx": 2, "role": "assistant", "text_preview": "done", "source_ts": at,
				"usage": map[string]any{"key": "live:x", "model": "claude-opus-5", "input": 100, "output": 50, "cost_usd": 0.02}},
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body.String())
	}
	// Captured usage on the ledger route, with a repeated key.
	for i := 0; i < 2; i++ {
		rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+claude+"/usage", ownerTok, map[string]any{
			"records": []map[string]any{{"key": "claude:msg_1", "idx": 3, "model": "claude-opus-5", "input": 1000, "output": 10, "cache_read": 2000, "source_ts": at}},
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("usage post: %d %s", rec.Code, rec.Body.String())
		}
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+codex+"/usage", ownerTok, map[string]any{
		"records": []map[string]any{{"key": "codex-total:9", "model": "gpt-6.1-sol", "input": 7, "output": 2}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("codex usage: %d %s", rec.Code, rec.Body.String())
	}
	if rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+codex+"/usage", ownerTok, map[string]any{
		"records": []map[string]any{{"model": "x", "input": 1}},
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("keyless usage must be rejected: %d", rec.Code)
	}
	if rec = doJSON(t, s, http.MethodPost, "/v1/agent-sessions/"+codex+"/usage", strangerTok, map[string]any{
		"records": []map[string]any{{"key": "k", "input": 1}},
	}); rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Fatalf("stranger must not write usage: %d", rec.Code)
	}

	var one struct {
		ByModel []usage.Total `json:"by_model"`
		Total   usage.Total   `json:"total"`
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+claude+"/usage", ownerTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("session usage: %d %s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &one)
	if len(one.ByModel) != 1 || one.Total.Input != 1100 || one.Total.Turns != 2 || one.Total.ReportedUSD != 0.02 || !one.Total.Estimated {
		t.Fatalf("session usage = %+v", one)
	}
	if rec = doJSON(t, s, http.MethodGet, "/v1/agent-sessions/"+claude+"/usage", strangerTok, nil); rec.Code == http.StatusOK {
		t.Fatal("stranger must not read usage")
	}

	var sum struct {
		GroupBy string        `json:"group_by"`
		Items   []usage.Total `json:"items"`
		Total   usage.Total   `json:"total"`
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/usage?group_by=agent&scope=mine&project_id="+proj.ID+"&from="+time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02"), ownerTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary: %d %s", rec.Code, rec.Body.String())
	}
	decodeBody(t, rec, &sum)
	if sum.GroupBy != "agent" || len(sum.Items) != 2 || sum.Total.Input != 1107 || sum.Total.UnpricedTokens != 9 {
		t.Fatalf("summary = %+v", sum)
	}
	rec = doJSON(t, s, http.MethodGet, "/v1/usage", strangerTok, nil)
	decodeBody(t, rec, &sum)
	if rec.Code != http.StatusOK || len(sum.Items) != 0 {
		t.Fatalf("stranger summary must be empty: %d %+v", rec.Code, sum)
	}
	if rec = doJSON(t, s, http.MethodGet, "/v1/usage?group_by=nope", ownerTok, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad group_by: %d", rec.Code)
	}
}
