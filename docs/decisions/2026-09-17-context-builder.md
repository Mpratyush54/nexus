# Context Builder & Hybrid Search — Design Decisions

Date: 2026-09-17
Scope: `internal/context/` (builder.go, search.go, decay.go) for
[Mpratyush54/nexus#6](https://github.com/Mpratyush54/nexus/issues/6)
(implementation-plan.md Phase 1.5 + 1.6 + 1.7).

## 1. Why cosine similarity computed locally in Go

Production ranking is pgvector's `embedding <=> $1` (cosine distance), but
the Go layer recomputes cosine over precomputed `[]float32` instead of
calling an embeddings API because:

- **No network, no keys, no cost.** The daemon's Memory Processor already
  paid for the embedding; re-scoring cached vectors is free and works
  offline. An API call per candidate at query time would add latency,
  billing, and a privacy surface (memory contents re-sent to a vendor).
- **Determinism and testability.** Pure-stdlib math (`math.Sqrt` over a
  dot product) is exactly reproducible in unit tests — semantic ranking,
  tag boosts, and re-rank weights are verified without mocks or fixtures.
- **Parity, not duplication.** The Go function mirrors pgvector cosine
  distance (`1 - similarity`), so in-memory re-ranking (merging vector
  hits with tag/key boosts) orders the same way Postgres does. The DB
  remains the source of truth for retrieval at scale; Go handles the
  final blend over the top-K candidates.

## 2. Why the IVFFlat assumption (<100K items, `lists = 100`)

The schema (Phase 1.1) indexes embeddings with IVFFlat, not HNSW, because:

- IVFFlat build time and memory are trivial at this scale, and recall is
  excellent when `lists` (~100) keeps each partition small.
- HNSW pays off past ~1M vectors with high-QPS ANN traffic we do not
  have; it costs more RAM and slower inserts for no measurable recall
  gain here.
- The plan already sets the switch-over trigger explicitly (HNSW at
  scale), so this is a reversible default, not a lock-in. The Go
  `HybridSearch` is index-agnostic — it re-scores whatever the DB
  returns — so swapping the index later changes nothing in this package.

## 3. Why XML output instead of JSON/Markdown

The Context Builder emits `<project_memory>` XML because:

- **LLMs parse explicit hierarchies better.** Named sections
  (`<session>`, `<personal>`, `<project>`, `<organization>`) plus
  per-item `key/confidence/scope` attributes give the model structural
  cues (scope, precedence, trust) that flat Markdown bullets lose.
- **Precedence is structural.** Override resolution (session shadows
  project on key collision) is expressed by *omitting* the shadowed
  item, and the surviving level is visible from the enclosing tag —
  no extra explanation tokens needed.
- **Budget truncation stays well-formed.** Dropping trailing low-priority
  items never breaks parsing, unlike cutting a JSON array or a Markdown
  table mid-row. The greedy fill algorithm relies on this property.
- **Cheap escaping.** `encoding/xml.EscapeText` (stdlib) is sufficient;
  no schema validator or template engine required.

## 4. Why re-rank weights 0.7 / 0.2 / 0.1

`score = 0.7·similarity + 0.2·max(tag,key) + 0.1·recency`:

- **0.7 vector-primary.** Semantic similarity is the only signal that
  generalizes to unseen phrasing — the whole point of pgvector over
  keyword search. It must dominate so a conceptually exact but
  differently-worded memory outranks a tag-coincidence.
- **0.2 tag/key boost.** Exact `key` hits ("testing/framework") and tag
  overlap are high-precision but low-recall; 0.2 lets them break ties
  and rescue near-miss vectors (verified in `TestHybridSearchTagBoost`:
  0.9-similarity + tag match beats 1.0-similarity with no match) without
  letting tag spam drown semantics.
- **0.1 recency.** Freshness (`exp(-days/90)`) is a tie-breaker only. A
  large recency weight would bury durable truths (org constraints from
  last year) under yesterday's chatter; 0.1 nudges only genuinely close
  contests.

## 5. Why the decay formula `base × 0.95^(days/30)`

- **Monthly 5% haircut is legible.** Anyone can reason "three idle months
  ≈ 86% of original" without a calculator, which matters because decayed
  confidence is *shown to the model* in the XML `confidence` attribute.
- **Geometric, not linear.** Linear decay hits zero on a schedule and
  needs clamping; geometric decay asymptotes, so ancient memories fade
  toward irrelevance but never go negative — no special-casing.
- **Idle clock, not wall clock.** Decay keys off `LastUsedAt` (fallback
  `UpdatedAt`/`CreatedAt`), and serving an item resets it via `use_count`
  / `last_used_at`. Frequently retrieved memories — pinned by usage —
  effectively opt out of decay, which is the desired "important things
  persist" behavior.
- **Stale gate needs both conditions.** `FlagStale` requires effective
  confidence < 0.2 AND 180+ idle days: a weak-but-used memory (live
  session scratch) and a strong-but-dormant one (org policy) both
  survive, matching Phase 2.7's "decayed below 0.2 AND use_count = 0"
  archival rule.

## Consequences

- `go build ./internal/context/` and `go test ./internal/context/` are
  green with stdlib-only imports; the package imports `internal/store`
  types read-only (never mutates items, never touches the DB).
- Known approximation: `TokenCount` counts bytes (≈ chars for ASCII
  memory text) as a cheap stand-in for tokenizer output; the 4000-char
  default (~1000 tokens) errs on the safe side.
