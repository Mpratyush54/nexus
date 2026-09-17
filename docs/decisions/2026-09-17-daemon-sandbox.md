# Daemon File Sandbox Design (2026-09-17)

Issue: Mpratyush54/nexus#3 — `[Phase 1] Workspace Daemon Core & File Sandbox`.
Scope: `internal/daemon/` core (`daemon.go`, `fileops.go`, `gitops.go`,
`commands.go`, `auth.go`, plus OS-specific `fileops_windows.go` /
`fileops_unix.go`).

## 1. Sandbox design (`SecureJoin`)

**Why lexical + kernel-truth, not just `filepath.Clean` + `HasPrefix`:**
`Clean` + `HasPrefix` (what the plan table names) stops `..` escapes but is
blind to three real attacks, all covered by tests:

- **Absolute paths** (`C:\Windows\...`, `/etc/passwd`): `filepath.Join`
  never discards its first argument, so a rooted-but-drivoless path like
  `\Windows\...` safely joins *inside* the root, while a drive-absolute
  path is caught by a `filepath.Rel`-based containment check
  (belt-and-braces `HasPrefix` retained per plan).
- **ADS streams** (`notes.txt:hidden`): `:` is illegal in Windows file
  names, so any `:` in a *relative* path is rejected outright; absolute
  candidates are re-checked after stripping the volume name (`C:`), which
  keeps `C:\root\file` legal while killing `C:\root\file.txt:hidden`.
- **Symlink-ish escapes** (`evil-link -> C:\outside`, `evil-dir/ -> ...`):
  the deepest existing ancestor is resolved and re-checked. Fail-closed:
  anything unresolvable-but-suspicious is rejected, never allowed.

**Why `GetFinalPathNameByHandleW` on Windows (`fileops_windows.go`):**
`filepath.EvalSymlinks` does **not** follow Windows junctions, and junctions
carry no symlink bit — they are invisible to `Lstat`-based detection too
(both verified empirically during development). A `<root>/evil-dir ->
C:\outside` junction would otherwise sail through. So on Windows,
canonicalization goes through the kernel (`GetFinalPathNameByHandleW` via
stdlib `syscall`, lazy-loaded `kernel32.dll`); unix keeps `EvalSymlinks`.
The junction-escape test passes on a locked-down box with zero symlink
privilege (junctions need none; created via `mklink /J` fallback in tests).

**Why canonicalize the root itself:** Windows temp paths use 8.3 short
names (`PRATYU~1`); comparing a short root against a kernel-resolved long
candidate false-positives. Both sides are canonicalized before comparison,
and comparison is case-folded on Windows only (case-sensitive elsewhere).

## 2. Secret fail-closed (`scan.NeverPatterns`)

**Why reuse instead of inventing patterns:** `internal/scan.NeverPatterns`
is the project's already-reviewed secret vocabulary (tokens, keys, API-key
assignments). Reads of files containing secrets are refused (no leak
through the daemon), and writes containing secrets are refused *before*
touching disk (no secret persistence). Any single pattern match blocks —
fail-closed, never fail-open. 1MB cap bounds both directions.

## 3. Command allowlist + 60s timeout (`commands.go`)

**Why a positive allowlist, not a denylist:** agents are arbitrary-input
attackers; deny-listing `rm`/`curl`/PowerShell is unwinnable. v1 allows
exactly `git [...]`, `go test [...]`, `npm test [...]`, `pytest [...]`
(`python -m pytest` included), `cargo test [...]`. Binary names must be
bare (no `/` or `\`), so callers cannot smuggle in `C:\tools\evil.exe` or
`./evil`. Execution never uses a shell (`os/exec` argv-direct), so
metacharacter injection is structurally impossible. Every run gets a 60s
`context` deadline and 32KB output cap (the interceptor's 4KB event cap
lives in its own layer).

## 4. Token file (`.central-memory/daemon.token`, mode 0600)

**Why a file, why 0600:** the daemon is local-first with no user accounts
yet (central JWT auth arrives in Phase 1.8 server work), so a 32-byte
`crypto/rand` bearer token sidecars trust between the user's own processes.
0600 keeps it out of other local users' hands on shared machines. On
Windows only the read-only bit is enforceable, so mode is best-effort
there (test asserts strictly on unix, documents the gap on Windows).

## 5. Stdlib-only, reuse of `project.Fingerprint`

**Why no new `go.mod` deps:** the daemon must install anywhere with zero
supply-chain surface; `net/http`, `os/exec`, `path/filepath`, `syscall`
cover everything Phase 1.3 needs (`fsnotify` belongs to the watcher
agent's file, not this one). Project identity reuses
`internal/project.Fingerprint` (origin URL + root commit) so daemon
registration can never drift from the CLI's identity model.
`internal/store` / `server` / `mcp` / `context` untouched per issue scope.

## Verification

- `go vet` + `go test ./internal/daemon/ -v`: 17/17 pass (traversal,
  absolute, ADS, NUL, junction-escape, secret fail-closed, 1MB cap,
  401/403/400 handler codes, allowlist table, token round-trip).
- Cross-compile: `GOOS=linux` / `GOOS=darwin` builds pass (OS split
  confined to `fileops_windows.go` / `fileops_unix.go`).

Note (2026-09-17): parallel agents own `interceptor.go` / `harvester.go` /
`watcher.go` in the same package; at close of this change those files had
a transient `Event`-type coordination conflict unrelated to this scope.
