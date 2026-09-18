# ISSUE-156 — Episode Search Limit-Before-Filter

- **Status:** Done
- **Scope:** `internal/store/{store,episodes}.go`, `internal/mcp/tools.go`,
  fakes + callers (`server/routes.go`, `cmd/mem`, `cmd/server` test),
  `docs/decisions/ADR-156-episode-search-filters.md` ONLY.

## What was built

- File/status filters pushed into `SearchEpisodes` on both backends,
  applied before `LIMIT`.
- MCP handler passes args through; Go post-filter removed.

## Verification

- New `TestEpisodeSearchFileMatchPastLimitWindow` (file match seeded
  last with limit=2 → count 1); suites green.
