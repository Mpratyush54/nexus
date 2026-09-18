// Branch content enumeration (nexus issue #97).
//
// DiffBranches/Merge operate on Entry snapshots, but the server previously
// had no store-level branch enumeration: the diff/merge routes returned stub
// shapes with "no rows copied" notes. ListBranchContents closes that gap: it
// enumerates every visible key on a branch through a narrow Loader seam the
// server adapts over BranchStore (SearchMemory for the key universe,
// ResolveRead for per-key branch views).
package branches

import (
	"context"
	"errors"
	"sort"
)

// BranchLoader supplies the two reads enumeration needs. Keys returns the
// deduplicated key universe for a project; Read resolves one key through
// branchID's overlay chain (branch -> parent -> main), reporting
// ErrBranchKeyNotFound when the key has no visible value on the branch.
type BranchLoader interface {
	Keys(ctx context.Context, projectID string) ([]string, error)
	Read(ctx context.Context, branchID, key string) (Entry, error)
}

// ErrBranchKeyNotFound is returned by BranchLoader.Read when a key from the
// universe has no visible value on the requested branch.
var ErrBranchKeyNotFound = errors.New("branches: key not visible on branch")

// ListBranchContents returns the visible Entry snapshot for branchID: every
// project key with a resolvable branch view, sorted by key. Keys without a
// branch view are skipped (tombstoned/superseded rows read as not-found),
// never fabricated. A nil loader or empty branchID is an error.
func ListBranchContents(ctx context.Context, l BranchLoader, projectID, branchID string) ([]Entry, error) {
	if l == nil {
		return nil, errors.New("branches: nil loader")
	}
	if branchID == "" {
		return nil, errors.New("branches: branch id is required")
	}
	keys, err := l.Keys(ctx, projectID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(keys))
	out := make([]Entry, 0, len(keys))
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		e, err := l.Read(ctx, branchID, k)
		if err != nil {
			if errors.Is(err, ErrBranchKeyNotFound) {
				continue
			}
			return nil, err
		}
		if e.Key == "" {
			e.Key = k
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
