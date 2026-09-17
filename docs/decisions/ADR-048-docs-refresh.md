# ADR-048-docs-refresh

- **ADR ID:** ADR-048-docs-refresh
- **Date:** 2026-09-17
- **Author:** docs-refresh agent (issue-48)
- **Issue:** #48 [Audit:Low] Docs: ORCHESTRATION stale; no logs for #19/#21/#23/#25
- **Status:** Accepted

## Context

`docs/ORCHESTRATION.md` still read as a forward-looking plan (Wave 1 "now",
Waves 2–4 pending) after waves 1–4 had already landed (`565c5fe` for #1–#18,
`0101610` for #20/#22/#24/#26, `23acf9c` gofmt for #49, all visible in
`git log --oneline -8`). GitHub issues #19/#21/#23/#25 show closed with
assignee Mpratyush54, yet no code, per-issue log, or ADR exists for any of
them. `docs/README.md` requires every critical decision logged and a
per-issue progress file, so the docs tree was non-compliant and misleading
(closed issues looked done).

## Options Considered

1. **Docs-only refresh + UNIMPLEMENTED stubs (chosen).** Update
   `docs/ORCHESTRATION.md` with landed branches/commits/verification numbers,
   add stub logs for #19/#21/#23/#25 marked UNIMPLEMENTED with assignee
   Mpratyush54 + scope pointers, log this fix as ISSUE-48.
2. Implement #19/#21/#23/#25 now. Rejected: #48 brief explicitly forbids
   implementing them here; they belong to their assignee with full logs/ADRs.
3. Reopen the GitHub issues instead of stubbing. Rejected: docs-side action
   only; issue-state changes are the maintainer's call, and the stub records
   the discrepancy either way.

## Decision

Docs-only change on branch `fix/audit-gofmt-49`: edit
`docs/ORCHESTRATION.md` (wave annotations + landed-status table + 1–49
coverage list), add `docs/issues/ISSUE-{19,21,23,25}.md` stubs
(UNIMPLEMENTED, Mpratyush54, scope pointers), add this ADR and
`docs/issues/ISSUE-48.md`. No other docs files touched; no code touched;
no commit (per brief).

## Why (Rationale)

`docs/README.md` mandates an ADR + per-issue log for every critical decision;
the missing logs for four closed issues violated that rule, and the stale
wave plan contradicted `git log`. Stubs (not full logs) are the honest record:
claiming Done would falsify status, while saying nothing preserves the gap.
Verification numbers are quoted from the landing commits because no Go
toolchain exists in this environment (`go` not recognized), so re-running
`go build`/`go vet` was impossible — stated openly in ORCHESTRATION.md.

## Consequences

- ORCHESTRATION.md matches `git log`; every issue 1–26 + 48 has a log or stub.
- #19/#21/#23/#25 remain open work for Mpratyush54 with scope pointers and
  acceptance criteria in their stubs.
- #28–#47 (open audits) and #49/#50 left untouched per #48 scope.

## Alternatives Rejected

See Options Considered: full implementation (out of scope, forbidden by
brief) and GitHub-side reopening (maintainer decision, not a docs fix).
