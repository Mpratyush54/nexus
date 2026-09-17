// Package migrate imports legacy single-user vault data into the
// multiplayer Postgres schema (implementation-plan.md Phase 1b,
// GitHub Mpratyush54/nexus issue #25).
//
// Legacy layout (v1 vault, see git snapshot 524342e):
//
//	<vault>/memory/global/learnings.md          # global chunks via `mem remember`
//	<vault>/memory/projects/<name>/MEMORY.md    # per-project chunks
//	<vault>/agents/<agent>/normalized/sessions.jsonl  # one JSON object per line
//
// CLI hook (wired by the nexus/mem entrypoint, not here):
//
//	nexus migrate --vault PATH [--dry-run]
//
//	--vault PATH  vault root to read (defaults to %USERPROFILE%\.central-memory)
//	--dry-run     parse, validate, dedupe and report counts without writing
//
// The entrypoint calls Run(vaultPath, dryRun) and prints the returned
// Counts. Run is intentionally read-only in this skeleton: it produces
// insert-ready DTOs and counts; the INSERT half binds to store.Store in a
// follow-up without changing any shape defined here.
//
// Design: stdlib-only. MemoryItem / ProjectSeed / HistoricalEvent are local
// DTOs mirroring internal/store types field-for-field (same names, same
// value vocabularies) so a thin adapter can map them 1:1 later. Depending
// on internal/store here would drag pgx/pgvector into the migration binary
// and couple it to sibling churn; depending on internal/project is safe
// (it is stdlib-only) and is how production fingerprints projects. Tests
// inject a fake FingerprintFunc so they never touch D:\ or git.
package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"central-memory/internal/project"
)

// CLIUsage documents the intended entrypoint wiring for `nexus migrate`.
// Kept here (not in main) so the migrate package owns its contract and the
// CLI stays a thin flag parser over Run.
const CLIUsage = `nexus migrate --vault PATH [--dry-run]

  --vault PATH  legacy vault root (default %USERPROFILE%\.central-memory)
  --dry-run     parse, validate and count only; write nothing

Reads memory/global/learnings.md, memory/projects/*/MEMORY.md and
agents/*/normalized/sessions.jsonl, seeds projects via Fingerprint,
and reports what would be (or was) imported.`

// ProjectSeed is the migration-local shape of a canonical project row:
// the normalized origin URL, the root-commit hash, and the folder fallback.
// One of CanonicalURL / RootCommit is expected to be set; FolderName is
// always set (last-resort identity, plan §1.2).
type ProjectSeed struct {
	CanonicalURL string
	RootCommit   string
	FolderName   string
	DisplayName  string
}

// Counts is the Run result: post-dedup, insert-ready row counts plus how
// many source records were skipped (too short, invalid JSON, duplicates).
type Counts struct {
	DryRun   bool
	Memories int
	Projects int
	Events   int
	Skipped  int
}

// FingerprintFunc matches project.Fingerprint: move-proof repo identity
// (origin URL survives moves/renames, root commit survives missing remotes).
// Production passes project.Fingerprint; tests inject fakes.
type FingerprintFunc func(dir string) (origin, root string)

// SeedProjects maps local project dirs to canonical seeds with 1:1
// dedup: same normalized URL (or, failing that, same root commit, or same
// folder name) yields exactly one seed. First dir seen wins.
func SeedProjects(dirs []string) []ProjectSeed {
	return SeedProjectsWith(dirs, project.Fingerprint)
}

// SeedProjectsWith is SeedProjects with an injectable fingerprinter.
// A nil fp is treated as "no git identity" (folder-name fallback only).
func SeedProjectsWith(dirs []string, fp FingerprintFunc) []ProjectSeed {
	seen := make(map[string]bool, len(dirs))
	var out []ProjectSeed
	for _, dir := range dirs {
		clean := filepath.Clean(dir)
		var origin, root string
		if fp != nil {
			origin, root = fp(dir)
		}
		norm := normalizeGitURL(origin)
		folder := filepath.Base(clean)
		key := ""
		switch {
		case norm != "":
			key = "url:" + norm
		case root != "":
			key = "root:" + root
		default:
			key = "folder:" + strings.ToLower(folder)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ProjectSeed{
			CanonicalURL: norm,
			RootCommit:   root,
			FolderName:   folder,
			DisplayName:  folder,
		})
	}
	return out
}

// normalizeGitURL is a local copy of store.NormalizeGitURL (kept local so
// this package stays stdlib-only): strips schemes, user@, scp-like colons,
// .git suffix; lowercases. The two copies must stay in sync — same inputs,
// same outputs — so seeds match what ResolveProject will look up later.
func normalizeGitURL(raw string) string {
	s := strings.TrimSpace(raw)
	for _, scheme := range []string{"https://", "http://", "ssh://", "git+ssh://", "git://"} {
		s = strings.TrimPrefix(s, scheme)
	}
	if i := strings.Index(s, "@"); i >= 0 && (strings.Index(s, "/") == -1 || i < strings.Index(s, "/")) {
		s = s[i+1:]
	}
	s = strings.Replace(s, ":", "/", 1)
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")
	return strings.ToLower(s)
}

// Run executes the Phase 1b import plan against a vault root: parses the
// global + per-project markdown memories, the per-agent sessions.jsonl
// files, dedups everything, and seeds projects from the detected leaves on
// disk (via project.Leaves + project.Fingerprint). It returns what would be
// inserted; with dryRun=false the same counts describe what the store
// adapter must insert (binding is a follow-up; see package doc).
func Run(vaultPath string, dryRun bool) (Counts, error) {
	var dirs []string
	for _, leaf := range project.Leaves() {
		if dir := project.LeafDir(leaf); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	return RunWith(vaultPath, dirs, dryRun, project.Fingerprint)
}

// RunWith is Run with injectable project dirs and fingerprinter for tests.
func RunWith(vaultPath string, projectDirs []string, dryRun bool, fp FingerprintFunc) (Counts, error) {
	var c Counts
	c.DryRun = dryRun
	if strings.TrimSpace(vaultPath) == "" {
		return c, fmt.Errorf("migrate: empty vault path")
	}
	if fi, err := os.Stat(vaultPath); err != nil || !fi.IsDir() {
		return c, fmt.Errorf("migrate: vault not found: %s", vaultPath)
	}

	var items []MemoryItem

	// Global learnings -> organization-level memories.
	if data, err := os.ReadFile(filepath.Join(vaultPath, "memory", "global", "learnings.md")); err == nil {
		got, skipped := parseMarkdownReport(string(data), "organization", "", "vault:learnings.md")
		items = append(items, got...)
		c.Skipped += skipped
	}

	// Per-project MEMORY.md -> project-level memories. os.ReadDir is
	// sorted, so import order is deterministic.
	if entries, err := os.ReadDir(filepath.Join(vaultPath, "memory", "projects")); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(vaultPath, "memory", "projects", e.Name(), "MEMORY.md"))
			if err != nil {
				continue
			}
			got, skipped := parseMarkdownReport(string(data), "project", e.Name(), "vault:MEMORY.md/"+e.Name())
			items = append(items, got...)
			c.Skipped += skipped
		}
	}

	items = DeduplicateItems(items)
	c.Memories = len(items)

	// Historical sessions -> SESSION_STARTED / SESSION_ENDED event pairs.
	var events []HistoricalEvent
	if agents, err := os.ReadDir(filepath.Join(vaultPath, "agents")); err == nil {
		for _, a := range agents {
			if !a.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(vaultPath, "agents", a.Name(), "normalized", "sessions.jsonl"))
			if err != nil {
				continue
			}
			got, skipped := ParseSessionsJSONLReport(string(data), a.Name())
			events = append(events, got...)
			c.Skipped += skipped
		}
	}
	events = DeduplicateEvents(events)
	c.Events = len(events)

	c.Projects = len(SeedProjectsWith(projectDirs, fp))
	return c, nil
}
