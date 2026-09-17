# Decision: Local Memory Processor on Designated Device — heuristic-first, stdlib-only

Date: 2026-09-17
Scope: `internal/daemon/processor.go` (+ `processor_test.go`)
(GitHub Mpratyush54/nexus issue #10, implementation-plan.md Phase 2.6/2.7/2.8 + §1.7)

## Context

The Memory Processor turns the raw event stream from all four extraction
layers (tool interception, transcript harvesting, instruction-file watcher,
piggyback hints) into structured, classified, deduplicated memory proposals.
The plan fixes three hard constraints: it runs on the user's device (zero
server-side LLM cost, user supplies own keys), exactly one daemon per project
does the proposing, and extraction quality must degrade gracefully when no LLM
key is configured.

## Decisions

### 1. Designated processor — only `is_designated_processor` proposes

- `Processor.Designated` mirrors `workspaces.is_designated_processor` (the
  project owner's daemon). `ShouldRun()` gates everything: non-designated
  daemons return nil and only consume memories via `memory_search`.
- Why: with N daemons proposing from overlapping event streams, the same
  Redis decision would be proposed N times with slightly different wording,
  and dedup (> 0.9 similarity) would have to clean up the mess after the
  fact. Single-writer per project eliminates multi-device divergence at the
  source (plan Locked Decisions). Other daemons converging as readers is
  exactly the Phase 2 exit criterion (Bob sees Alice's decision).
- Trade-off accepted: if the owner's device is offline, no new proposals land
  until it returns. Events are append-only, so nothing is lost — processing
  catches up. A future leader-election could remove the single point of
  stall, but is out of scope for v1.

### 2. Heuristic-first extraction, LLM behind a `Provider` interface

- `Provider.Extract(ctx, project, events, existing)` is the seam.
  `HeuristicProvider` (sentence-split + `ClassifyLevel`/`ClassifyScope` +
  confidence scoring) is the default and the offline fallback; a
  network-backed provider can replace it later without touching batching,
  dedup, caps, or confirmation logic.
- Why heuristic-first: (a) issue #10's acceptance criteria (atomic proposals,
  > 0.9 dedup, classification) are fully testable deterministically — no API
  key, no network, no flaky LLM assertions in CI; (b) the daemon package must
  stay stdlib-only (`go.mod` has no HTTP-LLM SDK, and adding one couples the
  cross-platform daemon to a vendor); (c) when the user's key IS present, the
  same `BuildExtractionPrompt` (§2.6 wording, versioned next to the heuristics
  it must agree with) drives the real call — prompt and stub cannot drift.
- Confidence scoring mirrors the confirmation lanes: explicit user statements
  (`IsExplicitStatement`) score 0.95 and take the 1h lane; reasoned sentences
  ("because…") score 0.8; background context scores 0.7 (24h lane).

### 3. Local LLM execution — keys never leave the device

- `ProviderKeyFromEnv()` reads `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` /
  `OLLAMA_HOST` from the local environment only. There is deliberately no
  key field on any struct that crosses the daemon→server boundary, and the
  server never receives prompts — only finished proposals.
- Why: the plan's Locked Decisions say "user supplies own API keys; data
  stays local". Shipping keys or transcripts server-side would convert a
  local-first memory tool into a data-collection service and break the trust
  model the whole passive-extraction design rests on (users tolerate silent
  tailing only because processing stays on their machine).

### 4. Safe-default classification + promotion, not precision up front

- `ClassifyLevel` defaults to SESSION when unsure (plan §2.6: "safer — can be
  promoted later"). An over-scoped item leaks across sessions; an
  under-scoped one just waits for `ShouldPromoteSessionToProject` (3+
  sessions ⇒ SESSION→PROJECT, plan §2.7).
- Marker priority is deliberate: personal ("I prefer…") beats session ("for
  now…") beats organization beats project. Rationale: misfiling a personal
  preference as project scope affects the whole team; misfiling a session
  note as personal only affects one user. Highest blast radius wins the
  tiebreak toward the narrower scope, except organization markers which are
  rare and lexically distinctive ("all APIs", "company policy").
- `ClassifyScope` checks constraints first for the same reason: a hard rule
  ("must not…", "never…") misclassified as a fact loses its must-not-violate
  force in the Context Builder output.

### 5. Dedup via bag-of-words cosine at the issue's > 0.9 threshold

- Production will use pgvector embedding distance; here `CosineSimilarity`
  over token-frequency vectors implements the identical threshold semantics
  (> 0.9 ⇒ discard). `Deduplicate` also guards within-batch dupes (the
  harvester's deep-analysis pass re-emits turns already seen incrementally).
- Why not exact-match: the same decision arrives worded differently from
  Layer 2 ("decided on Redis") vs Layer 4 (`memory_reflect` summary).
  Exact-match would double-propose; 0.9 cosine catches paraphrases while
  leaving genuinely distinct facts (tested: unrelated content scores far
  below threshold).

### 6. Episode detection requires the full §2.3 arc — trigger, fix, green re-run

- `DetectEpisodePattern` demands `COMMAND_EXECUTED(exit≠0)` → `FILE_MODIFIED`
  → same-command `COMMAND_EXECUTED(exit=0)`; investigation reads and the
  closing commit are optional (OPEN vs RESOLVED status). A different command
  going green does not verify the fix — this is the most common false-positive
  shape (agent fixes tests, then `go vet` passes while tests still fail).
- The same-command check uses the interceptor's `command + args` payload keys,
  so detection stays coupled to Layer 1's vocabulary, not to stringly-typed
  guesses.

### 7. Batching: 5min idle or `SESSION_TRANSCRIPT_COMPLETE`, bounded by budget

- `ShouldFlush` mirrors the harvester's `DefaultIdleTimeout` (5min): idle
  batches get the cheap incremental pass; session-complete batches get the
  full-transcript deep analysis. `Budget` (4000 chars, the §1.6 per-agent
  budget scale) and `BatchSize` (20) cap each pass first-N-wins, matching the
  Context Builder's bounded-budget philosophy — the processor must never
  propose more than the context pipeline can serve.
- Content < 20 chars is skipped (plan §1.1 `CHECK (length(content) >= 20)`),
  so "ok", "thanks", "lgtm" never become memories.

## Consequences

- `go build ./internal/daemon/` stays stdlib-only; no `go.mod` changes.
- Follow-ups (not this issue): network-backed `Provider` using
  `ProviderKeyFromEnv`; wiring `ProcessEvents` into the daemon goroutine with
  the harvester/interceptor channels; `internal/store` backing for
  `MemoryStore`; pgvector embeddings replacing `CosineSimilarity`.
