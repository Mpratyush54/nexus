package agentfiles

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

const section = StartMarker + "\nhello\n" + EndMarker

func TestMergeAppendsWithBlankLine(t *testing.T) {
	cases := map[string]string{
		"# Title\nSome text\n":   "# Title\nSome text\n\n" + section + "\n",
		"# Title\nSome text":     "# Title\nSome text\n\n" + section + "\n",
		"# Title\nSome text\n\n": "# Title\nSome text\n\n" + section + "\n",
		"":                       section + "\n",
	}
	for in, want := range cases {
		out, changed, err := Merge([]byte(in), "hello")
		if err != nil || !changed {
			t.Fatalf("Merge(%q): changed=%v err=%v", in, changed, err)
		}
		if string(out) != want {
			t.Fatalf("Merge(%q)=\n%q\nwant\n%q", in, out, want)
		}
		if !strings.HasPrefix(string(out), in) {
			t.Fatalf("existing text was altered: %q", out)
		}
	}
}

func TestMergeReplacesOnlyBetweenMarkers(t *testing.T) {
	in := "before\n" + StartMarker + "\nold stuff\nmore\n" + EndMarker + "\nafter\n"
	out, changed, err := Merge([]byte(in), "new body")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := "before\n" + StartMarker + "\nnew body\n" + EndMarker + "\nafter\n"
	if string(out) != want {
		t.Fatalf("got %q want %q", out, want)
	}
	// A hand-edited start-marker comment is still recognised and rewritten.
	in2 := "x\n<!-- nexus:memory:start edited -->\nold\n" + EndMarker + "\ny"
	out2, _, err := Merge([]byte(in2), "b")
	if err != nil || string(out2) != "x\n"+StartMarker+"\nb\n"+EndMarker+"\ny" {
		t.Fatalf("edited marker: %q %v", out2, err)
	}
}

func TestMergeMalformedMarkersLeaveFileUntouched(t *testing.T) {
	cases := []string{
		"a\n" + StartMarker + "\nno end\n",
		"a\n" + EndMarker + "\nno start\n",
		EndMarker + "\n" + StartMarker + "\n",
		StartMarker + "\n" + StartMarker + "\n" + EndMarker + "\n",
		StartMarker + "\n" + EndMarker + "\n" + EndMarker + "\n",
		"<!-- nexus:memory:start never closed\n" + EndMarker + "\n",
		StartMarker + "\n<!-- nexus:memory:end\n",
	}
	for _, in := range cases {
		out, changed, err := Merge([]byte(in), "body")
		if !errors.Is(err, ErrMalformedMarkers) || changed || string(out) != in {
			t.Fatalf("Merge(%q) = %q changed=%v err=%v", in, out, changed, err)
		}
	}
}

func TestMergePreservesCRLFAndBOM(t *testing.T) {
	in := "\xEF\xBB\xBF# Title\r\nline\r\n"
	out, changed, err := Merge([]byte(in), "a\nb")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := "\xEF\xBB\xBF# Title\r\nline\r\n\r\n" + StartMarker + "\r\na\r\nb\r\n" + EndMarker + "\r\n"
	if string(out) != want {
		t.Fatalf("got %q want %q", out, want)
	}
	if bytes.Contains(bytes.ReplaceAll(out, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatalf("lone LF introduced: %q", out)
	}
	// Second pass with the same body is a no-op.
	again, changed, err := Merge(out, "a\nb")
	if err != nil || changed || !bytes.Equal(again, out) {
		t.Fatalf("second merge changed=%v err=%v", changed, err)
	}
	// LF file without BOM stays LF without BOM.
	lf, _, _ := Merge([]byte("x\n"), "a\r\nb")
	if bytes.HasPrefix(lf, utf8BOM) || bytes.Contains(lf, []byte("\r")) {
		t.Fatalf("LF file changed style: %q", lf)
	}
}

func TestMergeNoOpWhenUnchanged(t *testing.T) {
	in := "top\n\n" + section + "\n"
	out, changed, err := Merge([]byte(in), "hello")
	if err != nil || changed || string(out) != in {
		t.Fatalf("expected no-op: %q changed=%v err=%v", out, changed, err)
	}
}

func TestMergeRejectsLargeAndNonUTF8(t *testing.T) {
	big := bytes.Repeat([]byte("a"), MaxFileBytes+1)
	if _, _, err := Merge(big, "x"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("large: %v", err)
	}
	if _, _, err := Merge([]byte{'a', 0xff, 0xfe}, "x"); !errors.Is(err, ErrNotUTF8) {
		t.Fatalf("utf8: %v", err)
	}
}

func TestRenderCapsAndSanitizes(t *testing.T) {
	var mems []Memory
	for i := 0; i < 15; i++ {
		mems = append(mems, Memory{ID: string(rune('a' + i)), Key: "k", Content: strings.Repeat("decision ", 80)})
	}
	brief := &Brief{Content: strings.Repeat("brief text ", 1000) + " <!-- nexus:memory:end -->", UpdatedAt: "t"}
	out := Render(brief, mems)
	if n := utf8.RuneCountInString(out); n > MaxSectionChars {
		t.Fatalf("section %d runes > cap", n)
	}
	if strings.Contains(out, "nexus:memory:") {
		t.Fatalf("marker leaked into body")
	}
	if !strings.Contains(out, "### Brief") || !strings.Contains(out, "### Key decisions") {
		t.Fatalf("missing headings:\n%s", out)
	}
	if strings.Count(out, "\n- ") > MaxMemories {
		t.Fatalf("too many memories")
	}
	if Render(nil, nil) != "" || Render(&Brief{Content: "  "}, []Memory{{Content: " "}}) != "" {
		t.Fatalf("empty render should be empty")
	}
	// A rendered body round-trips through Merge without malformed markers.
	if _, _, err := Merge([]byte("x\n"), out); err != nil {
		t.Fatalf("merge rendered: %v", err)
	}
	if _, _, err := Merge([]byte("x\n"), RenderOrPlaceholder(nil, nil)); err != nil {
		t.Fatalf("placeholder: %v", err)
	}
}

func TestFingerprintChanges(t *testing.T) {
	s := Settings{Enabled: true, Files: []string{"claude"}}
	b := &Brief{Content: "c", UpdatedAt: "1"}
	m := []Memory{{ID: "1", UpdatedAt: "1", Content: "x"}}
	base := Fingerprint(b, m, s)
	if base != Fingerprint(&Brief{Content: "c", UpdatedAt: "1"}, m, s) {
		t.Fatal("not deterministic")
	}
	if base == Fingerprint(&Brief{Content: "c", UpdatedAt: "2"}, m, s) {
		t.Fatal("brief updated_at ignored")
	}
	if base == Fingerprint(b, []Memory{{ID: "2", UpdatedAt: "1", Content: "x"}}, s) {
		t.Fatal("memory set ignored")
	}
	if base == Fingerprint(b, m, Settings{Enabled: true, Files: []string{"agents"}}) {
		t.Fatal("settings ignored")
	}
}

func TestSyncFileMissing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "CLAUDE.md")
	r := SyncFile(p, "body", false)
	if r.Action != ActionSkipped {
		t.Fatalf("create_missing=false: %+v", r)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("file created without opt-in")
	}
	r = SyncFile(p, "body", true)
	if r.Action != ActionCreated {
		t.Fatalf("create_missing=true: %+v", r)
	}
	got, _ := os.ReadFile(p)
	if string(got) != StartMarker+"\nbody\n"+EndMarker+"\n" {
		t.Fatalf("created content %q", got)
	}
	if r := SyncFile(filepath.Join(dir, "AGENTS.md"), "", true); r.Action != ActionSkipped {
		t.Fatalf("empty body should not create: %+v", r)
	}
}

func TestSyncFileUpdateNoOpAndSafety(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "AGENTS.md")
	orig := "\xEF\xBB\xBF# Agents\r\nkeep me\r\n"
	if err := os.WriteFile(p, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := SyncFile(p, "b1", false); r.Action != ActionUpdated {
		t.Fatalf("update: %+v", r)
	}
	first, _ := os.ReadFile(p)
	if !bytes.HasPrefix(first, []byte(orig)) {
		t.Fatalf("outside text changed: %q", first)
	}
	st1, _ := os.Stat(p)
	if r := SyncFile(p, "b1", false); r.Action != ActionUnchanged {
		t.Fatalf("no-op: %+v", r)
	}
	st2, _ := os.Stat(p)
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatalf("unchanged file was rewritten")
	}
	// Empty body clears an existing section to the placeholder.
	if r := SyncFile(p, "", false); r.Action != ActionUpdated {
		t.Fatalf("clear: %+v", r)
	}
	cleared, _ := os.ReadFile(p)
	if !bytes.Contains(cleared, []byte(emptyBody)) || !bytes.HasPrefix(cleared, []byte(orig)) {
		t.Fatalf("clear content %q", cleared)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}

	// Malformed markers: untouched, reported.
	bad := filepath.Join(dir, "GEMINI.md")
	badText := "x\n" + StartMarker + "\n"
	_ = os.WriteFile(bad, []byte(badText), 0o644)
	r := SyncFile(bad, "b", false)
	if r.Action != ActionSkipped || !errors.Is(r.Err, ErrMalformedMarkers) {
		t.Fatalf("malformed: %+v", r)
	}
	if got, _ := os.ReadFile(bad); string(got) != badText {
		t.Fatalf("malformed file modified")
	}

	// Large and non-UTF-8 files are skipped untouched.
	big := filepath.Join(dir, "big.md")
	_ = os.WriteFile(big, bytes.Repeat([]byte("a"), MaxFileBytes+10), 0o644)
	if r := SyncFile(big, "b", false); r.Action != ActionSkipped || !errors.Is(r.Err, ErrTooLarge) {
		t.Fatalf("big: %+v", r)
	}
	bin := filepath.Join(dir, "bin.md")
	_ = os.WriteFile(bin, []byte{0xff, 0xfe, 'a'}, 0o644)
	if r := SyncFile(bin, "b", false); r.Action != ActionSkipped || !errors.Is(r.Err, ErrNotUTF8) {
		t.Fatalf("bin: %+v", r)
	}
}

func TestSyncWorkspaceRespectsSettings(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# c\n"), 0o644)
	if res := SyncWorkspace(dir, Settings{Files: AllFiles}, "b"); res != nil {
		t.Fatalf("disabled should do nothing: %+v", res)
	}
	res := SyncWorkspace(dir, Settings{Enabled: true, Files: []string{"CLAUDE", "agents", "bogus"}}, "b")
	if len(res) != 2 || res[0].Action != ActionUpdated || res[1].Action != ActionSkipped {
		t.Fatalf("results: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("AGENTS.md created without create_missing")
	}
}

func TestSettingsDefaultOffAndRoundTrip(t *testing.T) {
	t.Setenv("CENTRAL_MEMORY_CONFIG_DIR", t.TempDir())
	root := filepath.Join(t.TempDir(), "Proj")
	s, err := GetSettings(root)
	if err != nil || s.Enabled || s.CreateMissing || len(s.Files) != 3 {
		t.Fatalf("default: %+v %v", s, err)
	}
	saved, err := SetSettings(root, Settings{Enabled: true, Files: []string{"gemini", "claude", "claude", "x"}, CreateMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(saved.Files, ",") != "claude,gemini" {
		t.Fatalf("normalised files %v", saved.Files)
	}
	got, err := GetSettings(root + string(filepath.Separator))
	if err != nil || !got.Enabled || !got.CreateMissing || strings.Join(got.Files, ",") != "claude,gemini" {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	other, _ := GetSettings(filepath.Join(t.TempDir(), "other"))
	if other.Enabled {
		t.Fatal("settings leaked to another workspace")
	}
}
