# ADR-006 — Context Builder & Vector Search Engine

- **ADR ID:** ADR-006-context-builder-vector-search
- **Date:** 2026-09-17
- **Author:** issue-#6 agent (Wave 1)
- **Issue:** #6 Context Builder & Vector Search Engine (`internal/context/` + memory search)
- **Status:** Accepted

## Context

Issue #6 owns `internal/store/memory.go` (vector search: cosine similarity,
tag/key filters, CONFIRMED + confidence>0.3 guards, 0.7/0.2/0.1 rerank) and
`internal/context/builder.go` (5-tier override, 0.95^(days/30) decay, XML
output, 4000-char budget) per `implementation-plan.md` §§1.5–1.7, while three
constraints collide:

1. **Parallel ownership** — `internal/store/db.go`, `projects.go`,
   `workspaces.go` belong to issues #1/#2 (another agent). I must not touch
   them, yet my `Search` needs *some* query surface.
2. **Build hygiene** — `go build ./...` must stay green for everyone; the plan
   (§1.9) allows `pgx/v5` + `pgvector-go`, but every new import is a chance to
   break another agent's package.
3. **Testability** — `Decay`, `ResolveOverride`, `BuildXML`, `Rank` must be
   pure and unit-testable without a database.

## Options Considered

1. **Import pgx + pgvector-go in `memory.go`, query with native vector types.**
   Pros: type-safe vectors end-to-end. Cons: couples the search seam to the
   driver; any version skew with issue #2's pool code breaks my build too;
   `Search` becomes untestable without a live Postgres or heavy fakes.
2. **(Chosen) Stdlib-only seam: SQL-builder + scalar math + minimal
   `Querier`/`Rows` interfaces.**
   `BuildSearchSQL` renders the plan §1.5 query with the embedding passed as a
   pgvector text literal (`FormatEmbedding` → `"[0.1,0.2,...]"`); `Rank`
   re-scores with pure `CosineSimilarity`/`TagMatchScore`/`RecencyScore`;
   `Search` runs through a 2-method `Querier` interface. `memory.go` imports
   only stdlib (`context`, `fmt`, `math`, `sort`, `strconv`, `strings`,
   `time`); `builder.go` imports only stdlib (`encoding/xml`, `math`, `sort`,
   `strconv`, `strings`, `time`).
3. **Share one `MemoryItem` type across store and context packages.**
   Rejected: it would force `internal/context` to import `internal/store`
   (DB-flavoured concerns leak into the renderer) and give the MCP/server
   agents (#7/#8) no clean mapping point. Each package owns its types; the
   boundary agents map explicitly.

## Decision

- `internal/store/memory.go`: `MemoryItem`/`SearchQuery`/`RankedMemory`
  types; `BuildSearchSQL` (cosine `embedding <=> $2`, `status='CONFIRMED'`,
  `confidence > 0.3`, optional `tags &&`, `key =`, `level =`, clamped LIMIT);
  pure `CosineSimilarity`, `TagMatchScore`, `RecencyScore`
  (`0.95^(days/30)` — same time constant as confidence decay),
  `RerankScore` (0.7/0.2/0.1), `Rank` (stable desc sort, Key tiebreak);
  `Rows`/`Querier` interfaces + `Search` that scans `COALESCE`d columns
  (no NULL-pointer scanning, no driver import).
- `internal/context/builder.go`: `Decay`/`DaysSince`/`EffectiveConfidence`;
  `ResolveOverride` (SESSION > PERSONAL > PROJECT > ORGANIZATION; EPHEMERAL
  ranks above SESSION as most-transient-wins, same-level ties prefer higher
  base confidence); `BuildXML` with cross-level re-resolution (sections are
  authoritative, winners keep caller order), priority fill
  active-task → session → episodes → personal → project → organization at
  whole-item granularity, emission in plan §1.6 document order, `encoding/xml`
  escaping throughout.
- `go.mod`/`go.sum`: added `pgx/v5 v5.11.0` + `pgvector-go v0.4.1` (plan
  §1.9 / orchestration ADR-000-go-deps allow-list). My files use neither —
  the addition repairs the `internal/store` build broken by issue #2's
  `db.go`, which imports both without vendoring them (see Consequences).
  (`fsnotify` in go.mod is the harvester agent's entry, not mine.)

## Why (Rationale)

- **Interface seam over driver import:** issue #2's `db.go` (landed
  mid-task) declares `DBTX.Query(...) (Rows, error)` referencing the `Rows`
  interface defined in *my* `memory.go`, and asserts `*DB : Querier` at
  compile time (`var _ Querier = (*DB)(nil)`) — both directions compile
  without either agent editing the other's file. This is the mandatory "why":
  the narrow-interface split is proven by the two files composing
  untouched, verified by `go build ./...` + `go vet` passing on the merged tree.
- **Text-literal embeddings:** pgvector accepts `"[..]"` as query input, so
  the search path needs no vector type; the symmetric read-path parser
  (`ParseEmbedding`, issue #2's file) owns the one `pgvector-go` use in the
  package. One owner per dependency = no version-skew breakage.
- **Shared decay/recency constant:** `RecencyScore` reuses the §1.7
  `0.95^(days/30)` curve, so "stale in search" and "stale on display" can
  never disagree about time; anchors tested at 0d≈100%, 90d≈86%, 180d≈74%.
- **Item-granular truncation:** XML is emitted only as whole elements, so
  `len(out) <= budget` (except the always-included task / fixed overhead
  edge) *implies* well-formed output — the budget test asserts both length
  and `xml.Decoder` validity together.
- **Evidence:** `go build ./...` 0, `go vet ./internal/context/
  ./internal/store/` 0, issue-#6 test files 23/23 pass (10 context + 13 store),
  including decay anchors, full override chain, session-overrides-project in
  both `ResolveOverride` and `BuildXML`, truncation priority
  (org dropped first, task+session kept), and `SQL contains <=>`.

## Consequences

- Boundary agents (#7 MCP, #8 server) map `store.MemoryItem` → `context.Item`
  explicitly (field lists are near-identical; no shared-type refactor needed).
- Decay is applied at **serve time** (`EffectiveConfidence`); SQL filters the
  stored base confidence. Archival flagging (< 0.2, `use_count` = 0) and the
  24h/4h/1h auto-confirm timers belong to the Memory Processor (#10).
- `Search` returns the SQL LIMIT window reranked; true top-K across the full
  table (vs. top-20 pre-filter) is a scale follow-up, not Phase-1 scope.
- `RunMigrations`/`ListMigrationFiles` in `db.go` reference a `migrations/`
  dir owned by issue #1; missing dir = no-op by design, no action here.

## Alternatives Rejected

- Native pgvector types in the query path (option 1): heavier coupling for
  zero Phase-1 recall gain; text literals are lossless for float32 vectors.
- Shared cross-package item type (option 3): creates an import from a pure
  renderer to a DB package and steals the boundary agents' mapping decision.
- Recency as `1/(1+days/30)`: plausible, but a second time constant that
  could drift from the §1.7 decay curve; rejected for coherence.
