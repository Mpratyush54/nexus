package desktopworker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
		progress(continuex.StreamProgress{Kind: "thinking", Text: "Looking at the request"})
		time.Sleep(30 * time.Millisecond)
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
func setupWorker(t *testing.T, approve func(context.Context, desktopbridge.Job, string, []string) bool) (*Worker, *desktopbridge.Memory, desktopbridge.Job, *fakeExecutor) {
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
		id := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/desktop/jobs/"), "/")[0]
		switch {
		case r.Method == "GET":
			out, err = m.Get(r.Context(), "owner", id)
		case strings.HasSuffix(r.URL.Path, "/progress"):
			var u desktopbridge.Update
			_ = json.NewDecoder(r.Body).Decode(&u)
			out, err = m.Update(r.Context(), "owner", "device", id, u, time.Now().UTC())
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
	w, m, j, exec := setupWorker(t, func(context.Context, desktopbridge.Job, string, []string) bool { return false })
	w.execute(t.Context(), j)
	got, _ := m.Get(t.Context(), "owner", j.ID)
	if exec.calls.Load() != 0 || got.State != "failed" || !strings.Contains(got.Error, "declined") {
		t.Fatalf("calls=%d job=%+v", exec.calls.Load(), got)
	}
}
func TestApprovedBrowserTurnStreamsAndPersistsDiffs(t *testing.T) {
	w, m, j, exec := setupWorker(t, func(context.Context, desktopbridge.Job, string, []string) bool { return true })
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
			// The thinking stays with the finished job, timed to the first words.
			if got.Thinking != "Looking at the request" || got.ThinkingMS < 25 || got.ThinkingMS > 2000 {
				t.Fatalf("thinking = %q, %d ms", got.Thinking, got.ThinkingMS)
			}
			return
		case <-deadline:
			t.Fatal("worker did not finish")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func TestBrowserCancellationStopsOnlyItsTurn(t *testing.T) {
	w, m, j, exec := setupWorker(t, func(context.Context, desktopbridge.Job, string, []string) bool { return true })
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

// recordingExecutor keeps the plan it was asked to run.
type recordingExecutor struct {
	fakeExecutor
	plan continuex.LaunchPlan
}

func (e *recordingExecutor) TurnStreaming(ctx context.Context, p continuex.LaunchPlan, root string, progress func(continuex.StreamProgress)) (continuex.TurnOutput, error) {
	e.plan = p
	return continuex.TurnOutput{Text: "I see a login form."}, nil
}

func imageJob(t *testing.T, w *Worker, m *desktopbridge.Memory, agent string) desktopbridge.Job {
	t.Helper()
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 24)...)
	now := time.Now().UTC()
	req := desktopbridge.Request{ID: "img-job", DeviceID: "device", SessionID: "session", ProjectID: "project", Agent: agent, Kind: "turn", Prompt: "what is on screen?",
		Attachments: []desktopbridge.Attachment{{Name: "shot.png", MediaType: "image/png", Data: base64.StdEncoding.EncodeToString(png)}}}
	if err := req.PrepareAttachments(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(t.Context(), desktopbridge.Job{Request: req, Owner: "owner", State: "queued", Revision: 1, Created: now, Expires: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	claimed, err := m.Claim(t.Context(), "owner", "device", now)
	if err != nil || claimed == nil {
		t.Fatal("claim", err)
	}
	return *claimed
}

func TestBrowserImagesReachTheHarnessAfterApproval(t *testing.T) {
	var shown []string
	w, m, first, _ := setupWorker(t, func(_ context.Context, _ desktopbridge.Job, _ string, images []string) bool {
		shown = images
		return true
	})
	_, _ = m.Cancel(t.Context(), "owner", first.ID, time.Now().UTC())
	w.ImageDir = t.TempDir()
	exec := &recordingExecutor{}
	deps := w.Deps
	w.Deps = func() core.Deps { d := deps(); d.ContinueExecutor = exec; return d }
	j := imageJob(t, w, m, "codex")
	w.execute(t.Context(), j)
	got, _ := m.Get(t.Context(), "owner", j.ID)
	if got.State != "completed" {
		t.Fatalf("job %+v", got)
	}
	if len(shown) != 1 || filepath.Dir(shown[0]) != w.ImageDir {
		t.Fatalf("approval must show the saved image: %v", shown)
	}
	if argv := strings.Join(exec.plan.Argv, " "); !strings.Contains(argv, "--image "+shown[0]) {
		t.Fatalf("codex must receive the image: %s", argv)
	}
	if data, err := os.ReadFile(shown[0]); err != nil || !bytes.HasPrefix(data, []byte("\x89PNG")) {
		t.Fatal("saved image is wrong", err)
	}
}

func TestDeclinedImageTurnRemovesSavedImages(t *testing.T) {
	var shown []string
	w, m, first, _ := setupWorker(t, func(_ context.Context, _ desktopbridge.Job, _ string, images []string) bool {
		shown = images
		return false
	})
	_, _ = m.Cancel(t.Context(), "owner", first.ID, time.Now().UTC())
	w.ImageDir = t.TempDir()
	j := imageJob(t, w, m, "claude")
	w.execute(t.Context(), j)
	if len(shown) != 1 {
		t.Fatal("approval never saw the image")
	}
	if _, err := os.Stat(shown[0]); !os.IsNotExist(err) {
		t.Fatal("declined image left on disk")
	}
}

func TestImagesForAHarnessWithoutImageSupportFailClearly(t *testing.T) {
	asked := false
	w, m, first, _ := setupWorker(t, func(context.Context, desktopbridge.Job, string, []string) bool { asked = true; return true })
	_, _ = m.Cancel(t.Context(), "owner", first.ID, time.Now().UTC())
	w.ImageDir = t.TempDir()
	j := imageJob(t, w, m, "cursor")
	w.execute(t.Context(), j)
	got, _ := m.Get(t.Context(), "owner", j.ID)
	if asked || got.State != "failed" || !strings.Contains(got.Error, "Cursor can't take images") {
		t.Fatalf("asked=%v job=%+v", asked, got)
	}
}

func TestClientSendsProtocolVersion(t *testing.T) {
	var header string
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		header = r.Header.Get(desktopbridge.ProtocolHeader)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})}
	c := desktopbridge.Client{BaseURL: "https://nexus.test", Token: "t", Key: "k", HTTP: client}
	if err := c.Call(t.Context(), "GET", "/v1/desktop/devices", nil, nil); err != nil || header != "2" {
		t.Fatalf("protocol header %q err %v", header, err)
	}
}

func TestThinkingClockStopsAtTheFirstWords(t *testing.T) {
	var c thinkingClock
	t0 := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	if c.ms(t0) != 0 {
		t.Fatal("no thinking yet")
	}
	c.replied(t0) // text before any thinking: nothing to time
	c.thinking(t0)
	c.thinking(t0.Add(3 * time.Second))
	if got := c.ms(t0.Add(5 * time.Second)); got != 5000 {
		t.Fatalf("while thinking: %d", got)
	}
	c.replied(t0.Add(7 * time.Second))
	c.replied(t0.Add(9 * time.Second))
	if got := c.ms(t0.Add(time.Minute)); got != 7000 {
		t.Fatalf("after the reply started: %d", got)
	}
}

func TestCapThinkingFitsTheServerLimit(t *testing.T) {
	got := capThinking(strings.Repeat("думаю ", 20000))
	if len(got) > desktopbridge.MaxThinkingBytes || !strings.HasPrefix(got, "…") {
		t.Fatalf("capped to %d bytes", len(got))
	}
}
