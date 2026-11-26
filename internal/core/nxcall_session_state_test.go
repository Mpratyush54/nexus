package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSessionsStateRoute(t *testing.T) {
	c := &routeCloud{}
	deps := Deps{Cloud: c, Online: true}
	call := func(args string) error {
		_, err := Call(context.Background(), "sessions.state", json.RawMessage(args), deps)
		return err
	}
	if err := call(`{"session_id":"s/1","pinned":true}`); err != nil {
		t.Fatal(err)
	}
	if c.method != "PATCH" || c.path != "/v1/agent-sessions/s%2F1/state" || string(c.body) != `{"pinned":true}` {
		t.Fatalf("pin = %s %s %s", c.method, c.path, c.body)
	}
	if err := call(`{"id":"s1","title":"","archived":false}`); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(c.body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 2 || body["title"] != "" || body["archived"] != false {
		t.Fatalf("rename clear body = %s", c.body)
	}
	if err := call(`{"session_id":"s1","title":"My chat","project_id":"p1"}`); err != nil {
		t.Fatal(err)
	}
	if string(c.body) != `{"title":"My chat"}` {
		t.Fatalf("rename body = %s", c.body)
	}
	for _, bad := range []string{`{"pinned":true}`, `{"session_id":"s1"}`, `{"session_id":"s1","pinned":"yes"}`, `{"session_id":"s1","title":3}`} {
		if err := call(bad); err == nil {
			t.Fatalf("%s should be rejected", bad)
		}
	}

	if _, err := Call(context.Background(), "timeline.list", json.RawMessage(`{"project":"p1","archived":"only"}`), deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.path, "archived=only") || !strings.Contains(c.path, "project_id=p1") {
		t.Fatalf("archived timeline = %s", c.path)
	}
	if _, err := Call(context.Background(), "timeline.list", json.RawMessage(`{"pinned":"only"}`), deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.path, "pinned=only") {
		t.Fatalf("pinned timeline = %s", c.path)
	}
}
