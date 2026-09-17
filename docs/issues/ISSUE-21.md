# ISSUE-21 — [CLI] Developer CLI Client (cmd/nexus)

- **Issue:** #21 — [CLI] Developer CLI Client (cmd/nexus)
- **Status:** UNIMPLEMENTED (stub log only — no code, no ADR; created for #48 docs refresh)
- **Assignee:** Mpratyush54
- **Scope pointer:** `implementation-plan.md` Target Directory Structure
  (`cmd/mem/main.go` — CLI client entrypoint) + Phase 1b (`mem recall` /
  `mem remember` as thin server-API clients). GitHub issue body:
  `cmd/nexus/main.go` with `status`, `memory search/propose/confirm/reject`,
  `session list/join/create`, `branch list/fork/checkout/diff/merge`,
  `episode list/search`, `doctor` subcommands; formatted tables + `--json`.
- **Why stub:** GitHub shows #21 closed (assignee Mpratyush54) but waves 1–4
  landed no code, log, or ADR for it (`git log`: `565c5fe` covers #1–#18,
  `0101610` covers #20/#22/#24/#26). Per #48 brief: record as unimplemented,
  do NOT implement here.
- **Acceptance for future implementer:** all memory/session/branch operations
  work from the terminal; clean tables + JSON output; full
  `docs/issues/ISSUE-21.md` log + ADR required.

## Verification

- [x] No files outside `docs/` touched by this stub (docs-only change for #48).
