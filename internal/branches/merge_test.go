package branches

import "testing"

func TestMergeAutoMergesNonConflicting(t *testing.T) {
	base := []Entry{
		{Key: "k1", Content: "base value one"},
		{Key: "k2", Content: "shared value two"},
	}
	source := []Entry{
		{Key: "k1", Content: "source edit one"},
		{Key: "k2", Content: "shared value two"},
		{Key: "k3", Content: "brand new three"},
	}
	target := []Entry{
		{Key: "k1", Content: "base value one"},
		{Key: "k2", Content: "shared value two"},
	}

	m := Merge(base, source, target)
	if len(m.Conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none", m.Conflicts)
	}
	if len(m.Merged) != 2 {
		t.Fatalf("merged = %v, want k1+k3", m.Merged)
	}
	for _, it := range m.Merged {
		if it.Status != StatusProposed {
			t.Fatalf("merged %q status = %q, want PROPOSED", it.Key, it.Status)
		}
	}
	if len(m.Superseded) != 1 || m.Superseded[0].Key != "k1" ||
		m.Superseded[0].OldContent != "base value one" || m.Superseded[0].NewContent != "source edit one" {
		t.Fatalf("superseded = %v, want k1 old->new", m.Superseded)
	}
}

func TestMergeConflict(t *testing.T) {
	base := []Entry{{Key: "k1", Content: "base value one"}}
	source := []Entry{{Key: "k1", Content: "source value one"}}
	target := []Entry{{Key: "k1", Content: "target value one"}}

	m := Merge(base, source, target)
	if len(m.Conflicts) != 1 || m.Conflicts[0].Key != "k1" {
		t.Fatalf("conflicts = %v, want [k1]", m.Conflicts)
	}
	for _, it := range m.Merged {
		if it.Key == "k1" {
			t.Fatalf("conflicting key k1 auto-merged (silent overwrite): %v", m.Merged)
		}
	}
	for _, s := range m.Superseded {
		if s.Key == "k1" {
			t.Fatalf("conflicting key k1 marked superseded: %v", m.Superseded)
		}
	}
}

func TestMergeIdenticalChangesNoConflict(t *testing.T) {
	base := []Entry{{Key: "k1", Content: "base value one"}}
	source := []Entry{{Key: "k1", Content: "same edit applied"}}
	target := []Entry{{Key: "k1", Content: "same edit applied"}}

	m := Merge(base, source, target)
	if len(m.Conflicts) != 0 || len(m.Merged) != 0 || len(m.Superseded) != 0 {
		t.Fatalf("identical edits must be a no-op: %+v", m)
	}
}

func TestMergeAddAddClashConflicts(t *testing.T) {
	var base []Entry
	source := []Entry{{Key: "k", Content: "source addition"}}
	target := []Entry{{Key: "k", Content: "target addition"}}

	m := Merge(base, source, target)
	if len(m.Conflicts) != 1 {
		t.Fatalf("add/add clash: %+v, want 1 conflict", m)
	}
	if m.Conflicts[0].BaseFound || !m.Conflicts[0].SourceFound || !m.Conflicts[0].TargetFound {
		t.Fatalf("add/add found flags wrong: %+v", m.Conflicts[0])
	}
}

func TestMergeSourceDeletionPropagates(t *testing.T) {
	base := []Entry{{Key: "k", Content: "base value here"}}
	source := []Entry{} // deleted on source
	target := []Entry{{Key: "k", Content: "base value here"}}

	m := Merge(base, source, target)
	if len(m.Conflicts) != 0 {
		t.Fatalf("conflicts = %v, want none", m.Conflicts)
	}
	if len(m.Deleted) != 1 || m.Deleted[0] != "k" {
		t.Fatalf("deleted = %v, want [k]", m.Deleted)
	}
}

func TestMergeEditDeleteClashConflicts(t *testing.T) {
	base := []Entry{{Key: "k", Content: "base value here"}}
	source := []Entry{} // deleted on source
	target := []Entry{{Key: "k", Content: "target edited this"}}

	m := Merge(base, source, target)
	if len(m.Conflicts) != 1 {
		t.Fatalf("edit/delete clash: %+v, want 1 conflict", m)
	}
	if len(m.Deleted) != 0 {
		t.Fatalf("deleted = %v, must not propagate under conflict", m.Deleted)
	}
}
