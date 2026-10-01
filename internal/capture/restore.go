package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"central-memory/internal/blobs"
)

// RebuildOutcome is the result of one rebuild recipe during restore.
type RebuildOutcome struct {
	Recipe  RebuildRecipe
	Ran     bool   // true when the install command was executed
	Command string // planned or executed command line
	Note    string // e.g. tool missing; hash verify still ok
	Err     string // non-empty when the command ran and failed
}

// RestoreReport summarizes hash verification and rebuild attempts.
type RestoreReport struct {
	Verified int
	Rebuilds []RebuildOutcome
}

// RestoreTree writes manifest files from store into dest, verifies each
// sha256, then runs rebuild recipes (default:node → npm/pnpm install
// --frozen-lockfile when the tool exists). Missing tools record the planned
// command and still pass hash verify for non-regenerable files.
func RestoreTree(dest string, man *Manifest, store blobs.BlobStore) (*RestoreReport, error) {
	if man == nil {
		return nil, fmt.Errorf("capture: manifest is required")
	}
	if store == nil {
		return nil, fmt.Errorf("capture: BlobStore is required")
	}
	dest = filepath.Clean(dest)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	rep := &RestoreReport{Rebuilds: []RebuildOutcome{}}
	for _, f := range man.Files {
		body, err := store.Get(f.SHA256)
		if err != nil {
			return nil, fmt.Errorf("capture: restore get %s: %w", f.Path, err)
		}
		sum := sha256.Sum256(body)
		got := hex.EncodeToString(sum[:])
		want := blobs.NormalizeSHA256(f.SHA256)
		if got != want {
			return nil, fmt.Errorf("capture: hash mismatch for %s: got %s want %s", f.Path, got, want)
		}
		out := filepath.Join(dest, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(out, body, 0o644); err != nil {
			return nil, err
		}
		rep.Verified++
	}
	for _, recipe := range man.Rebuild {
		rep.Rebuilds = append(rep.Rebuilds, runRebuild(dest, recipe))
	}
	return rep, nil
}

func runRebuild(dest string, recipe RebuildRecipe) RebuildOutcome {
	out := RebuildOutcome{Recipe: recipe, Command: recipe.Command}
	if recipe.Rule != "default:node" || strings.TrimSpace(recipe.Command) == "" {
		out.Note = "no rebuild runner for rule"
		return out
	}
	tool := strings.Fields(recipe.Command)[0]
	if _, err := exec.LookPath(tool); err != nil {
		out.Note = fmt.Sprintf("tool %q not found; recorded planned command", tool)
		return out
	}
	dir := dest
	if recipe.Dir != "" && recipe.Dir != "." {
		dir = filepath.Join(dest, filepath.FromSlash(recipe.Dir))
	}
	parts := strings.Fields(recipe.Command)
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out.Ran = true
	if err := cmd.Run(); err != nil {
		out.Err = err.Error()
	}
	return out
}

// NormalizeExcludePaths rewrites synthetic exclude placeholders to folder paths.
func NormalizeExcludePaths(exclude []string) []string {
	out := make([]string, 0, len(exclude))
	for _, e := range exclude {
		out = append(out, cleanExcludeRel(e))
	}
	return out
}
