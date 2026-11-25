package daemon

// Large transcripts are captured (never skipped silently), sub-agent runs
// are linked to their parent and snapshot their own transcript, and cloud
// sessions are upserted only when their metadata changes.

import (
	"bytes"
	"encoding/json"
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

// writeHugeJSONL makes a transcript of more than 64 MB cheaply: a sparse run
// of zero bytes (one overlong, skipped line) followed by real records.
func writeHugeJSONL(t *testing.T, path string, size int64, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, size); err != nil {
		t.Fatal(err)
	}
	writeLines(t, path, append([]string{""}, lines...)...)
}

func TestLargeJSONLTranscriptIsCaptured(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "projects", "big-chat.jsonl")
	writeHugeJSONL(t, p, maxWalkFileBytes+(2<<20), claudeUserLine(1, "first question in a long chat"), claudeUserLine(2, "second question"))
	h := NewHarvester(dir, nil)
	src := TranscriptSource{Agent: "claude", Format: FormatJSONL, Global: true}
	info, _ := os.Stat(p)
	if info.Size() <= maxWalkFileBytes {
		t.Fatalf("fixture is %d bytes, want > %d", info.Size(), maxWalkFileBytes)
	}
	_, got, ok := h.considerFile(src, p, info)
	if !ok || got != 2 {
		t.Fatalf("large transcript: ok=%v turns=%d, want captured with 2 turns", ok, got)
	}
	// Growth is read from the saved offset.
	writeLines(t, p, claudeUserLine(3, "third question"))
	info, _ = os.Stat(p)
	if _, got, ok = h.considerFile(src, p, info); !ok || got != 1 {
		t.Fatalf("append to large transcript: ok=%v turns=%d, want 1", ok, got)
	}
	for _, s := range SkippedTranscripts() {
		if s.Path == p {
			t.Fatalf("JSONL transcript reported as skipped: %+v", s)
		}
	}
}

func TestOversizedWholeFileTranscriptIsReportedOnce(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "huge-session.json")
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(p, maxWalkFileBytes+1); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	h := NewHarvester(dir, nil)
	src := TranscriptSource{Agent: "gemini", Format: FormatJSON, Global: true}
	info, _ := os.Stat(p)
	for i := 0; i < 3; i++ {
		if _, _, ok := h.considerFile(src, p, info); ok {
			t.Fatal("oversized JSON transcript should not be read")
		}
	}
	if n := strings.Count(logs.String(), p); n != 1 {
		t.Fatalf("skip logged %d times, want once:\n%s", n, logs.String())
	}
	found := false
	for _, s := range SkippedTranscripts() {
		if s.Path == p && s.Agent == "gemini" && s.Bytes == info.Size() {
			found = true
		}
	}
	if !found {
		t.Fatalf("skipped transcript missing from status: %+v", SkippedTranscripts())
	}
}

func TestSubagentParentNativeID(t *testing.T) {
	cases := map[string]string{
		"/home/me/.claude/projects/-repo/parent-1/subagents/agent-0123456789abcdef.jsonl": "parent-1",
		`C:\Users\me\.claude\projects\C--repo\p-2\subagents\agent-89abcdef01.jsonl`:       "p-2",
		"/home/me/.claude/projects/-repo/parent-1/subagents/notes.jsonl":                  "",
		"/home/me/.claude/projects/-repo/0b9f2b7e-3c41-4d0a-8f6e-5a7d9c1e2f30.jsonl":      "",
		"/home/me/.claude/projects/-repo/parent-1/other/agent-0123456789abcdef.jsonl":     "",
	}
	for path, want := range cases {
		if got := subagentParentNativeID(path); got != want {
			t.Errorf("subagentParentNativeID(%q) = %q, want %q", path, got, want)
		}
	}
}

// upsertCloud records agent-session upserts.
type upsertCloud struct {
	mu        sync.Mutex
	upserts   []map[string]any
	oldServer bool
	failTurns bool
}

func (c *upsertCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	switch {
	case r.URL.Path == "/v1/agent-sessions":
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		c.mu.Lock()
		c.upserts = append(c.upserts, body)
		old := c.oldServer
		c.mu.Unlock()
		if _, ok := body["parent_native_id"]; ok && old {
			http.Error(w, `invalid JSON body: json: unknown field "parent_native_id"`, http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"id":"cloud-1"}`)
	case strings.HasSuffix(r.URL.Path, "/turns/batch"):
		c.mu.Lock()
		fail := c.failTurns
		c.mu.Unlock()
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		io.WriteString(w, `{}`)
	default:
		io.WriteString(w, `{}`)
	}
}

func (c *upsertCloud) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.upserts)
}

func TestSubagentRunUpsertCarriesParent(t *testing.T) {
	for _, old := range []bool{false, true} {
		cloud := &upsertCloud{oldServer: old}
		srv := httptest.NewServer(cloud)
		hs := NewHTTPMemoryStore(srv.URL, "tok", "11111111-2222-4333-8444-555555555555")
		id, err := hs.MirrorAgentSessionTurnsWithParent(t.Context(), "claude", "agent-0123456789abcdef", "m1", "C:/w", "parent-1", nil, "")
		srv.Close()
		if err != nil || id != "cloud-1" {
			t.Fatalf("old=%v: id=%q err=%v", old, id, err)
		}
		if cloud.upserts[0]["parent_native_id"] != "parent-1" {
			t.Fatalf("old=%v: first upsert = %v", old, cloud.upserts[0])
		}
		if old && (len(cloud.upserts) != 2 || cloud.upserts[1]["parent_native_id"] != nil) {
			t.Fatalf("old server: want a retry without parent_native_id, got %v", cloud.upserts)
		}
	}
}

func TestSessionUpsertOnlyWhenMetadataChanges(t *testing.T) {
	cloud := &upsertCloud{}
	srv := httptest.NewServer(cloud)
	defer srv.Close()
	hs := NewHTTPMemoryStore(srv.URL, "tok", "11111111-2222-4333-8444-555555555555")
	ctx := t.Context()
	turn := []MirroredTurn{{Idx: 1, Role: "user", TextPreview: "hi"}}
	mirror := func(title, parent string) {
		t.Helper()
		if _, err := hs.MirrorAgentSessionTurnsWithParent(ctx, "claude", "native-1", "m1", "C:/w", parent, turn, title); err != nil && !cloud.failTurns {
			t.Fatal(err)
		}
	}
	mirror("", "")
	mirror("", "")
	mirror("", "")
	if n := cloud.count(); n != 1 {
		t.Fatalf("unchanged session upserted %d times, want 1", n)
	}
	mirror("Fix the cache", "")
	mirror("Fix the cache", "")
	mirror("", "")
	if n := cloud.count(); n != 2 {
		t.Fatalf("after a new title: %d upserts, want 2", n)
	}
	mirror("", "parent-1")
	mirror("", "parent-1")
	if n := cloud.count(); n != 3 {
		t.Fatalf("after a new parent: %d upserts, want 3", n)
	}
	// A failed upload forgets the session: it may be gone on the server.
	cloud.mu.Lock()
	cloud.failTurns = true
	cloud.mu.Unlock()
	mirror("", "")
	cloud.mu.Lock()
	cloud.failTurns = false
	cloud.mu.Unlock()
	mirror("", "")
	if n := cloud.count(); n != 4 {
		t.Fatalf("after a failed upload: %d upserts, want 4", n)
	}
	// A different project is a different session.
	hs.SetProjectID("99999999-2222-4333-8444-555555555555")
	mirror("", "")
	if n := cloud.count(); n != 5 {
		t.Fatalf("after a project change: %d upserts, want 5", n)
	}
}

func TestSubagentSnapshotUsesRunTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	root := t.TempDir()
	const run = "agent-0123456789abcdef"
	proj := filepath.Join(home, ".claude", "projects", claudeProjectEncoded(root))
	tr := filepath.Join(proj, "0b9f2b7e-3c41-4d0a-8f6e-5a7d9c1e2f30", "subagents", run+".jsonl")
	writeLinesMkdir(t, tr, claudeUserLine(1, "task for the sub-agent"), claudeUserLine(2, "more"))
	writeLinesMkdir(t, filepath.Join(proj, "notes.md"), "parent chat notes")

	snap, err := CollectSnapshot(root, run, "claude", "m1")
	if err != nil {
		t.Fatalf("sub-agent snapshot: %v", err)
	}
	if snap.TurnCount != 2 || len(snap.ArtifactsBundle) != 0 {
		t.Fatalf("turns=%d artifacts=%d, want the run's 2 lines and no parent artifacts", snap.TurnCount, len(snap.ArtifactsBundle))
	}
	raw, _ := ungzipBytes(snap.TranscriptPayload)
	if !bytes.Contains(raw, []byte("task for the sub-agent")) {
		t.Fatal("snapshot does not hold the run's transcript")
	}

	// A run whose transcript is gone is skipped without an error log.
	if _, err := CollectSnapshot(root, "agent-fedcba9876543210", "claude", "m1"); !errors.Is(err, errSubagentTranscriptGone) {
		t.Fatalf("missing run: err = %v", err)
	}
	cloud := &countingCloud{calls: map[string]int{}, bytes: map[string]int64{}}
	srv := httptest.NewServer(cloud)
	defer srv.Close()
	d, err := NewDaemon(root, "local-token")
	if err != nil {
		t.Fatal(err)
	}
	d.ServerURL, d.ServerToken = srv.URL, "tok"
	r := NewRuntime(d, "p", NewStaticDesignation(true))
	r.SetServerProjectID("11111111-2222-4333-8444-555555555555")
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	r.noteSessionTurns("agent-fedcba9876543210", "claude", 1)
	r.maybePushSnapshot(t.Context(), "agent-fedcba9876543210", true)
	r.noteSessionTurns(run, "claude", 2)
	r.maybePushSnapshot(t.Context(), run, true)
	if strings.Contains(logs.String(), "collect snapshot") || strings.Contains(logs.String(), "no transcript") {
		t.Fatalf("sub-agent snapshot logged an error:\n%s", logs.String())
	}
	if n := cloud.count("PUT /v1/blobs"); n != 1 {
		t.Fatalf("sub-agent run uploads = %d, want 1", n)
	}
}

func TestLargeSnapshotCadence(t *testing.T) {
	if LargeSnapshotBytes != 8<<20 || DefaultLargeSnapshotMinInterval != 30*time.Minute || DefaultSnapshotMinInterval != 5*time.Minute {
		t.Fatalf("snapshot cadence constants changed: %d bytes, %s large, %s small",
			LargeSnapshotBytes, DefaultLargeSnapshotMinInterval, DefaultSnapshotMinInterval)
	}
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	root := t.TempDir()
	proj := filepath.Join(home, ".claude", "projects", claudeProjectEncoded(root))
	const big, small = "0b9f2b7e-3c41-4d0a-8f6e-5a7d9c1e2f31", "0b9f2b7e-3c41-4d0a-8f6e-5a7d9c1e2f32"
	bigPath, smallPath := filepath.Join(proj, big+".jsonl"), filepath.Join(proj, small+".jsonl")
	writeHugeJSONL(t, bigPath, LargeSnapshotBytes+(1<<20), claudeUserLine(1, "long chat"))
	writeLinesMkdir(t, smallPath, claudeUserLine(1, "short chat"))

	cloud := &countingCloud{calls: map[string]int{}, bytes: map[string]int64{}}
	srv := httptest.NewServer(cloud)
	defer srv.Close()
	d, err := NewDaemon(root, "local-token")
	if err != nil {
		t.Fatal(err)
	}
	d.ServerURL, d.ServerToken = srv.URL, "tok"
	r := NewRuntime(d, "p", NewStaticDesignation(true))
	r.SetServerProjectID("11111111-2222-4333-8444-555555555555")
	r.SnapshotMinInterval = time.Nanosecond // periodic spacing out of the way
	uploads := func() int {
		return cloud.count("PUT /v1/blobs") + cloud.count("POST /v1/blobs/assemble")
	}
	ctx := t.Context()
	step := func(sid, path string, force bool) {
		writeLines(t, path, claudeUserLine(2, "more"))
		r.noteSessionTurns(sid, "claude", 1)
		r.maybePushSnapshot(ctx, sid, force)
	}
	step(big, bigPath, false) // first periodic: published
	step(big, bigPath, false) // changed, but within the large interval
	step(big, bigPath, false)
	if n := uploads(); n != 1 {
		t.Fatalf("large live transcript: %d uploads, want 1 within the interval", n)
	}
	step(big, bigPath, true) // idle / end: always published
	if n := uploads(); n != 2 {
		t.Fatalf("large transcript idle snapshot: %d uploads, want 2", n)
	}
	r.LargeSnapshotMinInterval = time.Nanosecond
	step(big, bigPath, false)
	if n := uploads(); n != 3 {
		t.Fatalf("large transcript after the interval: %d uploads, want 3", n)
	}
	step(small, smallPath, false)
	step(small, smallPath, false)
	if n := uploads(); n != 5 {
		t.Fatalf("small transcript keeps its cadence: %d uploads, want 5", n)
	}
}
