# ADR-011 — Episodes Engine: Auto-Detection & Retrieval

- **ADR ID:** ADR-011-episodes-engine-auto-detection
- **Date:** 2026-09-17
- **Author:** issue-#11 agent
- **Issue:** #11 Episodes engine (`internal/store/episodes.go`, plan §§2.3–2.5)
- **Status:** Accepted

## Context

Plan §2.3 requires the Memory Processor to recognize bug/incident arcs
automatically (fail → investigate → fix → verify → commit) and persist
them as searchable `episodes` rows; §2.4 requires three retrieval paths
(exact error-pattern match, file lookup, semantic similarity); §2.5 wires
matches into the Context Builder. Three constraints collide:

1. **Parallel ownership** — `internal/store/db.go`, `projects.go`,
   `workspaces.go`, `memory.go` belong to other issues and must not be
   touched, yet the engine needs a query seam and the pgvector literal
   helper.
2. **Testability** — the heuristic must be unit-testable without a
   database ("pure detector func … testable without DB").
3. **Precision vs recall** — the five-stage chain in the issue text is the
   canonical shape, but real arcs often skip stages (fix from memory with
   no reads; fix verified but never committed). Requiring every stage
   would silently drop real episodes.

## Options Considered

1. **Strict 5-stage matcher:** require all five stages in order, else nil.
   Pros: maximal precision, literal match to the issue text. Cons: misses
   the two most common real-world variants (no-read fix, uncommitted
   fix); recall loss with zero precision gain (the required stages alone
   already exclude clean runs).
2. **(Chosen) Core-required, edges-optional:** trigger (failing command)
   + ≥1 `FILE_MODIFIED` + later passing command are required; the
   `FILE_READ` cluster and closing `GIT_COMMITTED` are optional
   enrichers. Verification prefers the same command line, accepts any
   passing command as fallback.
3. **LLM-judged arcs:** send candidate windows to the Memory Processor's
   LLM for classification. Rejected: non-deterministic, untestable
   without a key, burns tokens per event window; the syntactic signal
   (exit codes + ordering) is already decisive.

## Decision

- `internal/store/episodes.go` (stdlib-only): `EventView` input shape;
  pure `DetectEpisode([]EventView) *EpisodeDraft` synthesizing
  trigger/investigation/root_cause/resolution/verification plus
  `Narrative` (the embed-the-whole-story text, §2.3 step 4);
  `ExtractErrorPatterns` (stderr-first, ≤5 deduped ≤200-char lines,
  ANSI-stripped); `FilesInvolved` from mods only, order-preserved,
  deduped; link roles trigger/investigation/fix/verification (+ commit
  as context, matching the `episode_events` CHECK set).
- Retrieval: `BuildEpisodeErrorSearchSQL` (`$2 = ANY(error_patterns)` +
  `RESOLVED` + `resolved_at DESC`), `BuildEpisodeFileSearchSQL`
  (`ANY(files_involved)`), `BuildEpisodeSimilaritySQL`
  (`embedding <=> $2`, `RESOLVED`, clamped LIMIT),
  `BuildCreateEpisodeSQL` (nil embedding → NULL for processor backfill),
  pure `OrderBySimilarity` mirroring `memory.go` Rank tiebreaks.
- Seam: locally-defined `EpisodeQuerier` (Query-only; `INSERT…RETURNING`
  goes through Query so creates stay driver-free) + reuse of `Rows` and
  `FormatEmbedding` from `memory.go`. `*DB` satisfies `EpisodeQuerier`
  implicitly — zero edits to other owners' files.

## Why (Rationale)

- **Recall without precision loss:** the required core (fail + fix +
  verify) is already sufficient to exclude every clean-run shape — proven
  by `TestEpisodeNoFalsePositiveCleanRun` (all-green runs, unfixed
  failures, and unverified fixes all yield nil). The optional stages only
  ever *enrich* text, never gate detection, so the full canonical chain
  still detects with maximal detail (`TestEpisodeFullArcDetection`
  asserts all 8 link roles including the commit-as-context).
- **Same seam proof as ADR-006:** stdlib-only file + narrow local
  interface composes with the pool owner's `*DB` untouched, verified by
  `go build ./...` + `go vet` passing on the merged tree.
- **Text-literal embeddings again:** `FormatEmbedding` reuse keeps the
  second pgvector query path driver-free, consistent with the memory
  search decision.
- **Honest root_cause:** the detector labels its synthesis a "heuristic
  inference" (command + exit + first error + files touched) rather than
  fabricating certainty — the LLM processor (issue #10) refines it.
- **Evidence:** `go build ./...` 0, `go vet ./internal/store/` 0,
  `go test ./internal/store/ -run TestEpisode` 10/10 pass.

## Consequences

- Issue #10 (Memory Processor) owns: mapping JSONB payloads → `EventView`,
  embedding `Narrative` into `NarrativeEmbedding`, `episode_events`
  linking from `Draft.Links`, backfilling `embedding` on rows created with
  NULL, and promotion of drafts to `EPISODE_OPENED` events.
- Context Builder (#6 follow-up) renders full `<episode>` XML from the
  five arc fields; it currently renders condensed summaries.
- Same-command verification preference is first-match within the
  post-fix window; multi-hypothesis arcs (several fix→fail cycles before
  the final pass) collapse to one episode anchored at the first failure —
  splitting is a follow-up.

## Alternatives Rejected

- Strict 5-stage matching (option 1): drops real episodes for no tested
  precision gain; rejected per recall argument above.
- LLM-judged arcs (option 3): non-deterministic and untestable offline;
  syntax (exit codes + order) already decides.
