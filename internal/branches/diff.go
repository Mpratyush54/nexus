// Package branches implements Phase 5 (implementation-plan §5) memory-branch
// collaboration: key-level diffing (diff.go), staleness detection
// (staleness.go), and 3-way merge with conflict surfacing (merge.go).
//
// The package is intentionally stdlib-only and takes no dependency on
// internal/store types: callers adapt store.MemoryItem rows into Entry values
// (branch semantics need only key + content). This keeps the diff/merge
// engine unit-testable without a database and reusable by both MemStore and
// the Postgres store.
package branches

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Entry is the minimal unit of branch comparison: one memory item reduced to
// its identity (Key) and its payload (Content). Store columns that do not
// affect branch semantics (confidence, status, embedding, level, scope, ...)
// are intentionally not carried here.
type Entry struct {
	Key     string
	Content string
}

// NormalizeContent canonicalizes content before hashing and comparison so
// that insignificant whitespace differences (leading/trailing blank lines
// added by editors or materializer templates) do not surface as
// modifications.
func NormalizeContent(s string) string {
	return strings.TrimSpace(s)
}

// contentHash returns the hex SHA-256 of normalized content. Hash comparison
// (rather than raw string comparison) gives Modified detection a stable,
// cache-friendly fingerprint per key.
func contentHash(s string) string {
	sum := sha256.Sum256([]byte(NormalizeContent(s)))
	return hex.EncodeToString(sum[:])
}

// Change describes one key whose content differs between two branches.
type Change struct {
	Key        string
	OldContent string
	NewContent string
}

// DiffResult is the key-level comparison of branch A (old/base side) against
// branch B (new/compare side).
type DiffResult struct {
	Added     []Entry  // keys present only in B
	Removed   []Entry  // keys present only in A
	Modified  []Change // keys in both, different content hash
	Unchanged []string // keys in both, identical content hash
}

// DiffBranches compares two branch snapshots key-by-key and buckets every key
// into added, removed, modified, or unchanged. Comparison is by key identity
// plus content hash: same key with byte-different (post-normalization)
// content counts as modified, never as an add+remove pair.
//
// Duplicate keys inside one snapshot collapse last-write-wins (matching CoW
// read resolution, where the newest write on a branch shadows older ones).
// All output slices are sorted by key for deterministic results.
func DiffBranches(a, b []Entry) DiffResult {
	am := indexByKey(a)
	bm := indexByKey(b)

	var out DiffResult
	for key, ae := range am {
		be, ok := bm[key]
		if !ok {
			out.Removed = append(out.Removed, ae)
			continue
		}
		if contentHash(ae.Content) != contentHash(be.Content) {
			out.Modified = append(out.Modified, Change{Key: key, OldContent: ae.Content, NewContent: be.Content})
		} else {
			out.Unchanged = append(out.Unchanged, key)
		}
	}
	for key, be := range bm {
		if _, ok := am[key]; !ok {
			out.Added = append(out.Added, be)
		}
	}

	sort.Slice(out.Added, func(i, j int) bool { return out.Added[i].Key < out.Added[j].Key })
	sort.Slice(out.Removed, func(i, j int) bool { return out.Removed[i].Key < out.Removed[j].Key })
	sort.Slice(out.Modified, func(i, j int) bool { return out.Modified[i].Key < out.Modified[j].Key })
	sort.Strings(out.Unchanged)
	return out
}

// indexByKey collapses a snapshot to one Entry per key, last write wins.
func indexByKey(es []Entry) map[string]Entry {
	m := make(map[string]Entry, len(es))
	for _, e := range es {
		m[e.Key] = e
	}
	return m
}
