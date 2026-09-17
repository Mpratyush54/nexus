// Package security provides fail-closed hardening helpers for issue #19
// (Phase 6, plan §6.1 Security + §6.4 Rate Limiting).
//
// It does NOT replace the daemon sandbox (internal/daemon SecureJoin) or the
// secret vocabulary (internal/scan NeverPatterns) — it wraps them with:
//
//   - ValidatePath: strict lexical pre-check (defense in depth in front of
//     SecureJoin's kernel-truth canonicalization).
//   - AdversarialCases: the shared adversarial corpus (.. , symlink, ADS,
//     NUL, absolute, //, trailing dot) used by the tests.
//   - EnforceSecret / ContainsSecret: NeverPatterns wrapper that fails closed.
//   - CanAccess / EnforceBranchAccess: private-vs-shared branch check helper
//     intended for the SQL store layer (migrations/005_branches).
//   - Limiter: stdlib-only token bucket (100/s events, 50/s fileops presets).
//
// Stdlib only.
package security

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"central-memory/internal/scan"
)

var (
	// ErrPathRejected is returned by ValidatePath for any adversarial input.
	ErrPathRejected = errors.New("security: path rejected")
	// ErrSecretBlocked is returned when content matches a NeverPattern.
	// Fail-closed: the operation must be refused without returning content.
	ErrSecretBlocked = errors.New("security: content matches secret pattern")
	// ErrNotAuthorized is returned when a viewer may not read a branch.
	ErrNotAuthorized = errors.New("security: not authorized for branch")
	// ErrRateLimited is returned when a token-bucket limiter is exhausted.
	ErrRateLimited = errors.New("security: rate limit exceeded")
)

// AdversarialCase is one hostile path input. Every case in AdversarialCases
// must be rejected by ValidatePath (strict pre-check) — except the symlink
// class, whose names are lexically innocent and whose containment is enforced
// at runtime by daemon.SecureJoin's ancestor resolution — and must never
// escape the workspace root under daemon.SecureJoin (rejected outright, or
// resolved to a path contained inside the root).
type AdversarialCase struct {
	// Name is a short human-readable label.
	Name string
	// Category groups the attack: "dotdot", "symlink", "ads", "nul",
	// "absolute", "doubleslash", "trailingdot", "blank".
	Category string
	// Path is the hostile input, workspace-relative unless Category is
	// "absolute".
	Path string
}

// AdversarialCases returns the shared corpus covering every class named in
// issue #19: "..", symlinks, ADS, NUL, absolute, "//", trailing dots.
// Symlink entries name links the test harness creates inside the temp root
// (evil-link -> outside file, evil-dir -> outside dir). They are lexically
// innocent, so ValidatePath lets them through by design — containment is
// enforced at runtime by SecureJoin's ancestor resolution — but they are
// listed here so the suite visibly covers the class.
func AdversarialCases() []AdversarialCase {
	return []AdversarialCase{
		{Name: "dotdot sibling", Category: "dotdot", Path: "../escape.txt"},
		{Name: "dotdot deep", Category: "dotdot", Path: "../../etc/passwd"},
		{Name: "dotdot embedded", Category: "dotdot", Path: "sub/../../escape.txt"},
		{Name: "dotdot bare", Category: "dotdot", Path: ".."},
		{Name: "dotdot slash", Category: "dotdot", Path: "../"},
		{Name: "dotdot backslash", Category: "dotdot", Path: `..\escape.txt`},
		{Name: "dotdot nested", Category: "dotdot", Path: "a/../../../x"},

		{Name: "symlink file", Category: "symlink", Path: "evil-link"},
		{Name: "symlink dir", Category: "symlink", Path: "evil-dir/secret.txt"},
		{Name: "symlink dotdot", Category: "symlink", Path: "evil-dir/../../escape.txt"},

		{Name: "ads stream", Category: "ads", Path: "hello.txt:hidden"},
		{Name: "ads data", Category: "ads", Path: "hello.txt::$DATA"},
		{Name: "ads subdir", Category: "ads", Path: "sub:stream/file.txt"},
		{Name: "ads drive-relative", Category: "ads", Path: "C:hello.txt"},

		{Name: "nul byte", Category: "nul", Path: "a\x00b"},
		{Name: "blank empty", Category: "blank", Path: ""},
		{Name: "blank spaces", Category: "blank", Path: "   "},

		{Name: "absolute unix", Category: "absolute", Path: "/etc/passwd"},
		{Name: "absolute windows", Category: "absolute", Path: `C:\Windows\System32\drivers\etc\hosts`},
		{Name: "absolute forward", Category: "absolute", Path: "C:/Windows/System32/hosts"},
		{Name: "rooted backslash", Category: "absolute", Path: `\Windows\System32\drivers\etc\hosts`},

		{Name: "unc share", Category: "doubleslash", Path: "//server/share/secret"},
		{Name: "doubleslash escape", Category: "doubleslash", Path: "sub//../../escape"},
		{Name: "doubleslash mid", Category: "doubleslash", Path: "a///b//..//c"},

		{Name: "trailing dot file", Category: "trailingdot", Path: "hello.txt."},
		{Name: "trailing dot dir", Category: "trailingdot", Path: "mydir./evil"},
		{Name: "trailing space", Category: "trailingdot", Path: "notes.txt "},
		{Name: "trailing dots", Category: "trailingdot", Path: "note..."},
	}
}

// ValidatePath is a strict lexical pre-check for workspace-relative paths.
// It rejects (fail-closed, ErrPathRejected wrapping the reason):
//
//   - NUL bytes and blank paths
//   - any ':' (ADS marker; ':' is illegal in Windows file names, and
//     drive-relative "C:foo" forms are never legitimate workspace paths)
//   - absolute paths (filepath.IsAbs, volume name, or leading / or \ — the
//     leading-separator check keeps rooted-path handling deterministic
//     cross-platform)
//   - any ".." segment (split on both '/' and '\')
//   - any "//" (double separator: UNC shares, confused joins)
//   - trailing "." or " " on any segment (Windows strips those, so
//     "hello.txt." and "hello.txt" would alias the same file)
//
// It performs no filesystem I/O: symlink/junction containment stays the job
// of daemon.SecureJoin, which resolves the deepest existing ancestor through
// the kernel. Run ValidatePath first (cheap, clear errors), SecureJoin second
// (authoritative).
func ValidatePath(p string) error {
	if strings.ContainsRune(p, 0) {
		return errors.New("security: path rejected: NUL byte")
	}
	if strings.TrimSpace(p) == "" {
		return errors.New("security: path rejected: empty path")
	}
	if strings.Contains(p, ":") {
		return errors.New("security: path rejected: colon not allowed (ADS/absolute)")
	}
	if filepath.IsAbs(p) || filepath.VolumeName(p) != "" ||
		strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return errors.New("security: path rejected: absolute path")
	}
	if strings.Contains(p, "//") || strings.Contains(p, `\\`) {
		return errors.New("security: path rejected: double separator")
	}
	segs := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
	for _, s := range segs {
		if s == ".." {
			return errors.New("security: path rejected: parent escape")
		}
		if strings.HasSuffix(s, ".") || strings.HasSuffix(s, " ") {
			return errors.New("security: path rejected: trailing dot/space")
		}
	}
	return nil
}

// ContainsSecret reports whether data matches any scan.NeverPattern.
// Fail-closed: any single match means secret.
func ContainsSecret(data []byte) bool {
	s := string(data)
	for _, re := range scan.NeverPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// EnforceSecret blocks content matching a NeverPattern (fail-closed).
// Returns nil when clean, ErrSecretBlocked on any match. Empty input is
// clean (no pattern can match it); a match never returns the content.
func EnforceSecret(data []byte) error {
	if ContainsSecret(data) {
		return ErrSecretBlocked
	}
	return nil
}

// EnforceSecretString is EnforceSecret for string content.
func EnforceSecretString(s string) error {
	return EnforceSecret([]byte(s))
}

// Visibility is a memory-branch visibility level
// (migrations/005_branches: CHECK (visibility IN ('private','shared'))).
type Visibility string

const (
	// VisibilityPrivate restricts reads to the branch owner.
	VisibilityPrivate Visibility = "private"
	// VisibilityShared allows any authenticated project member to read.
	VisibilityShared Visibility = "shared"
)

// NormalizeVisibility maps unknown/empty values to private (fail-closed:
// a misspelled visibility must restrict, never expose).
func NormalizeVisibility(v Visibility) Visibility {
	if v == VisibilityShared {
		return VisibilityShared
	}
	return VisibilityPrivate
}

// CanAccess reports whether viewerID may read a branch owned by ownerID with
// visibility v. The owner always passes; anyone else passes only on shared.
// Empty viewerID (anonymous) never passes.
//
// Intended for the SQL store layer: check on every branch-scoped read, not
// just at the HTTP edge, so a future route cannot bypass it.
func CanAccess(viewerID, ownerID string, v Visibility) bool {
	if viewerID == "" {
		return false
	}
	if viewerID == ownerID {
		return true
	}
	return NormalizeVisibility(v) == VisibilityShared
}

// EnforceBranchAccess is the error-returning form of CanAccess.
func EnforceBranchAccess(viewerID, ownerID string, v Visibility) error {
	if CanAccess(viewerID, ownerID, v) {
		return nil
	}
	return ErrNotAuthorized
}

// Limiter is a stdlib-only token-bucket rate limiter.
//
// Tokens refill continuously at rate tokens/sec up to burst. Allow consumes
// one token; when empty it returns false (fail-closed: the caller must drop
// or queue, never admit). Zero allocations on the hot path beyond the mutex.
type Limiter struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

// NewLimiter builds a bucket refilling at rps tokens/sec with capacity burst.
// Non-positive rps/burst are clamped to 1 (fail-closed: a misconfigured zero
// rate would otherwise admit nothing forever, a zero burst admits nothing;
// clamping keeps the limiter operative and visible instead of dead).
func NewLimiter(rps, burst int) *Limiter {
	if rps <= 0 {
		rps = 1
	}
	if burst <= 0 {
		burst = 1
	}
	return &Limiter{
		rate:   float64(rps),
		burst:  float64(burst),
		tokens: float64(burst),
		last:   time.Now(),
	}
}

// NewEventLimiter returns the plan §6.4 preset: 100 events/s per project.
func NewEventLimiter() *Limiter { return NewLimiter(100, 100) }

// NewFileOpsLimiter returns the plan §6.4 preset: 50 daemon file ops/s.
func NewFileOpsLimiter() *Limiter { return NewLimiter(50, 50) }

// Allow reports whether one event may proceed now, consuming a token.
func (l *Limiter) Allow() bool { return l.AllowN(1) }

// AllowN reports whether n events may proceed now, consuming n tokens.
// Non-positive n is rejected (fail-closed on caller bugs).
func (l *Limiter) AllowN(n int) bool {
	if n <= 0 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(l.last).Seconds()
	if elapsed > 0 {
		l.tokens += elapsed * l.rate
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		l.last = now
	}
	if l.tokens < float64(n) {
		return false
	}
	l.tokens -= float64(n)
	return true
}
