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
| `adapters/walk.go` | `CopyFiltered`: walk callback returns `werr`; `copyFile`/`filepath.Rel` failures abort the walk wrapped with the path; missing roots are skipped (agent dirs that were never created), other stat failures returned. `safeName` now returns `"<i>_<sanitized-base>"` (index prefix + existing `:`/space sanitization, extended to separators). Fresh-copy path also records `Was` (was resume-only). New `RootsForIn` seam (explicit leaves/roots; #111). |
| `adapters/registry.go` | `Export` returns the `CopyFiltered` error (wrapped with adapter name) and the agent-dir `MkdirAll` error; `index.json` is written only on success. `Normalize` now propagates `sessions.jsonl` encode and `transcript.md` write errors. `Restore`/`leafDirOf` resolved via platform roots (#111). |
| `adapters/base.go` | Comment only: `D:\X -> D:\X` → `<project root>\<project>` wording. |
| `adapters/walk_test.go` (NEW) | `TestSafeNameIndexedCollisionFree`, `TestCopyFilteredSkipsMissingRoots`, `TestCopyFilteredPropagatesCopyError`, `TestRootsForInJoinsLeafDotUnderEachRoot`, `TestExportPropagatesCopyError` (incl. "no index.json on failure"), `TestExportSuccessWritesIndex`. Temp dirs only. |

## Verification

- `gofmt -w` on touched files; `go build ./...`; `go vet` + `go test -count=1` on `adapters` — all green.
- No `Export` signature change (`Export(vault string) error` already returned `error`), so no caller updates needed — verified: the only `Export(`/`Restore(` references outside the interface are none (harvester uses `Registry()` for names only).

## Follow-ups (not this issue)

- `Discover` still swallows per-path walk errors (`_ = filepath.Walk`); same treatment as `CopyFiltered` if discovery must be strict (missing roots are the common case there too).
- `json.MarshalIndent` errors in `Export`/`writeManifest` still discarded — practically infallible for these shapes, but could be propagated for completeness.
