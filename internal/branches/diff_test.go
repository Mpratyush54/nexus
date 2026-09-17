package branches

import "testing"

func TestDiffBranches(t *testing.T) {
	a := []Entry{
		{Key: "keep", Content: "same content here"},
		{Key: "edit", Content: "old content here"},
		{Key: "del", Content: "gone content here"},
	}
	b := []Entry{
		{Key: "keep", Content: "same content here"},
		{Key: "edit", Content: "new content here"},
		{Key: "add", Content: "fresh content here"},
	}

	d := DiffBranches(a, b)

	if len(d.Added) != 1 || d.Added[0].Key != "add" {
		t.Fatalf("Added = %v, want [add]", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0].Key != "del" {
		t.Fatalf("Removed = %v, want [del]", d.Removed)
	}
	if len(d.Modified) != 1 || d.Modified[0].Key != "edit" ||
		d.Modified[0].OldContent != "old content here" || d.Modified[0].NewContent != "new content here" {
		t.Fatalf("Modified = %v, want [edit old->new]", d.Modified)
	}
	if len(d.Unchanged) != 1 || d.Unchanged[0] != "keep" {
		t.Fatalf("Unchanged = %v, want [keep]", d.Unchanged)
	}
}

func TestDiffBranchesEmpty(t *testing.T) {
	d := DiffBranches(nil, nil)
	if len(d.Added) != 0 || len(d.Removed) != 0 || len(d.Modified) != 0 || len(d.Unchanged) != 0 {
		t.Fatalf("empty diff = %+v, want all empty", d)
	}
}

func TestDiffBranchesIgnoresSurroundingWhitespace(t *testing.T) {
	a := []Entry{{Key: "k", Content: "some content here"}}
	b := []Entry{{Key: "k", Content: "\n  some content here\n"}}
	d := DiffBranches(a, b)
	if len(d.Modified) != 0 || len(d.Unchanged) != 1 {
		t.Fatalf("whitespace-only change: Modified=%v Unchanged=%v, want no modification", d.Modified, d.Unchanged)
	}
}

func TestDiffBranchesDuplicateKeysLastWins(t *testing.T) {
	a := []Entry{{Key: "k", Content: "v1 content here"}}
	b := []Entry{{Key: "k", Content: "v1 content here"}, {Key: "k", Content: "v2 content here"}}
	d := DiffBranches(a, b)
	if len(d.Modified) != 1 || d.Modified[0].NewContent != "v2 content here" {
		t.Fatalf("duplicate keys: Modified=%v, want last-write-wins v2", d.Modified)
	}
}
