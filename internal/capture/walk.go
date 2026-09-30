package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"central-memory/internal/blobs"
)

// heavyDirs are dependency/build folder names we skip while collecting
// markers so a huge node_modules does not slow the marker pass.
var heavyDirs = map[string]bool{
	"node_modules": true, "bower_components": true, ".pnpm-store": true,
	"vendor": true, ".venv": true, "venv": true, "__pycache__": true,
	"target": true, "dist": true, "build": true, "out": true,
	".next": true, ".nuxt": true, ".turbo": true, ".gradle": true,
	"DerivedData": true, "bin": true, "obj": true, "Pods": true,
	".cache": true, "coverage": true, ".pytest_cache": true,
	".git": true,
}

// FileRecord is one uploaded (or to-restore) file in a capture manifest.
type FileRecord struct {
	Path   string
	SHA256 string
	Size   int64
}

// RebuildRecipe is how restore regenerates an excluded dependency folder.
type RebuildRecipe struct {
	Rule     string // e.g. default:node
	Dir      string // workspace-relative install dir (usually ".")
	Lockfile string
	Command  string
}

// Manifest is the local-verifier capture result: path→sha256, rebuild
// recipes, exclusions, refusals, and how many new blobs were uploaded.
type Manifest struct {
	Files      []FileRecord
	Exclude    []string
	Rebuild    []RebuildRecipe
	Refuse     []string
	NewUploads int // distinct blobs not already in the store (dedup / P0-h)
	Plan       TreePlan
}

// CaptureWalk walks root, applies PlanTree rules, hashes included files,
// and stores bytes in store (BYTEA local S3 stand-in). A second capture of
// an unchanged tree reports NewUploads == 0.
func CaptureWalk(root string, store blobs.BlobStore) (*Manifest, error) {
	if store == nil {
		return nil, fmt.Errorf("capture: BlobStore is required")
	}
	root = filepath.Clean(root)
	entries, markers, err := listWorkspace(root)
	if err != nil {
		return nil, err
	}
	return captureFromEntries(root, entries, markers, store, func(rel string) ([]byte, error) {
		return os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	})
}

// captureFromEntries applies PlanTree, hashes includes, and uploads. size
// on entries drives the 10 GiB refuse rule without reading huge files.
func captureFromEntries(
	root string,
	entries []FileEntry,
	markers Markers,
	store blobs.BlobStore,
	readFile func(rel string) ([]byte, error),
) (*Manifest, error) {
	_ = root
	plan := PlanTree(entries, markers)
	man := &Manifest{
		Files:   []FileRecord{},
		Exclude: NormalizeExcludePaths(plan.Exclude),
		Refuse:  append([]string{}, plan.Refuse...),
		Rebuild: recipesFor(plan.Rebuild, markers),
		Plan:    plan,
	}
	for _, rel := range plan.Include {
		body, err := readFile(rel)
		if err != nil {
			return nil, fmt.Errorf("capture: read %s: %w", rel, err)
		}
		sum := sha256.Sum256(body)
		hexSum := hex.EncodeToString(sum[:])
		uploaded, err := store.Put(hexSum, body)
		if err != nil {
			return nil, fmt.Errorf("capture: put %s: %w", rel, err)
		}
		if uploaded {
			man.NewUploads++
		}
		man.Files = append(man.Files, FileRecord{
			Path:   rel,
			SHA256: hexSum,
			Size:   int64(len(body)),
		})
	}
	return man, nil
}

func listWorkspace(root string) ([]FileEntry, Markers, error) {
	markers, err := scanMarkers(root)
	if err != nil {
		return nil, Markers{}, err
	}
	var entries []FileEntry
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := relSlash(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		base := path.Base(rel)
		if d.IsDir() {
			if base == ".git" {
				return fs.SkipDir
			}
			if ok, _ := Regenerable(rel, markers); ok {
				// One exclude entry for the folder; do not descend.
				entries = append(entries, FileEntry{Rel: path.Join(rel, ".nexus-exclude"), Size: 0})
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entries = append(entries, FileEntry{Rel: rel, Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, Markers{}, err
	}
	return entries, markers, nil
}

func scanMarkers(root string) (Markers, error) {
	var names []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := relSlash(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if heavyDirs[path.Base(rel)] {
				return fs.SkipDir
			}
			return nil
		}
		names = append(names, rel)
		return nil
	})
	if err != nil {
		return Markers{}, err
	}
	return MarkersFromNames(names), nil
}

func relSlash(root, p string) (string, error) {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	return path.Clean(rel), nil
}

func recipesFor(rules []string, m Markers) []RebuildRecipe {
	out := make([]RebuildRecipe, 0, len(rules))
	for _, rule := range rules {
		switch rule {
		case "default:node":
			out = append(out, nodeRecipe(m))
		default:
			out = append(out, RebuildRecipe{Rule: rule, Dir: ".", Command: ""})
		}
	}
	if out == nil {
		out = []RebuildRecipe{}
	}
	return out
}

func nodeRecipe(m Markers) RebuildRecipe {
	r := RebuildRecipe{Rule: "default:node", Dir: "."}
	switch {
	case m.PnpmLock:
		r.Lockfile = "pnpm-lock.yaml"
		r.Command = "pnpm install --frozen-lockfile"
	case m.NpmLock:
		r.Lockfile = "package-lock.json"
		r.Command = "npm install --frozen-lockfile"
	case m.YarnLock:
		r.Lockfile = "yarn.lock"
		r.Command = "yarn install --immutable"
	default:
		r.Lockfile = "package.json"
		r.Command = "npm install"
	}
	return r
}

// PathSHA256Map returns path → sha256 for manifest files.
func (m *Manifest) PathSHA256Map() map[string]string {
	out := make(map[string]string, len(m.Files))
	for _, f := range m.Files {
		out[f.Path] = f.SHA256
	}
	return out
}

// HasRefuse reports whether any path was over the 10 GiB hard limit.
func (m *Manifest) HasRefuse() bool {
	return len(m.Refuse) > 0
}

// strip synthetic exclude placeholder to a clean folder path for display.
func cleanExcludeRel(rel string) string {
	rel = path.Clean(rel)
	if strings.HasSuffix(rel, "/.nexus-exclude") {
		return strings.TrimSuffix(rel, "/.nexus-exclude") + "/"
	}
	if path.Base(rel) == ".nexus-exclude" {
		return path.Dir(rel) + "/"
	}
	return rel
}
