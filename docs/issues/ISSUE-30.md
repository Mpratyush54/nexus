# ISSUE-30 — Ephemeral Memory Level (5th Tier)

- **Status:** Done (code gains the tier; plan untouched per assignee call)
- **Scope:** `migrations/009_memory_ephemeral.*` (canonical; the
  branch-local `009_ephemeral_level.*` was dropped at merge since master
  already shipped an equivalent idempotent 009), `internal/store`
  (`memory_constants.go`, `memory_transitions.go`, `models.go` comment),
  `internal/mcp/tools.go`, `internal/migrate/vault.go` (comment),
  `internal/daemon/processor.go` (taxonomy), `internal/context/builder.go`,
  tests, `docs/decisions/ADR-030-ephemeral-level.md` ONLY.
  `implementation-plan.md` deliberately unmodified.

## What was built

- Migration 009 widens `memory_items_level_check` with `ephemeral`
  (same constraint name; down restores 4 tiers).
- `LevelEphemeral` constant (store + daemon); validators accept it
  (store `ValidateMemoryLevel`, MCP `memory_write`).
- Builder: rank 4 (top), `EphemeralItems` input, `<ephemeral>` section
  first, override/shadow participation.
- Classification never emits it (explicit-producer tier).

## Verification

- `go build ./...` + `go vet ./internal/...` clean;
  `go test -count=1 ./internal/...` green, incl. new
  `TestValidateMemoryLevelFiveTiers`, `TestLevelRankEphemeralWins`,
  `TestAssembleXMLEphemeralSection/ShadowsSession`, MCP ephemeral case.
