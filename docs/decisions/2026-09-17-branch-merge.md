# Branch Diff, Staleness & Merge — Decisions (issue #18, Phase 5)

Date: 2026-09-17. Scope: `internal/branches/diff.go`, `merge.go`,
`staleness.go` (+ tests) only — new package, no existing files touched.
Every choice below carries Context / Decision / Alternatives / Why /
Consequences.

---

## 1. New package `internal/branches`, not `internal/store/branches.go`

- **Context:** Issue #18 scopes the work to `internal/store/branches.go`
  (Diff & Merge against store rows). The plan (§5.2) describes CoW
  resolution, diff, and merge as pure key semantics over branch snapshots.
- **Decision:** All Phase 5 collaboration logic lives in a new stdlib-only
  package `internal/branches` operating on a local `Entry{Key, Content}`
  DTO. Callers adapt `store.MemoryItem` rows into entries; the package
  imports nothing from `internal/store`.
- **Alternatives:** (a) Methods on `MemStore`/`PostgresStore` in
  `internal/store/branches.go` — couples pure set logic to DB plumbing and
  forces a database for every unit test. (b) Generic over `store.MemoryItem`
  — drags confidence/status/embedding columns into functions that never read
  them.
- **Why:** Diff/merge/staleness are deterministic functions of (key,
  content, timestamps); isolating them gives hermetic `go test` runs with
  zero fixtures and lets both `MemStore` and the Postgres store reuse the
  same engine without duplication.
- **Consequences:** A thin adapter (store row → `Entry`/`StaleCandidate`)
  is still owed at the store layer. Mitigated by keeping the DTOs minimal
  so the adapter is a field copy, not a transformation.

## 2. Why 3-way merge with the fork point as base

- **Context:** Plan §5.2 defines fork as zero-copy (new branch row with
  `parent_branch_id`, no data copied) and merge as source items → PROPOSED
  on target. A 2-way merge (source vs target only) cannot tell "source
  changed this" from "target changed this" from "both changed this".
- **Decision:** `Merge(base, source, target)` treats `base` as the parent
  snapshot at fork time (`forked_at_event`). Per-key rules: source-only
  change → auto-merge; target-only change → keep; identical agreement →
  no-op; divergent double-change → `CONFLICT`.
- **Alternatives:** (a) 2-way source-vs-target: every differing key looks
  identical, so the merger must either always overwrite (silent data loss)
  or always conflict (merge is useless when branches merely diverged on
  different keys). (b) Timestamp-based last-writer-wins: clock skew across
  daemons makes "newer" meaningless, and it silently discards the loser's
  content — exactly what issue #18 forbids.
- **Why:** The fork point is already recorded (`forked_at_event_id`,
  migration 005), so the ancestor is free. 3-way is the only scheme that
  auto-merges the common case (disjoint edits) while still surfacing true
  conflicts — the plan §5 exit criterion (fork → diverge → diff → merge
  with conflicts surfaced).
- **Consequences:** Callers must reconstruct the base snapshot (parent
  reads as of the fork event). That is a store-layer query, deliberately
  outside this package; `Merge` documents the contract instead of fetching.

## 3. Why key-based identity plus content hash (not row IDs, not embeddings)

- **Context:** CoW branches never copy rows: the same logical memory exists
  as different rows (or via parent-chain resolution) on each branch. Issue
  #18 asks for "key-level changes".
- **Decision:** Identity is `key` (the machine key, e.g.
  `testing/framework`); change detection is SHA-256 over
  whitespace-normalized content. Same key + different hash = modified, never
  an add+remove pair. Duplicate keys in one snapshot collapse
  last-write-wins, mirroring CoW read resolution.
- **Alternatives:** (a) Row UUIDs — meaningless across branches since fork
  copies nothing; every key would look added+removed. (b) Embedding cosine
  similarity (>0.9 ≈ same, per plan §2) — probabilistic, threshold-fragile,
  and needs a vector runtime; paraphrase-vs-edit ambiguity would make diffs
  non-deterministic. (c) Raw string equality — functionally equivalent but
  reports editor-added blank lines as modifications; `TrimSpace`
  normalization removes that noise while remaining exact, not fuzzy.
- **Why:** Keys are already the declared identity of a memory item (unique
  per scope in the schema); hashing makes comparison O(1) per key and gives
  consumers a stable fingerprint. Deterministic output (sorted by key) keeps
  tests and UIs stable.
- **Consequences:** A genuine rewrite under the same key reads as
  "modified", even if semantically unrelated — correct per CoW semantics
  (the key slot was reused), and the 3-way merge still protects it. Key
  renames read as add+remove; acceptable — renames are rare and the diff
  shows both sides.

## 4. Why staleness thresholds: confidence < 0.2 AND 180 days idle AND zero reuse

- **Context:** Two separate plan rules feed staleness: §1.7 decay curve
  (`0.95^(days/30)`), §2.7 archival ("confidence decayed below 0.2 AND
  `use_count = 0`"; "never used in 180 days: flagged"), and §5.4
  ("parent update on a key that exists on child → `potentially_stale`").
- **Decision:** `DetectStaleness` flags neglect only when all three hold:
  effective confidence < 0.2, idle ≥ 180 days, `use_count == 0`. Parent
  advancement (parent write strictly after `ForkedAt`) flags independently
  via `ReasonParentAdvanced`, regardless of confidence. `EffectiveConfidence`
  reuses the exact §1.7 formula (verified: 1.0 → ~0.86 at 90d, ~0.74 at
  180d).
- **Alternatives:** (a) Flag on confidence alone — would mark actively
  reused but low-confidence items stale, punishing precisely the items the
  team keeps consulting. (b) Flag on age alone — would archive yesterday's
  confirmed decisions. (c) Single combined score — hides *why* an item is
  stale; separate `Reasons` let the UI show "parent moved on" vs "nobody
  reads this" with different remediation (rebase vs archive).
- **Why:** The conjunction reproduces the plan's archival rule literally
  while the decay curve supplies the time dynamics; either signal alone
  over-flags. Parent-advance is unconditional because a child copy written
  before the parent's update is definitionally behind consensus, even at
  confidence 1.0.
- **Consequences:** A low-confidence item reused even once escapes the
  neglect flag until it idles another 180 days — intended (reuse = value).
  `LastUsedAt`-missing rows fall back to `UpdatedAt`, then `ForkedAt`, so
  legacy rows get a fair idle clock instead of instant-flagging.

## 5. Auto-merge as PROPOSED + SUPERSEDED marking, conflicts as data (never silent overwrites)

- **Context:** Issue #18: non-conflicting keys auto-promoted as PROPOSED on
  target; conflicting keys flagged with `conflict_with` for human review.
  Plan §5.2: same-value = skip, different-value = conflict.
- **Decision:** `MergeResult` separates four outcomes: `Merged` (always
  `StatusProposed`), `Superseded` (old → new content pairs, including
  tombstones with empty `NewContent` for propagated deletions),
  `Deleted` (keys to remove), `Conflicts` (full base/source/target triple
  plus Found flags). A conflicting key appears in *none* of the write
  paths — no silent overwrite is structurally possible. Target-only keys
  and identical bilateral edits are no-ops.
- **Alternatives:** (a) Auto-confirm merges as CONFIRMED — bypasses the
  §2.8 confirmation flow and lets a fork inject unreviewed facts into main.
  (b) Resolve conflicts by policy (e.g. source-wins with a log line) —
  violates the issue's explicit "without silent overwrites" criterion.
  (c) Single merged-snapshot output — forces the caller to re-diff to find
  what changed; the four-list shape is directly actionable (insert list,
  delete list, review list).
- **Why:** PROPOSED routes every auto-merge through the existing
  confirmation machinery (24h auto-confirm / manual review), so merging is
  safe by default; SUPERSEDED preserves lineage for blame-style history
  (the CoW model's stated goal); the `Conflict` triple gives reviewers
  everything needed to resolve without extra queries.
- **Consequences:** Callers must apply the four lists transactionally
  (insert PROPOSED, mark SUPERSEDED, delete, surface conflicts) — the
  package computes, never writes. Deletion only propagates when the other
  side left the key untouched; any edit/delete pairing is a conflict, which
  is conservative but matches "flag, don't guess".
