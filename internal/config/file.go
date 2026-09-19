package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// File holds the on-disk shape of ~/.config/central-memory/config.json
// (or %APPDATA%\central-memory\config.json on Windows).
type File struct {
	ServerURL     string `json:"server_url,omitempty"`
	AppURL        string `json:"app_url,omitempty"`
	Token         string `json:"token,omitempty"`
	UserID        string `json:"user_id,omitempty"`
	Username      string `json:"username,omitempty"`
	WorkspaceRoot string `json:"workspace_root,omitempty"`
}

// LoadFile reads the local config file. Missing/unreadable files return an
// empty File and a nil error so callers can treat "no config" as unset.
func LoadFile() (File, error) {
	path, err := ConfigPath()
	if err != nil || path == "" {
		return File{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return File{}, nil
		}
		return File{}, err
	}
	// PowerShell Set-Content -Encoding UTF8 writes a BOM that encoding/json rejects.
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	var cfg File
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return File{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return cfg, nil
}

// SaveFile merges non-empty fields from patch into the existing config and
// writes config.json with 0600 permissions.
func SaveFile(patch File) error {
	cur, err := LoadFile()
	if err != nil {
		return err
	}
	if v := strings.TrimSpace(patch.ServerURL); v != "" {
		cur.ServerURL = v
	}
	if v := strings.TrimSpace(patch.AppURL); v != "" {
		cur.AppURL = v
	}
	if v := strings.TrimSpace(patch.Token); v != "" {
		cur.Token = v
	}
	if v := strings.TrimSpace(patch.UserID); v != "" {
		cur.UserID = v
	}
	if v := strings.TrimSpace(patch.Username); v != "" {
		cur.Username = v
	}
	if v := strings.TrimSpace(patch.WorkspaceRoot); v != "" {
		cur.WorkspaceRoot = v
	}
	return writeFile(cur)
}

// ClearCredentials removes token/user fields while keeping server URLs.
func ClearCredentials() error {
	cur, err := LoadFile()
	if err != nil {
		return err
	}
	cur.Token = ""
	cur.UserID = ""
	cur.Username = ""
	return writeFile(cur)
}

func writeFile(cfg File) error {
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: mkdir: %w", err)
	}
	path := filepath.Join(dir, ConfigFileName)
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("config: write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("config: rename: %w", err)
	}
	return nil
}

// ResolveToken returns the first non-empty auth token from env, then config.
func ResolveToken() string {
	if v := firstNonEmpty(os.Getenv("NEXUS_TOKEN"), os.Getenv("CENTRAL_MEMORY_TOKEN"), os.Getenv("CENTRAL_SERVER_TOKEN")); v != "" {
		return v
	}
	cfg, err := LoadFile()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.Token)
}

// ResolveUserID returns the first non-empty user id from env, then config.
func ResolveUserID() string {
	if v := firstNonEmpty(os.Getenv("CENTRAL_USER_ID"), os.Getenv("NEXUS_USER_ID"), os.Getenv("USER_ID")); v != "" {
		return v
	}
	cfg, err := LoadFile()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.UserID)
}

// DefaultAppURL is the web UI used for "open browser" links when unset.
const DefaultAppURL = "https://nexus.pratyushes.dev"

// ResolveAppURL returns the web app base URL from config or the default.
func ResolveAppURL() string {
	cfg, err := LoadFile()
	if err == nil {
		if v := strings.TrimSpace(cfg.AppURL); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return DefaultAppURL
}
