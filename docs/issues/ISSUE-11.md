# ISSUE-11 — Episodes Engine (Auto-Detect + Retrieval)

- **Issue:** #11 — [Phase 2] Episodes engine `internal/store/episodes.go`
  (plan §§2.3–2.5)
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Scope constraint:** ONLY `internal/store/episodes.go` (+ test) and
  `docs/`. Did NOT touch `internal/store/db.go`, `projects.go`,
  `workspaces.go`, `memory.go`, or any other store file.

## What was built

| File | Contents |
|---|---|
| `internal/store/episodes.go` | Event-type/role/status constants; `EventView`/`EpisodeLink`/`EpisodeDraft`/`Episode`/`RankedEpisode` types; pure `DetectEpisode` (fail → reads → mods → pass → commit); `ExtractErrorPatterns`; `Narrative` synthesis; SQL builders (error-pattern, file, similarity, create); pure `OrderBySimilarity`; local `EpisodeQuerier` + `EpisodeStore` (`Create`, `ByErrorPattern`, `ByFile`, `Similar`) |
| `internal/store/episodes_test.go` | 10 tests: full arc detection, no-false-positive on clean runs, read/commit-optional arc, error-pattern extraction, error-pattern + file retrieval SQL builders (+ store paths), similarity SQL + scan, in-memory similarity ordering, create + embedding literal |
| `docs/decisions/ADR-011-episodes-engine-auto-detection.md` | Why-mandatory ADR (core-required/edges-optional recall argument, seam reuse, honest root_cause) |

## Decisions (see ADR-011 for rationale)

1. Core-required (fail + fix + verified pass), edges-optional (read
   cluster, closing commit) — full chain still detects with full detail.
2. Stdlib-only file; local `EpisodeQuerier`; reuse `Rows`/`FormatEmbedding`
   from `memory.go`; `*DB` satisfies the seam with no edits elsewhere.
3. `error_patterns` from stderr (stdout fallback), `files_involved` from
   mods only; commit linked as `context` ("resolution commit").
4. `root_cause` labeled heuristic inference; LLM refinement is #10's job.

## Verification (2026-09-17)

- `go build ./...` → exit 0
- `go vet ./internal/store/` → exit 0
- `go test ./internal/store/ -run TestEpisode -v` → **10/10 PASS**
  (`TestEpisodeFullArcDetection`, `TestEpisodeNoFalsePositiveCleanRun`,
  `TestEpisodeArcWithoutReadsOrCommit`, `TestEpisodeExtractErrorPatterns`,
  `TestEpisodeErrorPatternRetrievalSQL`, `TestEpisodeFileRetrievalSQL`,
  `TestEpisodeSimilaritySQL`, `TestEpisodeOrderBySimilarity`,
  `TestEpisodeCreate`, `TestEpisodeCreateSQLEmbeddingLiteral`)

Acceptance mapping: full arc detection (trigger/investigation/root_cause/
resolution/verification + 8 link roles), no false positives on clean runs,
error-pattern + file retrieval SQL builders (`ANY(...)` + `resolved_at
DESC`), semantic similarity ordering (`<=>` + `OrderBySimilarity`).

## Follow-ups (not this issue)

- #10 processor: JSONB payload → `EventView` mapping, `Narrative`
  embedding + backfill, `episode_events` linking from `Draft.Links`,
  `EPISODE_OPENED/RESOLVED` events, multi-hypothesis arc splitting.
- #6 builder follow-up: full `<episode>` XML rendering from the five arc
  fields (currently condensed summaries).
- Integration test (TEST_POSTGRES_DSN-gated) for `EpisodeStore` against
  live Postgres + pgvector, owned by whoever wires migrations/002.
