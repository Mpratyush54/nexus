# ISSUE-25 — [Migration] Legacy Vault & Session Transcript Importer (Phase 1b)

- **Issue:** #25 — [Migration] Legacy Vault & Session Transcript Importer (Phase 1b)
- **Status:** UNIMPLEMENTED (stub log only — no code, no ADR; created for #48 docs refresh)
- **Assignee:** Mpratyush54
- **Scope pointer:** `implementation-plan.md` Phase 1b (lines 589–596:
  parse `memory/global/learnings.md` + `memory/projects/*/MEMORY.md` into
  CONFIRMED items with embeddings; ingest `agents/*/normalized/sessions.jsonl`
  as historical events; seed `projects` via `project.Fingerprint()`).
  GitHub issue body: `internal/migrate/` parsers, `nexus migrate
  [--vault PATH] [--dry-run]` CLI command.
- **Why stub:** GitHub shows #25 closed (assignee Mpratyush54) but waves 1–4
  landed no code, log, or ADR for it (`git log`: `565c5fe` covers #1–#18,
  `0101610` covers #20/#22/#24/#26). Per #48 brief: record as unimplemented,
  do NOT implement here.
- **Acceptance for future implementer:** vault memories import with valid
  embeddings; fingerprints map 1:1 with no duplicates; full
  `docs/issues/ISSUE-25.md` log + ADR required.

## Verification

- [x] No files outside `docs/` touched by this stub (docs-only change for #48).
