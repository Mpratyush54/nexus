# ISSUE-47 — Test Hardening (Web, Watcher, Project, Scan, Materializer Clock)

- **Issue:** #47 — web.go zero tests; watcher live test bypasses
  deterministic PollOnce; project/scan have no tests; materializer
  fakeClock race
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Scope constraint:** ONLY NEW `internal/server/web_test.go` +
  `internal/daemon/watcher_test.go` (additive) + NEW
  `internal/project/project_test.go` + NEW `internal/scan/scan_test.go` +
  `internal/materializer/materializer_test.go` (race fix only — verified
  present first) + docs (`ADR-047`, this file). Read `internal/server/
  web.go`, `internal/daemon/watcher_test.go`, `internal/project/
  project.go`, `internal/scan/scan.go`,
  `internal/materializer/materializer_test.go` first, per the issue.
- **Plan refs:** `implementation-plan.md` Phase 3 (web dashboard static
  handler); watcher `PollOnce` as the documented timing-free test hook
  (`watcher.go`); `NeverPatterns` sync-fail-closed gate (`scan.go`).

## What was built

| File | Contents |
|---|---|
| `internal/server/web_test.go` (NEW) | 5 httptest tests over temp-dir fixtures: MIME/body/header table (`/`, `/app.js`, `/styles.css`); allowlist rejection (`/nope`, `/index.html`, `/APP.JS`, `/app.js.map`, API-looking paths); `..`/percent-encoded traversal → 404; allowlisted-but-missing file → 404; `RegisterWebRoutes` + API route on one mux proves no shadowing |
| `internal/daemon/watcher_test.go` (additive) | `waitForWatchEvent` retry helper; live smoke `TestWatchRunEmitsOnWrite` refactored onto it (body-only); 3 new deterministic `PollOnce` tests (nested `.github/` creation, over-cap hash-notice degradation, empty-root silence). No existing test deleted/renamed |
| `internal/project/project_test.go` (NEW) | `SystemDir` 12-case table; `hasMarker` 9-case table (incl. `*.sln` glob, near-miss, missing dir); `Fingerprint` off-repo → `("","")` + cache stability |
| `internal/scan/scan_test.go` (NEW) | `NeverPatterns` both-directions table: 7 secret shapes match, 10 clean inputs don't |
| `internal/materializer/materializer_test.go` (race fix) | `fakeClock` mutex-guarded (`Now`/`advance`/deadline-snapshot + locked poll in `After`); `sync` import; #47 comment |
| `docs/decisions/ADR-047-test-hardening.md` | Why-mandatory ADR (scope correction, options, per-test rationale, `-race` caveat, evidence) |
| `docs/issues/ISSUE-47.md` | This file |

## Decisions (see ADR-047 for rationale)

1. **Scope correction on "project URL normalization":** `project.go`
   contains no URL normalizer — `NormalizeRemoteURL` lives in
   `internal/store` (already 24-case covered in `store/projects_test.go`)
   and cannot be imported from `project_test.go` (import cycle:
   `store` → `project`). `project_test.go` pins this package's own
   normalization (`SystemDir` folding, marker matching, `Fingerprint`
   emptiness) instead of duplicating store coverage.
2. **One live smoke, rest deterministic:** `TestWatchRunEmitsOnWrite`
   stays the single fsnotify end-to-end proof behind `waitForWatchEvent`;
   all new watcher assertions use `PollOnce`.
3. **fakeClock race was present, so fixed:** unsynchronized `now`
   read (background `After` goroutine) vs write (`advance`) — fixed with
   a mutex, the minimal diff. `Materializer` itself was already
   mutex-guarded; only the test fake was racy.
4. **`go test -race` unavailable:** toolchain gcc lacks 64-bit support,
   so the race was verified by inspection and the fix by `go vet` +
   green suite (documented in ADR-047).

## Verification

- `go build ./...` → exit 0 (no output)
- `go vet ./internal/server/ ./internal/daemon/ ./internal/project/ ./internal/scan/ ./internal/materializer/` → exit 0 (no output)
- `go test -count=1` on touched packages → full green:

```text
ok  central-memory/internal/server       1.929s
ok  central-memory/internal/daemon       4.503s
ok  central-memory/internal/project      0.926s
ok  central-memory/internal/scan         0.832s
ok  central-memory/internal/materializer 0.825s
```

- New/updated tests observed passing: `TestWebMimeTypesAndBodies`,
  `TestWebAllowlistRejectsUnknown`, `TestWebTraversalRejected`,
  `TestWebMissingFileIs404`, `TestWebRegisterDoesNotShadowAPI`,
  `TestWatchPollOnceNestedCreation`,
  `TestWatchPollOnceOversizedDegradesToHashNotice`,
  `TestWatchPollOnceEmptyRootSilent`, `TestWatchRunEmitsOnWrite`,
  `TestSystemDirTable`, `TestHasMarkerTable`,
  `TestFingerprintNonRepoEmpty`, `TestNeverPatternsSecretsMatch`,
  `TestNeverPatternsNormalPathsDoNotMatch`, plus all pre-existing daemon
  and materializer tests (incl. debounce/delimiter/budget suites).
- `git status` shows only the six intended test files + two docs.
  Nothing committed, per the issue.

## Follow-ups (not this issue)

- CI: `go test -race ./internal/materializer/` once the toolchain has a
  working 64-bit gcc (could not run locally — see above).
- If `NormalizeRemoteURL` ever moves into `internal/project`, move its
  table with it and drop the ADR-047 scope note.
