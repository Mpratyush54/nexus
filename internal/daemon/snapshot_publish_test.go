package daemon

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPublishTrackBackoffAndPermanentErrors(t *testing.T) {
	if got := []time.Duration{snapshotBackoff(1), snapshotBackoff(2), snapshotBackoff(3), snapshotBackoff(4), snapshotBackoff(9)}; got[0] != 5*time.Minute ||
		got[1] != 15*time.Minute || got[2] != time.Hour || got[3] != 6*time.Hour || got[4] != 6*time.Hour {
		t.Fatalf("backoff steps = %v", got)
	}
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	var tr publishTrack
	if tr.fail(now, &mirrorStatusError{status: 500, msg: "boom"}, "s1", 10) {
		t.Fatal("500 is transient")
	}
	if tr.due(now.Add(4*time.Minute), "s2", 10) || !tr.due(now.Add(5*time.Minute), "s2", 10) {
		t.Fatal("first failure waits 5 minutes, content change or not")
	}
	tr.fail(now, errors.New("Post: context deadline exceeded"), "s2", 10)
	if tr.due(now.Add(14*time.Minute), "s3", 10) {
		t.Fatal("second failure waits 15 minutes")
	}
	tr.succeed()
	if !tr.due(now, "s3", 10) {
		t.Fatal("success resets")
	}
	// 413: this size (and larger) is not sent again; smaller content is.
	if !tr.fail(now, &mirrorStatusError{status: 413}, "s4", 100) {
		t.Fatal("413 is permanent")
	}
	later := now.Add(7 * time.Hour)
	if tr.due(later, "s5", 150) || !tr.due(later, "s5", 50) {
		t.Fatal("413 marks the size")
	}
	// 400: this content is not sent again; changed content is, after the backoff.
	tr.succeed()
	if !tr.fail(now, &mirrorStatusError{status: 400}, "s6", 10) {
		t.Fatal("400 is permanent")
	}
	if tr.due(later, "s6", 10) || !tr.due(later, "s7", 10) {
		t.Fatal("400 marks the content")
	}
	if p, _ := classifyUploadError(&mirrorStatusError{status: 401}); p {
		t.Fatal("401 is an auth problem, not the content's")
	}
}

// publishCloud answers the snapshot routes with configurable statuses.
type publishCloud struct {
	mu                 sync.Mutex
	versionStatus      int // for PUT /v1/blobs and chunks; 0 = OK
	legacyStatus       int
	blobPuts, legacies int
}

func (c *publishCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case r.URL.Path == "/v1/agent-sessions":
		io.WriteString(w, `{"id":"cloud-1"}`)
	case strings.HasPrefix(r.URL.Path, "/v1/blobs/") && !strings.HasSuffix(r.URL.Path, "/missing"):
		c.blobPuts++
		if c.versionStatus != 0 {
			http.Error(w, `{"error":"x"}`, c.versionStatus)
			return
		}
		io.WriteString(w, `{}`)
	case strings.HasSuffix(r.URL.Path, "/snapshot"):
		c.legacies++
		if c.legacyStatus != 0 {
			http.Error(w, `{"error":{"message":"could not record session owner"}}`, c.legacyStatus)
			return
		}
		io.WriteString(w, `{}`)
	case strings.HasSuffix(r.URL.Path, "/versions"):
		io.WriteString(w, `{"version":1}`)
	default:
		io.WriteString(w, `{}`)
	}
}

func (c *publishCloud) counts() (blobs, legacy int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.blobPuts, c.legacies
}

func publishRuntime(t *testing.T, cloud http.Handler) (*Runtime, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	root := t.TempDir()
	srv := httptest.NewServer(cloud)
	t.Cleanup(srv.Close)
	d, err := NewDaemon(root, "local-token")
	if err != nil {
		t.Fatal(err)
	}
	d.ServerURL, d.ServerToken = srv.URL, "tok"
	r := NewRuntime(d, "p", NewStaticDesignation(true))
	r.SetServerProjectID("11111111-2222-4333-8444-555555555555")
	r.SnapshotMinInterval = time.Nanosecond
	return r, filepath.Join(home, ".claude", "projects", claudeProjectEncoded(root))
}

// A failing publish is not retried (nor the transcript re-packed) until its
// backoff has passed, even though the chat keeps changing.
func TestFailedPublishBacksOffWithoutRecollecting(t *testing.T) {
	cloud := &publishCloud{versionStatus: http.StatusServiceUnavailable, legacyStatus: http.StatusInternalServerError}
	r, proj := publishRuntime(t, cloud)
	const sid = "0b9f2b7e-3c41-4d0a-8f6e-5a7d9c1e2f40"
	path := filepath.Join(proj, sid+".jsonl")
	for i := 0; i < 5; i++ {
		writeLinesMkdir(t, path, claudeUserLine(i, "more"))
		r.noteSessionTurns(sid, "claude", 1)
		r.maybePushSnapshot(t.Context(), sid, i%2 == 0)
	}
	blobs, legacy := cloud.counts()
	if blobs != 1 || legacy != 1 {
		t.Fatalf("5 cycles under failure: %d version and %d legacy uploads, want 1 and 1", blobs, legacy)
	}
	r.snapshotMu.Lock()
	v := r.snapTracks.version[sid]
	r.snapshotMu.Unlock()
	if v.failures != 1 || time.Until(v.retryAt) < 4*time.Minute {
		t.Fatalf("version track = %+v", v)
	}
}

// Once a session has a cloud version, its legacy snapshot is not pushed;
// a native id that is not a UUID never gets one.
func TestLegacySnapshotOnlyAsFallback(t *testing.T) {
	cloud := &publishCloud{}
	r, proj := publishRuntime(t, cloud)
	const sid = "0b9f2b7e-3c41-4d0a-8f6e-5a7d9c1e2f41"
	path := filepath.Join(proj, sid+".jsonl")
	for i := 0; i < 3; i++ {
		writeLinesMkdir(t, path, claudeUserLine(i, "more"))
		r.noteSessionTurns(sid, "claude", 1)
		r.maybePushSnapshot(t.Context(), sid, true)
	}
	if blobs, legacy := cloud.counts(); blobs != 3 || legacy != 0 {
		t.Fatalf("versions publish: %d version uploads, %d legacy, want 3 and 0", blobs, legacy)
	}

	cloud2 := &publishCloud{versionStatus: http.StatusInternalServerError}
	r2, proj2 := publishRuntime(t, cloud2)
	native := "rollout-2026-11-23T04-31-14-5d92f0af"
	_ = proj2
	codex := filepath.Join(filepath.Dir(filepath.Dir(proj2)), "..", ".codex", "sessions", "2026", "11", "23", native+".jsonl")
	writeLinesMkdir(t, codex, `{"type":"session_meta","payload":{"id":"x","cwd":"`+filepath.ToSlash(r2.Daemon.Root)+`"}}`)
	r2.noteSessionTurns(native, "codex", 1)
	r2.maybePushSnapshot(t.Context(), native, true)
	if _, legacy := cloud2.counts(); legacy != 0 {
		t.Fatalf("legacy snapshot pushed for a non-UUID id %q", native)
	}
}

// Uploads use their own connection pool (not the one the heartbeat and API
// calls share) and a deadline sized to the body, not the API's 20 s.
func TestUploadClientIsSeparateAndSizedToTheBody(t *testing.T) {
	hs := NewHTTPMemoryStore("http://127.0.0.1:1", "tok", "p")
	up := hs.uploadClient()
	if up.Transport == nil || up.Transport == http.DefaultTransport {
		t.Fatal("uploads share the default transport with the heartbeat and API calls")
	}
	if up.Timeout != 0 {
		t.Fatalf("upload client has a fixed %s timeout", up.Timeout)
	}
	if got := uploadTimeout(5 << 20); got < 2*time.Minute {
		t.Fatalf("a 5 MB piece gets %s, want at least 2 minutes", got)
	}
	if hs.HTTP.Timeout != 20*time.Second {
		t.Fatalf("API client timeout changed: %s", hs.HTTP.Timeout)
	}
}

// Above the upload cap nothing is uploaded (version or legacy), the
// transcript is not re-packed while it only grows, and capture status
// lists it.
func TestUploadSizeCap(t *testing.T) {
	if MaxUploadGzipBytes != 8<<20 {
		t.Fatalf("MaxUploadGzipBytes = %d, want 8 MB", MaxUploadGzipBytes)
	}
	cloud := &publishCloud{}
	r, proj := publishRuntime(t, cloud)
	const sid = "0b9f2b7e-3c41-4d0a-8f6e-5a7d9c1e2f50"
	path := filepath.Join(proj, sid+".jsonl")
	// Incompressible content: about 12 MB of random base64 compresses to
	// about 9 MB.
	noise := make([]byte, 6<<10)
	var lines []string
	for i := 0; i < 2048; i++ {
		_, _ = rand.Read(noise)
		lines = append(lines, claudeUserLine(i, base64.StdEncoding.EncodeToString(noise)))
	}
	writeLinesMkdir(t, path, lines...)
	r.noteSessionTurns(sid, "claude", 1)
	r.maybePushSnapshot(t.Context(), sid, true)
	if blobs, legacy := cloud.counts(); blobs != 0 || legacy != 0 {
		t.Fatalf("over the cap: %d version and %d legacy uploads, want none", blobs, legacy)
	}
	listed := false
	for _, s := range SkippedTranscripts() {
		if strings.Contains(s.Path, sid) && s.Reason == "too large to back up right now" {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("not listed in capture status: %+v", SkippedTranscripts())
	}
	r.snapshotMu.Lock()
	v, l := r.snapTracks.version[sid], r.snapTracks.legacy[sid]
	r.snapshotMu.Unlock()
	st, _ := os.Stat(path)
	if v.due(time.Now().Add(24*time.Hour), "other", st.Size()+1) || l.due(time.Now().Add(24*time.Hour), "other", st.Size()) {
		t.Fatal("a larger transcript would be uploaded again")
	}
	if !v.due(time.Now(), "other", st.Size()-1) {
		t.Fatal("a smaller transcript should be uploaded again")
	}
}

// A transcript over the raw limit is never read or compressed, now or
// while it keeps growing, and capture status lists it.
func TestRawTranscriptCapSkipsBeforeReading(t *testing.T) {
	if MaxRawTranscriptBytes != 32<<20 {
		t.Fatalf("MaxRawTranscriptBytes = %d, want 32 MiB", MaxRawTranscriptBytes)
	}
	cloud := &publishCloud{}
	r, proj := publishRuntime(t, cloud)
	const sid = "0b9f2b7e-3c41-4d0a-8f6e-5a7d9c1e2f60"
	path := filepath.Join(proj, sid+".jsonl")
	writeHugeJSONL(t, path, MaxRawTranscriptBytes+(1<<20), claudeUserLine(1, "huge live chat"))
	before := snapStats.collected.Load()
	for i := 0; i < 3; i++ {
		writeLines(t, path, claudeUserLine(2+i, "still growing"))
		r.noteSessionTurns(sid, "claude", 1)
		r.maybePushSnapshot(t.Context(), sid, i == 0)
	}
	if got := snapStats.collected.Load() - before; got != 0 {
		t.Fatalf("over-limit transcript read %d times, want never", got)
	}
	if blobs, legacy := cloud.counts(); blobs != 0 || legacy != 0 {
		t.Fatalf("uploads: %d version, %d legacy", blobs, legacy)
	}
	listed := false
	for _, s := range SkippedTranscripts() {
		if strings.Contains(s.Path, sid) && s.Reason == "too large to back up right now" {
			listed = true
		}
	}
	if !listed {
		t.Fatal("not listed in capture status")
	}
}

func TestMemLogLines(t *testing.T) {
	var buf strings.Builder
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	logMemLine("mem")
	old := freeOSMemoryAbove
	freeOSMemoryAbove = 0
	defer func() { freeOSMemoryAbove = old }()
	snapStats.collected.Store(2)
	snapStats.bytesRead.Store(3 << 20)
	endSnapshotCycle(time.Now(), 5)
	out := buf.String()
	for _, want := range []string{"mem: heap_alloc=", "heap_inuse=", "heap_sys=", "heap_released=", "next_gc=", "num_gc=", "goroutines=", "snapshot_queue=",
		"snapshot cycle: sessions=5 collected=2 read=3.0MiB took="} {
		if !strings.Contains(out, want) {
			t.Fatalf("log lacks %q:\n%s", want, out)
		}
	}
}
