# ISSUE-111 — Cross-platform violation (hardcoded `D:\`) + brittle service managers

- **Issue:** #111 — audit: Cross-platform violation with hardcoded `D:\` and brittle service managers
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Scope constraint:** ONLY `internal/project/project.go`, `internal/migrate/migrate.go` (the `D:\` line only), `internal/platform/` (all), `adapters/` (root joins + `D:\` prefix rules), new `adapters/walk_test.go`, + docs. No other files touched (notably `main.go`, which keeps calling the unchanged `Leaves()`/`Fingerprint` signatures); no git operations.

## Findings (all verified)

1. **Hardcoded `D:\`:** `project.Leaves`/`ResolveLeaf`/`ForPath`, `adapters` root joins + `D:\`-prefix fallbacks, `registry.go` restore-target check + `leafDirOf`, `migrate.go:149` — all broke execution off Windows/`D:\`.
2. **Brittle managers:** linux `Uninstall` discarded `systemctl` errors (false success); darwin `Uninstall` discarded `bootout` errors (service left running); windows `Status`/`Uninstall` grepped English `schtasks` text (fails on non-English systems).

## What changed

| File | Change |
|---|---|
| `internal/project/project.go` | Zero-arg `Leaves`/`ResolveLeaf`/`ForPath` kept (callers/tests pin them) and now delegate to new `*In` variants over explicit roots/leaves, defaulting to `platform.ProjectRoots()`. New `LeafDir`/`LeafDirIn` (first existing root wins, else first-root join) and `RootRel`/`RootRelIn` (case-insensitive rel-to-containing-root). No `D:\` literals left. |
| `adapters/walk.go` | `RootsForIn` seam (leaves × roots × dot-dirs); `resolveUncached`/`workspaceProject` `D:\` prefix rules → `project.RootRel`; `claudeDirProject` encodes `project.LeafDir(leaf)` instead of `D:\+leaf`. |
| `adapters/registry.go` | `leafDirOf`/`leafDirOfIn` via `project.LeafDirIn` + `platform.ProjectRoots()`; restore-target check uses it (with an explicit no-roots error). |
| `internal/migrate/migrate.go` | Line ~149 only: `filepath.Join(`D:\`, …)` → `project.LeafDir(leaf)` (empty skipped, so no junk seeds when unconfigured). |
| `internal/platform/{windows,linux,darwin}.go` | Uninstalls propagate stop/remove errors, staying idempotent when nothing is installed (unit-file/plist/task-existence check). Windows existence via `schtasks` exit codes + scheduler-reachability probe — no localized text parsing. Linux `Status`: `unknown`/empty `is-active` → `StatusUnknown` (was false `stopped`). |
| `internal/platform/platform.go` + `platform_test.go` | New pure `schtasksStateFromList` (localized `Status:` headers + multilingual running/idle tokens; unrecognized → `StatusUnknown`, never a guess) with a 13-case table test. |

## Verification

- `gofmt -w`; `go build ./...`; `go vet` + `go test -count=1` on `adapters` + `internal/project` + `internal/platform` + `internal/migrate` — all green.
- `internal/project` `*In` helpers additionally exercised via a throwaway in-module script (multi-root scan, leaf fallback, `RootRel`, `ForPath`, `ResolveLeaf`, missing-root skip) — removed afterwards; no new test file there per scope.
- Remaining `D:\` strings in owned trees are test fixtures (`migrate_test.go` fake dirs, one `safeName` fixture) and one historical comment — no functional hardcodes.

## Follow-ups (not this issue)

- Behaviour note: stale `D:\…` transcript paths on machines where `D:\` is not a configured root now resolve to `global` instead of their first segment — set `NEXUS_PROJECT_ROOTS=D:\…` there (documented in code + ADR-111).
- `adapters.Discover` still ignores walk errors (see ISSUE-108 follow-ups).
- darwin `Status` relies on `launchctl print` `state = running` keys (not localized — no change needed); linux/darwin uninstall paths are error-propagating by construction but only executable under their own GOOS.
