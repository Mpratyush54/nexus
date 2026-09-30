package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MaxHarvestPreviewBytes caps transcript preview reads (larger than workspace
// file ops so multi-turn JSONL sessions remain readable in Desktop).
const MaxHarvestPreviewBytes = 2 << 20

// AllowHarvestPath reports whether absPath is a known harvest transcript for
// this daemon (exact match against the latest scan hits).
func (d *Daemon) AllowHarvestPath(absPath string) bool {
	if d == nil {
		return false
	}
	want := filepath.Clean(strings.TrimSpace(absPath))
	if want == "" {
		return false
	}
	st := d.HarvestSnapshot()
	for _, f := range st.Files {
		if filepath.Clean(f.Path) == want {
			return true
		}
	}
	for _, p := range d.harvestAllowExtra {
		if filepath.Clean(p) == want {
			return true
		}
	}
	return false
}

// ReadHarvestFile reads an allowlisted harvest transcript (outside the
// workspace sandbox). Path must appear in the current harvest Files list.
func (d *Daemon) ReadHarvestFile(absPath string) ([]byte, string, error) {
	absPath = filepath.Clean(strings.TrimSpace(absPath))
	if absPath == "" {
		return nil, "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(absPath) {
		return nil, "", fmt.Errorf("harvest path must be absolute")
	}
	if !d.AllowHarvestPath(absPath) {
		return nil, "", fmt.Errorf("%w: not a harvested transcript for this workspace", ErrTraversal)
	}
	st, err := os.Stat(absPath)
	if err != nil {
		return nil, "", err
	}
	if !st.Mode().IsRegular() {
		return nil, "", ErrNotFile
	}
	if st.Size() > MaxHarvestPreviewBytes {
		return nil, "", ErrTooLarge
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, "", err
	}
	if len(data) > MaxHarvestPreviewBytes {
		return nil, "", ErrTooLarge
	}
	if containsSecret(data) {
		return nil, "", ErrSecretBlocked
	}
	return data, absPath, nil
}

// FormatTranscriptPreview turns JSONL (or raw text) into a readable conversation.
func FormatTranscriptPreview(raw []byte, maxTurns int) string {
	if maxTurns <= 0 {
		maxTurns = 80
	}
	lines := strings.Split(string(raw), "\n")
	var turns []Turn
	for _, line := range lines {
		t, ok := parseTurnLine([]byte(line))
		if !ok {
			continue
		}
		turns = append(turns, t)
	}
	if len(turns) == 0 {
		// Fallback: show trimmed raw for non-JSONL / sqlite dumps.
		text := strings.TrimSpace(string(raw))
		if text == "" {
			return "(empty transcript)"
		}
		if len(text) > 40_000 {
			text = text[:40_000] + "\n\n… truncated …"
		}
		return "Could not parse dialogue turns — showing raw content:\n\n" + text
	}
	if len(turns) > maxTurns {
		turns = turns[len(turns)-maxTurns:]
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%d turns (showing last %d)\n\n", countNonEmptyTurns(raw), len(turns)))
	for i, t := range turns {
		role := strings.ToUpper(t.Speaker)
		if role == "" {
			role = "UNKNOWN"
		}
		b.WriteString("── " + role)
		if !t.Timestamp.IsZero() {
			b.WriteString(" · " + t.Timestamp.UTC().Format("2006-01-02 15:04"))
		}
		b.WriteString(" ──\n")
		content := strings.TrimSpace(t.Content)
		if len(content) > 4000 {
			content = content[:4000] + "\n…(turn truncated)…"
		}
		b.WriteString(content)
		if i < len(turns)-1 {
			b.WriteString("\n\n")
		}
	}
	return b.String()
}

func countNonEmptyTurns(raw []byte) int {
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if _, ok := parseTurnLine([]byte(line)); ok {
			n++
		}
	}
	return n
}
