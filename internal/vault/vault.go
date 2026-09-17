package vault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// DefaultPath returns %USERPROFILE%\.central-memory (approved vault location).
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".central-memory")
}

// Layout creates the P1 vault skeleton. Repo A (GitHub git, hourly) holds
// memory + normalized + pointers only. Drive/raw go to Repo B (Azure Blob
// Hot, 15-min remote) + local robocopy mirror (15 min) — split from day one
// so drive churn never bloats memory recall.
func Layout(root string) error {
	dirs := []string{
		filepath.Join(root, "memory", "global"),
		filepath.Join(root, "memory", "projects"),
		filepath.Join(root, "agents"),
		filepath.Join(root, "system"),
		filepath.Join(root, "config"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	seed := map[string]string{
		filepath.Join(root, "memory", "global", "profile.md"):   "# Profile\n\n",
		filepath.Join(root, "memory", "global", "learnings.md"): "# Learnings\n\n",
		filepath.Join(root, "memory", "global", "decisions.md"): "# Decisions\n\n",
	}
	for p, c := range seed {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
				return err
			}
		}
	}
	manifest := map[string]any{
		"vault":      root,
		"created":    time.Now().UTC().Format(time.RFC3339),
		"repoA":      "github-private central-memory (memory+normalized+pointers, hourly sync)",
		"repoB":      "azure-blob-hot central-backup (drive+raw, remote 15min; local robocopy 15min; 30d lifecycle to Cool)",
		"restoreRule": "same absolute path only (D:\\X -> D:\\X), never silent rewrite",
	}
	b, _ := json.MarshalIndent(manifest, "", "  ")
	return os.WriteFile(filepath.Join(root, "manifest.json"), b, 0o644)
}
