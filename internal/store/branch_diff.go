// Branch diff, staleness, and merge (issue #18, plan §§5.2, 5.4).
//
// OWNERSHIP: internal/store/branches.go is owned by parallel issue #17
// (MemoryBranch CRUD + CoW resolution) and is NOT touched here. This file is
// a pure, DB-free overlay: it operates on the locally declared MemoryView
// projection and never imports #17 types (which have not landed — no
// branches.go exists in this tree at the time of writing). The ADR-018
// mapping table documents how MemoryView fields bind to the real
// memory_items row + memory_branches fork point once #17 lands.
//
// Semantics (plan §5.2):
//   - Diff(base, head) is key-level: Added (in head, not base), Modified
//     (same key, different content), Deleted (in base, not head).
//   - DetectStale compares a fork baseline against parent/child current
//     state: a child key is stale when the parent changed it after the fork.
//   - Merge(source, target) is two-way and never overwrites: source-only
//     keys become PROPOSED inserts on the target; same-value keys are
//     skipped; same-key/different-value keys become MergeConflicts flagged
//     for human review. Keys only on the target are left untouched (no
//     deletion propagation — merge is additive).
package store

import (
	"sort"
	"time"
)

// MemoryView is the minimal branch-diff projection of a memory item.
//
// Mapping to the #17/#1 schema (see ADR-018):
//   - Key     <-> memory_items.key
//   - Content <-> memory_items.content
//   - Status  <-> memory_items.status (Merge stamps StatusProposed)
//   - Rev     <-> originating events.id (fork point = branches.forked_at_event_id;
//     per-item rev = source_event_id). 0 means "unknown rev" and disables
//     rev-based tie-breaking (content comparison still applies).
//   - UpdatedAt <-> memory_items.updated_at (informational: carried into
//     results for review UIs, never used for equality).
type MemoryView struct {
	Key       string
	Content   string
	Status    string
	Rev       int64
	UpdatedAt time.Time
}

// KeyChange is one key-level diff entry. For Added, Base is empty; for
// Deleted, Head is empty; for Modified both are populated and differ.
type KeyChange struct {
	Key     string
	Base    MemoryView
	Head    MemoryView
	Changed bool // always true for Modified entries; convenience for UIs
}

// BranchDiff is the key-level diff of head relative to base (plan §5.2:
// "Collect local-only items on each branch, compare resolutions for shared
// keys"). All slices are sorted by Key and non-nil (empty, never nil) so
// callers can range without nil checks and tests compare deterministically.
type BranchDiff struct {
	Added    []KeyChange // in head, absent in base
	Modified []KeyChange // in both, different content
	Deleted  []KeyChange // in base, absent in head
}

// IsEmpty reports whether the diff carries no changes.
func (d BranchDiff) IsEmpty() bool {
	return len(d.Added) == 0 && len(d.Modified) == 0 && len(d.Deleted) == 0
}

// indexByKey collapses views to last-write-wins per key. Duplicate keys in
// one branch snapshot should not happen (CoW resolution returns one row per
// key), but last-wins keeps Diff total instead of panicking; callers
// feeding resolved branch states always pass unique keys.
func indexByKey(views []MemoryView) map[string]MemoryView {
	m := make(map[string]MemoryView, len(views))
	for _, v := range views {
		m[v.Key] = v
	}
	return m
}

// Diff compares base against head at key level. Content equality (not Rev,
// not UpdatedAt) decides Modified: equal content means converged even if
// the rows were written by different branches at different times.
func Diff(base, head []MemoryView) BranchDiff {
	b := indexByKey(base)
	h := indexByKey(head)
	out := BranchDiff{
		Added:    []KeyChange{},
		Modified: []KeyChange{},
		Deleted:  []KeyChange{},
	}
	for key, hv := range h {
		bv, ok := b[key]
		if !ok {
			out.Added = append(out.Added, KeyChange{Key: key, Head: hv})
			continue
		}
		if bv.Content != hv.Content {
			out.Modified = append(out.Modified, KeyChange{Key: key, Base: bv, Head: hv, Changed: true})
		}
	}
	for key, bv := range b {
		if _, ok := h[key]; !ok {
			out.Deleted = append(out.Deleted, KeyChange{Key: key, Base: bv})
		}
	}
	byKey := func(a, b KeyChange) bool { return a.Key < b.Key }
	sort.Slice(out.Added, func(i, j int) bool { return byKey(out.Added[i], out.Added[j]) })
	sort.Slice(out.Modified, func(i, j int) bool { return byKey(out.Modified[i], out.Modified[j]) })
	sort.Slice(out.Deleted, func(i, j int) bool { return byKey(out.Deleted[i], out.Deleted[j]) })
	return out
}

// StaleItem flags one child key whose parent moved after the fork (plan
// §5.4: "Parent update on a key that exists on child → flag child's item
// as potentially_stale"). ParentChanged is the staleness signal; ChildChanged
// distinguishes "child behind" (false: safe to fast-forward) from "child
// diverged" (true: needs the Merge conflict path, not a silent overwrite).
type StaleItem struct {
	Key           string
	ForkContent   string // parent state at fork time ("" if key did not exist at fork)
	ParentContent string
	ChildContent  string
	ParentChanged bool // parent content differs from fork baseline
	ChildChanged  bool // child content differs from fork baseline
	ForkExisted   bool // key existed at fork time
}

// IsStale reports the flag condition: the parent changed the key after the
// fork while the child still carries it.
func (s StaleItem) IsStale() bool { return s.ParentChanged }

// Diverged reports parent AND child both moved since the fork — staleness
// plus a local edit, i.e. route through Merge conflict review.
func (s StaleItem) Diverged() bool { return s.ParentChanged && s.ChildChanged }

// DetectStale flags child keys whose parent changed them after the fork.
//
//   - fork: parent branch state at fork time (the fork point; #17 will
//     persist it as branches.forked_at_event_id — callers reconstruct the
//     baseline by replaying events up to that id, or by passing the parent
//     snapshot taken at fork).
//   - parentNow: parent branch current resolved state.
//   - childNow: child branch current resolved state.
//
// A key is reported when it exists on the child AND the parent's current
// content differs from the fork baseline (update OR delete-after-fork; a
// parent delete is ParentContent "" with ParentChanged true). Keys the
// parent never touched since the fork are not reported, even if the child
// edited them (child-only edits are divergence-to-merge, not staleness).
// Result is sorted by Key and non-nil.
func DetectStale(fork, parentNow, childNow []MemoryView) []StaleItem {
	f := indexByKey(fork)
	p := indexByKey(parentNow)
	out := []StaleItem{}
	for _, cv := range childNow {
		pv, onParent := p[cv.Key]
		fv, atFork := f[cv.Key]

		var parentContent string
		var parentChanged bool
		switch {
		case onParent && atFork:
			parentContent = pv.Content
			parentChanged = pv.Content != fv.Content
		case onParent && !atFork:
			// Key postdates the fork on the parent side. It is new
			// information the child may want, but the child did not fork
			// it, so per plan §5.4 (flag on *update* of a forked key) it
			// is not staleness. Skip.
			continue
		case !onParent && atFork:
			// Parent deleted a forked key the child still carries.
			parentContent = ""
			parentChanged = true
		default:
			// Exists on neither parent-now nor fork: child-local key.
			continue
		}
		if !parentChanged {
			continue
		}
		forkContent := ""
		if atFork {
			forkContent = fv.Content
		}
		out = append(out, StaleItem{
			Key:           cv.Key,
			ForkContent:   forkContent,
			ParentContent: parentContent,
			ChildContent:  cv.Content,
			ParentChanged: true,
			ChildChanged:  atFork && cv.Content != fv.Content,
			ForkExisted:   atFork,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// MergeConflict is one same-key/different-value collision (plan §5.2:
// "Different-value = conflict"). Nothing is written for these keys — the
// TargetContent on the target branch is preserved verbatim and a human
// picks the winner.
type MergeConflict struct {
	Key           string
	SourceContent string
	TargetContent string
	Source        MemoryView
	Target        MemoryView
}

// MergeResult is the outcome of Merge. ToPropose rows are stamped
// StatusProposed: the store layer inserts them as PROPOSED on the target
// branch for the normal confirmation flow (plan §2.8), never as CONFIRMED
// overwrites. Skipped lists converged keys (same content, no action).
// All slices are sorted by Key and non-nil.
type MergeResult struct {
	ToPropose []MemoryView  // source-only keys, Status=PROPOSED, for insert on target
	Conflicts []MergeConflict // same key, different value — human review
	Skipped   []string      // same key, same value — already converged
}

// Merge computes a two-way, never-overwrite merge of source into target
// (plan §5.2: "Source branch items → PROPOSED on target. Same-value = skip.
// Different-value = conflict").
//
//   - Key only in source → appended to ToPropose (Status forced to
//     StatusProposed; Rev/UpdatedAt carried for provenance, target store
//     assigns fresh identity on insert).
//   - Key in both, equal content → Skipped.
//   - Key in both, different content → Conflicts; target untouched.
//   - Key only in target → ignored (merge is additive; no delete
//     propagation, no silent overwrite).
//
// The function is pure: it neither reads nor writes the target branch.
// Applying ToPropose (insert) and resolving Conflicts (human pick → new
// PROPOSED write) is the #17 store layer's job.
func Merge(source, target []MemoryView) MergeResult {
	t := indexByKey(target)
	res := MergeResult{
		ToPropose: []MemoryView{},
		Conflicts: []MergeConflict{},
		Skipped:   []string{},
	}
	for _, sv := range indexByKey(source) {
		tv, ok := t[sv.Key]
		switch {
		case !ok:
			cp := sv
			cp.Status = StatusProposed
			res.ToPropose = append(res.ToPropose, cp)
		case tv.Content == sv.Content:
			res.Skipped = append(res.Skipped, sv.Key)
		default:
			res.Conflicts = append(res.Conflicts, MergeConflict{
				Key:           sv.Key,
				SourceContent: sv.Content,
				TargetContent: tv.Content,
				Source:        sv,
				Target:        tv,
			})
		}
	}
	sort.Slice(res.ToPropose, func(i, j int) bool { return res.ToPropose[i].Key < res.ToPropose[j].Key })
	sort.Slice(res.Conflicts, func(i, j int) bool { return res.Conflicts[i].Key < res.Conflicts[j].Key })
	sort.Strings(res.Skipped)
	return res
}
