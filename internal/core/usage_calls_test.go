package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"central-memory/internal/usage"
)

// usageCloud answers the session usage route and records usage posts.
type usageCloud struct {
	mu        sync.Mutex
	usageBody []byte
	failGet   bool
	session   string
}

func (c *usageCloud) Do(_ context.Context, method, path string, body []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case method == http.MethodPost && strings.HasSuffix(path, "/usage"):
		c.usageBody = append([]byte(nil), body...)
	case method == http.MethodGet && strings.HasSuffix(path, "/usage"):
		if c.failGet {
			return nil, errors.New("404 page not found")
		}
		return []byte(c.session), nil
	}
	return []byte(`{"ok":true}`), nil
}

func TestContinueRecordSavesUsageLocallyAndToLedger(t *testing.T) {
	cloud := &usageCloud{}
	dir := t.TempDir()
	raw, _ := json.Marshal(map[string]any{
		"session_id": "s1", "agent": "claude", "prompt": "fix", "answer": "fixed",
		"usage": []usage.Entry{{Model: "claude-opus-5", Input: 10, Output: 20, CostUSD: usage.Cost(0.3)}, {Model: "x", Input: -5}},
	})
	out, err := Call(context.Background(), "continue.record", raw, Deps{Cloud: cloud, Online: true, ContinuationDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["usage_saved"] != true {
		t.Fatalf("usage not sent: %+v", out)
	}
	var posted struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(cloud.usageBody, &posted); err != nil || len(posted.Records) != 1 ||
		posted.Records[0]["cost_usd"] != 0.3 || !strings.HasPrefix(posted.Records[0]["key"].(string), "live:") {
		t.Fatalf("ledger post = %s", cloud.usageBody)
	}
	items, err := loadContinuations(dir, "s1")
	if err != nil || len(items) != 1 || len(items[0].Usage) != 1 || items[0].Usage[0].Output != 20 {
		t.Fatalf("local exchange usage = %+v %v", items, err)
	}
}

func TestSessionsUsageMergesCloudAndUnsyncedLocalTurns(t *testing.T) {
	dir := t.TempDir()
	// One exchange saved only locally (offline), with usage.
	if _, err := saveContinuation(dir, ContinuationExchange{SessionID: "s1", Agent: "codex", Prompt: "p", Answer: "a",
		Usage: []usage.Entry{{Model: "gpt-6.1-sol", Input: 5, Output: 1}}}); err != nil {
		t.Fatal(err)
	}
	cloud := &usageCloud{session: `{"by_model":[{"key":"claude-opus-5","model":"claude-opus-5","input":100,"output":10,"cost_usd":0.1,"has_cost":true,"turns":2}]}`}
	args, _ := json.Marshal(map[string]any{"session_id": "s1"})
	out, err := Call(context.Background(), "sessions.usage", args, Deps{Cloud: cloud, Online: true, ContinuationDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(sessionUsageResult)
	if res.Source != "cloud+local" || len(res.ByModel) != 2 || res.Total.Input != 105 || res.Total.UnpricedTokens != 6 || res.Total.CostUSD != 0.1 {
		t.Fatalf("merged = %+v", res)
	}

	// An older server without the usage route: local turns alone.
	cloud.failGet = true
	out, err = Call(context.Background(), "sessions.usage", args, Deps{Cloud: cloud, Online: true, ContinuationDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res = out.(sessionUsageResult); res.Source != "local" || res.Total.Input != 5 || res.CloudError == "" {
		t.Fatalf("local fallback = %+v", res)
	}
}

func TestUsageSummaryRoute(t *testing.T) {
	cloud := &routeCloud{result: []byte(`{"items":[]}`)}
	args, _ := json.Marshal(map[string]any{"from": "2026-11-01", "group_by": "day", "project_id": "p 1", "utc_offset_minutes": 330})
	if _, err := Call(context.Background(), "usage.summary", args, Deps{Cloud: cloud, Online: true}); err != nil {
		t.Fatal(err)
	}
	if cloud.method != http.MethodGet || !strings.HasPrefix(cloud.path, "/v1/usage?") ||
		!strings.Contains(cloud.path, "group_by=day") || !strings.Contains(cloud.path, "project_id=p+1") || !strings.Contains(cloud.path, "utc_offset_minutes=330") {
		t.Fatalf("route = %s %s", cloud.method, cloud.path)
	}
}
