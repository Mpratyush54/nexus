# ISSUE-10 — Memory Processor (background extraction on designated workspaces)

- **Status:** Done
- **Assignee:** issue-10 (processor subagent)
- **Scope:** `internal/daemon/processor.go`, `internal/daemon/processor_test.go`,
  `docs/` only. harvester/interceptor/watcher/daemon core untouched.
- **Plan refs:** `implementation-plan.md` §§2.2 (pipeline sink), 2.3 (episode
  auto-detect), 2.6 (extraction prompt), 2.7 (promotion — deferred), 2.8
  (confirmation flow); §1.1 (CHECK constraints), §1.3 (5-min idle).

## What was built

`internal/daemon/processor.go` (`package daemon`, stdlib only) — the §2.2
pipeline sink, running as a background goroutine on designated workspaces:

| Piece | Behavior |
|---|---|
| `Processor` + `Start` | Batches per session; sweeps every 1m; no-op when not `is_designated_processor` |
| `Ingest` / `Sink()` | Buffers turns/tool events; `SESSION_TRANSCRIPT_COMPLETE` marks immediate flush; sink adapter wires harvester output with zero changes to it |
| `CheckIdle` / `FlushSession` | Flush on 5-min idle or transcript-complete; buffer cleared only on success (failures retained for retry) |
| `LLMClient` + `StubLLMClient` + `LLMFunc` + `OllamaClient` | User-key LLM seam; Ollama via plain `net/http` (`/api/generate`); OpenAI/Anthropic implement the same method — **no new SDK deps, `go.mod` untouched** |
| `BuildExtractionPrompt` | §2.6 template verbatim + JSON contract with `explicit_user_statement` |
| `ParseExtractedMemories` + `NormalizeLevel/Scope` | Unknown level → SESSION (§2.6); unknown scope → fact; confidence clamped; 20–2000 char CHECK enforced pre-store |
| `IsNearDuplicate` (`VecCosine` / `TokenCosineSimilarity`) | Skip when cosine > 0.9 vs CONFIRMED; embedding path when available, token path otherwise |
| `ConfirmAfterFor` | Explicit → 1h; conf > 0.9 → 4h; else 24h (§2.8) |
| `DetectEpisode` | exit≠0 → reads → patch → verify → commit → `bug_fix` draft + `episode_events` roles; bare failures return nil |
| `ProcessorStore` | `ListConfirmed` / `SaveProposed` / `SaveEpisode` seam — persistence delegated to internal/store, never SQL here |

## Decisions

- See `docs/decisions/ADR-010-memory-processor.md` (interface-over-SDK LLM
  choice, dual-path dedup, pure episode hook with empty `RootCause`,
  retain-on-error buffers, scope-default `fact` rationale).
- One collision found and avoided without touching others' files:
  `firstLine` already existed in `gitops.go` — the processor helper is
  named `episodeFirstLine`.

## Verification

- `go build ./internal/daemon/ ./adapters/... .` — **OK**
- `go build ./...` — fails **only** in `internal/store` (`events.go` vs
  `episodes.go` redeclare `EventFileRead`/`EventFileModified`/
  `EventCommandExecuted`/`EventGitCommitted`) — pre-existing parallel-agent
  collision, out of scope, untouched.
- `go vet ./internal/daemon/` — **clean**
- `go test ./internal/daemon/ -run TestProcess -v` — **all 7 PASS**
  (`TestProcessClassificationDefaults`, `TestProcessDedupThreshold`,
  `TestProcessTimerRules`, `TestProcessBatching`,
  `TestProcessEpisodeAutoDetect`, `TestProcessNotDesignated`,
  `TestProcessFailureRetention`) — fake LLM + fake store, no network.
- `go test ./internal/daemon/` (full package) — **PASS**, no regressions.

## Follow-ups

1. Store layer implements `ProcessorStore` (real embeddings replace the
   token-cosine fallback at the boundary).
2. `RootCause` LLM enrichment for episode drafts.
3. §2.7 promotion/demotion counters (3+ sessions → project).
4. Hoist shared `EventSink`/`Clock` to `internal/daemon/events.go` (Wave 1).
5. `internal/store` event-constant collision belongs to its owners.
