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
	"os"
	"path/filepath"
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

// containsSecret reports whether data matches any NeverPattern.
// Fail-closed: any match blocks the operation.
func containsSecret(data []byte) bool {
	s := string(data)
	for _, re := range scan.NeverPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// ReadFile returns the file at workspace-relative path after sandbox,
// size-cap, and secret checks.
func ReadFile(root, unsafePath string) ([]byte, error) {
	p, err := SecureJoin(root, unsafePath)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, ErrNotFile
	}
	if st.Size() > MaxFileBytes {
		return nil, ErrTooLarge
	}
	data, err := os.ReadFile(p)
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
// size-cap, and secret checks. Parents are created as needed.
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
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, content, 0o644)
}
