// File sandbox for the workspace daemon (issue #3).
//
// Every read/write resolves the caller-supplied path with filepath.Clean and
// requires the result to stay under the workspace root (strings.HasPrefix on
// the root + separator). Traversal escapes (.., absolute paths outside the
// root) are rejected with 403. Content and paths matching
// internal/scan NeverPatterns (secret regexes) are also rejected with 403,
// fail-closed. Reads are capped at 1MB (413 over the cap).
package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"central-memory/internal/scan"
)

// MaxReadBytes caps POST /file/read responses at 1MB per the plan (§1.3).
const MaxReadBytes = 1 << 20

// ErrTraversal is returned when a path escapes the workspace sandbox.
var ErrTraversal = errors.New("path escapes workspace")

// ErrSecretHit is returned when a path or content matches a NeverPattern.
var ErrSecretHit = errors.New("secret pattern match")

// ResolveInSandbox maps a caller-supplied path to an absolute path confined
// under root. It applies filepath.Clean and a HasPrefix check against
// root + separator so that:
//
//   - "a/b.txt"            → <root>/a/b.txt            (allowed)
//   - "../../etc/passwd"   → escapes                   (ErrTraversal)
//   - "/etc/passwd"        → absolute escape           (ErrTraversal)
//   - "C:\Windows\..."     → absolute escape           (ErrTraversal)
//   - root itself          → allowed (dir listing callers handle EISDIR)
//
// The check is lexical (Clean + HasPrefix), exactly as the plan prescribes.
// Symlink escapes (a link inside root pointing outside) are NOT resolved
// here — see ADR-003 for why that is deferred to issue #19 hardening.
func ResolveInSandbox(root, userPath string) (string, error) {
	clean := filepath.Clean(userPath)
	// Cross-platform absolute-escape guard: on Windows a path like
	// "/etc/passwd" cleans to `\etc\passwd`, which IsAbs reports as false
	// (rooted, no volume) yet refers to the current drive's root — not the
	// workspace. Join would silently confine it inside root, but the intent
	// is unambiguously "outside the workspace", so reject rooted paths that
	// are not absolute outright. (On Unix this branch is dead code because
	// IsAbs already covers separator-rooted paths.)
	if !filepath.IsAbs(clean) && strings.HasPrefix(clean, string(filepath.Separator)) {
		return "", ErrTraversal
	}
	var abs string
	if filepath.IsAbs(clean) {
		abs = clean
	} else {
		abs = filepath.Join(root, clean)
	}
	abs = filepath.Clean(abs)

	if abs == root {
		return abs, nil
	}
	prefix := root + string(filepath.Separator)
	if !strings.HasPrefix(abs, prefix) {
		return "", ErrTraversal
	}
	return abs, nil
}

// ContainsSecret reports whether s matches any internal/scan NeverPattern
// (tokens, keys, env values). Used fail-closed on both paths and content.
func ContainsSecret(s string) bool {
	for _, re := range scan.NeverPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// ReadFileSandboxed reads userPath confined to root, enforcing the sandbox,
// the secret check, and the 1MB cap. Over-cap files yield an error that the
// handler maps to 413.
func ReadFileSandboxed(root, userPath string) ([]byte, error) {
	abs, err := ResolveInSandbox(root, userPath)
	if err != nil {
		return nil, err
	}
	if ContainsSecret(abs) {
		return nil, ErrSecretHit
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil {
		if st.IsDir() {
			return nil, errors.New("is a directory")
		}
		if st.Size() > MaxReadBytes {
			return nil, errors.New("file exceeds 1MB read cap")
		}
	}
	// LimitReader guards against size races between Stat and Read.
	data, err := io.ReadAll(io.LimitReader(f, MaxReadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxReadBytes {
		return nil, errors.New("file exceeds 1MB read cap")
	}
	if ContainsSecret(string(data)) {
		return nil, ErrSecretHit
	}
	return data, nil
}

// WriteFileSandboxed writes content to userPath confined to root, enforcing
// the sandbox and the secret check on both path and content.
func WriteFileSandboxed(root, userPath string, content []byte) error {
	abs, err := ResolveInSandbox(root, userPath)
	if err != nil {
		return err
	}
	if ContainsSecret(abs) {
		return ErrSecretHit
	}
	if ContainsSecret(string(content)) {
		return ErrSecretHit
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, content, 0o644)
}

type fileReadRequest struct {
	Path string `json:"path"`
}

type fileReadResponse struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Size    int    `json:"size"`
}

type fileWriteRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (d *Daemon) handleFileRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req fileReadRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxReadBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	data, err := ReadFileSandboxed(d.Root, req.Path)
	switch {
	case errors.Is(err, ErrTraversal):
		writeError(w, http.StatusForbidden, "path escapes workspace")
		return
	case errors.Is(err, ErrSecretHit):
		writeError(w, http.StatusForbidden, "refused: secret pattern match")
		return
	case err != nil && strings.Contains(err.Error(), "1MB"):
		writeError(w, http.StatusRequestEntityTooLarge, "file exceeds 1MB read cap")
		return
	case err != nil && strings.Contains(err.Error(), "directory"):
		writeError(w, http.StatusBadRequest, "path is a directory")
		return
	case err != nil:
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, "file not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "read failed")
		return
	}
	writeJSON(w, http.StatusOK, fileReadResponse{Path: req.Path, Content: string(data), Size: len(data)})
}

func (d *Daemon) handleFileWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req fileWriteRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxReadBytes*2)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	err := WriteFileSandboxed(d.Root, req.Path, []byte(req.Content))
	switch {
	case errors.Is(err, ErrTraversal):
		writeError(w, http.StatusForbidden, "path escapes workspace")
		return
	case errors.Is(err, ErrSecretHit):
		writeError(w, http.StatusForbidden, "refused: secret pattern match")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "write failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "path": req.Path})
}
