package diagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRollingFileRotatesBySizeAndDayAndKeepsFive(t *testing.T) {
	dir := t.TempDir()
	day := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	clock := day
	rf := NewRollingFile(dir, "core", 100, 5, func() string { return "header v1" })
	rf.SetClock(func() time.Time { return clock })
	defer rf.Close()
	line := []byte(strings.Repeat("x", 39) + "\n") // 40 bytes
	for i := 0; i < 20; i++ {
		if _, err := rf.Write(line); err != nil {
			t.Fatal(err)
		}
		// Distinct mtimes so "newest first" is well defined on coarse clocks.
		time.Sleep(2 * time.Millisecond)
	}
	files := LogFiles(dir, "core")
	if len(files) != 5 {
		t.Fatalf("kept %d files, want 5: %v", len(files), files)
	}
	if filepath.Base(files[0]) != "core-20261125.log" {
		t.Fatalf("newest file = %s, want the current core-20261125.log", files[0])
	}
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if st.Size() > 100 {
			t.Fatalf("%s is %d bytes, over the 100-byte cap", f, st.Size())
		}
		body, _ := os.ReadFile(f)
		if !strings.HasPrefix(string(body), "header v1\n") {
			t.Fatalf("%s does not start with the header: %q", f, body)
		}
	}
	// A new UTC day starts a new file.
	clock = day.Add(24 * time.Hour)
	if _, err := rf.Write(line); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "core-20261126.log")); err != nil {
		t.Fatalf("no file for the new day: %v", err)
	}
	if got := len(LogFiles(dir, "core")); got != 5 {
		t.Fatalf("after day roll kept %d files, want 5", got)
	}
	// Other prefixes (the desktop's nexus-*.log) are never pruned by the core.
	other := filepath.Join(dir, "nexus-20261120.log")
	_ = os.WriteFile(other, []byte("desktop\n"), 0o644)
	clock = clock.Add(24 * time.Hour)
	_, _ = rf.Write(line)
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("desktop log was pruned: %v", err)
	}
}

func TestLogWriterFormatsRedactsAndFillsRing(t *testing.T) {
	var out strings.Builder
	ring := NewRing(3)
	at := time.Date(2026, 11, 25, 10, 41, 5, 7_000_000, time.UTC)
	w := &LogWriter{Out: &out, Ring: ring, Now: func() time.Time { return at }}
	logger := log.New(w, "", 0)
	logger.Printf("sync: upload failed status=500 token=Bearer abcdefghijklmnop")
	logger.Printf("plain message for bob@example.com")
	logger.Printf("nexuscore: panic in capture\ngoroutine 1 [running]:")
	got := out.String()
	for _, want := range []string{
		"2026-11-25T10:41:05.007Z WARN [core] sync: upload failed status=500",
		"INFO [core] core: plain message for [email]",
		"ERROR [core] nexuscore: panic in capture",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "abcdefghijklmnop") || strings.Contains(got, "bob@") {
		t.Fatalf("log output not redacted:\n%s", got)
	}
	if lines := ring.Lines(); len(lines) != 3 || !strings.Contains(lines[2], "goroutine 1") {
		t.Fatalf("ring = %q, want the newest 3 lines", lines)
	}
}

func TestGuardRecoversHoldsReportAndRoundTrips(t *testing.T) {
	base := t.TempDir()
	ring := NewRing(0)
	ring.Add("2026-11-25T10:41:05.007Z INFO [core] capture: scanning C:\\Users\\Pratyush Mishra\\repo")
	Configure(Config{Base: base, Version: "0.4.0-test", Ring: ring})
	t.Cleanup(func() { Configure(Config{}) })
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	done := make(chan struct{})
	Go("test-worker", func() {
		defer close(done)
		var m map[string]int
		m["boom"] = 1 // nil map write panics
	})
	<-done
	time.Sleep(50 * time.Millisecond) // Guard runs after close(done)

	entries, err := os.ReadDir(HeldDir(base))
	if err != nil || len(entries) != 1 {
		t.Fatalf("held reports = %v (%v), want 1", entries, err)
	}
	raw, _ := os.ReadFile(filepath.Join(HeldDir(base), entries[0].Name()))
	var r Report
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	if r.Kind != KindCrash || r.Source != SourceCore || r.AppVersion != "0.4.0-test" || r.Context["via"] != "test-worker" {
		t.Fatalf("report fields = %+v", r)
	}
	if !strings.HasPrefix(r.Message, "panic: assignment to entry in nil map") || !strings.Contains(r.Stack, "phase2_test.go") {
		t.Fatalf("message/stack = %q / %.80q", r.Message, r.Stack)
	}
	if len(r.Logs) == 0 || strings.Contains(strings.Join(r.Logs, "\n"), "Pratyush") {
		t.Fatalf("logs missing or not redacted: %q", r.Logs)
	}
	// The file is the exact wire body: it normalises without changes.
	again := r
	if err := Normalize(&again, time.Now()); err != nil || again.Message != r.Message {
		t.Fatalf("held report does not round-trip: %v", err)
	}
}

func TestCaptureRePanics(t *testing.T) {
	base := t.TempDir()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr); Configure(Config{}) })
	defer func() {
		if v := recover(); v != "fatal" {
			t.Fatalf("recovered %v, want the original panic", v)
		}
		if entries, _ := os.ReadDir(HeldDir(base)); len(entries) != 1 {
			t.Fatalf("held = %d, want 1", len(entries))
		}
	}()
	func() {
		defer Capture(base, "1.0")()
		panic("fatal")
	}()
}

func TestUploaderSendsRetriesWithBackoffAndDrops(t *testing.T) {
	base := t.TempDir()
	var calls, authed, anonLeak atomic.Int32
	status := atomic.Int32{}
	status.Store(503)
	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/diagnostics" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var rep Report
		_ = json.NewDecoder(r.Body).Decode(&rep)
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			authed.Add(1)
			if rep.ReportID != "bbbbbbbb2222" {
				anonLeak.Add(1)
			}
		}
		seen[rep.ReportID]++
		w.Header().Set("X-Request-ID", "7K3M9QW2HX4D")
		code := int(status.Load())
		if code == 429 {
			w.Header().Set("Retry-After", "600")
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"report_id":"` + rep.ReportID + `"}`))
	}))
	defer srv.Close()

	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	u := &Uploader{Base: base, Server: func() string { return srv.URL }, Token: func() string { return "nxs_token123456" }, Now: func() time.Time { return now }}
	a := Report{ReportID: "aaaaaaaa1111", Kind: KindCrash, Source: SourceDesktop, Message: "a"}
	b := Report{ReportID: "bbbbbbbb2222", Kind: KindUserReport, Source: SourceDesktop, UserNote: "b"}
	if _, err := Enqueue(base, a, false); err != nil {
		t.Fatal(err)
	}
	// Enqueueing the same ID twice keeps one file (dedupe by report_id).
	_, _ = Enqueue(base, b, false)
	if _, err := Enqueue(base, b, true); err != nil {
		t.Fatal(err)
	}
	if n := len(u.Pending()); n != 2 {
		t.Fatalf("pending = %d, want 2", n)
	}

	// 503: both stay queued with a 30s backoff.
	res := u.Flush(context.Background(), "", false)
	if len(res) != 2 || !res[0].Waiting || res[0].RetryAt.Sub(now) != 30*time.Second || res[0].RequestID != "7K3M9QW2HX4D" {
		t.Fatalf("first flush = %+v", res)
	}
	// Not due yet: no request is made.
	before := calls.Load()
	u.Flush(context.Background(), "", false)
	if calls.Load() != before {
		t.Fatal("flush before the backoff elapsed sent again")
	}
	// Second failure doubles the wait; 429 honours a longer Retry-After.
	now = now.Add(31 * time.Second)
	status.Store(429)
	res = u.Flush(context.Background(), "aaaaaaaa1111", false)
	if len(res) != 1 || res[0].RetryAt.Sub(now) != 600*time.Second {
		t.Fatalf("429 flush = %+v", res)
	}
	if Backoff(2) != time.Minute || Backoff(20) != time.Hour {
		t.Fatalf("backoff = %v %v", Backoff(2), Backoff(20))
	}

	// Force (an explicit Send) ignores the backoff; 202 deletes the file.
	status.Store(202)
	res = u.Flush(context.Background(), "bbbbbbbb2222", true)
	if len(res) != 1 || !res[0].Sent {
		t.Fatalf("forced flush = %+v", res)
	}
	if authed.Load() == 0 {
		t.Fatal("signed report was sent without the saved sign-in")
	}
	// 400 drops the report for good.
	status.Store(400)
	now = now.Add(time.Hour)
	res = u.Flush(context.Background(), "", false)
	if len(res) != 1 || !res[0].Dropped {
		t.Fatalf("400 flush = %+v", res)
	}
	if n := len(u.Pending()); n != 0 {
		t.Fatalf("pending after send/drop = %d", n)
	}
	// The anonymous report never carried the token.
	if anonLeak.Load() != 0 {
		t.Fatalf("anonymous report sent with a token %d times", anonLeak.Load())
	}
}

func TestUploaderNetworkErrorKeepsReport(t *testing.T) {
	base := t.TempDir()
	u := &Uploader{Base: base, Server: func() string { return "http://127.0.0.1:1" }}
	_, _ = Enqueue(base, Report{ReportID: "cccccccc3333", Kind: KindError, Source: SourceCore, Message: "x"}, false)
	res := u.Flush(context.Background(), "", false)
	if len(res) != 1 || !res[0].Waiting || res[0].Error == "" {
		t.Fatalf("offline flush = %+v", res)
	}
	if len(u.Pending()) != 1 {
		t.Fatal("offline report was not kept")
	}
}

func TestPruneKeepsTwentyAndDropsOld(t *testing.T) {
	base := t.TempDir()
	now := time.Now()
	for i := 0; i < 25; i++ {
		id := NewReportID()
		path, err := Enqueue(base, Report{ReportID: id, Kind: KindError, Source: SourceCore, Message: "x"}, false)
		if err != nil {
			t.Fatal(err)
		}
		at := now.Add(-time.Duration(i) * time.Minute)
		_ = os.Chtimes(path, at, at)
	}
	old, _ := Hold(base, Report{ReportID: "oldoldold123", Kind: KindCrash, Source: SourceCore, Message: "x"})
	_ = os.Chtimes(old, now.Add(-8*24*time.Hour), now.Add(-8*24*time.Hour))
	Prune(base, now)
	entries, _ := os.ReadDir(PendingDir(base))
	if len(entries) != MaxQueued {
		t.Fatalf("pending after prune = %d, want %d", len(entries), MaxQueued)
	}
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("8-day-old held report survived: %v", err)
	}
}

func TestResponseRequestID(t *testing.T) {
	h := http.Header{}
	if id := ResponseRequestID(h, []byte(`{"error":{"message":"nope","request_id":"ABCDEFGH1234"}}`)); id != "ABCDEFGH1234" {
		t.Fatalf("body id = %q", id)
	}
	h.Set("X-Request-ID", "7K3M9QW2HX4D")
	if id := ResponseRequestID(h, nil); id != "7K3M9QW2HX4D" {
		t.Fatalf("header id = %q", id)
	}
}
