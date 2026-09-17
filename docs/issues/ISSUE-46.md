# ISSUE-46 — CI: -race, gofmt Gate, Live-Postgres Suite

- **Status:** Done (workflow + docs; first green Actions run pending)
- **Scope:** `.github/workflows/ci.yml` (new), `docs/decisions/
  ADR-046-ci-race-postgres.md` (new), `docs/issues/ISSUE-46.md` (this
  file) ONLY. `go.mod`/`go.sum`, `internal/store`, `migrations/`, all
  other code untouched.
- **Plan ref:** follow-up to `docs/issues/ISSUE-2.md` verification
  (`-race` unrunnable, gated tests SKIP) and #49 (gofmt regressions)

## What was built

- `.github/workflows/ci.yml` — `on: push, pull_request`, Go `1.26.x`
  (`actions/setup-go@v5`, matching `go.mod`'s `go 1.26.1` floor):
  - `build` (matrix `ubuntu-latest`/`windows-latest`): `go build ./...`,
    `go vet ./...`, `gofmt -l` fail-on-output under `shell: bash`;
    Windows installs mingw-w64 (`choco install mingw -y`) for 64-bit
    `-race`-capable gcc.
  - `test-race` (`ubuntu-latest`, `needs: build`):
    `go test -race -count=1 ./...`.
  - `postgres` (`ubuntu-latest`, `needs: build`): service
    `pgvector/pgvector:pg16`, ports `5432:5432`, `POSTGRES_USER/PASSWORD/
    DB` = `postgres/postgres/nexus_test`, `pg_isready` health check;
    `TEST_POSTGRES_DSN=postgres://postgres:postgres@localhost:5432/
    nexus_test?sslmode=disable`, then `go test -count=1 ./...` — the
    `TEST_POSTGRES_DSN`-gated tests apply `migrations/*.up.sql` via
    `RunMigrations`, covering migrations-apply with no extra harness.
- `docs/decisions/ADR-046-ci-race-postgres.md` — full ADR (context,
  options, decision, rationale, consequences).
- `docs/issues/ISSUE-46.md` — this file.

## Verification

- YAML self-review (Actions cannot run in this environment — no Go
  toolchain installed, MinGW.org GCC 6.3.0 is 32-bit-only so `-race`
  cannot build here, no Docker/Postgres for the service job):
  - [x] service image is `pgvector/pgvector:pg16` (vector extension
    present; matches PG16 prod target)
  - [x] ports `5432:5432` expose the service to the job
  - [x] service env sets `POSTGRES_USER`, `POSTGRES_PASSWORD`,
    `POSTGRES_DB=nexus_test`
  - [x] health check `pg_isready -U postgres` (interval 10s, timeout 5s,
    retries 5) gates test start on DB readiness
  - [x] job env `TEST_POSTGRES_DSN` points at `localhost:5432/nexus_test`
    with `sslmode=disable` and matches the service credentials/DB name
  - [x] job deps: `test-race` and `postgres` both `needs: build`
    (static gate first, slow suites in parallel after)
  - [x] Go `1.26.x` satisfies the `go.mod` (`go 1.26.1`) floor in all
    three jobs
  - [x] gofmt gate fails on any `gofmt -l` output; `shell: bash` keeps
    the check identical on Windows
  - [x] Windows leg installs mingw-w64 (64-bit gcc for `-race` cgo)
  - [x] only 3 new files; `git status` shows no other modifications
- First green run on push/PR (integrator-owned) is the closing proof:
  `build` green on both OSes, `test-race` PASS, `postgres` PASS with the
  previously-SKIP gated tests executing.

## Follow-ups

- Integrator: push branch and confirm the first green Actions run; paste
  run URL into this issue.
- Consider `go test -race` inside the `postgres` job once timing is
  known (race + live DB is the strongest signal).
- Consider GOMODCACHE caching if install time dominates the suite.
- Local dev note: `go test -race` and the gated DB tests remain
  unrunnable on 32-bit-gcc / Postgres-less machines — CI is the source
  of truth for both.
