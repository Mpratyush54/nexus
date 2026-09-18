# ISSUE-109 — Harvester identity-priority matching + atomic file-hash persistence

- **Status:** Done
- **Scope:** `internal/daemon/harvester.go` (identity fields, `normalizeGitURL`,
  `MatchLevel`/`ClassifyWorkspaceMatch`, `MatchesCandidate`, `Origin`/`RootCommit`),
  `internal/daemon/watcher.go` (`FileHashStore.saveLocked` atomic rewrite only),
  `internal/daemon/harvester_test.go` (2 new tests),
  `internal/daemon/watcher_test.go` (1 new test), plus
  `docs/decisions/ADR-109-harvester-matching-atomic-hash-store.md` and this file.
  No other files touched; no `go.mod` changes; no git operations performed.
- **Issue:** #109 (audit: fuzzy workspace matching; non-atomic file-hash persistence).

## What was found (verification)

- **Harvester — audit confirmed, worse than stale:** `MatchesWorkspace` used
  *only* folder-name substring matching; git remote/root identity was never
  consulted (the struct carried just `workspace` + `folderName`). Any two repos
  sharing a generic dir name (`api`, `server`) cross-attributed transcripts.
- **Watcher — audit confirmed:** `saveLocked` did a direct `os.WriteFile`; a
  crash mid-write truncated the state file (next boot reprocessed everything).
  `NewFileHashStore` missing/corrupt behavior was load-bearing (existing test)
  and is preserved byte-for-byte.

## What was built

- `harvester.go` — `Harvester` gains best-effort `origin`/`rootCommit`,
  populated in `NewHarvesterWithPoll` via the same-package `FingerprintOf`
  (reuses `internal/project.Fingerprint`, no duplication; `""` when not a git
  repo). `normalizeGitURL` is a local mirror of `store.NormalizeGitURL`
  (daemon stays stdlib-only; `internal/migrate` precedent; must stay in sync).
  Pure `ClassifyWorkspaceMatch` implements remote-URL → root-commit →
  folder-name: URL equality wins, equal roots rescue disagreeing URLs
  (forks/renamed remotes), and known-but-disagreeing identity rejects outright
  (no folder fallback — that fallback was the misattribution vector).
  `MatchesCandidate(origin, root, path)` is the new entry point;
  `MatchesWorkspace(path)` delegates with empty candidate identity, so its
  behavior is unchanged (existing test pins it).
- `watcher.go` — `saveLocked` now writes temp-file (same dir) + `Chmod 0600` +
  `Sync` + `Close` + `os.Rename`, with best-effort temp cleanup on any
  pre-rename error. `NewFileHashStore` untouched.
- Tests — `TestWorkspaceMatchPriority` (7-case ordering table, no git binary),
  `TestMatchesCandidateIdentity` (struct-literal identity, incl. generic-folder
  rejection), `TestFileHashStoreNeverHalfWritten` (500-mutation burst incl.
  mid-burst reload parse + no `.hashes-*.tmp` leftovers). Existing
  `TestFileHashStoreRoundTrip` and `TestMatchesWorkspace` unchanged and green.

## Verification

- `go build ./internal/daemon/...`: **clean**. (`go build ./...` fails in
  `internal/store/branches.go` — `*PostgresStore` missing `CopyItemsToBranch` —
  pre-existing, outside ownership, unrelated.)
- `go vet ./internal/daemon/`: **clean**.
- `go test -count=1 ./internal/daemon/`: **PASS** (15.5s; one run showed
  `TestStartHeartbeatBacksOffOnRepeatedFailures` flake — timing-sensitive,
  passes in isolation and on rerun, untouched by this change).
- `gofmt -w` applied to all four touched files.

## Follow-ups (not this issue)

- Thread transcript-side origin/root into `MatchesCandidate` call sites
  (`scanDir` still uses path-only matching; transcript formats rarely record
  git identity today).
- Feed `Harvester.Origin()/RootCommit()` into the daemon register path so
  `ResolveProject` receives canonical identity instead of folder names.
