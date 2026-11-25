package diagnostics

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRedactSecretsEmailsAndHomePaths(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.c2lnbmF0dXJlLXZhbHVl"
	cases := []struct {
		in      string
		gone    []string
		mustSee []string
	}{
		{"Authorization: Bearer abcdef1234567890", []string{"abcdef1234567890"}, []string{"[redacted]"}},
		{"token nxs_AbC123xyz_-987 leaked", []string{"AbC123xyz"}, []string{"nxs_[redacted]", "leaked"}},
		{"OPENAI sk-proj-abcdefghijklmnop1234 and sk-ant-api03-zzzzzzzzzzzzzzzz", []string{"abcdefghijklmnop", "zzzzzzzzzzzz"}, []string{"sk-[redacted]"}},
		{"jwt=" + jwt, []string{"eyJzdWIiOiJhbGljZSJ9"}, nil},
		{"session " + jwt + " expired", []string{"c2lnbmF0dXJl"}, []string{"session [redacted] expired"}},
		{"mail alice.smith+x@example.co.uk now", []string{"alice", "example.co.uk"}, []string{"[email]"}},
		{`open C:\Users\Pratyush Mishra\AppData\x.json`, nil, nil}, // spaces: name cut at space, checked below
		{`open C:\Users\pratyush\AppData\Local\nexus\log.txt failed`, []string{"pratyush"}, []string{`~\AppData\Local\nexus\log.txt failed`}},
		{`"path":"C:\\Users\\pratyush\\.nexus\\config.json"`, []string{"pratyush"}, []string{`~\\.nexus`}},
		{"at /Users/alice/src/app.go:12", []string{"alice"}, []string{"~/src/app.go:12"}},
		{"/home/bob/.config/nexus", []string{"bob"}, []string{"~/.config/nexus"}},
		{"file:///C:/Users/carol/x.ts", []string{"carol"}, []string{"~/x.ts"}},
		{"GET https://x.dev/cb?code=abc123&state=ok", []string{"abc123"}, []string{"state=ok"}},
		{`{"api_key": "verysecretvalue"}`, []string{"verysecretvalue"}, []string{`"api_key": "[redacted]`}},
		{"password=hunter22 user=x", []string{"hunter22"}, []string{"user=x"}},
		{"ghp_abcdefghijklmnopqrstuvwxyz0123 AKIAABCDEFGHIJKLMNOP", []string{"ghp_abc", "AKIAABCD"}, nil},
		{"https://bob:pa55w0rd@host/repo.git", []string{"pa55w0rd", "bob:"}, []string{"https://[redacted]@host"}},
		{"route /app/home/settings stays", nil, []string{"/app/home/settings"}},
	}
	for _, c := range cases {
		got := Redact(c.in)
		for _, g := range c.gone {
			if strings.Contains(got, g) {
				t.Errorf("Redact(%q) = %q, still contains %q", c.in, got, g)
			}
		}
		for _, m := range c.mustSee {
			if !strings.Contains(got, m) {
				t.Errorf("Redact(%q) = %q, want it to contain %q", c.in, got, m)
			}
		}
		if again := Redact(got); again != got {
			t.Errorf("Redact not idempotent: %q -> %q", got, again)
		}
	}
	// A Windows user name with a space keeps no part of the first word.
	if got := Redact(`C:\Users\Pratyush Mishra\x`); strings.Contains(got, "Pratyush") || strings.Contains(got, "Mishra") || got != `~\x` {
		t.Errorf("home path with space leaked first name: %q", got)
	}
}

func TestNormalizeCapsAndRedacts(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	logs := make([]string, MaxLogLines+50)
	for i := range logs {
		logs[i] = "line " + strings.Repeat("x", 10)
	}
	logs[len(logs)-1] = "last line with sk-abcdefghijklmnopqrstu " + strings.Repeat("y", MaxLogLineChars*2)
	future := now.Add(72 * time.Hour)
	r := &Report{
		Kind: " Crash ", Source: "WEB", AppVersion: "1.2.3\n", OS: "Windows",
		Message:   "boom for bob@example.com",
		Stack:     strings.Repeat("s", MaxStackChars+100),
		Logs:      logs,
		UserNote:  "my token is nxs_abcdefghijkl",
		Timestamp: &future,
		Context:   map[string]string{"route": "/app/chats?x=1", "token": "Bearer abcdefghijklmnop"},
	}
	if err := Normalize(r, now); err != nil {
		t.Fatal(err)
	}
	if r.Kind != KindCrash || r.Source != SourceWeb || r.AppVersion != "1.2.3" {
		t.Fatalf("normalized fields = %q %q %q", r.Kind, r.Source, r.AppVersion)
	}
	if len(r.ReportID) != 32 {
		t.Fatalf("generated report id = %q", r.ReportID)
	}
	if len(r.Logs) != MaxLogLines {
		t.Fatalf("logs = %d, want %d (newest kept)", len(r.Logs), MaxLogLines)
	}
	last := r.Logs[len(r.Logs)-1]
	if !strings.HasPrefix(last, "last line") || strings.Contains(last, "abcdefghijklmnop") || len(last) > MaxLogLineChars+4 {
		t.Fatalf("last log line not kept/redacted/capped: %d %q", len(last), last[:40])
	}
	if len(r.Stack) > MaxStackChars+4 {
		t.Fatalf("stack not capped: %d", len(r.Stack))
	}
	if strings.Contains(r.Message, "bob@") || strings.Contains(r.UserNote, "abcdefghijkl") || strings.Contains(r.Context["token"], "abcdefghijklmnop") {
		t.Fatalf("not redacted: %q %q %q", r.Message, r.UserNote, r.Context["token"])
	}
	if !r.Timestamp.Equal(now) {
		t.Fatalf("future timestamp should clamp to now, got %v", r.Timestamp)
	}
}

func TestNormalizeRejects(t *testing.T) {
	now := time.Now()
	for name, r := range map[string]*Report{
		"kind":      {Kind: "panic", Source: "web", Message: "x"},
		"source":    {Kind: "crash", Source: "phone", Message: "x"},
		"report_id": {ReportID: "bad id!", Kind: "crash", Source: "web", Message: "x"},
		"empty":     {Kind: "crash", Source: "web"},
	} {
		if err := Normalize(r, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	// A user report with only a note gets its message from the note.
	r := &Report{ReportID: "client-report-1", Kind: "user_report", Source: "web", UserNote: "Sync looks stuck\nmore"}
	if err := Normalize(r, now); err != nil || r.Message != "Sync looks stuck" || r.ReportID != "client-report-1" {
		t.Fatalf("note-only report: %v %q %q", err, r.Message, r.ReportID)
	}
}

func TestFingerprintGroupsSameProblem(t *testing.T) {
	goStack := func(addr, home string) string {
		return "panic: boom\n\ngoroutine 7 [running]:\nmain.run(0xc000012345, 0x1)\n\t" + home + "/src/nexus/run.go:42 +0x1d\nmain.main()\n\t" + home + "/src/nexus/main.go:10 +0x" + addr + "\n"
	}
	a := &Report{Kind: "crash", Source: "core", AppVersion: "1.0.0", Message: "boom 1", Stack: Redact(goStack("1d", "/home/alice"))}
	b := &Report{Kind: "crash", Source: "core", AppVersion: "1.0.0", Message: "boom 2", Stack: Redact(goStack("2f", "/Users/bob"))}
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatalf("same frames on different machines should group: %v vs %v", TopFrames(a.Stack, 3), TopFrames(b.Stack, 3))
	}
	c := *a
	c.AppVersion = "1.0.1"
	if Fingerprint(a) == Fingerprint(&c) {
		t.Fatal("different versions must not group")
	}
	d := *a
	d.Source = "desktop"
	if Fingerprint(a) == Fingerprint(&d) {
		t.Fatal("different sources must not group")
	}

	js1 := &Report{Kind: "error", Source: "web", AppVersion: "w1", Message: "TypeError: x is undefined",
		Stack: "TypeError: x is undefined\n    at Ab (https://nexus.example/assets/index-abc.js:1:234)\n    at Cd (https://nexus.example/assets/index-abc.js:1:999)"}
	js2 := *js1
	js2.Stack = strings.ReplaceAll(js1.Stack, "https://nexus.example", "http://localhost:5173")
	if Fingerprint(js1) != Fingerprint(&js2) {
		t.Fatal("same bundle frames on different hosts should group")
	}

	// No frames: messages differing only in numbers and IDs group.
	m1 := &Report{Kind: "error", Source: "web", Message: `Request 4521 failed for "abc" id 6f1c2a9e-1111-2222-3333-444455556666`}
	m2 := &Report{Kind: "error", Source: "web", Message: `Request 17 failed for "xyz" id 0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff`}
	if Fingerprint(m1) != Fingerprint(m2) {
		t.Fatal("message fallback should ignore numbers, quoted values and IDs")
	}
	if len(Fingerprint(m1)) != 16 {
		t.Fatalf("fingerprint length = %d", len(Fingerprint(m1)))
	}
}
