package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"central-memory/internal/desktopbridge"
	"central-memory/internal/store"
)

func TestDesktopHTTPSStreamDeliversBeforeCompletionAndReconnects(t *testing.T) {
	s := newTestServer()
	mem := s.Store.(*store.MemStore)
	ctx := t.Context()
	project, e := mem.ResolveProject(ctx, "", "", "stream-project")
	if e != nil {
		t.Fatal(e)
	}
	_, _ = mem.ClaimProject(ctx, project.ID, "owner")
	session, e := mem.UpsertAgentSession(ctx, &store.AgentSession{ProjectID: project.ID, OwnerUserID: "owner", Harness: "codex", NativeID: "stream-native"})
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	repo := s.Desktop
	_ = repo.Register(ctx, desktopbridge.Device{ID: "device", Owner: "owner", KeyHash: desktopbridge.HashKey(strings.Repeat("a", 64)), Projects: []string{project.ID}, Harnesses: []string{"codex"}, Seen: now})
	_, e = repo.Create(ctx, desktopbridge.Job{Request: desktopbridge.Request{ID: "stream-job", DeviceID: "device", SessionID: session.ID, ProjectID: project.ID, Kind: "turn", Agent: "codex", Prompt: "hello"}, Owner: "owner", State: "queued", Revision: 1, Created: now, Updated: now, Expires: now.Add(time.Minute)})
	if e != nil {
		t.Fatal(e)
	}
	claimed, e := repo.Claim(ctx, "owner", "device", now)
	if e != nil {
		t.Fatal(e)
	}
	token := loginAs(t, s, "owner")
	tls := httptest.NewTLSServer(s.Handler())
	defer tls.Close()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, tls.URL+"/v1/desktop/jobs/stream-job/events", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, e := tls.Client().Do(request)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("not SSE", response.StatusCode)
	}
	updates := make(chan desktopbridge.Job, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 2<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				var job desktopbridge.Job
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &job) == nil {
					updates <- job
				}
			}
		}
	}()
	await := func(state, text string) {
		t.Helper()
		timer := time.NewTimer(4 * time.Second)
		defer timer.Stop()
		for {
			select {
			case update := <-updates:
				if update.State == state && update.Text == text {
					return
				}
			case <-timer.C:
				t.Fatalf("did not stream %s %q before completion", state, text)
			}
		}
	}
	await("awaiting_approval", "")
	running, e := repo.Update(ctx, "owner", "device", claimed.ID, desktopbridge.Update{Revision: claimed.Revision, State: "running", Text: "first streamed words", Activities: []json.RawMessage{json.RawMessage(`{"id":"one","tool":"read","status":"running"}`)}}, time.Now().UTC())
	if e != nil {
		t.Fatal(e)
	}
	await("running", "first streamed words") // no completion has been sent yet
	_, e = repo.Update(ctx, "owner", "device", claimed.ID, desktopbridge.Update{Revision: running.Revision, State: "completed", Text: "complete"}, time.Now().UTC())
	if e != nil {
		t.Fatal(e)
	}
	await("completed", "complete")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("terminal stream stayed open")
	}
	// Reconnect returns the terminal snapshot, without claiming/enqueuing work.
	again, _ := http.NewRequestWithContext(ctx, http.MethodGet, request.URL.String(), nil)
	again.Header = request.Header.Clone()
	last, e := tls.Client().Do(again)
	if e != nil {
		t.Fatal(e)
	}
	defer last.Body.Close()
	scan := bufio.NewScanner(last.Body)
	found := false
	for scan.Scan() {
		if strings.HasPrefix(scan.Text(), "data: ") {
			var job desktopbridge.Job
			_ = json.Unmarshal([]byte(strings.TrimPrefix(scan.Text(), "data: ")), &job)
			found = job.State == "completed" && job.Text == "complete"
		}
	}
	if !found {
		t.Fatal("reconnection lost final snapshot")
	}
	if next, _ := repo.Claim(ctx, "owner", "device", time.Now().UTC()); next != nil {
		t.Fatal("reconnect replayed execution")
	}
}
