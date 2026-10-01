// Package idehistory scaffolds IDE chat-history writeback (D13 / P3).
//
// Live injection into vendor stores (Cursor state.vscdb, Antigravity .db,
// Windsurf encrypted .pb) is version-gated and remains off until a release is
// explicitly allowlisted. Until then, restores always:
//  1. copy the target file (and -wal/-shm siblings) into backupDir
//  2. write a clearly marked JSON sidecar under nexus-restored/
//
// Windsurf restore stays behind ErrExperimentalDisabled by default.
package idehistory

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrExperimentalDisabled is returned when an IDE restore path is off by default.
var ErrExperimentalDisabled = errors.New("idehistory: experimental restore disabled")

// ErrSeededFallback means history restore is unavailable for this IDE/version;
// callers should offer a seeded new session instead.
type ErrSeededFallback struct {
	Agent   string
	Version string
}

func (e *ErrSeededFallback) Error() string {
	agent := e.Agent
	if agent == "" {
		agent = "IDE"
	}
	ver := e.Version
	if ver == "" {
		ver = "(unknown)"
	}
	return fmt.Sprintf("History restore not supported for %s %s yet", agent, ver)
}

// SeededFallback reports that the client should fall back to a seeded session.
func (e *ErrSeededFallback) SeededFallback() bool { return true }

// AllowlistedCursorVersions is the set of Cursor IDE versions for which
// Nexus will attempt history restore scaffolding (backup + sidecar).
// Live vscdb row injection is not enabled for any version in this build.
var AllowlistedCursorVersions = map[string]bool{}

// AllowlistedAntigravityVersions gates Antigravity IDE restore scaffolding.
var AllowlistedAntigravityVersions = map[string]bool{}

// SessionPayload is the conversation blob written to the sidecar (and later
// to a vendor store once live injection is allowlisted).
type SessionPayload struct {
	SessionID string          `json:"session_id"`
	Agent     string          `json:"agent,omitempty"`
	Version   string          `json:"version,omitempty"` // IDE version string
	Title     string          `json:"title,omitempty"`
	Turns     json.RawMessage `json:"turns,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

// Result describes what the stub write produced.
type Result struct {
	BackupPath  string `json:"backup_path"`
	SidecarPath string `json:"sidecar_path"`
	Mode        string `json:"mode"` // always "sidecar" until live injection ships
	Note        string `json:"note,omitempty"`
}

const sidecarNote = "live vscdb/db injection is version-gated and off until allowlisted; sidecar only"

// WriteCursorHistory backs up targetDB, then writes nexus-restored/<session_id>.json
// next to the chats store (sibling of targetDB). It does not mutate state.vscdb.
func WriteCursorHistory(backupDir, targetDB string, sessionPayload SessionPayload) (*Result, error) {
	return writeIDEHistory("cursor", AllowlistedCursorVersions, backupDir, targetDB, sessionPayload)
}

// WriteAntigravityHistory mirrors the Cursor stub path for Antigravity IDE
// conversation stores (backup + nexus-restored sidecar; no live .db injection).
func WriteAntigravityHistory(backupDir, targetDB string, sessionPayload SessionPayload) (*Result, error) {
	return writeIDEHistory("antigravity", AllowlistedAntigravityVersions, backupDir, targetDB, sessionPayload)
}

// WriteWindsurfHistory is experimental and returns ErrExperimentalDisabled
// unless an explicit opt-in is added in a later release.
func WriteWindsurfHistory(backupDir, targetDB string, sessionPayload SessionPayload) (*Result, error) {
	_, _, _ = backupDir, targetDB, sessionPayload
	return nil, ErrExperimentalDisabled
}

func writeIDEHistory(agent string, allowlist map[string]bool, backupDir, targetDB string, payload SessionPayload) (*Result, error) {
	payload.SessionID = strings.TrimSpace(payload.SessionID)
	if payload.SessionID == "" {
		return nil, errors.New("idehistory: session_id is required")
	}
	if strings.TrimSpace(backupDir) == "" {
		return nil, errors.New("idehistory: backup_dir is required")
	}
	if strings.TrimSpace(targetDB) == "" {
		return nil, errors.New("idehistory: target_db is required")
	}
	ver := strings.TrimSpace(payload.Version)
	if !allowlist[ver] {
		return nil, &ErrSeededFallback{Agent: agentLabel(agent), Version: ver}
	}
	if strings.TrimSpace(payload.Agent) == "" {
		payload.Agent = agent
	}

	backupPath, err := backupTarget(backupDir, agent, targetDB)
	if err != nil {
		return nil, err
	}

	sidecarDir := filepath.Join(filepath.Dir(targetDB), "nexus-restored")
	if err := os.MkdirAll(sidecarDir, 0o755); err != nil {
		return nil, err
	}
	sidecarPath := filepath.Join(sidecarDir, sanitizeFileName(payload.SessionID)+".json")
	doc := map[string]any{
		"nexus_restored": true,
		"stub":           true,
		"agent":          agent,
		"written_at":     time.Now().UTC().Format(time.RFC3339),
		"note":           sidecarNote,
		"session":        payload,
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(sidecarPath, raw, 0o644); err != nil {
		return nil, err
	}
	return &Result{
		BackupPath:  backupPath,
		SidecarPath: sidecarPath,
		Mode:        "sidecar",
		Note:        sidecarNote,
	}, nil
}

func agentLabel(agent string) string {
	switch agent {
	case "cursor":
		return "Cursor"
	case "antigravity":
		return "Antigravity"
	case "windsurf":
		return "Windsurf"
	default:
		return agent
	}
}

func sanitizeFileName(id string) string {
	id = strings.TrimSpace(id)
	replacer := strings.NewReplacer("/", "_", "\\", "_", "..", "_", ":", "_")
	return replacer.Replace(id)
}

func backupTarget(backupDir, agent, targetDB string) (string, error) {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	destDir := filepath.Join(backupDir, agent, stamp)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	base := filepath.Base(targetDB)
	dest := filepath.Join(destDir, base)
	if err := copyFile(targetDB, dest); err != nil {
		// Target may not exist yet (fresh machine); still create a marker backup dir.
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		marker := []byte("nexus-idehistory: target missing at backup time\n")
		if err := os.WriteFile(dest+".missing", marker, 0o644); err != nil {
			return "", err
		}
		return destDir, nil
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		src := targetDB + suffix
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyFile(src, dest+suffix); err != nil {
			return "", err
		}
	}
	return dest, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
