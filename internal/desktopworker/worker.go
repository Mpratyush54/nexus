// Package desktopworker runs only inside the native application. Browsers
// enqueue typed requests; no cloud-supplied path, argv or RPC is executable.
package desktopworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"central-memory/internal/continuex"
	"central-memory/internal/core"
	"central-memory/internal/desktopbridge"
)

// UnsupportedProblem is reported when the connected server has no desktop
// channel (POST /v1/desktop/devices answers 404).
const UnsupportedProblem = "server-unsupported: this Nexus server does not offer browser-to-desktop execution yet"

type Worker struct {
	Client   desktopbridge.Client
	ID, Name string
	// Roots returns only locally approved project bindings, never cloud paths.
	Roots func() map[string]string
	// Approve asks the person on this computer; images are the saved
	// attachments so the prompt can show them before anything runs.
	Approve func(ctx context.Context, job desktopbridge.Job, root string, images []string) bool
	// ImageDir is where browser images are saved for the agent (default:
	// the user cache folder). Kept so the chat can show them afterwards.
	ImageDir string
	Deps     func() core.Deps
	Report   func(string)
}

func (w *Worker) report(e error) {
	if e != nil && w.Report != nil {
		w.Report(e.Error())
	}
}
func (w *Worker) register(ctx context.Context) error {
	projects := []string{}
	for id := range w.Roots() {
		projects = append(projects, id)
	}
	sort.Strings(projects)
	agents := []string{}
	for _, a := range []string{"codex", "cursor", "opencode", "claude"} {
		if continuex.DetectHarness(a).Installed {
			agents = append(agents, a)
		}
	}
	return w.Client.Call(ctx, http.MethodPost, "/v1/desktop/devices", map[string]any{"id": w.ID, "name": w.Name, "projects": projects, "harnesses": agents}, nil)
}
func (w *Worker) Run(ctx context.Context) {
	if w.Roots == nil || w.Deps == nil || w.Approve == nil {
		return
	}
	// One heartbeat worker and one claim loop, not one process per poll/token.
	heartbeatDone := make(chan struct{})
	// A server without the desktop routes answers 404: say so once, plainly,
	// and stop polling claims until a heartbeat finds the routes.
	var unsupported atomic.Bool
	go func() {
		defer close(heartbeatDone)
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			err := w.register(ctx)
			unsupported.Store(desktopbridge.IsMissingRoute(err))
			if unsupported.Load() {
				if w.Report != nil {
					w.Report(UnsupportedProblem)
				}
			} else if err != nil {
				w.report(err)
			} else if w.Report != nil {
				w.Report("")
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	defer func() { <-heartbeatDone }()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if unsupported.Load() {
			continue
		}
		var response struct {
			Job *desktopbridge.Job `json:"job"`
		}
		if e := w.Client.Call(ctx, http.MethodPost, "/v1/desktop/devices/"+url.PathEscape(w.ID)+"/claim", struct{}{}, &response); e != nil {
			w.report(e)
			continue
		}
		if response.Job != nil {
			w.execute(ctx, *response.Job)
		}
	}
}
func (w *Worker) execute(parent context.Context, j desktopbridge.Job) {
	ctx, cancel := context.WithDeadline(parent, j.Expires)
	defer cancel()
	// Cancellation is observed even while a native approval dialog is open.
	done := make(chan struct{})
	defer close(done)
	go w.watch(ctx, cancel, j.ID, done)
	root := w.Roots()[j.ProjectID]
	if root == "" {
		w.finish(parent, j, "failed", nil, errors.New("Workspace is no longer approved on this device"))
		return
	}
	images, discard, e := w.saveAttachments(j)
	if e != nil {
		w.finish(parent, j, "failed", nil, e)
		return
	}
	ran := false
	defer func() {
		if !ran {
			discard()
		}
	}()
	if j.Kind == "turn" && !w.Approve(ctx, j, root, images) {
		w.finish(parent, j, "failed", nil, errors.New("Request was declined or approval expired in Nexus Desktop"))
		return
	}
	if ctx.Err() != nil {
		return
	}
	// Consent can be revoked while the dialog is open.
	if w.Roots()[j.ProjectID] != root {
		w.finish(parent, j, "failed", nil, errors.New("Workspace approval changed"))
		return
	}
	var running desktopbridge.Job
	if e := w.Client.Call(ctx, http.MethodPost, "/v1/desktop/jobs/"+url.PathEscape(j.ID)+"/progress", desktopbridge.Update{Revision: j.Revision, State: "running", Status: "Preparing agent"}, &running); e != nil {
		return
	}
	j = running
	// Approval and execution have different bounded deadlines.
	cancel()
	ctx, runCancel := context.WithDeadline(parent, j.Expires)
	defer runCancel()
	// The watcher above owns the approval context; watch the execution context
	// separately so browser cancellation still stops this exact turn.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go w.watch(ctx, runCancel, j.ID, watchDone)
	if j.Kind == "models" {
		catalog, e := continuex.HarnessModelsCached(ctx, j.Agent, false)
		w.finish(parent, j, "completed", catalog, e)
		return
	}
	deps := w.Deps()
	deps.Root = root
	var session struct {
		ProjectID string `json:"project_id"`
		Title     string `json:"title"`
	}
	if deps.Cloud == nil {
		w.finish(parent, j, "failed", nil, core.ErrOffline)
		return
	}
	data, e := deps.Cloud.Do(ctx, http.MethodGet, "/v1/agent-sessions/"+url.PathEscape(j.SessionID), nil)
	if e != nil || json.Unmarshal(data, &session) != nil || session.ProjectID != j.ProjectID {
		w.finish(parent, j, "failed", nil, errors.New("Could not verify the session project"))
		return
	}
	// A browser can select another harness or device. Seed from authorized
	// conversation text, never attempt to resume an unrelated native ID.
	contextText, e := w.sessionContext(ctx, deps, j.SessionID)
	if e != nil {
		w.finish(parent, j, "failed", nil, e)
		return
	}
	var mu sync.Mutex
	progress := desktopbridge.Update{Revision: j.Revision, State: "running", Status: "Starting " + j.Agent}
	var thought thinkingClock
	deps.ContinueProgress = func(_ string, p continuex.StreamProgress) {
		mu.Lock()
		defer mu.Unlock()
		switch p.Kind {
		case "text":
			if len(p.Text) <= 512<<10 {
				progress.Text = p.Text
			}
			thought.replied(time.Now())
			progress.ThinkingMS = thought.ms(time.Now())
		case "thinking":
			progress.Thinking = capThinking(p.Text)
			thought.thinking(time.Now())
			progress.ThinkingMS = thought.ms(time.Now())
		case "status":
			progress.Status = p.Text
		case "activity":
			if len(p.Text) > 64<<10 {
				return
			}
			var a struct {
				ID string `json:"id"`
			}
			if json.Unmarshal([]byte(p.Text), &a) != nil {
				return
			}
			for i, old := range progress.Activities {
				var b struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(old, &b)
				if a.ID != "" && a.ID == b.ID {
					progress.Activities[i] = json.RawMessage(p.Text)
					return
				}
			}
			if len(progress.Activities) < 100 {
				progress.Activities = append(progress.Activities, json.RawMessage(p.Text))
			}
		}
	}
	flushDone := make(chan struct{})
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		tick := time.NewTicker(350 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-flushDone:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			mu.Lock()
			progress.ThinkingMS = thought.ms(time.Now())
			snapshot := progress
			snapshot.Activities = append([]json.RawMessage(nil), progress.Activities...)
			mu.Unlock()
			var updated desktopbridge.Job
			if e := w.Client.Call(ctx, http.MethodPost, "/v1/desktop/jobs/"+url.PathEscape(j.ID)+"/progress", snapshot, &updated); e != nil {
				runCancel()
				return
			}
			mu.Lock()
			progress.Revision = updated.Revision
			mu.Unlock()
		}
	}()
	ran = true
	args, _ := json.Marshal(map[string]any{"session_id": j.SessionID, "agent": j.Agent, "prompt": j.Prompt, "model": j.Model, "resume": "seeded", "context": contextText, "images": images, "workspace_root": root, "trust_workspace": true, "request_id": j.ID})
	result, e := core.Call(ctx, "continue.here", args, deps)
	close(flushDone)
	<-flushed
	mu.Lock()
	j.Revision = progress.Revision
	j.Text = progress.Text
	j.Activities = progress.Activities
	thought.replied(time.Now())
	j.Thinking, j.ThinkingMS = progress.Thinking, thought.ms(time.Now())
	mu.Unlock()
	if ctx.Err() != nil {
		w.finish(parent, j, "failed", nil, errors.New("Execution interrupted; check the workspace before retrying"))
		return
	}
	w.finish(parent, j, "completed", result, e)
}

// saveAttachments checks a job's images again (never trusting the server's
// validation alone) and writes them where the agent can read them. discard
// removes them when the turn never runs.
func (w *Worker) saveAttachments(j desktopbridge.Job) (paths []string, discard func(), err error) {
	discard = func() {}
	if len(j.Attachments) == 0 {
		return nil, discard, nil
	}
	if j.Kind != "turn" || len(j.Attachments) > desktopbridge.MaxAttachments {
		return nil, discard, errors.New("This request carries more images than Nexus accepts")
	}
	if !imageHarness(j.Agent) {
		return nil, discard, fmt.Errorf("%s can't take images from the web yet. Choose Claude Code, Codex, or OpenCode", agentLabel(j.Agent))
	}
	dir := w.ImageDir
	if dir == "" {
		root, e := os.UserCacheDir()
		if e != nil {
			return nil, discard, e
		}
		dir = filepath.Join(root, "Nexus", "relay-images")
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, discard, e
	}
	pruneImages(dir, 200)
	discard = func() {
		for _, p := range paths {
			_ = os.Remove(p)
		}
	}
	for i, a := range j.Attachments {
		data, e := desktopbridge.DecodeAttachment(a)
		if e != nil {
			discard()
			return nil, func() {}, e
		}
		ext := continuex.ImageExtension(a.MediaType)
		name := fmt.Sprintf("web-%s-%d%s", safeID(j.ID), i+1, ext)
		path := filepath.Join(dir, name)
		if e := os.WriteFile(path, data, 0600); e != nil {
			discard()
			return nil, func() {}, e
		}
		paths = append(paths, path)
	}
	return paths, discard, nil
}

func imageHarness(agent string) bool {
	return agent == "claude" || agent == "codex" || agent == "opencode"
}

func agentLabel(agent string) string {
	switch agent {
	case "cursor":
		return "Cursor"
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "opencode":
		return "OpenCode"
	}
	return agent
}

func safeID(id string) string {
	out := []rune{}
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			out = append(out, r)
		}
		if len(out) >= 40 {
			break
		}
	}
	return string(out)
}

// pruneImages keeps the newest files so saved images cannot grow forever.
func pruneImages(dir string, keep int) {
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) < keep {
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		a, _ := entries[i].Info()
		b, _ := entries[j].Info()
		return a != nil && b != nil && a.ModTime().After(b.ModTime())
	})
	for _, entry := range entries[keep-1:] {
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

func (w *Worker) watch(ctx context.Context, cancel context.CancelFunc, id string, done <-chan struct{}) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	failures := 0
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		var j desktopbridge.Job
		if e := w.Client.Call(ctx, http.MethodGet, "/v1/desktop/jobs/"+url.PathEscape(id), nil, &j); e != nil {
			failures++
			if failures >= 3 {
				cancel()
				return
			}
			continue
		}
		failures = 0
		if desktopbridge.Terminal(j.State) {
			cancel()
			return
		}
	}
}
func (w *Worker) finish(parent context.Context, j desktopbridge.Job, state string, result any, e error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	u := desktopbridge.Update{Revision: j.Revision, State: state, Text: j.Text, Activities: j.Activities, Thinking: j.Thinking, ThinkingMS: j.ThinkingMS}
	if e != nil {
		u.State = "failed"
		u.Error = e.Error()
		if len(u.Error) > 4000 {
			u.Error = u.Error[:4000]
		}
	}
	if result != nil {
		u.Result, _ = json.Marshal(result)
		if len(u.Result) > 1<<20 {
			u.Result = nil
			u.State = "failed"
			u.Error = "Result exceeded the remote display limit; the local transcript is retained"
		}
	}
	w.report(w.Client.Call(ctx, http.MethodPost, "/v1/desktop/jobs/"+url.PathEscape(j.ID)+"/progress", u, nil))
}
func (w *Worker) sessionContext(ctx context.Context, deps core.Deps, id string) (string, error) {
	// Fetch pages in source order; bounded total bytes and pages avoid unbounded
	// browser-triggered work. Explicitly fail rather than silently omit history.
	cursor := ""
	text := ""
	for page := 0; page < 100; page++ {
		path := "/v1/agent-sessions/" + url.PathEscape(id) + "/fetch?limit=100&cursor=" + url.QueryEscape(cursor)
		b, e := deps.Cloud.Do(ctx, http.MethodGet, path, nil)
		if e != nil {
			return "", e
		}
		var p struct {
			Items []struct {
				Role string `json:"role"`
				Text string `json:"text_preview"`
			} `json:"items"`
			Next string `json:"next_cursor"`
		}
		if e = json.Unmarshal(b, &p); e != nil {
			return "", e
		}
		for _, turn := range p.Items {
			if turn.Role == "user" || turn.Role == "assistant" {
				text += fmt.Sprintf("\n%s: %s", turn.Role, turn.Text)
				if len(text) > 12000 {
					text = text[len(text)-12000:]
				}
			}
		}
		if p.Next == "" {
			return continuex.SafeContinuationContext(strings.ToValidUTF8(text, "")), nil
		}
		cursor = p.Next
	}
	return "", errors.New("Conversation is too large to seed safely; fork a shorter conversation first")
}

// thinkingClock times the agent's thinking: from the first thinking it
// reports to the first words of its reply (or the end of the turn).
type thinkingClock struct{ start, end time.Time }

func (c *thinkingClock) thinking(now time.Time) {
	if c.start.IsZero() {
		c.start = now
	}
}

func (c *thinkingClock) replied(now time.Time) {
	if !c.start.IsZero() && c.end.IsZero() {
		c.end = now
	}
}

// ms is how long it has thought so far (0 when it has not).
func (c *thinkingClock) ms(now time.Time) int64 {
	if c.start.IsZero() {
		return 0
	}
	end := now
	if !c.end.IsZero() {
		end = c.end
	}
	ms := end.Sub(c.start).Milliseconds()
	if limit := int64(desktopbridge.RunLifetime / time.Millisecond); ms > limit {
		ms = limit
	}
	return ms
}

// capThinking keeps thinking within what the server accepts.
func capThinking(text string) string {
	text = continuex.CapThinking(text)
	if len(text) > desktopbridge.MaxThinkingBytes {
		text = text[len(text)-desktopbridge.MaxThinkingBytes:]
		text = strings.ToValidUTF8(text, "")
	}
	return text
}
