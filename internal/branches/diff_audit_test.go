package branches

import (
	"testing"
)

func TestAuditDiffHashNormalization(t *testing.T) {
	a := []Entry{{Key: "k", Content: "hello"}}
	b := []Entry{{Key: "k", Content: "  hello\n\n"}}
	if got := DiffBranches(a, b); len(got.Unchanged) != 1 || len(got.Modified) != 0 {
		t.Errorf("whitespace-only diff should be unchanged: %+v", got)
	}
	// Byte-different content is modified, never add+remove.
	got := DiffBranches([]Entry{{Key: "k", Content: "v1"}}, []Entry{{Key: "k", Content: "v2"}})
	if len(got.Modified) != 1 || len(got.Added) != 0 || len(got.Removed) != 0 {
		t.Errorf("same key different bytes must be modified-only: %+v", got)
	}
	if got.Modified[0].OldContent != "v1" || got.Modified[0].NewContent != "v2" {
		t.Errorf("change payload wrong: %+v", got.Modified[0])
	}
	if NormalizeContent("  x\n") != "x" {
		t.Errorf("NormalizeContent should TrimSpace")
	}
}

func TestAuditDiffSortAndBuckets(t *testing.T) {
	a := []Entry{{Key: "z"}, {Key: "a", Content: "1"}, {Key: "m", Content: "same"}}
	b := []Entry{{Key: "b", Content: "new"}, {Key: "a2", Content: "x"}, {Key: "m", Content: "same"}}
	got := DiffBranches(a, b)
	// a: z removed, a removed, m unchanged; b: b added, a2 added.
	if len(got.Removed) != 2 || len(got.Added) != 2 || len(got.Unchanged) != 1 {
		t.Fatalf("buckets wrong: %+v", got)
	}
	for i := 1; i < len(got.Added); i++ {
		if !(got.Added[i-1].Key < got.Added[i].Key) {
			t.Errorf("Added not sorted: %+v", got.Added)
		}
	}
	for i := 1; i < len(got.Removed); i++ {
		if !(got.Removed[i-1].Key < got.Removed[i].Key) {
			t.Errorf("Removed not sorted: %+v", got.Removed)
		}
	}
	for i := 1; i < len(got.Unchanged); i++ {
		if !(got.Unchanged[i-1] < got.Unchanged[i]) {
			t.Errorf("Unchanged not sorted: %+v", got.Unchanged)
		}
	}
	if got := DiffBranches(nil, nil); len(got.Added) != 0 || len(got.Removed) != 0 || len(got.Modified) != 0 || len(got.Unchanged) != 0 {
		t.Errorf("empty diff should be empty: %+v", got)
	}
}

func TestAuditDiffDuplicateLastWins(t *testing.T) {
	a := []Entry{{Key: "k", Content: "old"}, {Key: "k", Content: "new"}}
	got := DiffBranches(a, []Entry{{Key: "k", Content: "new"}})
	if len(got.Unchanged) != 1 {
		t.Errorf("duplicate keys collapse last-write-wins: %+v", got)
	}
	got = DiffBranches(a, nil)
	if len(got.Removed) != 1 || got.Removed[0].Content != "new" {
		t.Errorf("removed should carry last write: %+v", got)
	}
}

func TestAuditDiffEmptyVsWhitespace(t *testing.T) {
	// Both normalize to "": unchanged, not modified.
	got := DiffBranches([]Entry{{Key: "k", Content: ""}}, []Entry{{Key: "k", Content: "   \n  "}})
	if len(got.Unchanged) != 1 {
		t.Errorf("empty vs whitespace should be unchanged: %+v", got)
	}
}
