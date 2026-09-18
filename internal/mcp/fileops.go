// Package mcp — fileops.go: hardened workspace file access (nexus issue #96).
//
// handleFileRead/handleFileWrite previously resolved paths with a local
// lexical-only helper, bypassing the daemon's SecureJoin hardening and the
// secret vocabulary (internal/scan.NeverPatterns). This file replicates the
// daemon fileops enforcement inside the MCP layer (the issue's sanctioned
// alternative to proxying through daemon fileops):
//
//   - secureJoin: blank/NUL/ADS-colon rejection, Abs+Clean+Rel containment,
//     and symlink-escape detection by resolving the deepest existing ancestor
//     with filepath.EvalSymlinks and re-checking containment.
//   - Secret screening: file names AND contents (both directions) match
//     against scan.NeverPatterns plus the daemon's extra corpus; any hit
//     refuses the operation before bytes cross the boundary.
//   - Audit logging: successful reads/writes invoke Config.FileAccessLog
//     (op, workspace-relative path, byte count) so tool file access is
//     observable like the daemon interceptor events.
//
// Delta vs daemon.SecureJoin (documented, not drift): root canonicalization
// uses stdlib EvalSymlinks where the daemon on Windows goes through
// GetFinalPathNameByHandle (junction traversal). Behavior on lexical
// escapes, ADS, NUL, and symlink-out-of-root is identical.
package mcp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"central-memory/internal/scan"
)

// ErrTraversal is returned when a tool path escapes the workspace root.
var ErrTraversal = errors.New("mcp: path escapes workspace root")

// ErrSecretBlocked is returned when a file name or content matches the
// secret vocabulary. Fail-closed: nothing is read or written.
var ErrSecretBlocked = errors.New("mcp: content matches secret pattern")

// extraSecretPatterns mirrors the daemon hardening corpus
// (internal/daemon/fileops.go): tokens the shared scan vocabulary does not
// cover yet. Any match blocks the operation.
var extraSecretPatterns = func() []*regexp.Regexp {
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
}()

// containsSecret reports whether a file name or its bytes match the secret
// vocabulary (shared NeverPatterns + daemon extra corpus).
func containsSecret(name string, data []byte) bool {
	s := name + "\n" + string(data)
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

// foldPath normalizes a path for containment comparison: Windows compares
// case-insensitively, elsewhere exactly (mirrors daemon foldPath).
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
	if !(c == r || strings.HasPrefix(c, r+string(filepath.Separator))) {
		return fmt.Errorf("%w: %q", ErrTraversal, rel)
	}
	return nil
}

// secureJoin resolves a workspace-relative (or absolute) tool path against
// root and returns the absolute target, rejecting traversal, ADS streams,
// NUL bytes, and symlink escapes (same contract as daemon.SecureJoin).
func secureJoin(root, unsafePath string) (string, error) {
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
	rootReal := rootAbs
	if resolved, evalErr := filepath.EvalSymlinks(rootAbs); evalErr == nil {
		rootReal = filepath.Clean(resolved)
	}

	var candidate string
	if filepath.IsAbs(unsafePath) {
		candidate = filepath.Clean(unsafePath)
	} else {
		// Relative paths must not contain ':' at all (ADS marker;
		// illegal in Windows file names; also rejects C:foo oddities).
		if strings.Contains(unsafePath, ":") {
			return "", fmt.Errorf("%w: colon not allowed (ADS/absolute)", ErrTraversal)
		}
		candidate = filepath.Join(rootReal, unsafePath)
	}

	// Canonicalize via the deepest existing ancestor so symlink/junction
	// escapes are exposed before the containment check.
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
	if resolved, evalErr := filepath.EvalSymlinks(anc); evalErr == nil {
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

	// ADS check on the volume-stripped canonical path (covers absolute ADS
	// forms like C:\root\file.txt:hidden).
	rest := strings.TrimPrefix(canonical, filepath.VolumeName(canonical))
	if strings.Contains(rest, ":") {
		return "", fmt.Errorf("%w: colon not allowed (ADS/absolute)", ErrTraversal)
	}

	if err := checkContained(rootReal, canonical); err != nil {
		if checkContained(rootReal, candidate) == nil {
			return "", fmt.Errorf("%w: symlink escape", ErrTraversal)
		}
		return "", err
	}
	return canonical, nil
}

// logFileAccess invokes the configured audit hook (best-effort, never fails
// the operation). op is "read" or "write".
func (s *Server) logFileAccess(op, relPath string, size int) {
	if s == nil || s.cfg.FileAccessLog == nil || relPath == "" {
		return
	}
	func() {
		defer func() { _ = recover() }()
		s.cfg.FileAccessLog(op, relPath, size)
	}()
}
