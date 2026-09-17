# ADR-108-adapter-export-errors

- **ADR ID:** ADR-108-adapter-export-errors
- **Date:** 2026-09-17
- **Issue:** #108 — Adapter export swallows errors and destination naming collisions
- **Status:** Accepted

## Context

`CopyFiltered` reported success when file copies failed, and `Export` wrote `index.json` regardless — downstream `Restore` then trusted an index whose raw files were missing. Separately, `safeName` ignored its root-index parameter, so same-named roots from different checkouts overwrote each other.

## Decision

1. **Fail the export on any copy/walk failure.** `CopyFiltered` aborts on the first walk/relativize/copy error (wrapped with the path) and `Export` returns it before writing `index.json`. No aggregation: a partial vault must never look clean, and resume-on-retry already handles the interrupted-harvest case.
2. **Skip missing roots, fail on real errors.** Optional agent dirs that were never created are skipped via an `os.Stat` pre-check; this preserves the old lenient behavior for the common case while genuine mid-walk failures propagate.
3. **Index-prefixed destination names** (`"<i>_<base>"`), keeping existing sanitization. The index is stable per export (root order from `roots()`), so resume still hits the same paths.

## Alternatives Considered

- **Collect-and-continue with aggregated error return.** More complete per-run, but risks huge partial vaults and complicates the resume invariant; rejected in favor of abort-on-first-error plus idempotent retry.
- **Content-hash suffixes instead of indices.** Stronger identity but churns paths and duplicates the `Was`/fingerprint identity work; index prefix is sufficient since roots are enumerated deterministically.

## Consequences

- `Export` callers can trust a nil error + `index.json` as "complete"; failure leaves no fresh index behind.
- `Discover` intentionally left lenient (follow-up if strict discovery is wanted).
