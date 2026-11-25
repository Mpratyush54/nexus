//go:build livesmoke

package continuex

// Manual check against the real, signed-in Claude Code CLI (costs a few
// tokens). Run: go test -tags livesmoke -run TestClaudeLiveSmoke -v ./internal/continuex/

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeLiveSmoke(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	s, err := StartClaudeLive(ctx, LiveOptions{Workspace: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Send(ctx, "Use the Write tool to create a file named hello.txt containing exactly the word nexus. Do not do anything else."); err != nil {
		t.Fatal(err)
	}
	var sawDelta, sawApproval, sawTool bool
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timeout; delta=%v approval=%v tool=%v", sawDelta, sawApproval, sawTool)
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatalf("events closed early; delta=%v approval=%v tool=%v", sawDelta, sawApproval, sawTool)
			}
			t.Logf("%-10s tool=%s req=%s text=%.120q", ev.Kind, ev.ToolName, ev.RequestID, ev.Text)
			switch ev.Kind {
			case "text_delta":
				sawDelta = true
			case "approval":
				sawApproval = true
				if err := s.Approve(ev.RequestID, true, ""); err != nil {
					t.Fatal(err)
				}
			case "tool_start":
				sawTool = true
			case "turn_done":
				data, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
				if err != nil {
					t.Fatalf("file not created (approval=%v): %v", sawApproval, err)
				}
				if !strings.Contains(string(data), "nexus") {
					t.Fatalf("unexpected content %q", data)
				}
				if s.NativeID() == "" {
					t.Fatal("no native session id")
				}
				t.Logf("ok: native=%s approval=%v delta=%v", s.NativeID(), sawApproval, sawDelta)
				return
			}
		}
	}
}
