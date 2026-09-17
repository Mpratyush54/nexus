// Package daemon implements the local workspace daemon core: token auth,
// sandboxed file operations, git inspection, and allowlisted commands.
package daemon

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Token directory/file names inside the workspace root.
const (
	tokenDirName  = ".central-memory"
	tokenFileName = "daemon.token"
)

// GenerateToken returns a random 32-byte token hex-encoded (64 chars).
func GenerateToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// TokenPath returns <root>/.central-memory/daemon.token.
func TokenPath(root string) string {
	return filepath.Join(root, tokenDirName, tokenFileName)
}

// SaveToken writes token to the token file, creating the parent dir with
// 0700 and the file with 0600.
func SaveToken(root, token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("daemon: empty token")
	}
	dir := filepath.Join(root, tokenDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(TokenPath(root), []byte(token), 0o600)
}

// LoadToken reads the token file. It errors when missing or blank.
func LoadToken(root string) (string, error) {
	raw, err := os.ReadFile(TokenPath(root))
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", errors.New("daemon: token file is empty")
	}
	return tok, nil
}

// EnsureToken loads the existing token or generates, persists, and returns
// a new one. The file is created with mode 0600.
func EnsureToken(root string) (string, error) {
	if tok, err := LoadToken(root); err == nil {
		return tok, nil
	}
	tok, err := GenerateToken()
	if err != nil {
		return "", err
	}
	if err := SaveToken(root, tok); err != nil {
		return "", err
	}
	return tok, nil
}

// bearerToken extracts the token from "Authorization: Bearer <token>".
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, prefix))
}

// checkAuth constant-time compares the request bearer against the daemon token.
func (d *Daemon) checkAuth(r *http.Request) bool {
	got := bearerToken(r)
	if got == "" || d.Token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(d.Token)) == 1
}

// requireAuth wraps a handler, rejecting unauthenticated requests with 401.
func (d *Daemon) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.checkAuth(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}
