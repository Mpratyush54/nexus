package agentrun

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"central-memory/internal/continuex"
)

// fakeLive is an in-memory agent: the test drives its events.
type fakeLive struct {
	events    chan continuex.LiveEvent
	sent      chan string
	approvals chan string
	interrupt chan struct{}
	closeOnce sync.Once
	closed    chan struct{}
	native    string
}

func newFake(native string) *fakeLive {
	return &fakeLive{events: make(chan continuex.LiveEvent, 256), sent: make(chan string, 16), approvals: make(chan string, 16),
		interrupt: make(chan struct{}, 4), closed: make(chan struct{}), native: native}
}
func (f *fakeLive) Send(_ context.Context, text string) error { f.sent <- text; return nil }
func (f *fakeLive) Approve(id string, allow bool, _ string) error {
	f.approvals <- fmt.Sprintf("%s:%v", id, allow)
	return nil
}
func (f *fakeLive) Interrupt() error                   { f.interrupt <- struct{}{}; return nil }
func (f *fakeLive) Events() <-chan continuex.LiveEvent { return f.events }
func (f *fakeLive) NativeID() string                   { return f.native }
func (f *fakeLive) Close() error {
	f.closeOnce.Do(func() { close(f.closed); close(f.events) })
	return nil
}

type harness struct {
	t      *testing.T
	m      *Manager
	mu     sync.Mutex
	events []map[string]any
	lives  map[string]*fakeLive // session -> latest fake
	starts []StartSpec
	saved  []TurnRecord
	dir    string
}

func newHarness(t *testing.T, max int, dir string) *harness {
	h := &harness{t: t, lives: map[string]*fakeLive{}, dir: dir}
	h.m = New(Config{
		MaxRunning: max, StateDir: dir, DeltaFlush: 5 * time.Millisecond,
		Start: func(_ context.Context, spec StartSpec) (continuex.LiveSession, error) {
			f := newFake("native-" + spec.ProjectID)
			h.mu.Lock()
			h.starts = append(h.starts, spec)
			h.lives[spec.ProjectID] = f
			h.mu.Unlock()
			return f, nil
		},
		Record: func(r TurnRecord) (any, error) {
			h.mu.Lock()
			h.saved = append(h.saved, r)
			h.mu.Unlock()
			return map[string]any{"text": r.Answer}, nil
		},
		Emit: func(e map[string]any) {
			cp := map[string]any{}
			for k, v := range e {
				cp[k] = v
			}
			h.mu.Lock()
			h.events = append(h.events, cp)
			h.mu.Unlock()
		},
	})
	t.Cleanup(h.m.Shutdown)
	return h
}

// send uses the session ID as project ID so each chat gets its own fake.
func (h *harness) send(session, text, mode string) map[string]any {
	h.t.Helper()
	r, err := h.m.Send(SendRequest{SessionID: session, ProjectID: session, Agent: "claude", Workspace: h.dir, Text: text, Mode: mode})
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *harness) live(session string) *fakeLive {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		f := h.lives[session]
		h.mu.Unlock()
		if f != nil {
			return f
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("no agent started for %s", session)
	return nil
}

func (h *harness) waitFor(what string, ok func(map[string]any) bool) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		for _, e := range h.events {
			if ok(e) {
				h.mu.Unlock()
				return e
			}
		}
		h.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", what)
	return nil
}

func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("timed out")
	}
	var zero T
	return zero
}

func isType(typ, session string) func(map[string]any) bool {
	return func(e map[string]any) bool { return e["type"] == typ && e["session_id"] == session }
}

func TestConcurrencyCapAndQueue(t *testing.T) {
	h := newHarness(t, 2, t.TempDir())
	h.send("a", "one", ModeAsk)
	h.send("b", "two", ModeAsk)
	if r := h.send("c", "three", ModeAsk); r["queued"] != true {
		t.Fatalf("third chat should queue: %v", r)
	}
	list := h.m.List()
	if list["running"] != 2 || list["queued"] != 1 {
		t.Fatalf("counts %v", list)
	}
	recv(t, h.live("a").sent)
	recv(t, h.live("b").sent)
	// Finishing a frees a slot; c starts.
	h.live("a").events <- continuex.LiveEvent{Kind: "text", Text: "done a"}
	h.live("a").events <- continuex.LiveEvent{Kind: "turn_done"}
	h.waitFor("a recorded", isType("live.recorded", "a"))
	if got := recv(t, h.live("c").sent); got != "three" {
		t.Fatalf("c got %q", got)
	}
	h.waitFor("c started", isType("live.turn_started", "c"))
	if l := h.m.List(); l["running"] != 2 || l["queued"] != 0 {
		t.Fatalf("after: %v", l)
	}
	// a finished while away: unread until attached.
	st := h.m.Attach("a")
	if st["status"] != StatusDone {
		t.Fatalf("a status %v", st["status"])
	}
	if len(h.saved) != 1 || h.saved[0].Answer != "done a" || h.saved[0].Prompt != "one" {
		t.Fatalf("saved %+v", h.saved)
	}
}

func TestBufferingAndReplay(t *testing.T) {
	h := newHarness(t, 6, t.TempDir())
	h.send("a", "hello", ModeAsk)
	f := h.live("a")
	recv(t, f.sent)
	for _, d := range []string{"Hel", "lo ", "wor", "ld"} {
		f.events <- continuex.LiveEvent{Kind: "text_delta", Text: d}
		time.Sleep(8 * time.Millisecond) // spans several flushes
	}
	f.events <- continuex.LiveEvent{Kind: "tool_start", ToolID: "t1", ToolName: "Read", ToolInput: json.RawMessage(`{"file_path":"x"}`)}
	f.events <- continuex.LiveEvent{Kind: "tool_end", ToolID: "t1", ToolOutput: "ok"}
	f.events <- continuex.LiveEvent{Kind: "text_delta", Text: " again"}
	h.waitFor("tool end", func(e map[string]any) bool { return e["kind"] == "tool_end" })
	h.waitFor("last delta", func(e map[string]any) bool { return e["kind"] == "text_delta" && e["text"] == " again" })

	// The chat was closed and reopened: the snapshot replays the turn.
	snap := h.m.Attach("a")
	if snap["status"] != StatusRunning || snap["prompt"] != "hello" {
		t.Fatalf("snapshot %v", snap)
	}
	events := snap["events"].([]map[string]any)
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e["kind"].(string))
	}
	if fmt.Sprint(kinds) != "[text_delta tool_start tool_end text_delta]" {
		t.Fatalf("replay kinds %v", kinds)
	}
	if events[0]["text"] != "Hello world" {
		t.Fatalf("merged delta %q", events[0]["text"])
	}
	// Sequence numbers grow, so the UI can drop events it already replayed.
	if events[3]["seq"].(uint64) <= events[0]["seq"].(uint64) || snap["seq"].(uint64) < events[3]["seq"].(uint64) {
		t.Fatal("seq order")
	}
	// Streaming continues after the snapshot.
	f.events <- continuex.LiveEvent{Kind: "text", Text: "Hello world again"}
	f.events <- continuex.LiveEvent{Kind: "turn_done"}
	rec := h.waitFor("recorded", isType("live.recorded", "a"))
	if rec["seq"].(uint64) <= snap["seq"].(uint64) {
		t.Fatal("recorded seq")
	}
}

func TestApprovalsModesAndRules(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, 6, dir)
	h.send("a", "work", ModeEdits)
	f := h.live("a")
	recv(t, f.sent)
	// Edits in the project are auto-approved in edits mode.
	f.events <- continuex.LiveEvent{Kind: "approval", RequestID: "r1", ToolName: "Edit", ToolInput: mustJSON(map[string]any{"file_path": filepath.Join(dir, "a.txt"), "old_string": "a", "new_string": "b"})}
	if got := recv(t, f.approvals); got != "r1:true" {
		t.Fatalf("edit: %s", got)
	}
	// Commands ask.
	f.events <- continuex.LiveEvent{Kind: "approval", RequestID: "r2", ToolName: "Bash", ToolInput: mustJSON(map[string]any{"command": "npm test"})}
	ev := h.waitFor("approval", func(e map[string]any) bool { return e["kind"] == "approval" && e["request_id"] == "r2" })
	if ev["action"].(Action).Summary != "Run `npm test`" {
		t.Fatalf("summary %v", ev["action"])
	}
	h.waitFor("status approval", func(e map[string]any) bool { return e["type"] == "live.status" && e["status"] == StatusApproval })
	if snap := h.m.Attach("a"); len(snap["pending"].([]map[string]any)) != 1 {
		t.Fatal("pending in snapshot")
	}
	// A second chat in the same project waits on the same command.
	_, err := h.m.Send(SendRequest{SessionID: "b", ProjectID: "a", Agent: "claude", Workspace: dir, Text: "more", Mode: ModeAsk})
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	fb := h.lives["a"] // same project key in the fake starter
	h.mu.Unlock()
	recv(t, fb.sent)
	fb.events <- continuex.LiveEvent{Kind: "approval", RequestID: "r3", ToolName: "Bash", ToolInput: mustJSON(map[string]any{"command": "npm test -- --watch"})}
	h.waitFor("b approval", func(e map[string]any) bool { return e["kind"] == "approval" && e["request_id"] == "r3" })
	// "Always allow in this project" answers both.
	if err := h.m.Approve("a", "r2", AllowProject, ""); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{recv(t, f.approvals): true}
	got[recv(t, fb.approvals)] = true
	if !got["r2:true"] || !got["r3:true"] {
		t.Fatalf("answers %v", got)
	}
	if rules := h.m.Rules.Rules("a"); len(rules) != 1 || rules[0].Prefix != "npm test" {
		t.Fatalf("rules %+v", rules)
	}
	// The rule applies from now on, without asking.
	f.events <- continuex.LiveEvent{Kind: "approval", RequestID: "r4", ToolName: "Bash", ToolInput: mustJSON(map[string]any{"command": "npm test"})}
	if got := recv(t, f.approvals); got != "r4:true" {
		t.Fatal(got)
	}
	// Deny, and allow for this chat only.
	f.events <- continuex.LiveEvent{Kind: "approval", RequestID: "r5", ToolName: "Bash", ToolInput: mustJSON(map[string]any{"command": "rm -rf build"})}
	h.waitFor("r5", func(e map[string]any) bool { return e["request_id"] == "r5" && e["kind"] == "approval" })
	_ = h.m.Approve("a", "r5", Deny, "")
	if got := recv(t, f.approvals); got != "r5:false" {
		t.Fatal(got)
	}
	f.events <- continuex.LiveEvent{Kind: "approval", RequestID: "r6", ToolName: "Bash", ToolInput: mustJSON(map[string]any{"command": "make build"})}
	h.waitFor("r6", func(e map[string]any) bool { return e["request_id"] == "r6" && e["kind"] == "approval" })
	_ = h.m.Approve("a", "r6", AllowChat, "")
	recv(t, f.approvals)
	f.events <- continuex.LiveEvent{Kind: "approval", RequestID: "r7", ToolName: "Bash", ToolInput: mustJSON(map[string]any{"command": "make build"})}
	if got := recv(t, f.approvals); got != "r7:true" {
		t.Fatal(got)
	}
	// Switching to full access answers what is waiting.
	f.events <- continuex.LiveEvent{Kind: "approval", RequestID: "r8", ToolName: "Bash", ToolInput: mustJSON(map[string]any{"command": "curl example.com"})}
	h.waitFor("r8", func(e map[string]any) bool { return e["request_id"] == "r8" && e["kind"] == "approval" })
	_ = h.m.SetMode("a", ModeFull)
	if got := recv(t, f.approvals); got != "r8:true" {
		t.Fatal(got)
	}
}

func TestFollowupsQueueAndSendNow(t *testing.T) {
	h := newHarness(t, 6, t.TempDir())
	h.send("a", "first", ModeAsk)
	f := h.live("a")
	recv(t, f.sent)
	if r := h.send("a", "second", ModeAsk); r["followup"] != true {
		t.Fatalf("follow-up %v", r)
	}
	h.send("a", "third", ModeAsk)
	if snap := h.m.Attach("a"); len(snap["followups"].([]map[string]any)) != 2 {
		t.Fatal("followups in snapshot")
	}
	if _, err := h.m.RemoveFollowup("a", 1); err != nil {
		t.Fatal(err)
	}
	// "Send now" goes first and stops the running turn.
	if _, err := h.m.Send(SendRequest{SessionID: "a", ProjectID: "a", Agent: "claude", Workspace: h.dir, Text: "urgent", When: "now"}); err != nil {
		t.Fatal(err)
	}
	recv(t, f.interrupt)
	f.events <- continuex.LiveEvent{Kind: "text", Text: "partial"}
	f.events <- continuex.LiveEvent{Kind: "turn_done", IsError: true, Text: "interrupted"}
	if got := recv(t, f.sent); got != "urgent" {
		t.Fatalf("next sent %q", got)
	}
	f.events <- continuex.LiveEvent{Kind: "text", Text: "ok"}
	f.events <- continuex.LiveEvent{Kind: "turn_done"}
	if got := recv(t, f.sent); got != "second" {
		t.Fatalf("then %q", got)
	}
}

func TestRestartShowsInterruptedAndResumes(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, 6, dir)
	h.send("a", "long job", ModeEdits)
	recv(t, h.live("a").sent)
	h.m.Shutdown() // Nexus quits mid-turn
	select {
	case <-h.live("a").closed:
	default:
		t.Fatal("agent left running after shutdown")
	}

	h2 := newHarness(t, 6, dir)
	snap := h2.m.Attach("a")
	if snap["status"] != StatusInterrupted {
		t.Fatalf("status %v", snap["status"])
	}
	if rec := snap["interrupted"].(*runningRecord); rec.Prompt != "long job" || rec.NativeID != "native-a" {
		t.Fatalf("record %+v", rec)
	}
	if _, err := h2.m.Resume("a"); err != nil {
		t.Fatal(err)
	}
	recv(t, h2.live("a").sent)
	if h2.starts[0].ResumeID != "native-a" || h2.starts[0].Mode != "acceptEdits" {
		t.Fatalf("resume spec %+v", h2.starts[0])
	}
	h2.live("a").events <- continuex.LiveEvent{Kind: "text", Text: "resumed"}
	h2.live("a").events <- continuex.LiveEvent{Kind: "turn_done"}
	h2.waitFor("recorded", isType("live.recorded", "a"))
	// Finished: nothing is left for a third start.
	h3 := newHarness(t, 6, dir)
	if st := h3.m.Attach("a")["status"]; st != StatusIdle {
		t.Fatalf("third start status %v", st)
	}
}

func TestProcessExitFailsTurnAndRestartsOnNextSend(t *testing.T) {
	h := newHarness(t, 6, t.TempDir())
	h.send("a", "x", ModeAsk)
	f := h.live("a")
	recv(t, f.sent)
	_ = f.Close()
	h.waitFor("exited", func(e map[string]any) bool { return e["kind"] == "exited" })
	h.mu.Lock()
	h.lives["a"] = nil
	h.mu.Unlock()
	h.send("a", "y", ModeAsk)
	if got := recv(t, h.live("a").sent); got != "y" {
		t.Fatal(got)
	}
}

func TestModeChangeRestartsOnSameSession(t *testing.T) {
	h := newHarness(t, 6, t.TempDir())
	h.send("a", "x", ModeAsk)
	f := h.live("a")
	recv(t, f.sent)
	f.events <- continuex.LiveEvent{Kind: "text", Text: "ok"}
	f.events <- continuex.LiveEvent{Kind: "turn_done"}
	h.waitFor("recorded", isType("live.recorded", "a"))
	h.mu.Lock()
	h.lives["a"] = nil
	h.mu.Unlock()
	h.send("a", "y", ModeFull)
	recv(t, h.live("a").sent)
	if len(h.starts) != 2 || h.starts[1].Mode != "bypassPermissions" || h.starts[1].ResumeID != "native-a" {
		t.Fatalf("starts %+v", h.starts)
	}
	select {
	case <-f.closed:
	case <-time.After(time.Second):
		t.Fatal("old process not closed")
	}
}
