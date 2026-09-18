# ISSUE-154 — decodeJSON Trailing Garbage + Body Size Limit

- **Status:** Done
- **Scope:** `internal/server/server.go` (`decodeJSON` + `maxBodyBytes`),
  `internal/server/server_test.go`, `docs/decisions/ADR-154-decodejson-hardening.md` ONLY.

## What was built

- Second-decode EOF check rejects `{"a":1}{"b":2}`-style smuggling with 400.
- 2MB `MaxBytesReader` cap (daemon parity) rejects unbounded streams with 400.

## Verification

- New `TestDecodeJSONRejectsTrailingGarbage` + `TestDecodeJSONRejectsOversizedBody`;
  `go vet` clean; server suite green.
