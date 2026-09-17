package store

import (
	"testing"
	"time"
)

func diffFixtureViews() (base, head []MemoryView) {
	ts := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	base = []MemoryView{
		{Key: "keep/same", Content: "unchanged fact about tests", Status: StatusConfirmed, Rev: 1, UpdatedAt: ts},
		{Key: "edit/me", Content: "old wording", Status: StatusConfirmed, Rev: 2, UpdatedAt: ts},
		{Key: "drop/me", Content: "removed fact", Status: StatusConfirmed, Rev: 3, UpdatedAt: ts},
	}
	head = []MemoryView{
		{Key: "keep/same", Content: "unchanged fact about tests", Status: StatusConfirmed, Rev: 9, UpdatedAt: ts.Add(time.Hour)},
		{Key: "edit/me", Content: "new wording", Status: StatusConfirmed, Rev: 10, UpdatedAt: ts.Add(time.Hour)},
		{Key: "new/key", Content: "branch-local discovery", Status: StatusProposed, Rev: 11, UpdatedAt: ts.Add(time.Hour)},
	}
	return base, head
}

func keys(changes []KeyChange) []string {
	out := make([]string, len(changes))
	for i, c := range changes {
		out[i] = c.Key
	}
	return out
}

func TestDiffDiverged(t *testing.T) {
	base, head := diffFixtureViews()
	d := Diff(base, head)

	if got := keys(d.Added); len(got) != 1 || got[0] != "new/key" {
		t.Fatalf("Added = %v, want [new/key]", got)
	}
	if got := keys(d.Modified); len(got) != 1 || got[0] != "edit/me" {
		t.Fatalf("Modified = %v, want [edit/me]", got)
	}
	if got := keys(d.Deleted); len(got) != 1 || got[0] != "drop/me" {
		t.Fatalf("Deleted = %v, want [drop/me]", got)
	}
	if d.IsEmpty() {
		t.Fatal("IsEmpty = true on diverged diff")
	}
	// Payloads: Added carries Head only, Deleted carries Base only,
	// Modified carries both with differing content.
	if d.Added[0].Head.Content != "branch-local discovery" {
		t.Fatalf("Added Head content = %q", d.Added[0].Head.Content)
	}
	if d.Deleted[0].Base.Content != "removed fact" {
		t.Fatalf("Deleted Base content = %q", d.Deleted[0].Base.Content)
	}
	m := d.Modified[0]
	if m.Base.Content != "old wording" || m.Head.Content != "new wording" || !m.Changed {
		t.Fatalf("Modified entry = %+v", m)
	}
}

func TestDiffIdenticalAndEmpty(t *testing.T) {
	base, _ := diffFixtureViews()
	if d := Diff(base, base); !d.IsEmpty() {
		t.Fatalf("self-diff not empty: %+v", d)
	}
	if d := Diff(nil, nil); !d.IsEmpty() {
		t.Fatalf("nil diff not empty: %+v", d)
	}
	d := Diff(nil, nil)
	if d.Added == nil || d.Modified == nil || d.Deleted == nil {
		t.Fatal("diff slices must be non-nil")
	}
}

func TestDiffContentEqualityIgnoresMetadata(t *testing.T) {
	// Same content, different Rev/Status/timestamps: converged, not modified.
	base := []MemoryView{{Key: "k", Content: "same", Status: StatusConfirmed, Rev: 1}}
	head := []MemoryView{{Key: "k", Content: "same", Status: StatusProposed, Rev: 99, UpdatedAt: time.Now()}}
	if d := Diff(base, head); !d.IsEmpty() {
		t.Fatalf("metadata-only difference flagged: %+v", d)
	}
}

func TestMergeNonConflictingAutoProposed(t *testing.T) {
	source := []MemoryView{
		{Key: "shared/same", Content: "agreed fact here", Status: StatusConfirmed, Rev: 5},
		{Key: "src/only", Content: "source branch finding", Status: StatusConfirmed, Rev: 6},
	}
	target := []MemoryView{
		{Key: "shared/same", Content: "agreed fact here", Status: StatusConfirmed, Rev: 7},
		{Key: "tgt/only", Content: "target branch finding", Status: StatusConfirmed, Rev: 8},
	}
	res := Merge(source, target)

	if len(res.ToPropose) != 1 || res.ToPropose[0].Key != "src/only" {
		t.Fatalf("ToPropose = %+v, want [src/only]", res.ToPropose)
	}
	if res.ToPropose[0].Status != StatusProposed {
		t.Fatalf("ToPropose status = %q, want PROPOSED (auto-propose, never confirmed overwrite)", res.ToPropose[0].Status)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "shared/same" {
		t.Fatalf("Skipped = %v, want [shared/same]", res.Skipped)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("Conflicts = %+v, want none", res.Conflicts)
	}
}

func TestMergeConflictFlagging(t *testing.T) {
	source := []MemoryView{{Key: "testing/framework", Content: "we use pytest", Status: StatusConfirmed, Rev: 5}}
	target := []MemoryView{{Key: "testing/framework", Content: "we use go test", Status: StatusConfirmed, Rev: 9}}
	before := append([]MemoryView(nil), target...)

	res := Merge(source, target)

	if len(res.Conflicts) != 1 {
		t.Fatalf("Conflicts = %+v, want 1", res.Conflicts)
	}
	c := res.Conflicts[0]
	if c.Key != "testing/framework" || c.SourceContent != "we use pytest" || c.TargetContent != "we use go test" {
		t.Fatalf("conflict = %+v", c)
	}
	if len(res.ToPropose) != 0 {
		t.Fatalf("conflicting key must not be auto-proposed: %+v", res.ToPropose)
	}
	// No-overwrite: Merge is pure; target slice is untouched.
	if len(target) != len(before) || target[0].Content != before[0].Content {
		t.Fatalf("target mutated by Merge: %+v", target)
	}
}

func TestMergeNoOverwriteTargetOnlyKeys(t *testing.T) {
	// Keys only on the target are left alone: merge is additive, no
	// deletion propagation.
	source := []MemoryView{{Key: "a", Content: "source fact", Status: StatusConfirmed}}
	target := []MemoryView{
		{Key: "a", Content: "source fact", Status: StatusConfirmed},
		{Key: "tgt/keep", Content: "stays on target", Status: StatusConfirmed},
	}
	res := Merge(source, target)
	if len(res.ToPropose) != 0 || len(res.Conflicts) != 0 {
		t.Fatalf("unexpected merge output: %+v", res)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("Skipped = %v", res.Skipped)
	}
}

func TestMergeEmptySource(t *testing.T) {
	target := []MemoryView{{Key: "a", Content: "fact", Status: StatusConfirmed}}
	res := Merge(nil, target)
	if len(res.ToPropose) != 0 || len(res.Conflicts) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("empty source must merge to nothing: %+v", res)
	}
}

func TestStaleParentUpdateFlagged(t *testing.T) {
	fork := []MemoryView{{Key: "auth/mode", Content: "JWT for all APIs"}}
	parentNow := []MemoryView{{Key: "auth/mode", Content: "JWT for all APIs, mTLS for payments"}}
	childNow := []MemoryView{{Key: "auth/mode", Content: "JWT for all APIs"}}

	stale := DetectStale(fork, parentNow, childNow)
	if len(stale) != 1 {
		t.Fatalf("stale = %+v, want 1 flag", stale)
	}
	s := stale[0]
	if !s.IsStale() || s.Key != "auth/mode" {
		t.Fatalf("stale item = %+v", s)
	}
	if s.Diverged() {
		t.Fatal("child did not edit; Diverged must be false (plain behind)")
	}
	if s.ForkContent != "JWT for all APIs" || s.ParentContent != "JWT for all APIs, mTLS for payments" {
		t.Fatalf("stale payload = %+v", s)
	}
}

func TestStaleNoParentChange(t *testing.T) {
	fork := []MemoryView{{Key: "k", Content: "v"}}
	parentNow := []MemoryView{{Key: "k", Content: "v"}}
	// Child edited locally while parent stood still: not staleness.
	childNow := []MemoryView{{Key: "k", Content: "v2 child edit"}}
	if stale := DetectStale(fork, parentNow, childNow); len(stale) != 0 {
		t.Fatalf("stale = %+v, want none (parent untouched)", stale)
	}
}

func TestStaleDivergedBothChanged(t *testing.T) {
	fork := []MemoryView{{Key: "k", Content: "base"}}
	parentNow := []MemoryView{{Key: "k", Content: "parent rewrote"}}
	childNow := []MemoryView{{Key: "k", Content: "child rewrote"}}

	stale := DetectStale(fork, parentNow, childNow)
	if len(stale) != 1 {
		t.Fatalf("stale = %+v, want 1", stale)
	}
	if !stale[0].IsStale() || !stale[0].Diverged() {
		t.Fatalf("both-moved key must be stale AND diverged: %+v", stale[0])
	}
}

func TestStaleParentDeleteFlagged(t *testing.T) {
	fork := []MemoryView{{Key: "k", Content: "v"}}
	parentNow := []MemoryView{} // parent deleted after fork
	childNow := []MemoryView{{Key: "k", Content: "v"}}

	stale := DetectStale(fork, parentNow, childNow)
	if len(stale) != 1 || !stale[0].IsStale() {
		t.Fatalf("parent delete must flag staleness: %+v", stale)
	}
	if stale[0].ParentContent != "" {
		t.Fatalf("deleted parent content must be empty: %+v", stale[0])
	}
}

func TestStaleIgnoresChildLocalAndParentNewKeys(t *testing.T) {
	fork := []MemoryView{{Key: "old", Content: "v"}}
	parentNow := []MemoryView{
		{Key: "old", Content: "v"},
		{Key: "parent/new", Content: "postdates fork"},
	}
	childNow := []MemoryView{
		{Key: "old", Content: "v"},
		{Key: "child/local", Content: "never on parent"},
		{Key: "parent/new", Content: "child independently wrote same"},
	}
	if stale := DetectStale(fork, parentNow, childNow); len(stale) != 0 {
		t.Fatalf("stale = %+v, want none", stale)
	}
}
