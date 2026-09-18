package scan

import (
	"regexp"
	"strings"
)

// NeverPatterns fails a sync closed on hit: tokens, keys, env values.
// NOTE: the api-key pattern requires a long token-like value so prose and
// docs mentioning "api_key" don't false-positive and block every sync.
var NeverPatterns = []*regexp.Regexp{
	regexp.MustCompile(`glpat-[A-Za-z0-9_\-]+`),
	regexp.MustCompile(`ghp_[A-Za-z0-9]+`),
	regexp.MustCompile(`sk-ant-[A-Za-z0-9\-]+`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`(?i)api[_-]?key\s*[:=]\s*['"]?[A-Za-z0-9_\-]{20,}['"]?`),
}

// SkipDirs are harvester outputs that are never secret-scanned nor
// git-synced: raw agent transcripts and the local drive mirror are consumed
// by the harvester/daemon sync leg, not GitHub. (Vault .gitignore enforces
// the same list for git.) Legacy note (Issue #117): these dirs previously
// traveled via a backup leg; they are now transcript-locator outputs.
var SkipDirs = []string{"raw", "drive-mirror"}

// ShouldSkip reports whether a vault path is a harvester output (raw agent
// transcripts, local drive mirror) and must be skipped by git sync scan.
// Matches whole path segments only (so a project named "draw" is not skipped).
func ShouldSkip(p string) bool {
	norm := strings.ReplaceAll(p, "/", `\`)
	for _, d := range SkipDirs {
		for _, seg := range strings.Split(norm, `\`) {
			if seg == d {
				return true
			}
		}
	}
	return false
}

// DriveIgnore mirrors rebuildables we never store (rebuilt via manifest:
// npm ci / pip install). Keeps harvested repos small and remote sync cheap.
var DriveIgnore = []string{
	"node_modules/", "dist/", "build/", ".next/", "out/",
	"__pycache__/", "*.pyc", ".venv/", "venv/",
	".git/", "*.log", ".DS_Store", "*.pem", "*.key", ".env*",
}

// Cadence documents the harvester/daemon sync tiers (Issue #117): the old
// robocopy-mirror + restic-to-Azure backup narrative is removed. Local
// harvest runs on a short cycle, the daemon syncs state remotely on the
// same cycle, and app-level rollups run daily. Legacy backup transports
// (robocopy /MIR, restic) are not part of the current architecture;
// internal/migrate remains the supported legacy-vault importer.
const Cadence = "local: filesystem harvest every 15min; remote: daemon sync every 15min; apps: daily"
