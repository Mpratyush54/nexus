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

// SkipDirs are never secret-scanned nor git-synced: raw agent DBs and the
// local drive mirror travel via the hourly restic->Azure leg, not GitHub.
// (Vault .gitignore enforces the same list for git.)
var SkipDirs = []string{"raw", "drive-mirror"}

// ShouldSkip reports whether a vault path belongs to the restic/Azure leg
// (raw agent DBs, local drive mirror) and must be skipped by git sync scan.
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
// npm ci / pip install). Keeps Repo B small and R2/Azure free-tier safe.
var DriveIgnore = []string{
	"node_modules/", "dist/", "build/", ".next/", "out/",
	"__pycache__/", "*.pyc", ".venv/", "venv/",
	".git/", "*.log", ".DS_Store", "*.pem", "*.key", ".env*",
}

// Cadence documents the tier decision. Azure Blob HOT (not Cool) is the
// remote tier: Hot has no minimum retention and no early-deletion penalty,
// so restic's prune/repack churn and a 15-minute push cycle cost nothing
// extra — Cool would penalize exactly that pattern. At single-digit GB,
// Hot (~$0.018/GB-mo) is trivial against $100/yr credits. A lifecycle rule
// tiers objects untouched for 30+ days down to Cool automatically: Hot
// behavior for active data, Cool pricing for aged-out snapshots, no manual
// bucket management. No B2/R2 until credits run out or cash cost matters.
const Cadence = "local: robocopy /MIR every 15min; remote: restic->azure-blob-hot every 15min (+30d lifecycle to Cool); apps: daily"
