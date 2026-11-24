package desktopworker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"central-memory/internal/continuex"
	"central-memory/internal/core"
	"central-memory/internal/desktopbridge"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fakeCloud struct {
	project string
	writes  atomic.Int32
}

func (c *fakeCloud) Do(_ context.Context, method, path string, _ []byte) ([]byte, error) {
	if method == "POST" {
		c.writes.Add(1)
		return []byte(`{"created":2}`), nil
	}
	if strings.Contains(path, "/fetch?") {
		return []byte(`{"items":[{"role":"user","text_preview":"earlier context"}],"next_cursor":""}`), nil
	}
	return json.Marshal(map[string]string{"project_id": c.project})
}

type fakeExecutor struct {
	calls   atomic.Int32
	started chan struct{}
	wait    bool
}

func (e *fakeExecutor) Turn(ctx context.Context, p continuex.LaunchPlan, root string) (string, error) {
	r, err := e.TurnStreaming(ctx, p, root, nil)
	return r.Text, err
}
func (e *fakeExecutor) TurnStreaming(ctx context.Context, _ continuex.LaunchPlan, _ string, progress func(continuex.StreamProgress)) (continuex.TurnOutput, error) {
	e.calls.Add(1)
	if e.started != nil {
		close(e.started)
	}
	if progress != nil {
		progress(continuex.StreamProgress{Kind: "text", Text: "partial text"})
		progress(continuex.StreamProgress{Kind: "activity", Text: `{"id":"tool-1","tool":"read","status":"completed","input":"README.md"}`})
	}
	if e.wait {
		<-ctx.Done()
		return continuex.TurnOutput{}, ctx.Err()
	}
	select {
	case <-ctx.Done():
		return continuex.TurnOutput{}, ctx.Err()
	case <-time.After(600 * time.Millisecond):
	}
	return continuex.TurnOutput{Text: "full text", ChangedFiles: []string{"a.txt"}, FileDiffs: []continuex.FileDiff{{Path: "a.txt", Diff: "@@ -1 +1 @@\n-old\n+new"}}}, nil
}
func setupWorker(t *testing.T, approve func(context.Context, desktopbridge.Job, string) bool) (*Worker, *desktopbridge.Memory, desktopbridge.Job, *fakeExecutor) {
	t.Helper()
	m := desktopbridge.NewMemory()
	ctx := t.Context()
	now := time.Now().UTC()
	j := desktopbridge.Job{Request: desktopbridge.Request{ID: "job", DeviceID: "device", SessionID: "session", ProjectID: "project", Agent: "codex", Kind: "turn", Prompt: "hello"}, Owner: "owner", State: "queued", Revision: 1, Created: now, Expires: now.Add(time.Minute)}
	_, _ = m.Create(ctx, j)
	claimed, e := m.Claim(ctx, "owner", "device", now)
	if e != nil {
		t.Fatal(e)
	}
	j = *claimed
	exec := &fakeExecutor{}
	cloud := &fakeCloud{project: "project"}
	root := t.TempDir()
	dir := t.TempDir()
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer test" || r.Header.Get("X-Nexus-Desktop-Key") != "key" {
			t.Error("missing authentication")
		}
		var out any
		var err error
		switch {
		case r.Method == "GET":
			out, err = m.Get(r.Context(), "owner", "job")
		case strings.HasSuffix(r.URL.Path, "/progress"):
			var u desktopbridge.Update
			_ = json.NewDecoder(r.Body).Decode(&u)
			out, err = m.Update(r.Context(), "owner", "device", "job", u, time.Now().UTC())
		}
		code := 200
		if err != nil {
			code = 409
		}
		b, _ := json.Marshal(out)
		return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(b)), Header: make(http.Header)}, nil
	})}
	w := &Worker{ID: "device", Client: desktopbridge.Client{BaseURL: "https://nexus.test", Token: "test", Key: "key", HTTP: client}, Roots: func() map[string]string { return map[string]string{"project": root} }, Approve: approve,
		Deps: func() core.Deps {
			return core.Deps{ContinueExecutor: exec, Cloud: cloud, Online: true, ContinuationDir: dir}
		}, Report: func(s string) { t.Log(s) },
	}
	return w, m, j, exec
}
func TestDeclinedBrowserTurnNeverExecutes(t *testing.T) {
	w, m, j, exec := setupWorker(t, func(context.Context, desktopbridge.Job, string) bool { return false })
	w.execute(t.Context(), j)
	got, _ := m.Get(t.Context(), "owner", j.ID)
	if exec.calls.Load() != 0 || got.State != "failed" || !strings.Contains(got.Error, "declined") {
		t.Fatalf("calls=%d job=%+v", exec.calls.Load(), got)
	}
}
func TestApprovedBrowserTurnStreamsAndPersistsDiffs(t *testing.T) {
	w, m, j, exec := setupWorker(t, func(context.Context, desktopbridge.Job, string) bool { return true })
	done := make(chan struct{})
	go func() { defer close(done); w.execute(t.Context(), j) }()
	deadline := time.After(5 * time.Second)
	sawLive := false
	for {
		got, _ := m.Get(t.Context(), "owner", j.ID)
		if got.State == "running" && got.Text == "partial text" && len(got.Activities) > 0 {
			sawLive = true
		}
		select {
		case <-done:
			got, _ = m.Get(t.Context(), "owner", j.ID)
			if !sawLive || got.State != "completed" || !strings.Contains(string(got.Result), "-old\\n+new") || exec.calls.Load() != 1 {
				t.Fatalf("live=%v calls=%d job=%+v", sawLive, exec.calls.Load(), got)
			}
			return
		case <-deadline:
			t.Fatal("worker did not finish")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func TestBrowserCancellationStopsOnlyItsTurn(t *testing.T) {
	w, m, j, exec := setupWorker(t, func(context.Context, desktopbridge.Job, string) bool { return true })
	exec.wait = true
	exec.started = make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); w.execute(t.Context(), j) }()
	select {
	case <-exec.started:
	case <-time.After(3 * time.Second):
		t.Fatal("not started")
	}
	_, _ = m.Cancel(t.Context(), "owner", j.ID, time.Now().UTC())
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("cancel did not reach executor")
	}
	got, _ := m.Get(t.Context(), "owner", j.ID)
	if got.State != "cancelled" {
		t.Fatal("late result overwrote cancel", got.State)
	}
}
