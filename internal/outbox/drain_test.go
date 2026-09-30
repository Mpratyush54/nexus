package outbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

type fakeCloud struct {
	mu     sync.Mutex
	posts  []recorded
	failOn map[string]int // path -> failures remaining before success
	calls  map[string]int
}

type recorded struct {
	Method string
	Path   string
	Body   []byte
}

func (f *fakeCloud) Do(_ context.Context, method, path string, body []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[path]++
	if n := f.failOn[path]; n > 0 {
		f.failOn[path] = n - 1
		return nil, errors.New("simulated upload failure")
	}
	f.posts = append(f.posts, recorded{Method: method, Path: path, Body: append([]byte(nil), body...)})
	return []byte(`{"ok":true}`), nil
}

func TestDrainOfflineEnqueueThenAck(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate offline capture: enqueue without a live cloud.
	if err := CaptureEnqueue(s, "t1", "turn", "POST", "/v1/agent-sessions/s1/turns", []byte(`{"idx":1,"role":"user"}`)); err != nil {
		t.Fatal(err)
	}
	if err := CaptureEnqueue(s, "t2", "turn", "POST", "/v1/agent-sessions/s1/turns", []byte(`{"idx":2,"role":"assistant"}`)); err != nil {
		t.Fatal(err)
	}
	if err := CaptureEnqueue(s, "b1", "blob", "PUT", "/v1/blobs/abc", []byte(`{"project_id":"p1","body_b64":"e30="}`)); err != nil {
		t.Fatal(err)
	}
	if err := CaptureEnqueue(s, "m1", "manifest", "POST", "/v1/agent-sessions/s1/versions/1/complete", []byte(`{"transcript":{"blob":"sha256:abc"}}`)); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Pending()); n != 4 {
		t.Fatalf("pending before drain = %d", n)
	}

	cloud := &fakeCloud{}
	res, err := Drain(context.Background(), s, cloud)
	if err != nil {
		t.Fatal(err)
	}
	if res.Uploaded != 4 || res.Pending != 0 {
		t.Fatalf("result = %+v", res)
	}
	if len(s.Pending()) != 0 {
		t.Fatalf("pending after ack = %+v", s.Pending())
	}
	if len(cloud.posts) != 4 {
		t.Fatalf("posts = %d", len(cloud.posts))
	}
	// Kind order: turns, then blob, then manifest.
	wantPaths := []string{
		"/v1/agent-sessions/s1/turns",
		"/v1/agent-sessions/s1/turns",
		"/v1/blobs/abc",
		"/v1/agent-sessions/s1/versions/1/complete",
	}
	for i, p := range wantPaths {
		if cloud.posts[i].Path != p {
			t.Fatalf("post[%d] path = %s want %s", i, cloud.posts[i].Path, p)
		}
	}
	if cloud.posts[2].Method != "PUT" {
		t.Fatalf("blob method = %s", cloud.posts[2].Method)
	}
}

func TestDrainMidFailureThenRetryZeroLoss(t *testing.T) {
	s, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("t%d", i)
		path := "/v1/agent-sessions/s1/turns"
		body := []byte(fmt.Sprintf(`{"idx":%d}`, i))
		if err := CaptureEnqueue(s, id, "turn", "POST", path, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := CaptureEnqueue(s, "b1", "blob", "PUT", "/v1/blobs/fail-once", []byte(`{"project_id":"p"}`)); err != nil {
		t.Fatal(err)
	}
	if err := CaptureEnqueue(s, "m1", "manifest", "POST", "/v1/agent-sessions/s1/versions/1/complete", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	cloud := &fakeCloud{failOn: map[string]int{"/v1/blobs/fail-once": 1}}
	res, err := Drain(context.Background(), s, cloud)
	if err == nil {
		t.Fatal("expected mid-drain failure")
	}
	if res.Uploaded != 3 {
		t.Fatalf("uploaded before fail = %d", res.Uploaded)
	}
	pending := s.Pending()
	if len(pending) != 2 {
		t.Fatalf("pending after fail = %+v", pending)
	}
	ids := map[string]bool{}
	for _, it := range pending {
		ids[it.ID] = true
	}
	if !ids["b1"] || !ids["m1"] {
		t.Fatalf("expected blob+manifest retained, got %+v", pending)
	}
	if pending[0].ID == "b1" && pending[0].Attempt < 1 {
		t.Fatalf("blob attempt not bumped: %+v", pending[0])
	}

	// Retry: same cloud now succeeds; zero loss — remaining items upload once.
	res, err = Drain(context.Background(), s, cloud)
	if err != nil {
		t.Fatal(err)
	}
	if res.Uploaded != 2 || res.Pending != 0 {
		t.Fatalf("retry result = %+v", res)
	}
	if len(s.Pending()) != 0 {
		t.Fatalf("pending not empty: %+v", s.Pending())
	}
	// Blob path was attempted twice (fail + success); body posted only once on success.
	if cloud.calls["/v1/blobs/fail-once"] != 2 {
		t.Fatalf("blob attempts = %d", cloud.calls["/v1/blobs/fail-once"])
	}
	blobPosts := 0
	for _, p := range cloud.posts {
		if p.Path == "/v1/blobs/fail-once" {
			blobPosts++
		}
	}
	if blobPosts != 1 {
		t.Fatalf("successful blob posts = %d", blobPosts)
	}
}
