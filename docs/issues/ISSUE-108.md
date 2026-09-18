# ISSUE-108 — Adapter export swallows errors + destination naming collisions

- **Issue:** #108 — audit: Adapter export swallows errors and destination naming collisions
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Scope constraint:** ONLY `adapters/` (`walk.go`, `registry.go`, `base.go`, new `walk_test.go`) + docs. No other files touched; no git operations. (Audit cited `adapters/base.go`; the real code is `adapters/walk.go` + `registry.go`.)

## Findings (both verified)

1. **Copy/walk errors swallowed:** `CopyFiltered` returned `nil` on `copyFile` failure and discarded the `filepath.Walk` error (`_ =`); `genericAdapter.Export` discarded the error with `_`. A failed copy reported a clean export and still wrote `index.json`.
2. **Naming collision:** `safeName(root, i)` ignored `i` despite the "tagging with root index" contract — two roots with the same basename overwrote each other under one directory.

## What changed

| File | Change |
|---|---|
| `adapters/walk.go` | `CopyFiltered`: walk/mkdir/copy failures aggregated via `errors.Join` (missing roots skipped); `safeName` returns `"%02d-<sanitized-base>"` (zero-padded index prefix + `:`/space sanitization). Fresh-copy path also records `Was`. |
| `adapters/registry.go` | `Export` returns the `CopyFiltered` error; `index.json` is ALWAYS written (manifest records partial state even on failure — the returned error, not a missing index, signals failure). |
| `adapters/base.go` | Vault-shim framing (Issue #117): Export/Restore/Normalize retained as legacy shim. |
| `adapters/walk_test.go` (NEW) | `TestSafeNameIndexedCollisionFree`, `TestCopyFilteredSkipsMissingRoots`, `TestCopyFilteredPropagatesCopyError`, `TestRootsForJoinsLeafDotUnderEachRoot` (join math; the `RootsForIn` seam was dropped at merge — roots resolve via project/platform globals), `TestExportPropagatesCopyError` (error + index-still-written), `TestExportSuccessWritesIndex`. Sources staged outside `/tmp` (`mksrc` under `$HOME`): `ClassifyPath` ignores any `/tmp/`-segment path and Linux `t.TempDir()` lives under `/tmp`, so TempDir-staged sources are silently skipped. |

## Verification

- `gofmt -w` on touched files; `go build ./...`; `go vet` + `go test -count=1` on `adapters` — all green.
- No `Export` signature change (`Export(vault string) error` already returned `error`), so no caller updates needed — verified: the only `Export(`/`Restore(` references outside the interface are none (harvester uses `Registry()` for names only).

## Follow-ups (not this issue)

- `Discover` still swallows per-path walk errors (`_ = filepath.Walk`); same treatment as `CopyFiltered` if discovery must be strict (missing roots are the common case there too).
- `json.MarshalIndent` errors in `Export`/`writeManifest` still discarded — practically infallible for these shapes, but could be propagated for completeness.

## Merge note (2026-09-18, PR #123 resolution)

- At merge time `origin/master` had independently closed #108/#111 with a
  superset implementation (`errors.Join` aggregation, `%02d-` safeName,
  `platform.ProjectRoots`, `rootRelativeLeaf`, legacy `D:\` candidates,
  Issue #117 vault-shim framing). All 8 conflicted files took master's
  side; the branch's `RootsForIn`/`RootRel` seam approach was dropped.
- Kept from this PR: `walk_test.go` (fixed for merged behavior — see the
  `/tmp` note above — plus the `safeName` format and always-write-index
  updates) and the issue/ADR docs (updated to merged reality).
