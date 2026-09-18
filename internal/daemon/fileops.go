// Sandboxed file operations for the workspace daemon.
//
// Every path goes through SecureJoin: lexical cleaning, containment inside
// the workspace root, ADS/colon rejection, and symlink-escape detection.
// Reads/writes additionally fail closed on secret-pattern matches
// (internal/scan.NeverPatterns) and enforce a 1MB size cap.
package daemon

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"central-memory/internal/scan"
)

// MaxFileBytes caps single file reads and writes (1MB, per Phase 1.3).
const MaxFileBytes = 1 << 20

var (
	// ErrTraversal is returned when a path escapes the workspace sandbox.
	ErrTraversal = errors.New("daemon: path escapes workspace sandbox")
	// ErrSecretBlocked is returned when content matches a NeverPattern
	// (fail-closed: the operation is refused without returning content).
	ErrSecretBlocked = errors.New("daemon: content matches secret pattern")
	// ErrTooLarge is returned when content exceeds MaxFileBytes.
	ErrTooLarge = errors.New("daemon: file exceeds 1MB cap")
	// ErrNotFile is returned when the target is not a regular file.
	ErrNotFile = errors.New("daemon: not a regular file")
)

// SecureJoin resolves a workspace-relative (or absolute) unsafePath against
// root and returns the absolute target. It rejects:
//
//   - NUL bytes and blank paths
//   - Windows ADS streams ("notes.txt:hidden"): any ':' in the path
//     *after* stripping the volume name is rejected. (Legitimate relative
//     paths never contain ':' — it is illegal in Windows file names — while
//     absolute paths keep their "C:" volume prefix.)
//   - absolute paths outside root and ".." escapes (filepath.Clean +
//     filepath.Rel containment, belt-and-braces HasPrefix check)
//   - symlink escapes: the deepest existing ancestor is resolved with
//     resolveExisting and re-checked for containment. The root itself is
//     also canonicalized first, so Windows 8.3 short names (PRATYU~1) and
//     junctions never cause false positives. (On Windows, resolution goes
//     through GetFinalPathNameByHandle because EvalSymlinks does not
//     follow junctions.)
func SecureJoin(root, unsafePath string) (string, error) {
	if strings.ContainsRune(unsafePath, 0) {
		return "", fmt.Errorf("%w: NUL byte", ErrTraversal)
	}
	if strings.TrimSpace(unsafePath) == "" {
		return "", fmt.Errorf("%w: empty path", ErrTraversal)
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootAbs = filepath.Clean(rootAbs)
	// Canonicalize the root (resolves 8.3 short names, junctions, and
	// case variants) so later comparisons are canonical-vs-canonical.
	rootReal := rootAbs
	if resolved, evalErr := resolveExisting(rootAbs); evalErr == nil {
		rootReal = filepath.Clean(resolved)
	}

	var candidate string
	if filepath.IsAbs(unsafePath) {
		candidate = filepath.Clean(unsafePath)
	} else {
		// Relative paths must not contain ':' at all: it is illegal in
		// Windows file names and is the ADS marker ("notes.txt:hidden").
		// Drive-relative oddities ("C:foo") are rejected here too.
		if strings.Contains(unsafePath, ":") {
			return "", fmt.Errorf("%w: colon not allowed (ADS/absolute)", ErrTraversal)
		}
		candidate = filepath.Join(rootReal, unsafePath)
	}

	// Canonicalize the candidate via its deepest existing ancestor so
	// comparisons below are canonical-vs-canonical (kills 8.3 short-name
	// and case false positives, and exposes symlink/junction escapes).
	// The filesystem root always exists, so the climb always terminates
	// with a resolvable ancestor.
	anc := candidate
	for {
		if _, statErr := os.Lstat(anc); statErr == nil {
			break
		}
		parent := filepath.Dir(anc)
		if parent == anc {
			break
		}
		anc = parent
	}
	canonical := candidate
	if resolved, evalErr := resolveExisting(anc); evalErr == nil {
		rem, relErr := filepath.Rel(anc, candidate)
		if relErr != nil {
			return "", fmt.Errorf("%w: %v", ErrTraversal, relErr)
		}
		canonical = filepath.Clean(resolved)
		if rem != "." {
			canonical = filepath.Join(canonical, rem)
		}
		canonical = filepath.Clean(canonical)
	}

	// ADS / alternate-stream check on the volume-stripped canonical path.
	// (Covers absolute ADS forms like `C:\root\file.txt:hidden`.)
	rest := strings.TrimPrefix(canonical, filepath.VolumeName(canonical))
	if strings.Contains(rest, ":") {
		return "", fmt.Errorf("%w: colon not allowed (ADS/absolute)", ErrTraversal)
	}

	if err := checkContained(rootReal, canonical); err != nil {
		// Distinguish symlink escapes for clearer errors: if the lexical
		// candidate was inside but the canonical one is not, a link
		// redirected it.
		if checkContained(rootReal, candidate) == nil {
			return "", fmt.Errorf("%w: symlink escape", ErrTraversal)
		}
		return "", err
	}

	return canonical, nil
}

// foldPath normalizes a path for containment comparison: on Windows the
// filesystem is case-insensitive, so compare lowercased; elsewhere exact.
func foldPath(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

// checkContained reports ErrTraversal unless candidate == root or lies
// strictly inside root.
func checkContained(rootAbs, candidate string) error {
	r, c := foldPath(rootAbs), foldPath(candidate)
	rel, err := filepath.Rel(r, c)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTraversal, err)
	}
	if rel == "." {
		return nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %q", ErrTraversal, rel)
	}
	// Belt-and-braces prefix check required by the implementation plan.
	if !(c == r || strings.HasPrefix(c, r+string(filepath.Separator))) {
		return fmt.Errorf("%w: %q", ErrTraversal, rel)
	}
	return nil
}

// extraSecretPatterns supplements internal/scan NeverPatterns with the
// daemon hardening corpus (issue #118): sk-/sk-proj-, slack xox tokens,
// PEM private keys, github_pat_, Bearer/JWT entropy. Defined here (not in
// internal/scan, owned by another agent) so daemon enforcement stays
// fail-closed even before the shared vocabulary expands.
var extraSecretPatterns = initExtraSecretPatterns()

func initExtraSecretPatterns() []*regexp.Regexp {
	patterns := []string{
		`sk-[A-Za-z0-9]{20,}`,
		`sk-proj-[A-Za-z0-9_\-]{20,}`,
		`xox[baprs]-[A-Za-z0-9\-]{8,}`,
		`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`,
		`github_pat_[A-Za-z0-9_]{20,}`,
		`(?i)bearer\s+[A-Za-z0-9_\-\.~\+/]{20,}={0,2}`,
		`eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`,
		`(?i)aws_secret_access_key\s*[:=]\s*['"]?[A-Za-z0-9/\+]{30,}['"]?`,
		`(?i)client_secret\s*[:=]\s*['"]?[A-Za-z0-9_\-]{16,}['"]?`,
	}
	var out []*regexp.Regexp
	for _, p := range patterns {
		if re, err := regexp.Compile(p); err == nil {
			out = append(out, re)
		}
	}
	return out
}

// containsSecret reports whether data matches any NeverPattern or the
// daemon-local extra corpus. Fail-closed: any match blocks the operation.
func containsSecret(data []byte) bool {
	s := string(data)
	for _, re := range scan.NeverPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	for _, re := range extraSecretPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// openNoFollow opens path without following a trailing symlink where the
// platform supports it (Unix O_NOFOLLOW); elsewhere it falls back to a
// plain open — post-open verifyOpenedFile still rejects symlink swaps.
func openNoFollow(path string) (*os.File, error) {
	return openNoFollowPlatform(path)
}

// isSymlinkError reports whether err looks like an O_NOFOLLOW symlink refusal.
func isSymlinkError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "symlink") || strings.Contains(s, "too many links") ||
		strings.Contains(s, "eloop") || strings.Contains(s, "not a directory")
}

// readAllCapped reads up to limit+1 bytes so callers can detect overflow.
func readAllCapped(f *os.File, limit int64) ([]byte, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	// limit is MaxFileBytes+1 (≤2MB+1): a single bounded allocation.
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 32<<10)
	var total int64
	for {
		n, err := f.Read(tmp)
		if n > 0 {
			total += int64(n)
			if total > limit {
				// Drain-free overflow signal: return what we have plus one
				// extra byte so the caller sees len > MaxFileBytes.
				buf = append(buf, tmp[:n]...)
				return buf, nil
			}
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err == io.EOF {
				return buf, nil
			}
			return nil, err
		}
	}
}

// verifyOpenedFile performs post-open verification (issue #92): the opened
// descriptor and a fresh Lstat of the path must agree (no swap between
// check and use), the final component must not be a symlink, and the
// canonical path must still be contained in the root.
func verifyOpenedFile(root, canonical string, f *os.File) error {
	fst, err := f.Stat()
	if err != nil {
		return err
	}
	if !fst.Mode().IsRegular() {
		return ErrNotFile
	}
	lst, err := os.Lstat(canonical)
	if err != nil {
		return err
	}
	if lst.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: symlink target swapped post-check", ErrTraversal)
	}
	if !os.SameFile(fst, lst) {
		return fmt.Errorf("%w: file swapped post-check (TOCTOU)", ErrTraversal)
	}
	// Re-resolve containment post-open: a parent swapped to a symlink
	// after SecureJoin must still be caught.
	rootReal := canonicalRoot(root)
	if resolved, err := resolveExisting(canonical); err == nil {
		canonical = filepath.Clean(resolved)
	} else if resolved, err := resolveExisting(filepath.Dir(canonical)); err == nil {
		canonical = filepath.Join(filepath.Clean(resolved), filepath.Base(canonical))
	}
	if err := checkContained(rootReal, canonical); err != nil {
		return err
	}
	return nil
}

// canonicalRoot returns the canonicalized root for containment checks.
func canonicalRoot(root string) string {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return filepath.Clean(root)
	}
	rootAbs = filepath.Clean(rootAbs)
	if resolved, err := resolveExisting(rootAbs); err == nil {
		return filepath.Clean(resolved)
	}
	return rootAbs
}

// ReadFile returns the file at workspace-relative path after sandbox,
// size-cap, and secret checks. It opens the file first and verifies the
// descriptor post-open (issue #92: O_NOFOLLOW-style + SameFile check),
// extending TOCTOU protection to reads.
func ReadFile(root, unsafePath string) ([]byte, error) {
	p, err := SecureJoin(root, unsafePath)
	if err != nil {
		return nil, err
	}
	f, err := openNoFollow(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, err
		}
		// Symlink final component (O_NOFOLLOW ELOOP) surfaces as traversal.
		if isSymlinkError(err) {
			return nil, fmt.Errorf("%w: symlink target", ErrTraversal)
		}
		// Fall back to a plain open so non-symlink errors keep prior shape.
		f, err = os.Open(p)
		if err != nil {
			return nil, err
		}
	}
	defer f.Close()
	if err := verifyOpenedFile(root, p, f); err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > MaxFileBytes {
		return nil, ErrTooLarge
	}
	data, err := readAllCapped(f, MaxFileBytes+1)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileBytes {
		return nil, ErrTooLarge
	}
	if containsSecret(data) {
		return nil, ErrSecretBlocked
	}
	return data, nil
}

// WriteFile writes content to the workspace-relative path after sandbox,
// size-cap, and secret checks. Parents are created as needed. Writes are
// atomic (temp file in the target dir + fsync + rename, mode 0600 per
// issue #118) and re-verify containment post-open (issue #92).
func WriteFile(root, unsafePath string, content []byte) error {
	if len(content) > MaxFileBytes {
		return ErrTooLarge
	}
	if containsSecret(content) {
		return ErrSecretBlocked
	}
	p, err := SecureJoin(root, unsafePath)
	if err != nil {
		return err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if filepath.Clean(p) == filepath.Clean(rootAbs) {
		return ErrNotFile
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	// Re-verify containment after MkdirAll: a parent may have been swapped
	// for a symlink between SecureJoin and the mkdir.
	if p2, err := SecureJoin(root, unsafePath); err != nil {
		return err
	} else if filepath.Clean(p2) != filepath.Clean(p) {
		// Canonical path moved under us; re-resolve to the fresh value.
		p = p2
	}
	dir := filepath.Dir(p)
	tmp, err := os.CreateTemp(dir, ".tmp-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup on failure; success path renames away.
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	// Post-open verification of the temp file (regular, same file).
	tf, err := os.Open(tmpName)
	if err != nil {
		return err
	}
	fst, err := tf.Stat()
	tf.Close()
	if err != nil {
		return err
	}
	if !fst.Mode().IsRegular() {
		return ErrNotFile
	}
	// Final containment re-check before the atomic rename.
	if p3, err := SecureJoin(root, unsafePath); err != nil {
		return err
	} else {
		p = p3
	}
	if err := os.Rename(tmpName, p); err != nil {
		return err
	}
	_ = os.Chmod(p, 0o600)
	// Sync the directory so the rename is durable (best-effort).
	if df, err := os.Open(dir); err == nil {
		_ = df.Sync()
		_ = df.Close()
	}
	return nil
}
