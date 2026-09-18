# ADR-111-platform-roots-service-managers

- **ADR ID:** ADR-111-platform-roots-service-managers
- **Date:** 2026-09-17
- **Issue:** #111 — Cross-platform violation with hardcoded `D:\` and brittle service managers
- **Status:** Accepted

## Context

Project discovery assumed a single `D:\` volume, breaking Linux/macOS/containers, and the service managers either hid failures (linux/darwin uninstall) or depended on English CLI text (windows `schtasks`).

## Decision

1. **Roots are configuration, not code.** `platform.ProjectRoots()` (`NEXUS_PROJECT_ROOTS`, else home) is the single source; `internal/project` exposes `*In` variants taking explicit roots/leaves while the zero-arg `Leaves`/`ResolveLeaf`/`ForPath`/`Fingerprint` signatures are frozen (callers in `main.go` and existing tests pin them). Leaf IDs stay portable forward-slash paths relative to their root.
2. **Uninstalls are idempotent but honest.** Missing unit/plist/task → nil (already uninstalled); any stop/remove failure on an installed service is returned. No more false success.
3. **Windows status from exit codes, not language.** Task existence comes from the `schtasks` exit code (+ a bare-query reachability probe to separate "missing task" from "scheduler broken"); the `Status:` value is parsed by the pure, unit-tested `schtasksStateFromList`, which maps localized running/idle tokens and returns `StatusUnknown` for anything unrecognized rather than guessing.

## Alternatives Considered

- **Changing `Leaves()`/`Fingerprint()` signatures to take roots.** Cleaner long-term, but breaks out-of-scope callers (`main.go`) and existing test pins; rejected — the `*In` seams give testability without the blast radius.
- **Full locale tables for every `schtasks` string.** Unbounded and fragile; exit codes plus conservative unknown-on-unrecognized is robust by construction.

## Consequences

- Machines with projects outside the home dir must set `NEXUS_PROJECT_ROOTS` (previously implicit `D:\`); stale `D:\…` paths there now resolve to `global` instead of a guessed segment.
- Non-English Windows may report `StatusUnknown` for unrecognized task states — explicit and honest, never a false stopped/running.
