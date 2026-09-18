package migrate

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAuditSplitChunks(t *testing.T) {
	data := "# Learnings\n\n## 2026-09-01 10:00\ntags: a, b\n\nThis is a sufficiently long chunk body one.\n\n## 2026-09-02 11:00\n\nThis is a sufficiently long chunk body two here.\n"
	items := ParseLearningsMD(data)
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d: %+v", len(items), items)
	}
	// Title prelude not counted as skipped (parse engine returns skip counts;
	// public wrapper hides them — pin via short-chunk accounting below).
	// CRLF tolerated.
	crlf := "## 2026-09-01 10:00\r\ntags: x\r\n\r\nThis is a sufficiently long chunk body one.\r\n"
	if got := ParseLearningsMD(crlf); len(got) != 1 {
		t.Errorf("CRLF should parse: %+v", got)
	}
	// Chunk at byte 0 without leading newline.
	if got := ParseLearningsMD("## 2026-09-01 10:00\n\nThis is a sufficiently long chunk body one.\n"); len(got) != 1 {
		t.Errorf("byte-0 chunk: %+v", got)
	}
	// Title-only file yields nothing (prelude dropped, not a chunk).
	if got := ParseLearningsMD("# Just a title\n"); len(got) != 0 {
		t.Errorf("title-only should yield 0: %+v", got)
	}
}

func TestAuditTagsAndConfidence(t *testing.T) {
	data := "## 2026-09-01 10:00 standup notes\ntags: Go, go, BACKEND , ,frontend\n\nThis chunk body is definitely long enough to pass.\n"
	items := ParseLearningsMD(data)
	if len(items) != 1 {
		t.Fatalf("got %+v", items)
	}
	it := items[0]
	want := []string{"go", "backend", "frontend"}
	if len(it.Tags) != len(want) {
		t.Fatalf("tags = %v, want %v (lowercased, deduped)", it.Tags, want)
	}
	for i := range want {
		if it.Tags[i] != want[i] {
			t.Fatalf("tags = %v, want %v", it.Tags, want)
		}
	}
	// time + tags -> 1.0.
	if it.Confidence != 1.0 {
		t.Errorf("confidence = %v, want 1.0 (time+tags)", it.Confidence)
	}
	if !strings.Contains(it.ContextSnippet, "standup notes") {
		t.Errorf("title should be snippet prefix: %q", it.ContextSnippet)
	}
	// time-only -> 0.9 ; tags-only -> 0.9 ; neither -> 0.8.
	timeOnly := ParseLearningsMD("## 2026-09-01 10:00\n\nThis chunk body is definitely long enough here.\n")
	if len(timeOnly) != 1 || timeOnly[0].Confidence != 0.9 {
		t.Errorf("time-only confidence: %+v", timeOnly)
	}
	tagsOnly := ParseLearningsMD("## my note\ntags: x\n\nThis chunk body is definitely long enough here.\n")
	if len(tagsOnly) != 1 || tagsOnly[0].Confidence != 0.9 {
		t.Errorf("tags-only confidence: %+v", tagsOnly)
	}
	neither := ParseLearningsMD("## my note\n\nThis chunk body is definitely long enough here.\n")
	if len(neither) != 1 || neither[0].Confidence != 0.8 {
		t.Errorf("bare confidence: %+v", neither)
	}
	// Non-tags first line is body, not tags (case-insensitive prefix only).
	notags := ParseLearningsMD("## 2026-09-01 10:00\n\ntags are useful but this line is not a tag line and is long\n")
	if len(notags) != 1 || len(notags[0].Tags) != 0 {
		t.Errorf("body starting with 'tags ' w/o colon must not parse as tags: %+v", notags)
	}
}

func TestAuditRuneBounds(t *testing.T) {
	// Below 20-run floor skipped.
	if got := ParseLearningsMD("## 2026-09-01 10:00\n\nshort\n"); len(got) != 0 {
		t.Errorf("short chunk must be skipped: %+v", got)
	}
	// Exactly 20 runes passes.
	exact20 := strings.Repeat("a", 20)
	if got := ParseLearningsMD("## t\n\n" + exact20 + "\n"); len(got) != 1 {
		t.Errorf("20-rune floor must pass: %+v", got)
	}
	// Above 2000 truncated on rune boundary (multi-byte safe).
	big := strings.Repeat("é", 2500) // 1 rune, 2 bytes each
	got := ParseLearningsMD("## t\n\n" + big + "\n")
	if len(got) != 1 {
		t.Fatalf("big chunk dropped: %d", len(got))
	}
	if n := utf8.RuneCountInString(got[0].Content); n != 2000 {
		t.Errorf("truncated runes = %d, want 2000", n)
	}
	if !utf8.ValidString(got[0].Content) {
		t.Error("truncation split a rune (invalid UTF-8)")
	}
}

func TestAuditDedupNormalizeDrift(t *testing.T) {
	a := MemoryItem{Content: "Hello   World  Test Case Here Now"}
	b := MemoryItem{Content: "hello world test case here now"}
	if got := DeduplicateItems([]MemoryItem{a, b}); len(got) != 1 {
		t.Errorf("case/whitespace-insensitive dedup failed: %+v", got)
	}
	// First wins, order preserved.
	c := MemoryItem{Content: "Another distinct memory content here!"}
	if got := DeduplicateItems([]MemoryItem{a, c, b}); len(got) != 2 || got[0].Content != a.Content {
		t.Errorf("first-wins order: %+v", got)
	}
	// normalizeGitURL must mirror store.NormalizeGitURL (local copy kept in
	// sync by design). Drift cases: schemes, user@, scp colon, .git, case.
	cases := map[string]string{
		"git@github.com:Mpratyush54/nexus.git":     "github.com/mpratyush54/nexus",
		"https://github.com/Mpratyush54/nexus.git": "github.com/mpratyush54/nexus",
		"ssh://git@github.com/Mpratyush54/nexus":   "github.com/mpratyush54/nexus",
		"  https://GitHub.com/Org/Repo/ ":          "github.com/org/repo",
	}
	for in, want := range cases {
		if got := normalizeGitURL(in); got != want {
			t.Errorf("normalizeGitURL(%q) = %q, want %q (drift vs store!)", in, got, want)
		}
	}
	// Project-level parser stamps level/project/CONFIRMED.
	got := ParseProjectMemoryMD("myproj", "## 2026-09-01 10:00\n\nThis is a sufficiently long chunk body one.\n")
	if len(got) != 1 || got[0].Level != "project" || got[0].Project != "myproj" || got[0].Status != "CONFIRMED" {
		t.Errorf("project shape wrong: %+v", got)
	}
	glob := ParseLearningsMD("## 2026-09-01 10:00\n\nThis is a sufficiently long chunk body one.\n")
	if len(glob) != 1 || glob[0].Level != "organization" {
		t.Errorf("global shape wrong: %+v", glob)
	}
	// Key format migrated/<slug>-<crc8hex>.
	if !strings.HasPrefix(glob[0].Key, "migrated/") || len(glob[0].Key) < len("migrated/a-12345678") {
		t.Errorf("key shape wrong: %q", glob[0].Key)
	}
	// Scope heuristic ordering: constraint > preference > decision > pattern > fact.
	for content, want := range map[string]string{
		"You must never do this thing at all ever": "constraint",
		"I prefer tabs over spaces in this file":   "preference",
		"We decided to use postgres for storage":   "decision",
		"Our convention is hexagonal architecture": "pattern",
		"Just a plain observation about the sky":   "fact",
	} {
		if got := classifyScope(content); got != want {
			t.Errorf("classifyScope(%q) = %q, want %q", content, got, want)
		}
	}
}
