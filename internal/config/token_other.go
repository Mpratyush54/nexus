//go:build !windows

package config

import "errors"

func protectToken(token string) (string, error) {
	// Unix keeps the established token field in the owner-only (0600) config.
	// Returning no protected payload tells writeFile to retain that field;
	// it must not label plaintext as an encrypted Windows credential.
	return "", nil
}

func unprotectToken(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	return "", errors.New("config: protected token storage is only supported on Windows")
}
