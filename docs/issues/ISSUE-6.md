# ISSUE-6 — Context Builder & Vector Search Engine

- **Issue:** #6 — [Phase 1] Context Builder & Vector Search Engine
  (`internal/context/` + memory search, plan §§1.5–1.7)
- **Status:** Done (implementation + tests + docs; awaiting merge)
- **Assignee:** Wave-1 subagent
- **Scope constraint:** ONLY `internal/context/`, `internal/store/memory.go`
  (+ tests), `docs/`, and `go.mod`/`go.sum` (deps). Did NOT touch
  `internal/store/db.go`, `projects.go`, `workspaces.go`.

## What was built

| File | Contents |
|---|---|
| `internal/store/memory.go` | `MemoryItem`/`SearchQuery`/`RankedMemory`; `BuildSearchSQL` (`embedding <=> $2`, `CONFIRMED`, `confidence > 0.3`, tag/key/level filters, LIMIT clamp); `FormatEmbedding`; pure `CosineSimilarity`, `TagMatchScore`, `RecencyScore`, `RerankScore`, `Rank`; minimal `Rows`/`Querier` + `Search` |
| `internal/store/memory_test.go` | 13 tests: SQL guards (`<=>`), filters/placeholders, LIMIT clamp, cosine edge cases, tag/recency/rerank math, semantic rank, tag-boost upset, recency tiebreak, fake-`Querier` end-to-end `Search`, query error |
| `internal/context/builder.go` | `Decay`/`DaysSince`/`EffectiveConfidence`; `LevelRank` + `ResolveOverride` (SESSION > PERSONAL > PROJECT > ORGANIZATION, EPHEMERAL top); `BuildXML` + `BuildStats` (priority fill task→session→episodes→personal→project→org, item-granular truncation, always-valid XML) |
| `internal/context/builder_test.go` | 10 tests: decay anchors (0d/90d/180d), effective confidence, full override chain, session-over-project, tiebreak, budget truncation order + validity, override inside `BuildXML`, full §1.6 document + decayed display, default budget, escaping |
| `docs/decisions/ADR-006-context-builder-vector-search.md` | Why-mandatory ADR (seam design, text-literal embeddings, shared decay constant, item-granular truncation) |
| `go.mod` / `go.sum` | Added plan-§1.9-approved `pgx/v5 v5.11.0`, `pgvector-go v0.4.1` (+ `mod tidy` indirects). My files use neither — the addition un-breaks the `internal/store` build (see below) |

## Decisions (see ADR-006 for rationale)

1. Stdlib-only seam: no driver/vector imports in my files; embedding travels
   as pgvector text literal; `Querier`/`Rows` interfaces for the pool owner.
2. `context` does not import `store` — boundary agents (#7/#8) map types.
3. Recency reuses the §1.7 decay curve (one time constant, not two).
4. Truncation at whole-item granularity ⇒ budget compliance implies valid XML.

## Cross-agent note (no files touched)

Issue #2's `internal/store/db.go` landed mid-task importing `pgx/v5` +
`pgvector-go` with no `go.mod` entries, breaking `go build ./...` for the
whole `internal/store` package (including this issue's file). It also
references my `Rows` interface and asserts `*DB : Querier` — the seam composes
untouched from both sides. Fix applied without editing `db.go`: `go get` +
`go mod tidy` for the two plan-§1.9-allow-listed deps only
(orchestration ADR-000-go-deps permits). Left for #2: `migrations/` wiring,
`projects.go`/`workspaces.go`, `TEST_POSTGRES_DSN` integration coverage.

## Verification (2026-09-17, go1.27.0, stdlib + pgx/pgvector-go in module)

- `go build ./...` → exit 0
- `go vet ./internal/context/ ./internal/store/` → exit 0
- Issue-#6 tests: **23/23 PASS** — `go test ./internal/context/` 10/10;
  `go test ./internal/store/ -run 'TestBuild|TestFormat|TestCosine|TestTag|TestRecency|TestRerank|TestRank|TestSearch'` 13/13
  (full output in agent transcript)
- `gofmt -l` on owned files → clean (pre-existing unformatted files in
  `internal/project`, `internal/scan`, `adapters` left for their owners)

> ⚠️ Package-level `go test ./internal/store/` shows **2 failures owned by
> issue #2**, in files outside this issue's scope (not touched per
> constraint): `TestStore_RunMigrationsMissingDirIsNoop` (expects
> `(nil, nil)` for a missing dir, `RunMigrations` returns the `ReadDir`
> error instead of the documented no-op) and
> `TestStore_RunMigrationsPropagatesExecError` (expects the error to name
> `002_bad.up.sql`, execution stops at `001_ok.up.sql`). Both are
> `db_test.go`-vs-`db.go` inconsistencies for the #2 agent to fix; all 13
> `memory_test.go` tests in the same package pass.

Acceptance mapping: semantic rank (`TestRankSemanticOrder`,
`TestRankTagBoostUpset`), session-overrides-project
(`TestResolveOverrideSessionOverProject`,
`TestBuildXMLSessionOverridesProject`), valid XML within budget
(`TestBuildXMLBudgetTruncationOrder` asserts `xml.Decoder` validity AND
`len <= budget`; `TestBuildXMLFullDocument` asserts §1.6 section order).

## Follow-ups (not this issue)

- #10 processor: decay-based archival flagging, auto-confirm timers,
  SESSION→PROJECT promotion counters.
- #7/#8: `store.MemoryItem` → `context.Item` mapping; `memory_search` MCP
  response envelope (`token_count`, `budget_remaining`, `reflection_hint`).
- #11: full episode XML (trigger/investigation/root-cause/…); builder
  currently renders condensed `<episode>` summaries.
- Scale: `Search` reranks the SQL LIMIT window; revisit pre-filter size with
  real data (IVFFlat `lists=100` per §1.1).
