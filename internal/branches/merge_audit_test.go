package branches

import (
	"testing"
)

func E(k, c string) Entry { return Entry{Key: k, Content: c} }

func TestAuditMergeSourceOnlyAdd(t *testing.T) {
	got := Merge(nil, []Entry{E("n", "v")}, nil)
	if len(got.Merged) != 1 || got.Merged[0].Status != StatusProposed {
		t.Fatalf("source-only add must auto-promote as PROPOSED: %+v", got)
	}
	if len(got.Superseded) != 0 {
		t.Errorf("pure add displaces nothing: %+v", got.Superseded)
	}
	// Target-only key untouched.
	got = Merge(nil, nil, []Entry{E("t", "v")})
	if len(got.Merged) != 0 || len(got.Conflicts) != 0 || len(got.Deleted) != 0 {
		t.Errorf("target-only key must be no-op: %+v", got)
	}
}

func TestAuditMergeTargetUnchangedTakesSource(t *testing.T) {
	base := []Entry{E("k", "base")}
	src := []Entry{E("k", "src-edit")}
	tgt := []Entry{E("k", "base")}
	got := Merge(base, src, tgt)
	if len(got.Merged) != 1 || len(got.Conflicts) != 0 {
		t.Fatalf("target-unchanged should take source: %+v", got)
	}
	if got.Merged[0].Content != "src-edit" || got.Merged[0].Status != StatusProposed {
		t.Errorf("merged payload wrong: %+v", got.Merged[0])
	}
	if len(got.Superseded) != 1 || got.Superseded[0].OldContent != "base" || got.Superseded[0].NewContent != "src-edit" {
		t.Errorf("superseded mark wrong: %+v", got.Superseded)
	}
	// Source unchanged: keep target edit, no writes.
	got = Merge(base, []Entry{E("k", "base")}, []Entry{E("k", "tgt-edit")})
	if len(got.Merged) != 0 || len(got.Conflicts) != 0 || len(got.Deleted) != 0 {
		t.Errorf("source-unchanged must keep target: %+v", got)
	}
}

func TestAuditMergeConflicts(t *testing.T) {
	base := []Entry{E("k", "base")}
	// Both changed divergently.
	got := Merge(base, []Entry{E("k", "s")}, []Entry{E("k", "t")})
	if len(got.Conflicts) != 1 || len(got.Merged) != 0 {
		t.Fatalf("divergent edit/edit must conflict: %+v", got)
	}
	c := got.Conflicts[0]
	if !c.BaseFound || !c.SourceFound || !c.TargetFound {
		t.Errorf("Found flags wrong for edit/edit: %+v", c)
	}
	// Add/add divergent.
	got = Merge(nil, []Entry{E("k", "s")}, []Entry{E("k", "t")})
	if len(got.Conflicts) != 1 {
		t.Fatalf("add/add clash must conflict: %+v", got)
	}
	if got.Conflicts[0].BaseFound {
		t.Errorf("add/add must have BaseFound=false: %+v", got.Conflicts[0])
	}
	// Add/add identical: agree, no-op.
	got = Merge(nil, []Entry{E("k", "same")}, []Entry{E("k", "same")})
	if len(got.Conflicts) != 0 || len(got.Merged) != 0 {
		t.Errorf("identical additions must agree: %+v", got)
	}
	// Identical edits both sides: no-op.
	got = Merge(base, []Entry{E("k", "same-edit")}, []Entry{E("k", "same-edit")})
	if len(got.Conflicts) != 0 || len(got.Merged) != 0 {
		t.Errorf("identical edits must agree: %+v", got)
	}
}

func TestAuditMergeDeletePaths(t *testing.T) {
	base := []Entry{E("k", "base")}
	// Source deleted, target untouched -> propagate + superseded with empty NewContent.
	got := Merge(base, nil, []Entry{E("k", "base")})
	if len(got.Deleted) != 1 || got.Deleted[0] != "k" {
		t.Fatalf("clean source-delete must propagate: %+v", got)
	}
	if len(got.Superseded) != 1 || got.Superseded[0].NewContent != "" {
		t.Errorf("delete superseded mark must have empty NewContent: %+v", got.Superseded)
	}
	// Source deleted but target edited -> conflict.
	got = Merge(base, nil, []Entry{E("k", "tgt-edit")})
	if len(got.Conflicts) != 1 || len(got.Deleted) != 0 {
		t.Fatalf("edit/delete clash must conflict: %+v", got)
	}
	// Target deleted, source untouched -> stands (no-op).
	got = Merge(base, []Entry{E("k", "base")}, nil)
	if len(got.Conflicts) != 0 || len(got.Deleted) != 0 || len(got.Merged) != 0 {
		t.Errorf("clean target-delete must stand: %+v", got)
	}
	// Target deleted, source edited -> conflict.
	got = Merge(base, []Entry{E("k", "src-edit")}, nil)
	if len(got.Conflicts) != 1 {
		t.Fatalf("source-edit/target-delete must conflict: %+v", got)
	}
	// Deleted both sides -> agree.
	got = Merge(base, nil, nil)
	if len(got.Conflicts) != 0 || len(got.Deleted) != 0 {
		t.Errorf("delete/delete must agree: %+v", got)
	}
}

func TestAuditMergeSortedAndWhitespace(t *testing.T) {
	base := []Entry{E("b", "x"), E("a", "x")}
	src := []Entry{E("b", "y"), E("a", "z"), E("c", "new")}
	tgt := []Entry{E("b", "x"), E("a", "x")}
	got := Merge(base, src, tgt)
	if len(got.Merged) != 3 {
		t.Fatalf("want 3 merged, got %+v", got)
	}
	for i := 1; i < len(got.Merged); i++ {
		if !(got.Merged[i-1].Key < got.Merged[i].Key) {
			t.Fatalf("Merged not sorted: %+v", got.Merged)
		}
	}
	for i := 1; i < len(got.Superseded); i++ {
		if !(got.Superseded[i-1].Key < got.Superseded[i].Key) {
			t.Fatalf("Superseded not sorted: %+v", got.Superseded)
		}
	}
	// Whitespace-only source edit is not an edit (hash-normalized).
	got = Merge([]Entry{E("k", "v")}, []Entry{E("k", "  v\n")}, []Entry{E("k", "v")})
	if len(got.Merged) != 0 || len(got.Conflicts) != 0 {
		t.Errorf("whitespace-only change must be no-op: %+v", got)
	}
}

func TestAuditMergeNoDepthCapInPackage(t *testing.T) {
	// PIN: internal/branches imposes NO depth limit. MaxBranchDepth=5 lives in
	// internal/store (fork-time chain guard), not in this diff/merge engine.
	// A 10-key merge must succeed without error (Merge returns a struct, never error).
	var base, src, tgt []Entry
	for i := 0; i < 10; i++ {
		k := string(rune('a' + i))
		base = append(base, E(k, "v0"))
		src = append(src, E(k, "v1"))
		tgt = append(tgt, E(k, "v0"))
	}
	got := Merge(base, src, tgt)
	if len(got.Merged) != 10 {
		t.Errorf("depth-10 fan-out should merge cleanly (no cap here): %+v", got)
	}
}

func TestAuditMergeEpisodesUnbranched(t *testing.T) {
	// PIN: episodes are unbranched — Entry carries only Key+Content, so episode
	// lifecycle fields (status, resolution, verification) cannot influence
	// merge verdicts. Two entries with same key+content agree even if their
	// notional episodes differ (not representable here by design).
	got := Merge([]Entry{E("k", "same")}, []Entry{E("k", "same")}, []Entry{E("k", "same")})
	if len(got.Merged) != 0 || len(got.Conflicts) != 0 {
		t.Errorf("identical content must agree regardless of episode metadata: %+v", got)
	}
}
