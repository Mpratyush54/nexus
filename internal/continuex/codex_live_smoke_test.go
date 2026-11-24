//go:build livesmoke

package continuex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Run explicitly: go test -tags livesmoke ./internal/continuex/ -run CodexLiveSmoke -count=1 -v
func TestCodexLiveSmoke(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	s, err := StartCodexLive(ctx, LiveOptions{Workspace: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Send(ctx, "Create a file named hello.txt containing exactly the word nexus. Do nothing else."); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case e, ok := <-s.Events():
			if !ok {
				t.Fatal("events closed before turn completion")
			}
			t.Logf("event: %+v", e)
			switch e.Kind {
			case "approval":
				if err := s.Approve(e.RequestID, true, ""); err != nil {
					t.Fatal(err)
				}
			case "exited":
				t.Fatalf("unexpected event: %+v", e)
			case "turn_done":
				if e.IsError {
					t.Fatalf("turn failed: %+v", e)
				}
				data, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != "nexus" {
					t.Fatalf("unexpected file content %q", data)
				}
				if s.NativeID() == "" {
					t.Fatal("missing native id")
				}
				return
			}
		}
	}
}
