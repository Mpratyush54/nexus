package chatstatus

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

type fakeCloud struct {
	mu     sync.Mutex
	bodies [][]Report
	read   []string
	err    error
}

func (f *fakeCloud) Do(_ context.Context, method, path string, body []byte) ([]byte, error) {
	if method != http.MethodPut || path != "/v1/chat-status" {
		panic("unexpected request " + method + " " + path)
	}
	var in struct {
		Items []Report `json:"items"`
	}
	_ = json.Unmarshal(body, &in)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.bodies = append(f.bodies, in.Items)
	return json.Marshal(map[string]any{"read": f.read})
}

type statusErr int

func (e statusErr) Error() string   { return "status" }
func (e statusErr) HTTPStatus() int { return int(e) }

func status(id, s string, unread bool) map[string]any {
	return map[string]any{"type": "live.status", "session_id": id, "status": s, "unread": unread}
}

func TestChangesAreSentTogetherAndTheLatestWins(t *testing.T) {
	cloud := &fakeCloud{}
	p := &Publisher{Cloud: func() Doer { return cloud }}
	p.Note(map[string]any{"type": "live.event", "session_id": "a", "kind": "text_delta"}) // not a status
	p.Note(status("a", "running", false))
	p.Note(status("b", "approval", false))
	p.Note(status("a", "done", true))
	p.Note(status("", "running", false))   // no chat
	p.Note(status("c", "exploded", false)) // unknown status
	sent := p.Flush(t.Context(), false)
	want := []Report{{"a", "done", true}, {"b", "approval", false}}
	if len(sent) != 2 || sent[0] != want[0] || sent[1] != want[1] {
		t.Fatalf("sent %+v", sent)
	}
	if again := p.Flush(t.Context(), false); len(again) != 0 {
		t.Fatalf("resent %+v", again)
	}
}

func TestKeepaliveRepeatsWorkAndClearsQuietChats(t *testing.T) {
	cloud := &fakeCloud{}
	listed := []Report{{"a", "running", false}, {"b", "done", true}, {"c", "done", false}}
	var readMu sync.Mutex
	var read []string
	p := &Publisher{
		Cloud: func() Doer { return cloud },
		List:  func() []Report { return listed },
		Read:  func(id string) { readMu.Lock(); read = append(read, id); readMu.Unlock() },
	}
	sent := p.Flush(t.Context(), true)
	if len(sent) != 2 || sent[0].SessionID != "a" || sent[1].SessionID != "b" {
		t.Fatalf("keepalive %+v", sent)
	}
	// "a" finished and its live.status was missed; "b" was read on the web.
	listed = []Report{{"b", "done", true}}
	cloud.read = []string{"b"}
	sent = p.Flush(t.Context(), true)
	if len(sent) != 2 || sent[0] != (Report{"a", "idle", false}) || sent[1].SessionID != "b" {
		t.Fatalf("second keepalive %+v", sent)
	}
	if len(read) != 1 || read[0] != "b" {
		t.Fatalf("read %v", read)
	}
}

func TestFailuresRetryAndOldServersAreLeftAlone(t *testing.T) {
	now := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	cloud := &fakeCloud{err: statusErr(http.StatusBadGateway)}
	var signedIn = false
	p := &Publisher{Cloud: func() Doer {
		if !signedIn {
			return nil
		}
		return cloud
	}, Now: func() time.Time { return now }}
	p.Note(status("a", "running", false))
	if sent := p.Flush(t.Context(), false); sent != nil {
		t.Fatalf("signed out but sent %+v", sent)
	}
	signedIn = true
	p.Flush(t.Context(), false) // 502: kept
	cloud.err = nil
	if sent := p.Flush(t.Context(), false); len(sent) != 1 {
		t.Fatalf("retry sent %+v", sent)
	}
	cloud.err = statusErr(http.StatusNotFound)
	p.Note(status("a", "done", true))
	p.Flush(t.Context(), false)
	cloud.err = nil
	p.Note(status("b", "running", false))
	if sent := p.Flush(t.Context(), false); sent != nil {
		t.Fatalf("an old server was asked again at once: %+v", sent)
	}
	now = now.Add(31 * time.Minute)
	if sent := p.Flush(t.Context(), false); len(sent) != 2 {
		t.Fatalf("after the pause %+v", sent)
	}
}

func TestRunDebouncesABurst(t *testing.T) {
	cloud := &fakeCloud{}
	p := &Publisher{Cloud: func() Doer { return cloud }, Debounce: 30 * time.Millisecond, Keepalive: time.Hour}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go p.Run(ctx)
	for _, s := range []string{"queued", "running", "approval", "running"} {
		p.Note(status("a", s, false))
		time.Sleep(2 * time.Millisecond)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cloud.mu.Lock()
		n := len(cloud.bodies)
		cloud.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond)
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	if len(cloud.bodies) != 1 || len(cloud.bodies[0]) != 1 || cloud.bodies[0][0].Status != "running" {
		t.Fatalf("bodies %+v", cloud.bodies)
	}
}
