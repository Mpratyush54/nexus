# ADR-010-memory-processor

- **ADR ID:** ADR-010-memory-processor
- **Date:** 2026-09-17
- **Author:** issue-10 (processor subagent)
- **Issue:** #10 Memory Processor (`internal/daemon/processor.go`)
- **Status:** Accepted

## Context

The 4-layer pipeline (plan §2.2) funnels every event — tool interceptions,
harvested turns, instruction diffs, voluntary writes — into one consumer that
must turn raw activity into durable memories without ever disrupting the user.
Constraints from the issue: run only on `is_designated_processor` workspaces
(plan: owner's daemon, v1 consistency); batch on 5-min idle or
`SESSION_TRANSCRIPT_COMPLETE`; LLM via the user's own key with **no new SDK
deps**; extraction prompt per §2.6 with SESSION default; dedup skip at
cosine > 0.9 vs CONFIRMED; auto-confirm timers 24h / 4h / 1h (§2.8); episode
auto-detect per §2.3 with persistence delegated to the store layer. Ownership:
only `processor.go` (+ test) and `docs/` — harvester/interceptor/watcher/
daemon core are read-only.

## Options Considered

1. **Single-method `LLMClient` + stub + Ollama-HTTP over net/http (chosen).**
   One method — `Complete(ctx, prompt) (string, error)` — covers Ollama
   (`POST /api/generate`, `stream:false`), and any future OpenAI/Anthropic
   caller holding the user's key implements the same interface over plain
   HTTP. Zero entries added to `go.mod`. `LLMFunc` adapts bare funcs; the
   stub records prompts for test assertions.
2. **Vendor SDK per provider.** Would pin API shapes and add deps the plan
   (§1.9) never approved; extraction needs only text-in/text-out. Rejected.
3. **Dedup via store embeddings only.** Correct in production (pgvector
   cosine) but untestable without a DB and unavailable to a stdlib-only
   daemon file. Chosen instead: `VecCosine` when both sides carry
   same-length embeddings, else `TokenCosineSimilarity` (word-frequency
   cosine; identical texts score exactly 1.0). Same 0.9 threshold either
   way; the store owner swaps in real embeddings at the boundary.
4. **Episode detection inside the LLM pass.** Tempting (richer root-cause),
   but the §2.3 signal sequence (exit≠0 → reads → patch → verify → commit)
   is deterministic over tool events, costs zero tokens, and runs even when
   the LLM is down. Chosen: pure `DetectEpisode` hook first, LLM extraction
   second, both persisted through `ProcessorStore`. `RootCause` is left
   empty by the hook — tool events show *what*, not *why*.
5. **Flush-and-drop on error.** Simpler, but a transient LLM outage would
   silently lose a session's extraction. Chosen: the buffer is cleared only
   on success; failures retain events for the next sweep (covered by
   `TestProcessFailureRetention`).

## Decision

Implemented `internal/daemon/processor.go` (`package daemon`, stdlib only):

- `Processor` — per-session buffers fed by `Ingest`/`Sink()` (the sink
  adapter lets daemon core wire `NewHarvester(root, proc.Sink())` without
  touching harvester.go); `CheckIdle` flushes completed-or-5-min-idle
  sessions; `Start` ticks the sweep but returns immediately when not
  designated. Reuses `Clock`, `EventSink`, `EventSessionTranscriptComplete`
  and the `Event*` constants — nothing redeclared.
- `BuildExtractionPrompt` — transcribes the §2.6 template verbatim
  (level rules, scope rules, SESSION default, `{existing_memories}` +
  `{event_batch}` slots) plus a strict JSON-array output contract carrying
  `explicit_user_statement`.
- `ParseExtractedMemories` — tolerates prose around the array, normalizes
  via `NormalizeLevel` (unknown → session) / `NormalizeScope` (unknown →
  fact), clamps confidence to [0,1], enforces the 20–2000 char CHECK window
  (short/empty items counted `SkippedInvalid`, never sent to the store).
- `ConfirmAfterFor` — explicit → 1h, conf > 0.9 → 4h, else 24h (§2.8).
- `DetectEpisode` — first exit≠0 trigger, then reads→investigation,
  writes→fix, same-command exit-0→verification, commit→verification;
  bare failures with no follow-up return nil (noise, not an arc).
- `ProcessorStore` (`ListConfirmed` / `SaveProposed` / `SaveEpisode`) — the
  seam internal/store implements; the processor never imports it.
- `OllamaClient` — local-first HTTP extraction, 120s default timeout,
  8MB response cap.

## Why (Rationale)

This is the only combination meeting every acceptance criterion at once:
designated-only gating is proven by `TestProcessNotDesignated` (no buffer,
no LLM, no store touches); batching by `TestProcessBatching` (silent until
5-min idle, immediate on transcript-complete, fake clock); classification
defaults by `TestProcessClassificationDefaults` (empty/bogus → SESSION/fact,
valid labels preserved); dedup by `TestProcessDedupThreshold` (text path +
  vector path + 0.707 below-threshold negative); timers by
  `TestProcessTimerRules` (all three rules + boundary 0.9 + explicit-wins);
  episodes by `TestProcessEpisodeAutoDetect` (full arc roles, two negatives,
  store delegation). Verification is green: daemon build OK,
  `go vet ./internal/daemon/` clean, `go test -run TestProcess` 7/7 PASS,
  full daemon suite PASS (see `docs/issues/ISSUE-10.md`).

## Consequences

- No `go.mod`/`go.sum` changes (stdlib only — net/http, encoding/json).
- `go build ./...` still fails, but **only** in `internal/store`
  (`events.go` vs `episodes.go` redeclare `EventFileRead` et al.) — a
  parallel-agent collision owned by other issues, untouched per scope.
- Follow-ups: real embeddings at the store boundary; `RootCause` LLM
  enrichment; promotion/demotion counters (§2.7); hoisting the shared
  `EventSink`/`Clock` to `events.go` once Wave 1 lands.
