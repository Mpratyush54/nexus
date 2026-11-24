//go:build livesmoke

package continuex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestACPLiveSmoke_Cursor(t *testing.T)   { acpSmoke(t, "cursor") }
func TestACPLiveSmoke_OpenCode(t *testing.T) { acpSmoke(t, "opencode") }
func TestACPLiveSmoke_Gemini(t *testing.T)   { acpSmoke(t, "gemini") }
func TestACPLiveSmoke_Kimi(t *testing.T)     { acpSmoke(t, "kimi") }

func acpSmoke(t *testing.T, agent string) {
	argv := acpArgv[agent]
	_, err := exec.LookPath(argv[0])
	if err != nil && agent == "cursor" {
		_, err = exec.LookPath("agent")
	}
	if err != nil {
		t.Skipf("%s not on PATH: %v", agent, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	dir := t.TempDir()
	s, err := StartACPLive(ctx, agent, LiveOptions{Workspace: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Send(ctx, "Create a file named hello.txt containing exactly the word nexus. Do nothing else."); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("%s: %v", agent, ctx.Err())
		case e, ok := <-s.Events():
			if !ok {
				t.Fatal("agent exited before turn_done")
			}
			t.Logf("%s: %s %s %s", agent, e.Kind, e.ToolName, e.Text)
			if e.Kind == "approval" {
				if err = s.Approve(e.RequestID, true, ""); err != nil {
					t.Fatal(err)
				}
			}
			if e.Kind == "error" || (e.Kind == "turn_done" && e.IsError) {
				t.Fatalf("%s: %s", agent, e.Text)
			}
			if e.Kind == "turn_done" {
				b, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
				if err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(string(b)) != "nexus" {
					t.Fatalf("unexpected file content %q", b)
				}
				return
			}
		}
	}
}
