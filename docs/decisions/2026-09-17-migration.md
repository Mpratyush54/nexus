# Legacy Vault & Session Import (Phase 1b) — Decisions

Date: 2026-09-17. Scope: `internal/migrate/*` (issue #25 / plan Phase 1b).
Every choice below carries Context / Decision / Alternatives / Why /
Consequences.

---

## 1. Heuristic markdown parsing, not a strict schema

- **Context:** The v1 vault has no schema — `mem remember` appended
  `\n## <timestamp>\ntags: <tags>\n\n<text>\n` chunks to `learnings.md` /
  `MEMORY.md`, but users hand-edited files too (missing `tags:` lines,
  title words after the timestamp, CRLF endings, bare-date headers).
  The old `mem recall` already treated `\n## ` as the chunk delimiter.
- **Decision:** Split on the same `\n## ` delimiter recall used, then
  best-effort parse each chunk: optional leading timestamp (several
  layouts), optional `tags:` line, free-text body. Unshaped chunks are
  skipped and counted, never fatal.
- **Alternatives:** (a) Strict parse, abort on first malformed chunk —
  one bad chunk would block thousands of good ones. (b) Regex-extract only
  perfect chunks silently — same outcome as ours but hides data loss.
- **Why:** Recall-compatibility means anything recall could read, migrate
  reads identically; skip-and-count bounds data loss to a visible number
  (`Counts.Skipped`) instead of a failed run.
- **Consequences:** Parser accepts slightly more than the writer produced
  (e.g. title-only headers are tolerated). The skipped count in dry-run
  output is the audit trail — a large number means hand inspection.

## 2. Imported memories land as CONFIRMED, not PROPOSED

- **Context:** New memories enter as PROPOSED and need confirmation
  (plan §2.8). Vault memories were explicitly saved by a human running
  `mem remember` — often months ago — and have been served by recall since.
- **Decision:** All parsed chunks become `Status = CONFIRMED` with a
  confidence heuristic: 1.0 (timestamp + tags), 0.9 (either), 0.8
  (neither). Scope is keyword-classified (constraint / preference /
  decision / pattern / fact) on the plan's own vocabulary.
- **Alternatives:** (a) PROPOSED + 24h auto-confirm — re-litigates every
  old decision and spams the confirmation queue with hundreds of rows.
  (b) CONFIRMED at flat 1.0 — overstates hand-edited, tagless chunks.
- **Why:** An explicit `remember` is the strongest confirmation signal the
  system ever gets — stronger than the passive extraction that earns
  auto-confirm in §2.8. The confidence gradient preserves the provenance
  difference between a fully-formed entry and a fragment.
- **Consequences:** Imported rows are immediately visible to
  `memory_search`. If a vault held junk, it ships as junk — mitigated by
  dry-run review and by the 20-char CHECK floor that drops fragments.

## 3. `--dry-run` parses, validates, dedups — and writes nothing

- **Context:** The vault is the user's only copy of pre-Postgres knowledge;
  a buggy first import that half-writes or duplicates rows is worse than no
  import. Embeddings also cost money per row, so operators want the row
  count before spending.
- **Decision:** `Run(vaultPath, dryRun) (Counts, error)` always does the
  full read/parse/validate/dedup pipeline and returns
  `{Memories, Projects, Events, Skipped}`; `dryRun` selects "report only"
  vs "hand insert-ready DTOs to the store adapter". No code path in this
  package opens the database.
- **Alternatives:** (a) No dry-run, import directly — blind writes, no
  audit. (b) Dry-run as a separate code path — drifts from the real path
  and lies about counts.
- **Why:** Single pipeline means the dry-run counts are exactly the import
  counts. Operators run `nexus migrate --vault PATH --dry-run`, inspect
  `Skipped`, then run without the flag.
- **Consequences:** The INSERT binding (store adapter, embeddings
  backfill) is a follow-up; DTO shapes are frozen so that binding needs no
  parser changes.

## 4. Stdlib-only package with local DTOs; Fingerprint injected

- **Context:** `internal/store` pulls pgx/pgvector; at migration time
  sibling packages churn in parallel. The mcp package already set the
  precedent: local DTOs + narrow surface = builds green regardless.
- **Decision:** `internal/migrate` imports only stdlib plus
  `internal/project` (itself stdlib-only) for `Fingerprint`. `MemoryItem`,
  `ProjectSeed`, `HistoricalEvent` mirror store types field-for-field.
  `SeedProjectsWith` / `RunWith` take a `FingerprintFunc` so tests inject
  fakes and never touch `D:\` or git; `SeedProjects` / `Run` wire the real
  `project.Fingerprint` (+ `project.Leaves`) for production.
- **Alternatives:** (a) Import store types directly — couples migration to
  pgx and to store refactors. (b) Shell out per project in tests with real
  git repos — slow, Windows-hostile, flakes without git config.
- **Why:** The migration binary must build on any machine with no database
  and no fixtures; production still gets move-proof identity
  (URL → root commit → folder, same priority as `ResolveProject`).
- **Consequences:** `normalizeGitURL` is a local copy of
  `store.NormalizeGitURL` — the two must stay in sync (noted in code), or
  seeds will miss at lookup time. DTO drift is caught by inspection, not
  the compiler; acceptable for a one-shot importer.

## 5. One session row becomes a SESSION_STARTED + SESSION_ENDED pair

- **Context:** `sessions.jsonl` holds session summaries (`session_id`,
  `project`, `updated`, optional `was`/`started`/`ended`), not per-turn
  transcripts. The event store (§2.1) models lifecycle explicitly.
- **Decision:** Each unique (agent, session_id) row emits exactly the pair;
  timestamps prefer `started`/`ended`, fall back to `updated`; the `was`
  field is preserved as `Note: "moved from X"`. Duplicates, invalid JSON,
  and session-less rows are skipped and counted.
- **Alternatives:** (a) Single event per row — loses the ended endpoint the
  schema was designed around. (b) Synthesize turn events — fabricating
  history that never existed.
- **Why:** Lifecycle endpoints are all the history there is; a faithful
  pair preserves ordering and duration without inventing content.
- **Consequences:** Sessions with no timestamps get zero-time endpoints
  (the INSERT binding defaults them). Cross-file repeats collapse via
  `DeduplicateEvents` on (type, agent, session).

## 6. Content-identity dedup (1:1, first wins)

- **Context:** The same fact was often remembered globally and per-project,
  and harvesters re-exported sessions. Naive import would fork duplicate
  projects and double memories.
- **Decision:** Projects dedup by normalized URL → root commit → folder
  (mirroring `ResolveProject` priority); memories by normalized content;
  events by (type, agent, session). First seen wins everywhere, so import
  order (sorted reads) makes output deterministic.
- **Alternatives:** (a) No dedup, clean up in SQL later — duplicates get
  embeddings ($$) and pollute search first. (b) Fuzzy dedup — risks
  merging distinct memories that share phrasing.
- **Why:** Exact-content dedup is conservative (never merges distinct
  thoughts) and catches the real duplication pattern (copy-paste across
  global/project files, re-harvests).
- **Consequences:** Near-duplicate phrasings import twice — left for the
  Memory Processor's promotion pass (§2.7), which is the right layer for
  semantic judgments.
