package adapters

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	projectpkg "central-memory/internal/project"
)

// genericAdapter implements Adapter via configured native roots.
//
// Role (Issue #117): adapters are transcript locators for the harvester —
// Discover/Classify locate agent transcript files under home and project
// dot-dirs. Export/Restore/Normalize below are the legacy vault-backup shim
// (vault/agent/raw + index.json + manifest.json); they are retained for
// backward compatibility (migrate imports legacy vaults) but new code should
// treat adapters as locators, not backup agents. The vault parameter is a
// legacy vault root; harvester paths use project roots instead.
type genericAdapter struct {
	name       string
	agentDirs  []string // relative to %USERPROFILE%
	projectDot []string // relative to <project-root>\<proj>\ (see project.Roots)
	absRoots   []string // absolute dirs (env-expanded at use)
	maxBytes   int64    // large-file cap (50MB pointer rule)
}

func (g genericAdapter) Name() string { return g.name }

func (g genericAdapter) roots() []string {
	home, _ := os.UserHomeDir()
	roots := RootsFor(home, g.agentDirs, g.projectDot)
	for _, a := range g.absRoots {
		if expanded := os.ExpandEnv(a); expanded != "" {
			roots = append(roots, expanded)
		}
	}
	return roots
}

func (g genericAdapter) Discover() ([]Artifact, error) {
	home, _ := os.UserHomeDir()
	var out []Artifact
	for _, r := range g.roots() {
		_ = filepath.Walk(r, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if ClassifyPath(p) != Backup {
				return nil
			}
			out = append(out, Artifact{Agent: g.name, Kind: kindOf(p), NativePath: p, Project: ProjectOf(p, home), Was: ProjectWas(p, home)})
			return nil
		})
	}
	return out, nil
}

func (g genericAdapter) Classify(a Artifact) Classification { return ClassifyPath(a.NativePath) }

func (g genericAdapter) rawDir(vault string) string {
	return filepath.Join(vault, "agents", g.name, "raw")
}

func (g genericAdapter) indexPath(vault string) string {
	return filepath.Join(vault, "agents", g.name, "index.json")
}

// Export copies BACKUP files to vault + writes index.json (native->raw)
// so Restore can map back. Skipped large files are recorded, not copied.
//
// Deprecated legacy-vault shim (Issue #117): retained for backward
// compatibility (migrate imports legacy vaults). All copy, mkdir, marshal,
// and write errors are propagated and aggregated (Issue #108) — a clean
// return means every file landed.
func (g genericAdapter) Export(vault string) error {
	home, _ := os.UserHomeDir()
	copied, skipped, copyErr := CopyFiltered(g.roots(), g.rawDir(vault), g.maxBytes, "", home)
	for i := range copied {
		copied[i].Agent = g.name
		copied[i].Kind = kindOf(copied[i].NativePath)
		// Move-proof identity: git origin + root commit of the leaf repo.
		// If the folder is later moved/renamed, rows still match by repo.
		if leafDir := leafDirOf(copied[i].Project); leafDir != "" {
			copied[i].Repo, copied[i].Root = projectpkg.Fingerprint(leafDir)
		}
	}
	if err := os.MkdirAll(filepath.Join(vault, "agents", g.name), 0o755); err != nil {
		return errors.Join(copyErr, fmt.Errorf("%s: create agent dir: %w", g.name, err))
	}
	b, merr := json.MarshalIndent(map[string]any{
		"agent": g.name, "at": time.Now().UTC().Format(time.RFC3339),
		"files": copied, "skipped_large": skipped,
	}, "", "  ")
	if merr != nil {
		return errors.Join(copyErr, fmt.Errorf("%s: encode index: %w", g.name, merr))
	}
	if err := os.WriteFile(g.indexPath(vault), b, 0o644); err != nil {
		return errors.Join(copyErr, fmt.Errorf("%s: write index: %w", g.name, err))
	}
	if err := writeManifest(vault, g.name, skipped); err != nil {
		return errors.Join(copyErr, err)
	}
	return copyErr
}

// Restore copies vault raw files back to native paths. Project filter:
// empty = all agents' files everywhere; set = only that project's files
// across this adapter. Same-absolute-path enforced: project restores require
// the leaf's directory under the configured project roots (Issue #111) to
// exist; existing files are backed up to <path>.pre-restore-TIMESTAMP before
// overwrite. `at` is accepted for historical snapshots but only the latest
// export is held, so a non-empty value warns and restores latest.
//
// Deprecated legacy-vault shim (Issue #117): retained for backward
// compatibility. Copy/mkdir failures are aggregated and returned (Issue
// #108) instead of reporting a clean restore.
func (g genericAdapter) Restore(vault, project, at string) error {
	if at != "" {
		fmt.Fprintf(os.Stderr, "[%s] note: --at %q accepted but only the latest export is held; restoring latest\n", g.name, at)
	}
	if project != "" {
		// Accept leaf IDs, git origin URLs, and root-commit hashes: moved or
		// renamed folders still resolve to their current leaf.
		if leaf := projectpkg.ResolveLeaf(project); leaf != "" {
			project = leaf
		}
		target := projectpkg.LeafDir(project)
		if target == "" {
			// Fall back to leaf-relative display when roots are unset.
			target = filepath.FromSlash(project)
		}
		if _, err := os.Stat(target); os.IsNotExist(err) {
			return errNoTarget{project, target}
		}
	}
	data, err := os.ReadFile(g.indexPath(vault))
	if err != nil {
		return fmt.Errorf("%s: nothing exported yet (run harvest first): %w", g.name, err)
	}
	var idx struct {
		Files []Artifact `json:"files"`
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		return err
	}
	stamp := time.Now().Format("20060102-150405")
	restored := 0
	var errs []error
	var skippedMissing int
	for _, f := range idx.Files {
		if project != "" && f.Project != project && f.Repo != project && f.Root != project {
			continue
		}
		if _, err := os.Stat(f.RawPath); err != nil {
			skippedMissing++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f.NativePath), 0o755); err != nil {
			errs = append(errs, fmt.Errorf("mkdir %s: %w", filepath.Dir(f.NativePath), err))
			continue
		}
		if _, err := os.Stat(f.NativePath); err == nil {
			if berr := copyFile(f.NativePath, f.NativePath+".pre-restore-"+stamp); berr != nil {
				errs = append(errs, fmt.Errorf("backup %s: %w", f.NativePath, berr))
				continue
			}
		}
		if err := copyFile(f.RawPath, f.NativePath); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", f.NativePath, err))
			continue
		}
		restored++
	}
	fmt.Printf("[%s] restored %d files%s\n", g.name, restored, suffix(project))
	if skippedMissing > 0 {
		fmt.Fprintf(os.Stderr, "[%s] note: skipped %d files with missing vault copies\n", g.name, skippedMissing)
	}
	return errors.Join(errs...)
}

func suffix(project string) string {
	if project == "" {
		return ""
	}
	return " for project " + project
}

// Normalize builds the harvester transcript index (sessions.jsonl +
// transcript.md) from Discover results.
//
// Deprecated legacy-vault shim (Issue #117): the output layout is retained
// for migrate compatibility. All encoding and write errors are propagated
// (Issue #108).
func (g genericAdapter) Normalize(vault string) error {
	arts, err := g.Discover()
	if err != nil {
		return fmt.Errorf("%s: discover: %w", g.name, err)
	}
	ndir := filepath.Join(vault, "agents", g.name, "normalized")
	if err := os.MkdirAll(ndir, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(ndir, "sessions.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	md, err := os.Create(filepath.Join(ndir, "transcript.md"))
	if err != nil {
		return fmt.Errorf("%s: create transcript.md: %w", g.name, err)
	}
	defer md.Close()
	if _, err := md.WriteString("# " + g.name + " normalized index\n\n"); err != nil {
		return fmt.Errorf("%s: write transcript header: %w", g.name, err)
	}
	var errs []error
	for _, a := range arts {
		info, statErr := os.Stat(a.NativePath)
		var size int64
		var mod string
		if statErr != nil {
			errs = append(errs, fmt.Errorf("stat %s: %w", a.NativePath, statErr))
		} else {
			size = info.Size()
			mod = info.ModTime().UTC().Format(time.RFC3339)
		}
		repo, root := "", ""
		if leafDir := leafDirOf(a.Project); leafDir != "" {
			repo, root = projectpkg.Fingerprint(leafDir)
		}
		if err := enc.Encode(map[string]any{
			"session_id": a.NativePath, "agent": g.name, "project": a.Project,
			"kind": a.Kind, "updated": mod, "size": size,
			"repo": repo, "root": root, "was": a.Was,
		}); err != nil {
			errs = append(errs, fmt.Errorf("encode %s: %w", a.NativePath, err))
			continue
		}
		if _, err := md.WriteString("- [" + a.Project + "/" + a.Kind + "] " + a.NativePath + "\n"); err != nil {
			errs = append(errs, fmt.Errorf("write transcript %s: %w", a.NativePath, err))
		}
	}
	// Close-time flush errors (buffered encoder/file) must not be swallowed.
	if err := f.Close(); err != nil {
		errs = append(errs, fmt.Errorf("%s: close sessions.jsonl: %w", g.name, err))
	}
	if err := md.Close(); err != nil {
		errs = append(errs, fmt.Errorf("%s: close transcript.md: %w", g.name, err))
	}
	return errors.Join(errs...)
}

// leafDirOf maps a project ID ("a/b" or "a") to its absolute directory
// under the configured project roots (Issue #111). Kept for backward
// compatibility; new code should prefer project.LeafDir directly.
func leafDirOf(leaf string) string {
	return projectpkg.LeafDir(leaf)
}

func kindOf(p string) string {
	ext := filepath.Ext(p)
	switch ext {
	case ".md":
		return "plan"
	case ".json", ".jsonl", ".db", ".vscdb":
		return "session"
	default:
		return "config"
	}
}

func writeManifest(vault, agent string, skipped []Artifact) error {
	if err := os.MkdirAll(filepath.Join(vault, "agents", agent), 0o755); err != nil {
		return fmt.Errorf("%s: create agent dir: %w", agent, err)
	}
	b, err := json.MarshalIndent(map[string]any{
		"agent": agent, "at": time.Now().UTC().Format(time.RFC3339),
		"skipped_large": skipped,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("%s: encode manifest: %w", agent, err)
	}
	return os.WriteFile(filepath.Join(vault, "agents", agent, "manifest.json"), b, 0o644)
}

type errNoTarget struct {
	project, target string
}

func (e errNoTarget) Error() string {
	return "restore requires same absolute path " + e.target + " for project " + e.project + " (create it or restore side-by-side manually)"
}

// Registry of adapters. Stateful tools (sessions on disk) get full
// adapters. Pure API models (GLM, DeepSeek via TokenRouter, etc.) have no
// local state — they are covered normalized-only via `mem run` (P4), which
// logs transcripts into the vault the same way Normalize does.
func Registry() []Adapter {
	const fiftyMB = 50 << 20
	codeWS := `${APPDATA}\Code\User\workspaceStorage`
	agUser := `${APPDATA}\Antigravity\User`
	return []Adapter{
		genericAdapter{name: "claude", agentDirs: []string{".claude"}, projectDot: []string{".claude"}, maxBytes: fiftyMB},
		genericAdapter{name: "opencode", agentDirs: []string{".config" + string(filepath.Separator) + "opencode", ".local" + string(filepath.Separator) + "share" + string(filepath.Separator) + "opencode"}, projectDot: []string{".opencode"}, maxBytes: fiftyMB},
		genericAdapter{name: "cursor", agentDirs: []string{".cursor"}, projectDot: []string{".cursor"}, maxBytes: fiftyMB},
		genericAdapter{name: "codex", agentDirs: []string{".codex"}, projectDot: []string{".codex"}, maxBytes: fiftyMB},
		genericAdapter{name: "antigravity", agentDirs: []string{".antigravity"},
			absRoots: []string{agUser + `\workspaceStorage`, agUser + `\globalStorage`, agUser + `\settings.json`, agUser + `\snippets`, agUser + `\History`},
			maxBytes: fiftyMB},
		genericAdapter{name: "copilot", agentDirs: []string{".copilot"}, projectDot: []string{".github"},
			absRoots: []string{codeWS}, maxBytes: fiftyMB},
		genericAdapter{name: "codeium", agentDirs: []string{".codeium"}, maxBytes: fiftyMB},
		genericAdapter{name: "kimi", agentDirs: []string{".kimi-code"}, maxBytes: fiftyMB},
		genericAdapter{name: "windsurf", agentDirs: []string{".windsurf"}, maxBytes: fiftyMB},
		genericAdapter{name: "gemini", agentDirs: []string{".gemini"}, maxBytes: fiftyMB},
		genericAdapter{name: "grok", agentDirs: []string{".grok"}, maxBytes: fiftyMB},
		genericAdapter{name: "commandcode", agentDirs: []string{".commandcode"}, maxBytes: fiftyMB},
		genericAdapter{name: "cagent", agentDirs: []string{".cagent"}, maxBytes: fiftyMB},
		genericAdapter{name: "zcode", agentDirs: []string{".zcode"}, maxBytes: fiftyMB},
	}
}
