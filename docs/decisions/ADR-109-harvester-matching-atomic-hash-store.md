# ADR-109-harvester-matching-atomic-hash-store

- **ADR ID:** ADR-109-harvester-matching-atomic-hash-store
- **Date:** 2026-09-17
- **Author:** issue-109 subagent
- **Issue:** #109 audit — harvester matches workspaces by folder-name fallback
  only; watcher persists file-hash state with a non-atomic direct write
- **Status:** Accepted

## Context

`Harvester.MatchesWorkspace` attributed transcripts by folder-name substring
alone, so unrelated repos with generic directory names (`api`, `server`,
`frontend`) were harvested into the wrong workspace stream. The store layer
already resolves projects by canonical-URL → root-commit → folder-name
(`MemStore.ResolveProject`, `store.NormalizeGitURL`), and move-proof repo
identity already exists (`internal/project.Fingerprint` → origin + root
commit, reused daemon-side via `FingerprintOf`) — the harvester just never
used either. Separately, `FileHashStore.saveLocked` rewrote JSON state with a
single `os.WriteFile`, so a mid-write crash truncated the file and forced full
reprocessing on reboot. Constraints: only `harvester.go` / `watcher.go` (+
their tests) and two new docs; daemon stays stdlib-only (no `internal/store`
import); no git binary in tests; existing missing/corrupt-file semantics pinned.

## Options Considered

1. **Identity-priority pure matcher + atomic temp-write (chosen).**
   Harvester carries best-effort origin/root; a pure
   `ClassifyWorkspaceMatch` orders URL > root > folder and rejects
   known-disagreeing identity without folder rescue; `saveLocked` does
   same-dir temp + fsync + rename with best-effort cleanup.
2. **Import `store.NormalizeGitURL` directly.** Rejected: drags the Postgres
   dependency set into the stdlib-only daemon (same layering rule as #34);
   the local mirror follows the `internal/migrate` precedent instead.
3. **Folder-name matching with an exclusion list for generic names.**
   Rejected: blocklists never converge and still misattribute; identity
   comparison fixes the cause, not instances.
4. **fsync-the-directory / full WAL scheme for the state file.**
   Rejected: directory fsync is not portable to Windows and a four-file
   watch map does not justify a log; file-fsync + same-dir atomic rename
   meets the acceptance criterion.

## Decision

- `harvester.go`: `origin`/`rootCommit` fields via `FingerprintOf` at
  construction; local `normalizeGitURL`; `MatchLevel` +
  `ClassifyWorkspaceMatch` (URL win; root rescues URL disagreement;
  known disagreement rejects; single-sided identity falls back to path);
  `MatchesCandidate` (new) vs `MatchesWorkspace` (unchanged behavior,
  now documented as weakest fallback).
- `watcher.go`: `saveLocked` atomic rewrite only; `NewFileHashStore`
  semantics identical.

## Why (Rationale)

- **Priority mirroring is the correctness fix:** the harvester now speaks
  the same identity language as `ResolveProject`, so attribution and
  registration can no longer disagree about which repo a workspace is.
- **Rejection on known disagreement closes the audit hole:** falling back
  to folder names when both sides *know* they differ was the misattribution;
  single-sided (or no) identity still degrades gracefully to today's
  path check, so non-git workspaces see zero behavior change.
- **Atomic rename bounds crash damage to the old state:** readers only ever
  observe the pre- or post-write file, never a prefix — the mid-burst
  reload test pins exactly this.
- **Evidence:** `go build ./internal/daemon/...` OK; `go vet` clean;
  `go test -count=1 ./internal/daemon/` PASS (3 new + all pre-existing
  tests) — see `docs/issues/ISSUE-109.md`.

## Consequences

- Transcripts become attributable by git identity wherever that identity is
  available; `scanDir` still path-matches until transcript metadata carries
  origin/root (follow-up).
- State-file writes cost one temp create + fsync + rename per mutation —
  negligible at four watched files per workspace.
- Known limitation: `normalizeGitURL` is a second copy of the store helper
  (stdlib-only constraint); the two must stay in sync, noted in code.

## Alternatives Rejected

See Options 2–4 above: store import (layering), generic-name blocklist
(symptom-patching), dir-fsync/WAL (non-portable, disproportionate).
