// 3-way branch merge for Phase 5 (implementation-plan §5.2, issue #18).
//
// Merge folds source-branch items into a target branch using base — the
// parent fork-point snapshot — as the common ancestor:
//
//   - Keys the source changed (or added) while the target left alone are
//     auto-merged as PROPOSED items on the target; the displaced target
//     value is recorded as SUPERSEDED, never silently overwritten.
//   - Keys both sides changed to different content (or added/deleted
//     incompatibly) land in Conflicts for human review (issue #18
//     `conflict_with`); nothing is written for them.
//   - Keys both sides changed identically, or neither side changed, are
//     no-ops.
package branches

import "sort"

const (
	// StatusProposed marks auto-merged items awaiting confirmation, matching
	// the memory_items status enum (plan §1.1).
	StatusProposed = "PROPOSED"
	// StatusSuperseded marks target values displaced by an auto-merge.
	StatusSuperseded = "SUPERSEDED"
	// StatusConflict marks keys the merge refuses to auto-resolve.
	StatusConflict = "CONFLICT"
)

// Conflict is a key the 3-way merge will not auto-resolve. The Found flags
// distinguish "absent" (added/deleted on one side) from "present with empty
// content", so reviewers can tell an add/add clash from an edit/edit clash
// or an edit/delete clash.
type Conflict struct {
	Key           string
	BaseContent   string
	SourceContent string
	TargetContent string
	BaseFound     bool
	SourceFound   bool
	TargetFound   bool
}

// MergedItem is a source-side key auto-promoted onto the target. Status is
// always PROPOSED: merges propose, humans (or the plan §2.8 confirmation
// flow) confirm.
type MergedItem struct {
	Key     string
	Content string
	Status  string
}

// SupersededMark records a target-side value displaced by an auto-merge. An
// empty NewContent means the merge propagates a source-side deletion.
type SupersededMark struct {
	Key        string
	OldContent string
	NewContent string
}

// MergeResult is the outcome of merging source into target:
//
//   - Merged: items to insert on the target as PROPOSED.
//   - Superseded: target values those items displace (old -> new).
//   - Deleted: keys to remove from the target (source deleted them while the
//     target left them untouched).
//   - Conflicts: keys requiring human review; nothing is written for them.
type MergeResult struct {
	Merged     []MergedItem
	Conflicts  []Conflict
	Superseded []SupersededMark
	Deleted    []string
}

// Merge performs a 3-way merge of source into target with base as the fork
// point (parent snapshot at fork time). All output slices are sorted by key
// for deterministic results.
func Merge(base, source, target []Entry) MergeResult {
	bm := indexByKey(base)
	sm := indexByKey(source)
	tm := indexByKey(target)

	keys := make(map[string]struct{}, len(bm)+len(sm)+len(tm))
	for k := range bm {
		keys[k] = struct{}{}
	}
	for k := range sm {
		keys[k] = struct{}{}
	}
	for k := range tm {
		keys[k] = struct{}{}
	}

	var out MergeResult
	for key := range keys {
		be, inB := bm[key]
		se, inS := sm[key]
		te, inT := tm[key]

		conflict := func() {
			out.Conflicts = append(out.Conflicts, Conflict{
				Key:           key,
				BaseContent:   be.Content,
				SourceContent: se.Content,
				TargetContent: te.Content,
				BaseFound:     inB,
				SourceFound:   inS,
				TargetFound:   inT,
			})
		}
		takeSource := func() {
			out.Merged = append(out.Merged, MergedItem{Key: key, Content: se.Content, Status: StatusProposed})
			out.Superseded = append(out.Superseded, SupersededMark{Key: key, OldContent: te.Content, NewContent: se.Content})
		}

		switch {
		case inS && !inB && !inT:
			// Added on source only: auto-promote, nothing displaced.
			out.Merged = append(out.Merged, MergedItem{Key: key, Content: se.Content, Status: StatusProposed})
		case inT && !inB && !inS:
			// Target-only key: untouched by source, keep as-is.
		case inS && inT && !inB:
			// Added on both sides: identical additions agree, divergent
			// additions conflict.
			if contentHash(se.Content) != contentHash(te.Content) {
				conflict()
			}
		case inB && inS && inT:
			sHash, tHash, bHash := contentHash(se.Content), contentHash(te.Content), contentHash(be.Content)
			switch {
			case sHash == tHash:
				// Both sides agree (both unchanged or identically changed).
			case sHash == bHash:
				// Source unchanged: keep target's edit.
			case tHash == bHash:
				// Target unchanged: take source's edit, mark displaced.
				takeSource()
			default:
				// Same key, divergent content on both sides.
				conflict()
			}
		case inB && inS && !inT:
			// Deleted on target: stands if source left it alone,
			// conflicts if source also edited it.
			if contentHash(se.Content) != contentHash(be.Content) {
				conflict()
			}
		case inB && !inS && inT:
			// Deleted on source: propagate if target left it alone,
			// conflicts if target also edited it.
			if contentHash(te.Content) == contentHash(be.Content) {
				out.Deleted = append(out.Deleted, key)
				out.Superseded = append(out.Superseded, SupersededMark{Key: key, OldContent: te.Content, NewContent: ""})
			} else {
				conflict()
			}
		case inB && !inS && !inT:
			// Deleted on both sides: agree, nothing to do.
		}
	}

	sort.Slice(out.Merged, func(i, j int) bool { return out.Merged[i].Key < out.Merged[j].Key })
	sort.Slice(out.Conflicts, func(i, j int) bool { return out.Conflicts[i].Key < out.Conflicts[j].Key })
	sort.Slice(out.Superseded, func(i, j int) bool { return out.Superseded[i].Key < out.Superseded[j].Key })
	sort.Strings(out.Deleted)
	return out
}
