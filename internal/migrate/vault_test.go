package migrate

import (
	"strings"
	"testing"
)

const learningsFixture = `# Learnings

## 2026-09-10 15:04
tags: go, testing

The team uses pytest with fixture-based setup and testcontainers for Postgres integration tests.

## 2026-09-12
tags: security

All APIs must use JWT authentication and reject expired tokens on every upgrade path.

## 2026-09-13 09:00

Alice prefers detailed error messages with full stack context for debugging sessions.
`

func TestParseLearningsMD(t *testing.T) {
	items := ParseLearningsMD(learningsFixture)
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	first := items[0]
	if first.Level != "organization" {
		t.Errorf("level = %q, want organization", first.Level)
	}
	if first.Status != "CONFIRMED" {
		t.Errorf("status = %q, want CONFIRMED", first.Status)
	}
	if first.Project != "" {
		t.Errorf("project = %q, want empty (global)", first.Project)
	}
	if len(first.Tags) != 2 || first.Tags[0] != "go" || first.Tags[1] != "testing" {
		t.Errorf("tags = %v, want [go testing]", first.Tags)
	}
	if first.Confidence != 1.0 {
		t.Errorf("confidence = %v, want 1.0 (timestamp + tags)", first.Confidence)
	}
	if first.Source != "vault:learnings.md" {
		t.Errorf("source = %q", first.Source)
	}
	if !strings.HasPrefix(first.Key, "migrated/") {
		t.Errorf("key = %q, want migrated/ prefix", first.Key)
	}
	if first.CreatedAt.Format("2006-01-02") != "2026-09-10" {
		t.Errorf("createdAt = %v, want 2026-09-10", first.CreatedAt)
	}

	// Timestamp + tags -> 1.0.
	if items[1].Confidence != 1.0 {
		t.Errorf("confidence = %v, want 1.0 (timestamp + tags)", items[1].Confidence)
	}
	// Constraint keyword classification.
	if items[1].Scope != "constraint" {
		t.Errorf("scope = %q, want constraint", items[1].Scope)
	}
	// Preference keyword classification; timestamp but no tags -> 0.9.
	if items[2].Scope != "preference" {
		t.Errorf("scope = %q, want preference", items[2].Scope)
	}
	if items[2].Confidence != 0.9 {
		t.Errorf("confidence = %v, want 0.9 (timestamp, no tags)", items[2].Confidence)
	}
}

func TestParseLearningsMDShortChunkSkipped(t *testing.T) {
	data := "# Learnings\n\n## 2026-09-10 15:04\ntags: x\n\ntoo short\n"
	items, skipped := parseMarkdownReport(data, "organization", "", "vault:learnings.md")
	if len(items) != 0 {
		t.Fatalf("got %d items, want 0 (below 20-char floor)", len(items))
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
}

func TestParseProjectMemoryMD(t *testing.T) {
	data := "## 2026-09-11 10:00\ntags: deploy\n\nThe team decided on blue-green deploys after the Friday outage postmortem.\n"
	items := ParseProjectMemoryMD("shop", data)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	it := items[0]
	if it.Level != "project" || it.Project != "shop" {
		t.Errorf("level/project = %q/%q, want project/shop", it.Level, it.Project)
	}
	if it.Scope != "decision" {
		t.Errorf("scope = %q, want decision", it.Scope)
	}
	if it.Source != "vault:MEMORY.md/shop" {
		t.Errorf("source = %q", it.Source)
	}
}

func TestParseChunkHeaderVariants(t *testing.T) {
	// Inline title words become the snippet, never tags.
	items, _ := parseMarkdownReport("## 2026-09-10 go testing notes\ntags: x\n\nContent here is long enough to be insertable okay.\n", "organization", "", "s")
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if len(items[0].Tags) != 1 || items[0].Tags[0] != "x" {
		t.Errorf("tags = %v, title words must not leak into tags", items[0].Tags)
	}
	if !strings.Contains(items[0].ContextSnippet, "go testing notes") {
		t.Errorf("snippet = %q, want title preserved", items[0].ContextSnippet)
	}

	// No timestamp at all -> 0.8 confidence, zero time.
	items, _ = parseMarkdownReport("## just a heading\n\nThis chunk has no timestamp but is long enough to import.\n", "organization", "", "s")
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].Confidence != 0.8 {
		t.Errorf("confidence = %v, want 0.8 (no time, no tags)", items[0].Confidence)
	}
	if !items[0].CreatedAt.IsZero() {
		t.Errorf("createdAt should be zero when no timestamp parses")
	}
}

func TestDeduplicateItems(t *testing.T) {
	a := ParseLearningsMD("## 2026-09-10 15:04\ntags: go\n\nThe team uses pytest with fixture-based setup and testcontainers.\n")
	b := ParseProjectMemoryMD("shop", "## 2026-09-11 10:00\ntags: other\n\nThe team uses pytest with fixture-based setup and testcontainers.\n")
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("fixtures must each yield 1 item (got %d, %d)", len(a), len(b))
	}
	// Same key derivation proves content-identical chunks converge,
	// so cross-file dedup can rely on content, not keys.
	if a[0].Key != b[0].Key {
		t.Fatalf("identical content gave different keys: %q vs %q", a[0].Key, b[0].Key)
	}
	merged := DeduplicateItems(append(a, b...))
	if len(merged) != 1 {
		t.Fatalf("merged = %d items, want 1", len(merged))
	}
}
