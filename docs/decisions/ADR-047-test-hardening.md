# ADR-047 — Test Hardening: Web, Watcher, Project, Scan, Materializer Clock

- **ADR ID:** ADR-047-test-hardening
- **Date:** 2026-09-17
- **Author:** Muse Spark (opencode)
- **Issue:** #47 Test hardening (web.go zero tests; watcher live test; project/scan no tests; materializer fakeClock race)
- **Status:** Accepted

## Context

Issue #47 targets five test gaps, each in a different package, under a
strict scope constraint (only the listed files; no commits):

1. **`internal/server/web.go` — zero tests.** The dashboard static handler
   pins security-relevant behavior — an exact three-path allowlist with no
   SPA fallback, per-asset MIME types, `no-store`/`nosniff` headers — but
   nothing pinned it. A future edit could add a catch-all (shadowing API
   404s) or depend on host MIME tables with no test failing.
2. **`internal/daemon/watcher_test.go` — live-fsnotify test.** The only
   timing-sensitive test (`TestWatchRunEmitsOnWrite`) inlined its retry
   loop, and the deterministic `PollOnce` hook (the documented unit-test
   seam in `watcher.go`) was covered by just one end-to-end test.
3. **`internal/project/` — no tests. `internal/scan/` — no tests.**
   `scan.NeverPatterns` fails a sync closed on hit: an over-broad pattern
   blocks every sync, an under-broad one leaks secrets — both directions
   need pinning.
4. **`internal/materializer/materializer_test.go` — suspected fakeClock
   race.** `fakeClock.now` is written by `advance` on the test goroutine
   and read by the `After` polling goroutine with no synchronization.

Correction recorded during the work: the issue brief describes
"`internal/project/project.go` (URL normalization funcs)", but `project.go`
contains no URL normalizer. The remote-URL normalizer is
`store.NormalizeRemoteURL` (`internal/store/projects.go`), already pinned
by a 24-case table in `store/projects_test.go` — and `project_test.go`
cannot import it (import cycle: `store` already imports `project` for
`Fingerprint`). So `project_test.go` pins this package's own normalization
semantics instead (see Decision).

Constraints: additive edits only to `watcher_test.go` (existing tests keep
passing); `materializer_test.go` touched only if the race is still present
(it was); docs limited to `ADR-047` + `ISSUE-47`; no commit;
`go build ./...` + `go vet` + `go test` on touched packages all green.

## Options Considered

1. **Table tests calling `store.NormalizeRemoteURL` from
   `internal/project`.**
   Pros: literal match to the brief's "remote-URL normalization table".
   Cons: impossible without an import cycle (`store` → `project`), and it
   would duplicate the existing 24-case `store` table. Rejected.
2. **(Chosen) Package-local tables + honest scope note.**
   `project_test.go` pins `SystemDir` case-folding, `hasMarker` (incl. the
   `*.sln` glob), and `Fingerprint`'s best-effort empty identity off-repo;
   the ADR/ISSUE record why `NormalizeRemoteURL` stays in `store` and where
   it is already covered. Pros: truthful, cycle-free, no duplication.
3. **Delete the live-fsnotify test, go all-`PollOnce`.**
   Pros: zero flakiness. Cons: loses the only proof the fsnotify →
   debounce → emit path works end to end (e.g. watch registration on the
   root, parent-dir handling). Rejected; instead the live test stays as the
   single smoke behind one shared retry helper.
4. **Channel-based `fakeClock.After` rewrite.**
   Pros: eliminates the polling goroutine entirely. Cons: larger diff to a
   test file the brief says to touch minimally, and `Run` semantics (level-
   triggered `After`) are harder to fake with channels. Rejected; a mutex
   is the minimal correct fix.

## Decision

- **NEW `internal/server/web_test.go`** (package `server`, httptest,
  temp-dir fixtures — never the repo `web/` dir): MIME/body/header table
  for `/`, `/app.js`, `/styles.css`; allowlist rejection incl.
  `/index.html` (allowlisted URL is `/`, not the filename), case-variant
  `/APP.JS`, and API-looking paths; `..`/percent-encoded traversal → 404;
  allowlisted-but-missing file → 404 (never 500); `RegisterWebRoutes` on a
  real mux with an API route proves no shadowing (`/api/ping` → handler,
  `/unknown` and `/api/unknown` → 404, i.e. no SPA fallback). `WebDir` is
  overridden + restored for the mux test since `RegisterWebRoutes` reads
  the global.
- **`internal/daemon/watcher_test.go` (additive only):** one shared
  `waitForWatchEvent` retry helper (50 ms poll, `t.Fatalf` on timeout);
  the single live smoke `TestWatchRunEmitsOnWrite` refactored onto it
  (body-only change, same 10 s budget, same assertions); three new
  deterministic `PollOnce` tests — nested `.github/` creation after
  construction (the fsnotify-gap fallback), over-cap retention degrading
  to a hash-change notice (no line diff), and empty-root silence (no
  phantom removals). No existing test deleted or renamed.
- **NEW `internal/project/project_test.go`:** `SystemDir` 12-case table,
  `hasMarker` 9-case table over temp dirs (incl. `*.sln` glob,
  near-miss `go.mod.bak`, missing dir), `Fingerprint` off-repo → `("","")`
  with cache stability. Plus the scope-correction note in the file header.
- **NEW `internal/scan/scan_test.go`:** `NeverPatterns` both-directions
  table — 7 secret shapes must match (glpat/ghp/sk-ant/AKIA/`api_key`
  long-value variants), 10 clean inputs must not (docs prose, short and
  placeholder values, bare filenames, empty).
- **`internal/materializer/materializer_test.go` (race was present, so
  fixed):** `fakeClock` gains `sync.Mutex`; `Now`/`advance` lock, and
  `After` snapshots the deadline under lock then polls via a locked read,
  sending the locked snapshot on fire. Comment cites #47.
- **Race-verification caveat:** `go test -race` could not run — the
  toolchain's gcc lacks 64-bit support (`cc1.exe: sorry, unimplemented:
  64-bit mode not compiled in`). The race was verified by inspection
  (unsynchronized cross-goroutine `time.Time` read/write) and the fix by
  `go vet` + the full green suite.

## Why (Rationale)

- **Each new test fails for exactly one real regression:** widening the
  allowlist, adding an SPA fallback, or shadowing an API path fails
  `web_test.go`; broadening `api[_-]?key` to short values fails the
  clean-side scan table (which would block every sync in production);
  dropping a `NeverPatterns` entry fails the secret side; removing the
  `*.sln` marker or `SystemDir` folding fails `project_test.go`; the
  `PollOnce` trio fails if the fsnotify-gap fallback, the over-cap
  degradation, or the never-seen-removal guard regresses.
- **Flakiness budget is explicit and minimal:** exactly one wall-clock test
  remains (the live fsnotify smoke), all other watcher assertions go
  through the documented deterministic `PollOnce` seam; the retry helper is
  the single place a timeout lives.
- **Scope discipline held:** `git status` shows only the six intended
  files (4 new test files, 2 edited test files) plus the two docs; `go
  build ./...` 0, `go vet` on the five touched packages 0, `go test` on
  the five touched packages green (see ISSUE-47 for output).
- **The project-package correction avoids a cycle and duplication:**
  duplicating `NormalizeRemoteURL` coverage via a `store` import is
  impossible (cycle) and pointless (24 cases already green in `store`).

## Consequences

- `web.go`, `scan.NeverPatterns`, and the project helpers now have
  regression coverage; the watcher suite is deterministic except one
  clearly-marked smoke test; `fakeClock` is race-safe under `-race` once
  CI provides a working gcc.
- Follow-ups (not this issue): run `go test -race` on
  `internal/materializer` in CI; if `NormalizeRemoteURL` semantics ever
  need to move into `internal/project`, move the table with it and delete
  the scope note here.

## Alternatives Rejected

- Cross-package `store` import from `project_test.go` (option 1):
  import cycle + duplication; rejected without further evaluation.
- Deleting the live-fsnotify test (option 3): loses end-to-end proof of
  watch registration and debounce; rejected in favor of one helper-backed
  smoke.
- Channel-based fake-clock rewrite (option 4): larger diff than the
  minimal mutex fix for identical safety; rejected.
