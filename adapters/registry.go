package adapters

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"central-memory/internal/platform"
	projectpkg "central-memory/internal/project"
)

// genericAdapter implements Adapter via configured native roots.
type genericAdapter struct {
	name       string
	agentDirs  []string // relative to %USERPROFILE%
	projectDot []string // relative to <project root>\<proj>\
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
// Copy/walk failures abort the export with an error and index.json is NOT
// written, so callers never mistake a partial copy for a clean export.
func (g genericAdapter) Export(vault string) error {
	home, _ := os.UserHomeDir()
	copied, skipped, err := CopyFiltered(g.roots(), g.rawDir(vault), g.maxBytes, "", home)
	if err != nil {
		return fmt.Errorf("%s: export: %w", g.name, err)
	}
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
		return fmt.Errorf("%s: create agent dir: %w", g.name, err)
	}
	b, _ := json.MarshalIndent(map[string]any{
		"agent": g.name, "at": time.Now().UTC().Format(time.RFC3339),
		"files": copied, "skipped_large": skipped,
	}, "", "  ")
	if err := os.WriteFile(g.indexPath(vault), b, 0o644); err != nil {
		return err
	}
	return writeManifest(vault, g.name, skipped)
}

// Restore copies vault raw files back to native paths. Project filter:
// empty = all agents' files everywhere; set = only that project's files
// across this adapter. Same-absolute-path enforced: project restores require
// <project root>\<project> to exist (first configured platform root holding
// the leaf); existing files are backed up to
// <path>.pre-restore-TIMESTAMP before overwrite. `at` is accepted for future
// restic snapshots (P3); P2 only holds latest and warns.
func (g genericAdapter) Restore(vault, project, at string) error {
	if at != "" {
		fmt.Fprintf(os.Stderr, "[%s] note: --at %q accepted but P2 holds latest only (time travel lands in P3/restic)\n", g.name, at)
	}
	if project != "" {
		// Accept leaf IDs, git origin URLs, and root-commit hashes: moved or
		// renamed folders still resolve to their current leaf.
		if leaf := projectpkg.ResolveLeaf(project); leaf != "" {
			project = leaf
		}
		target := leafDirOf(project)
		if target == "" {
			return errNoTarget{project, "(no project roots configured)"}
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
	for _, f := range idx.Files {
		if project != "" && f.Project != project && f.Repo != project && f.Root != project {
			continue
		}
		if _, err := os.Stat(f.RawPath); err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f.NativePath), 0o755); err != nil {
			continue
		}
		if _, err := os.Stat(f.NativePath); err == nil {
			_ = copyFile(f.NativePath, f.NativePath+".pre-restore-"+stamp)
		}
		if err := copyFile(f.RawPath, f.NativePath); err == nil {
			restored++
		}
	}
	fmt.Printf("[%s] restored %d files%s\n", g.name, restored, suffix(project))
	return nil
}

func suffix(project string) string {
	if project == "" {
		return ""
	}
	return " for project " + project
}

func (g genericAdapter) Normalize(vault string) error {
	arts, _ := g.Discover()
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
		return err
	}
	defer md.Close()
	if _, err := md.WriteString("# " + g.name + " normalized index\n\n"); err != nil {
		return fmt.Errorf("%s: write transcript.md: %w", g.name, err)
	}
	for _, a := range arts {
		info, _ := os.Stat(a.NativePath)
		var size int64
		var mod string
		if info != nil {
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
			return fmt.Errorf("%s: encode sessions.jsonl: %w", g.name, err)
		}
		if _, err := md.WriteString("- [" + a.Project + "/" + a.Kind + "] " + a.NativePath + "\n"); err != nil {
			return fmt.Errorf("%s: write transcript.md: %w", g.name, err)
		}
	}
	return nil
}

// leafDirOf maps a project ID ("a/b" or "a") to its absolute directory
// under the configured platform roots (first root holding the leaf).
func leafDirOf(leaf string) string {
	return leafDirOfIn(leaf, platform.ProjectRoots())
}

// leafDirOfIn is leafDirOf over explicit roots (testability seam).
func leafDirOfIn(leaf string, roots []string) string {
	return projectpkg.LeafDirIn(leaf, roots)
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
	_ = os.MkdirAll(filepath.Join(vault, "agents", agent), 0o755)
	b, _ := json.MarshalIndent(map[string]any{
		"agent": agent, "at": time.Now().UTC().Format(time.RFC3339),
		"skipped_large": skipped,
	}, "", "  ")
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
