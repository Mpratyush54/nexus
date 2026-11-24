package daemon

// Regression tests for the capture pipeline's memory use: transcripts are
// tailed and snapshotted without holding them (or copies of them) in
// memory, and per-cycle work does not grow with the length of a chat.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"central-memory/internal/security"
	"central-memory/internal/store"
)

// allocDuring returns the bytes allocated by the whole process while f runs.
func allocDuring(f func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func filler(n int) string {
	words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"}
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		b.WriteString(words[(i*7+i/3)%len(words)])
		b.WriteByte(' ')
	}
	return b.String()[:n]
}

// apiBlobLine is a Claude Code api-request-blob record of about n bytes.
func apiBlobLine(n int) string {
	raw, _ := json.Marshal(map[string]any{
		"body": map[string]any{"messages": []any{map[string]any{"role": "user", "content": filler(n),
			"usage": map[string]any{"input_tokens": 1}}}},
		"type": "api-request-blob", "requestId": "req_1",
	})
	return string(raw)
}

func claudeUserLine(i int, text string) string {
	raw, _ := json.Marshal(map[string]any{"type": "user", "sessionId": "s",
		"timestamp": fmt.Sprintf("2026-11-01T10:%02d:%02dZ", (i/60)%60, i%60),
		"message":   map[string]any{"role": "user", "content": text}})
	return string(raw)
}

func TestJSONTopLevelType(t *testing.T) {
	cases := []struct {
		line string
		want string
		ok   bool
	}{
		{`{"type":"api-request-blob","body":{"type":"user"}}`, "api-request-blob", true},
		{`{"body":{"type":"user","x":[1,{"type":"a"}]},"type":"progress"}`, "progress", true},
		{` { "a" : "b\"type\":\"x" , "type" : "summary" } `, "summary", true},
		{`{"a":1,"b":true,"c":null,"type":"system"}`, "system", true},
		{`{"message":{"type":"assistant"}}`, "", false},
		{`{"type":7}`, "", false},
		{`[{"type":"x"}]`, "", false},
		{`{"type":"unterminated`, "", false},
		{``, "", false},
	}
	for _, c := range cases {
		got, ok := jsonTopLevelType([]byte(c.line))
		if got != c.want || ok != c.ok {
			t.Errorf("jsonTopLevelType(%s) = %q,%v want %q,%v", c.line, got, ok, c.want, c.ok)
		}
	}
	if skippableTranscriptLine([]byte(`{"type":"turn_context","payload":{"model":"m"}}`)) {
		t.Error("turn_context names the usage model and must not be skipped")
	}
	if !skippableTranscriptLine([]byte(apiBlobLine(100))) {
		t.Error("api-request-blob must be skipped")
	}
}

func TestTranscriptLineReader(t *testing.T) {
	in := "one\r\n" + strings.Repeat("x", 300) + "\n\nthree\ntorn"
	lr := newTranscriptLineReader(strings.NewReader(in), 100)
	type got struct {
		line     string
		size     int
		overlong bool
	}
	var out []got
	for {
		line, size, overlong, ok := lr.next()
		if !ok {
			break
		}
		out = append(out, got{string(line), size, overlong})
	}
	want := []got{{"one", 5, false}, {"", 301, true}, {"", 1, false}, {"three", 6, false}}
	if fmt.Sprint(out) != fmt.Sprint(want) {
		t.Fatalf("lines = %v, want %v", out, want)
	}
}

// A transcript made mostly of api-request-blob records is tailed without
// buffering or decoding those records.
func TestTailFileStreamsAndSkipsBlobRecords(t *testing.T) {
	t.Setenv("NEXUS_HARVEST_BACKFILL", "all")
	dir := t.TempDir()
	p := filepath.Join(dir, "chat.jsonl")
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, claudeUserLine(i, fmt.Sprintf("question %d about the cache", i)), apiBlobLine(500<<10))
	}
	writeLines(t, p, lines...)
	st, _ := os.Stat(p)
	h := NewHarvester(dir, nil)
	var turns []Turn
	alloc := allocDuring(func() {
		var err error
		if turns, err = h.TailFile(p); err != nil {
			t.Fatal(err)
		}
	})
	if len(turns) != 40 {
		t.Fatalf("turns = %d, want 40", len(turns))
	}
	if h.offsets[p] != st.Size() {
		t.Fatalf("offset = %d, want %d", h.offsets[p], st.Size())
	}
	// The file is ~20 MB. Reading it whole, copying it to a string and
	// decoding every blob into maps allocated well over 100 MB.
	if limit := uint64(4 << 20); alloc > limit {
		t.Fatalf("TailFile of a %d MB transcript allocated %.1f MB, want < %d MB",
			st.Size()>>20, float64(alloc)/(1<<20), limit>>20)
	}
}

// Source positions key cloud turns; they must stay what they always were.
func TestTailFileSourcePositionsUnchanged(t *testing.T) {
	t.Setenv("NEXUS_HARVEST_BACKFILL", "all")
	dir := t.TempDir()
	p := filepath.Join(dir, "chat.jsonl")
	l1 := claudeUserLine(1, "first question")
	l2 := apiBlobLine(1000)
	l3 := claudeUserLine(3, "second question")
	if err := os.WriteFile(p, []byte(l1+"\r\n"+l2+"\n"+l3+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := NewHarvester(dir, nil)
	turns, err := h.TailFile(p)
	if err != nil || len(turns) != 2 {
		t.Fatalf("turns=%d err=%v", len(turns), err)
	}
	// bufio.ScanLines semantics: each line without "\r\n"/"\n", plus one.
	if want := int64(len(l1) + 1); turns[0].SourcePosition != want {
		t.Fatalf("pos[0] = %d want %d", turns[0].SourcePosition, want)
	}
	if want := int64(len(l1) + 1 + len(l2) + 1 + len(l3) + 1); turns[1].SourcePosition != want {
		t.Fatalf("pos[1] = %d want %d", turns[1].SourcePosition, want)
	}
}

// An overlong record no longer ends the read: turns after it are kept.
func TestTailFileOverlongLineKeepsLaterTurns(t *testing.T) {
	t.Setenv("NEXUS_HARVEST_BACKFILL", "all")
	dir := t.TempDir()
	p := filepath.Join(dir, "chat.jsonl")
	writeLines(t, p, claudeUserLine(1, "before"), claudeUserLine(2, filler(maxJSONLLineBytes+10)), claudeUserLine(3, "after"))
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"type":"user","message":{"role":"user","content":"torn`)
	f.Close()
	h := NewHarvester(dir, nil)
	turns, err := h.TailFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 || turns[0].Content != "before" || turns[1].Content != "after" {
		t.Fatalf("turns = %+v", turns)
	}
	// The torn tail is read once it is finished.
	f, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`"}}` + "\n")
	f.Close()
	if turns, err = h.TailFile(p); err != nil || len(turns) != 1 || turns[0].Content != "torn" {
		t.Fatalf("torn tail: %+v %v", turns, err)
	}
}

// A chat that keeps growing costs the same per cycle at its 60th cycle as
// at its 5th, and its end-of-session batch stays bounded.
func TestHarvesterCyclesStayBounded(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "live.jsonl")
	writeLines(t, p, claudeUserLine(0, "start"))
	h := NewHarvester(dir, nil)
	if _, err := h.TailFile(p); err != nil {
		t.Fatal(err)
	}
	const cycles, perCycle = 60, 20
	allocs := make([]uint64, cycles)
	total := 1
	for c := 0; c < cycles; c++ {
		var lines []string
		for i := 0; i < perCycle; i++ {
			lines = append(lines, claudeUserLine(total+i, fmt.Sprintf("turn %d %s", total+i, filler(2000))))
		}
		lines = append(lines, apiBlobLine(400<<10))
		writeLines(t, p, lines...)
		total += perCycle
		allocs[c] = allocDuring(func() {
			if got, err := h.TailFile(p); err != nil || len(got) != perCycle {
				t.Fatalf("cycle %d: %d turns, %v", c, len(got), err)
			}
		})
	}
	avg := func(xs []uint64) float64 {
		var s uint64
		for _, x := range xs {
			s += x
		}
		return float64(s) / float64(len(xs))
	}
	early, late := avg(allocs[2:12]), avg(allocs[cycles-10:])
	if late > early*1.5+256<<10 {
		t.Fatalf("per-cycle allocation grew with the chat: early %.0f KB, late %.0f KB", early/1024, late/1024)
	}
	if late > 2<<20 {
		t.Fatalf("per-cycle allocation %.0f KB for %d new turns, want < 2 MB", late/1024, perCycle)
	}
	h.mu.Lock()
	n := len(h.batch[p])
	h.mu.Unlock()
	if n > 2*maxIdleBatchTurns {
		t.Fatalf("idle batch holds %d turns, want <= %d", n, 2*maxIdleBatchTurns)
	}
	h.now = func() time.Time { return time.Now().Add(time.Hour) }
	evs := h.CheckIdle()
	if len(evs) != 1 || evs[0].Payload["turn_count"] != total {
		t.Fatalf("idle batch turn_count = %v, want %d", evs[0].Payload["turn_count"], total)
	}
	if got := len(evs[0].Payload["turns"].([]any)); got > 2*maxIdleBatchTurns {
		t.Fatalf("idle batch carries %d turns", got)
	}
}

// oldGzipTranscript is the pre-streaming CollectSnapshot transcript path.
func oldGzipTranscript(raw []byte) ([]byte, int) {
	if security.ContainsSecret(raw) {
		if redacted, ok := RedactSecrets(string(raw)); ok {
			raw = []byte(redacted)
		}
	}
	n := bytes.Count(raw, []byte("\n"))
	if len(raw) > 0 && !bytes.HasSuffix(raw, []byte("\n")) {
		n++
	}
	return raw, n
}

func TestGzipSnapshotTranscriptMatchesWholeFile(t *testing.T) {
	cases := map[string]string{
		"empty":        "",
		"clean":        "{\"a\":1}\n{\"b\":\"sk-abcdefghijklmnopqrstuvwxyz\"}\n",
		"secret":       "{\"a\":\"ghp_abcdefghijklmnop123\"}\n{\"b\":\"sk-abcdefghijklmnopqrstuvwxyz\"}\r\n{\"c\":3}\n",
		"unterminated": "{\"a\":1}\n{\"b\":2}",
		"long":         "{\"a\":\"" + filler(200<<10) + "\"}\n{\"k\":\"AKIAABCDEFGHIJKLMNOP\"}\n",
	}
	for name, in := range cases {
		gz, lines, err := gzipSnapshotTranscript(bytes.NewReader([]byte(in)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := ungzipBytes(gz)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want, wantLines := oldGzipTranscript([]byte(in))
		if !bytes.Equal(got, want) || lines != wantLines {
			t.Fatalf("%s: got %q (%d lines), want %q (%d lines)", name, trimForLog(got), lines, trimForLog(want), wantLines)
		}
	}
}

func trimForLog(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "…"
	}
	return string(b)
}

func TestStreamingJSONBodyMatchesMarshal(t *testing.T) {
	diff := []byte("diff --git a b")
	payload := bytes.Repeat([]byte{0, 1, 2, 250, 251}, 40001)
	fields := map[string]any{"project_id": "p", "turn_count": 3, "git_dirty": true, "msg": "quote \" and <tag>"}
	length, open, err := streamingJSONBody(fields, []b64Field{
		bytesField("diff_b64", diff), bytesField("empty_b64", nil), bytesField("payload_b64", payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ { // GetBody must replay the same body
		rc, err := open()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(rc)
		if int64(len(body)) != length {
			t.Fatalf("length = %d, declared %d", len(body), length)
		}
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		want := map[string]any{"project_id": "p", "turn_count": 3.0, "git_dirty": true, "msg": "quote \" and <tag>",
			"diff_b64": base64.StdEncoding.EncodeToString(diff), "empty_b64": "",
			"payload_b64": base64.StdEncoding.EncodeToString(payload)}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("body mismatch")
		}
	}
}

func bigTranscript(t *testing.T, path string, mb int) []byte {
	t.Helper()
	var b bytes.Buffer
	for i := 0; b.Len() < mb<<20; i++ {
		b.WriteString(claudeUserLine(i, fmt.Sprintf("%d %s", i, filler(3000))))
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// countingCloud accepts the snapshot endpoints and drains bodies without
// buffering them.
type countingCloud struct {
	mu    sync.Mutex
	calls map[string]int
	bytes map[string]int64
}

func (c *countingCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n, _ := io.Copy(io.Discard, r.Body)
	key := r.Method + " " + r.URL.Path
	if strings.HasPrefix(r.URL.Path, "/v1/blobs/") {
		key = r.Method + " /v1/blobs"
	}
	if strings.HasPrefix(r.URL.Path, "/sessions/") && strings.HasSuffix(r.URL.Path, "/snapshot") {
		key = r.Method + " /sessions/snapshot"
	}
	c.mu.Lock()
	c.calls[key]++
	c.bytes[key] += n
	c.mu.Unlock()
	if r.ContentLength >= 0 && n != r.ContentLength {
		http.Error(w, "short body", http.StatusBadRequest)
		return
	}
	switch {
	case r.URL.Path == "/v1/agent-sessions":
		io.WriteString(w, `{"id":"cloud-1"}`)
	case strings.HasSuffix(r.URL.Path, "/versions"):
		io.WriteString(w, `{"version":1}`)
	default:
		io.WriteString(w, `{}`)
	}
}

func (c *countingCloud) count(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[key]
}

// Collecting and uploading a snapshot of a large chat no longer holds the
// transcript (or base64/JSON copies of it) in memory.
func TestSnapshotOfLargeTranscriptIsStreamed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	root := t.TempDir()
	const sid = "6a1d3c52-0b7e-4b8e-9d55-2f6c1e0c9a11"
	tr := filepath.Join(home, ".claude", "projects", claudeProjectEncoded(root), sid+".jsonl")
	raw := bigTranscript(t, tr, 24)

	var snap *store.SessionSnapshot
	collect := allocDuring(func() {
		var err error
		if snap, err = CollectSnapshot(root, sid, "claude", "m1"); err != nil {
			t.Fatal(err)
		}
	})
	if snap.TurnCount != bytes.Count(raw, []byte("\n")) {
		t.Fatalf("turn count = %d", snap.TurnCount)
	}
	// Old path: ReadFile + string copy + gzip buffer = > 3x the transcript.
	if limit := uint64(len(snap.TranscriptPayload)) + 8<<20; collect > limit {
		t.Fatalf("CollectSnapshot of %d MB allocated %.1f MB (gzip payload %.1f MB)", len(raw)>>20,
			float64(collect)/(1<<20), float64(len(snap.TranscriptPayload))/(1<<20))
	}

	cloud := &countingCloud{calls: map[string]int{}, bytes: map[string]int64{}}
	srv := httptest.NewServer(cloud)
	defer srv.Close()
	hs := NewHTTPMemoryStore(srv.URL, "tok", "11111111-2222-4333-8444-555555555555")
	snap.SessionID = sid
	upload := allocDuring(func() {
		if err := hs.PublishAgentSessionVersion(context.Background(), snap, root); err != nil {
			t.Fatal(err)
		}
		if err := hs.PushSnapshot(context.Background(), snap); err != nil {
			t.Fatal(err)
		}
	})
	// Old path: ungzip (grown by ReadAll) + base64 string + JSON body, twice
	// over for the two endpoints: > 6x the transcript.
	if limit := uint64(8 << 20); upload > limit {
		t.Fatalf("uploading a %d MB snapshot allocated %.1f MB, want < %d MB", len(raw)>>20, float64(upload)/(1<<20), limit>>20)
	}
	cloud.mu.Lock()
	sent := cloud.bytes["PUT /v1/blobs"]
	cloud.mu.Unlock()
	if want := int64(base64.StdEncoding.EncodedLen(len(raw))); sent < want {
		t.Fatalf("blob body %d bytes, want at least the base64 transcript (%d)", sent, want)
	}
}

// An unchanged transcript is not packed and uploaded again.
func TestMaybePushSnapshotSkipsUnchangedTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	root := t.TempDir()
	const sid = "0b9f2b7e-3c41-4d0a-8f6e-5a7d9c1e2f30"
	tr := filepath.Join(home, ".claude", "projects", claudeProjectEncoded(root), sid+".jsonl")
	writeLinesMkdir(t, tr, claudeUserLine(1, "hello"))

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
	r.noteSessionTurns(sid, "claude", 1)

	ctx := context.Background()
	r.maybePushSnapshot(ctx, sid, true)
	r.maybePushSnapshot(ctx, sid, true)
	if n := cloud.count("PUT /v1/blobs"); n != 1 {
		t.Fatalf("unchanged transcript uploaded %d times, want 1", n)
	}
	writeLinesMkdir(t, tr, claudeUserLine(2, "more"))
	r.maybePushSnapshot(ctx, sid, true)
	if n := cloud.count("PUT /v1/blobs"); n != 2 {
		t.Fatalf("changed transcript: %d uploads, want 2", n)
	}
}

func writeLinesMkdir(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeLines(t, path, lines...)
}
