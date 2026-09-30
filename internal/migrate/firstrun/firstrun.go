// Package firstrun migrates legacy nexus-daemon autostart and local state
// into the WinUI / embedded-core outbox path (product-spec §7.8 step 5, P2).
//
// Best-effort contract:
//   - DetectDaemonAutostart finds HKCU Run values mentioning nexus-daemon
//     (Windows). Non-Windows tests inject a fake Run list via
//     NEXUS_FIRSTRUN_AUTOSTART_FILE (name=value lines).
//   - ImportDaemonState copies known .central-memory daemon state into an
//     outbox directory when present; missing files are skipped, never fatal.
//   - ClearDaemonAutostart removes matching Run entries so the old daemon
//     no longer starts at login.
//
// :7272 is retired — callers must not recommend the daemon TCP proxy.
package firstrun

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// EnvAutostartFile overrides the Run-key source for tests (and non-Windows).
// Format: one "Name=Value" line per entry (value may contain '=').
const EnvAutostartFile = "NEXUS_FIRSTRUN_AUTOSTART_FILE"

// AutostartHit is one login Run entry that references nexus-daemon.
type AutostartHit struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ImportResult documents a best-effort state import.
type ImportResult struct {
	Source      string   `json:"source"`
	OutboxDir   string   `json:"outbox_dir"`
	CopiedFiles []string `json:"copied_files"`
	Skipped     []string `json:"skipped"`
}

// Result is the combined firstrun migration report.
type Result struct {
	Autostart        []AutostartHit `json:"autostart"`
	AutostartCleared int            `json:"autostart_cleared"`
	Import           ImportResult   `json:"import"`
	Notes            []string       `json:"notes"`
}

// knownStateNames are files under .central-memory worth copying into the
// outbox migration folder when present. Globs are handled separately.
var knownStateNames = []string{
	"offsets.json",
	"harvest_offsets.json",
	"harvest.json",
	"daemon.state.json",
	"spool.json",
	"daemon.workspace",
}

// resolveCentralMemory returns the .central-memory directory to read.
// src may be the vault itself or a workspace root containing it.
func resolveCentralMemory(src string) (string, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return "", fmt.Errorf("firstrun: empty source path")
	}
	clean := filepath.Clean(src)
	base := filepath.Base(clean)
	if strings.EqualFold(base, ".central-memory") {
		return clean, nil
	}
	nested := filepath.Join(clean, ".central-memory")
	if fi, err := os.Stat(nested); err == nil && fi.IsDir() {
		return nested, nil
	}
	if fi, err := os.Stat(clean); err == nil && fi.IsDir() {
		return clean, nil
	}
	return clean, nil
}

// ImportDaemonState copies offsets/state from a legacy .central-memory tree
// into outboxDir/migrated-daemon/. Best-effort: missing sources are listed
// in Skipped; only I/O failures on existing files return an error.
func ImportDaemonState(src, outboxDir string) (ImportResult, error) {
	res := ImportResult{OutboxDir: outboxDir}
	cm, err := resolveCentralMemory(src)
	if err != nil {
		return res, err
	}
	res.Source = cm

	if strings.TrimSpace(outboxDir) == "" {
		return res, fmt.Errorf("firstrun: empty outbox dir")
	}
	destRoot := filepath.Join(outboxDir, "migrated-daemon")
	if err := os.MkdirAll(destRoot, 0o700); err != nil {
		return res, err
	}

	if _, err := os.Stat(cm); err != nil {
		res.Skipped = append(res.Skipped, "source missing: "+cm)
		return res, nil
	}

	for _, name := range knownStateNames {
		from := filepath.Join(cm, name)
		if err := copyFileBestEffort(from, filepath.Join(destRoot, name), &res); err != nil {
			return res, err
		}
	}

	// daemon.*.json (spec §7.8): any JSON sibling matching the prefix.
	entries, err := os.ReadDir(cm)
	if err != nil {
		res.Skipped = append(res.Skipped, "readdir: "+err.Error())
		return res, nil
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "daemon.") || !strings.HasSuffix(name, ".json") {
			continue
		}
		// Already covered exact names above; still copy any extra daemon.*.json.
		if name == "daemon.state.json" {
			continue
		}
		from := filepath.Join(cm, name)
		if err := copyFileBestEffort(from, filepath.Join(destRoot, name), &res); err != nil {
			return res, err
		}
	}

	manifestPath := filepath.Join(destRoot, "firstrun-manifest.json")
	raw, _ := json.MarshalIndent(res, "", "  ")
	_ = os.WriteFile(manifestPath, raw, 0o600)
	return res, nil
}

func copyFileBestEffort(from, to string, res *ImportResult) error {
	fi, err := os.Stat(from)
	if err != nil {
		res.Skipped = append(res.Skipped, filepath.Base(from)+": not present")
		return nil
	}
	if fi.IsDir() {
		res.Skipped = append(res.Skipped, filepath.Base(from)+": is directory")
		return nil
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	res.CopiedFiles = append(res.CopiedFiles, filepath.Base(from))
	return nil
}

// Run detects daemon autostart, imports state, and optionally clears Run entries.
// clearAutostart removes matching HKCU / fake-file entries after detection.
func Run(src, outboxDir string, clearAutostart bool) (Result, error) {
	var r Result
	r.Notes = append(r.Notes,
		"best-effort import: missing .central-memory files are skipped",
		"do not recommend :7272; use embedded nexuscore / nexus capture --foreground",
	)

	hits, err := DetectDaemonAutostart()
	if err != nil {
		return r, err
	}
	r.Autostart = hits

	imp, err := ImportDaemonState(src, outboxDir)
	if err != nil {
		return r, err
	}
	r.Import = imp

	if clearAutostart {
		n, err := ClearDaemonAutostart()
		if err != nil {
			return r, err
		}
		r.AutostartCleared = n
	}
	return r, nil
}

// DefaultSource returns ~/.central-memory when home is known.
func DefaultSource() string {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".central-memory")
	}
	return ".central-memory"
}
