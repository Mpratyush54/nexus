# ADR-156 - Episode Search Filters Inside the Query

- **ADR ID:** ADR-156-episode-search-filters
- **Date:** 2026-09-18
- **Author:** ParthKhandelwal537
- **Issue:** #156 MCP episode search limits SQL results before file/status filtering
- **Status:** Accepted

## Context

`handleEpisodeSearch` fetched `limit` rows, then dropped non-matching
rows in Go: a file/status match past the raw limit window returned
`count: 0` despite existing. Widening `store.SearchEpisodes` (both
backends + all callers) is contained — everything is in-repo.

## Decisions

1. `SearchEpisodes(..., file, status string, limit int)`: Postgres adds
   `unnest(files_involved)` ILIKE + `UPPER(status)` predicates before
   `LIMIT`; MemStore mirrors in Go. Server/cmd callers pass zero values
   (unchanged behavior); MCP passes the tool args.
2. MCP drops its Go post-filter (`fileInvolved` removed); the fakeStore
   mirrors pre-limit semantics so handler tests pin the contract.
