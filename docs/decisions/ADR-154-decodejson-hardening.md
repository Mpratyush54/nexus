# ADR-154 - decodeJSON Hardening: Trailing Data + Body Cap

- **ADR ID:** ADR-154-decodejson-hardening
- **Date:** 2026-09-18
- **Author:** ParthKhandelwal537
- **Issue:** #154 decodeJSON allows trailing garbage, no body size limit
- **Status:** Accepted

## Context

`decodeJSON` claimed to disallow trailing garbage but `Decoder.Decode`
stops after the first value, so `{"a":1}{"b":2}` succeeded. Bodies were
also unbounded (daemon caps at 2MB via `http.MaxBytesReader`).

## Decisions

1. Second `Decode(&struct{}{})` must yield `io.EOF`, else 400 — all
   server body decoding flows through this one function (verified: no
   other `NewDecoder(r.Body)` in the package).
2. `http.MaxBytesReader(w, r.Body, maxBodyBytes)` with 2MB cap mirroring
   the daemon. Oversized reads fail closed with 400, never OOM.
