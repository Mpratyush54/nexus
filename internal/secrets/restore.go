package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrRestorePath is returned when the destination path is empty or escapes.
var ErrRestorePath = errors.New("secrets: restore path is required")

// RestoreFile unwraps ciphertext with dataKey and writes the plaintext bytes
// to path (mode 0600). Parent directories are created as needed.
func RestoreFile(path string, ciphertext, dataKey []byte) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return ErrRestorePath
	}
	plain, err := Open(dataKey, ciphertext)
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return os.WriteFile(path, plain, 0o600)
}
