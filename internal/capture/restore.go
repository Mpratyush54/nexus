package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"central-memory/internal/blobs"
	"central-memory/internal/hiddenproc"
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
		out, _, err := restorePath(dest, f.Path)
		if err != nil {
			return nil, fmt.Errorf("capture: restore path %q: %w", f.Path, err)
		}
		if err := prepareRestoreDir(dest, out); err != nil {
			return nil, err
		}
		if info, err := os.Lstat(out); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("capture: refusing to overwrite symlink %s", f.Path)
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

func restorePath(root, rel string) (string, string, error) {
	raw := strings.TrimSpace(rel)
	if raw == "" {
		return "", "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(raw) || filepath.VolumeName(raw) != "" {
		return "", "", fmt.Errorf("absolute paths are not allowed")
	}
	slash := strings.ReplaceAll(raw, "\\", "/")
	if strings.HasPrefix(slash, "/") {
		return "", "", fmt.Errorf("absolute paths are not allowed")
	}
	clean := path.Clean(slash)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", "", fmt.Errorf("path escapes destination")
	}
	out := filepath.Join(root, filepath.FromSlash(clean))
	within, err := filepath.Rel(root, out)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(os.PathSeparator)) || filepath.IsAbs(within) {
		return "", "", fmt.Errorf("path escapes destination")
	}
	return out, clean, nil
}

// prepareRestoreDir creates the missing parents of out while refusing to
// follow a symlinked directory. A manifest is remote input, so restoring
// through a pre-existing symlink would write outside dest.
func prepareRestoreDir(root, out string) error {
	parent := filepath.Dir(out)
	rel, err := filepath.Rel(root, parent)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	current := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.MkdirAll(current, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("capture: refusing to restore through symlink %s", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("capture: restore parent %s is not a directory", current)
		}
	}
	return nil
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
		resolved, _, err := restorePath(dest, recipe.Dir)
		if err != nil {
			out.Err = fmt.Sprintf("unsafe rebuild directory: %v", err)
			return out
		}
		dir = resolved
	}
	parts := strings.Fields(recipe.Command)
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	hiddenproc.Configure(cmd)
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
