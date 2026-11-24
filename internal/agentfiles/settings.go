package agentfiles

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"central-memory/internal/config"
)

// File names the instruction files Nexus can manage.
const (
	FileClaude = "claude" // CLAUDE.md
	FileAgents = "agents" // AGENTS.md
	FileGemini = "gemini" // GEMINI.md
)

// AllFiles lists every supported file id in write order.
var AllFiles = []string{FileClaude, FileAgents, FileGemini}

// FileName maps a file id to its file name at the project root ("" when
// the id is unknown).
func FileName(id string) string {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case FileClaude:
		return "CLAUDE.md"
	case FileAgents:
		return "AGENTS.md"
	case FileGemini:
		return "GEMINI.md"
	}
	return ""
}

// Settings is the per-workspace opt-in. The zero value (and the default)
// is off: nothing is written until Enabled is set.
type Settings struct {
	Enabled bool `json:"enabled"`
	// Files lists file ids (claude, agents, gemini) Nexus may update.
	Files []string `json:"files"`
	// CreateMissing allows creating a listed file that does not exist.
	// Default false: only files that already exist are updated.
	CreateMissing bool `json:"create_missing"`
}

// DefaultSettings is off, all three files listed, no file creation.
func DefaultSettings() Settings {
	return Settings{Files: append([]string(nil), AllFiles...)}
}

// Normalize lowercases, dedupes and orders Files, dropping unknown ids.
func (s Settings) Normalize() Settings {
	seen := map[string]bool{}
	for _, f := range s.Files {
		f = strings.ToLower(strings.TrimSpace(f))
		if FileName(f) != "" {
			seen[f] = true
		}
	}
	out := s
	out.Files = []string{}
	for _, f := range AllFiles {
		if seen[f] {
			out.Files = append(out.Files, f)
		}
	}
	return out
}

// SettingsFileName lives beside config.json in the Nexus config dir.
const SettingsFileName = "agent-memory-files.json"

type settingsDoc struct {
	Version    int                 `json:"version"`
	Workspaces map[string]Settings `json:"workspaces"`
}

var settingsMu sync.Mutex

// SettingsPath returns the settings file path (honours
// CENTRAL_MEMORY_CONFIG_DIR like config.json does).
func SettingsPath() (string, error) {
	p, err := config.ConfigPath()
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", fmt.Errorf("agentfiles: no config directory")
	}
	return filepath.Join(filepath.Dir(p), SettingsFileName), nil
}

// WorkspaceKey normalises a workspace root for settings lookup.
func WorkspaceKey(root string) string {
	k := config.NormalizeWorkspacePath(root)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		k = strings.ToLower(k)
	}
	return k
}

// GetSettings returns the workspace's settings, or DefaultSettings (off)
// when none are stored.
func GetSettings(workspaceRoot string) (Settings, error) {
	key := WorkspaceKey(workspaceRoot)
	if key == "" {
		return DefaultSettings(), fmt.Errorf("agentfiles: empty workspace root")
	}
	settingsMu.Lock()
	defer settingsMu.Unlock()
	doc, err := loadSettingsDoc()
	if err != nil {
		return DefaultSettings(), err
	}
	if s, ok := doc.Workspaces[key]; ok {
		return s.Normalize(), nil
	}
	return DefaultSettings(), nil
}

// SetSettings stores the workspace's settings and returns the normalised
// value that was saved.
func SetSettings(workspaceRoot string, s Settings) (Settings, error) {
	key := WorkspaceKey(workspaceRoot)
	if key == "" {
		return Settings{}, fmt.Errorf("agentfiles: empty workspace root")
	}
	s = s.Normalize()
	settingsMu.Lock()
	defer settingsMu.Unlock()
	doc, err := loadSettingsDoc()
	if err != nil {
		return Settings{}, err
	}
	doc.Workspaces[key] = s
	if err := saveSettingsDoc(doc); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// ListSettings returns every stored workspace setting keyed by
// WorkspaceKey.
func ListSettings() (map[string]Settings, error) {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	doc, err := loadSettingsDoc()
	if err != nil {
		return nil, err
	}
	return doc.Workspaces, nil
}

func loadSettingsDoc() (settingsDoc, error) {
	doc := settingsDoc{Version: 1, Workspaces: map[string]Settings{}}
	path, err := SettingsPath()
	if err != nil {
		return doc, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return doc, nil
		}
		return doc, err
	}
	raw = bytes.TrimPrefix(raw, utf8BOM)
	if err := json.Unmarshal(raw, &doc); err != nil {
		return settingsDoc{Version: 1, Workspaces: map[string]Settings{}}, fmt.Errorf("agentfiles: parse %s: %w", path, err)
	}
	if doc.Workspaces == nil {
		doc.Workspaces = map[string]Settings{}
	}
	return doc, nil
}

func saveSettingsDoc(doc settingsDoc) error {
	path, err := SettingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	doc.Version = 1
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writeAtomic(path, raw, 0o600, nil)
}
