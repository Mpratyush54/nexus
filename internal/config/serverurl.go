// Package config resolves shared runtime settings for nexus binaries
// (issue #169 / Phase 9): compile-time ServerURL defaults and the
// 4-tier cascade (CLI flag → env → config file → compile default).
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ConfigFileName is the JSON config basename under the central-memory
// config directory (~/.config/central-memory/config.json on Linux).
const ConfigFileName = "config.json"

// ConfigDirName is the directory under os.UserConfigDir (or ~/.config)
// that holds config.json.
const ConfigDirName = "central-memory"

// fileConfig is the on-disk shape of ~/.config/central-memory/config.json.
type fileConfig struct {
	ServerURL string `json:"server_url"`
}

// ResolveServerURL implements tiers 2–4 of the ServerURL cascade:
//
//  2. Environment: CENTRAL_SERVER_URL, then NEXUS_SERVER
//  3. Config file: ~/.config/central-memory/config.json ("server_url")
//  4. compileDefault (typically main.defaultServerURL, ldflags-overridable)
//
// Tier 1 (CLI -server flag) is applied by callers: pass the flag value as
// the flag package default via ResolveServerURL(defaultServerURL), or
// override after parse when the flag was set explicitly.
func ResolveServerURL(compileDefault string) string {
	if v := firstNonEmpty(os.Getenv("CENTRAL_SERVER_URL"), os.Getenv("NEXUS_SERVER")); v != "" {
		return v
	}
	if v := ServerURLFromConfig(); v != "" {
		return v
	}
	return strings.TrimSpace(compileDefault)
}

// ServerURLFromConfig reads server_url from the local config file.
// Missing/unreadable/malformed files are treated as unset ("").
func ServerURLFromConfig() string {
	path, err := ConfigPath()
	if err != nil || path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg fileConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.ServerURL)
}

// ConfigPath returns the absolute path to the central-memory config.json.
// Layout: $XDG_CONFIG_HOME/central-memory/config.json (or the OS equivalent
// of os.UserConfigDir), matching the Phase 9 plan path
// ~/.config/central-memory/config.json on Linux.
func ConfigPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ConfigFileName), nil
}

func configDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv("CENTRAL_MEMORY_CONFIG_DIR")); override != "" {
		return override, nil
	}
	base, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(base) == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			if err != nil {
				return "", err
			}
			return "", homeErr
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, ConfigDirName), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
