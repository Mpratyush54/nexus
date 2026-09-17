package main

import (
	"strings"
	"testing"
)

func TestRenderTableContainsHeadersAndRows(t *testing.T) {
	got := renderTable(
		[]string{"ID", "KEY", "CONTENT"},
		[][]string{
			{"mem_1", "testing/framework", "uses pytest"},
			{"mem_2", "security/auth", "jwt only"},
		},
	)
	for _, want := range []string{"ID", "KEY", "CONTENT", "mem_1", "testing/framework", "uses pytest", "mem_2", "jwt only"} {
		if !strings.Contains(got, want) {
			t.Errorf("renderTable output missing %q:\n%s", want, got)
		}
	}
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines (header + 2 rows), got %d:\n%s", len(lines), got)
	}
}

func TestRenderTableEmptyRows(t *testing.T) {
	got := renderTable([]string{"ID", "TITLE"}, nil)
	if !strings.Contains(got, "ID") || !strings.Contains(got, "TITLE") {
		t.Errorf("header missing from empty table:\n%s", got)
	}
	if lines := strings.Split(strings.TrimSpace(got), "\n"); len(lines) != 1 {
		t.Errorf("expected header-only output, got %d lines:\n%s", len(lines), got)
	}
}

func TestRenderTableAlignsColumns(t *testing.T) {
	got := renderTable(
		[]string{"A", "B"},
		[][]string{{"x", "long-value-here"}, {"long-key-here", "y"}},
	)
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	// The short first-column value must be padded so B starts at the same
	// offset on every row.
	idx := func(s string) int { return strings.Index(s, "long-value-here") }
	headerB := strings.Index(lines[0], "B")
	if idx(lines[1]) <= 0 || headerB <= 0 || idx(lines[1]) != strings.Index(lines[1], "long-value-here") {
		t.Errorf("columns not aligned:\n%s", got)
	}
	_ = headerB
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 80); got != "short" {
		t.Errorf("short string changed: %q", got)
	}
	if got := truncate("a  b\tc\n d", 80); got != "a b c d" {
		t.Errorf("whitespace not collapsed: %q", got)
	}
	long := strings.Repeat("x", 100)
	got := truncate(long, 80)
	if len([]rune(got)) > 80 {
		t.Errorf("truncate exceeded limit: %d chars", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated string missing ellipsis: %q", got)
	}
}

func TestParseErrorMessageEnvelope(t *testing.T) {
	got := parseErrorMessage(404, []byte(`{"error":{"code":404,"message":"no active workspace for project"}}`))
	if got != "no active workspace for project" {
		t.Errorf("envelope not parsed: %q", got)
	}
	got = parseErrorMessage(500, []byte(`boom`))
	if got != "boom" {
		t.Errorf("raw fallback failed: %q", got)
	}
	got = parseErrorMessage(418, nil)
	if got == "" {
		t.Error("expected non-empty fallback for empty body")
	}
}

func TestStrField(t *testing.T) {
	m := map[string]any{"id": "abc", "is_dirty": true, "n": 1.5}
	if strField(m, "missing", "id") != "abc" {
		t.Error("strField missed fallback key")
	}
	if strField(m, "is_dirty") != "true" {
		t.Error("strField bool wrong")
	}
	if strField(m, "absent") != "" {
		t.Error("strField should be empty for missing keys")
	}
}

func TestFilterByLevel(t *testing.T) {
	items := []*memoryItemJSON{
		{ID: "1", Level: "project"},
		{ID: "2", Level: "session"},
	}
	kept := filterByLevel(items, "SESSION")
	if len(kept) != 1 || kept[0].ID != "2" {
		t.Errorf("case-insensitive level filter failed: %+v", kept)
	}
	if got := itemsOrEmpty(nil); got == nil || len(got) != 0 {
		t.Error("itemsOrEmpty(nil) should return empty non-nil slice")
	}
}
