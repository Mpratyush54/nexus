// Package capture holds the pure rules for what a session version keeps:
// regenerable excludes (D8), retention (D10), and storage state (D19).
package capture

import (
	"path"
	"strings"
	"time"
)

// Markers are files present in the tree that decide whether a dependency
// folder can be regenerated.
type Markers struct {
	PackageJSON   bool
	PnpmLock      bool
	NpmLock       bool
	YarnLock      bool
	GoMod         bool
	VendorModules bool
	ComposerLock  bool
	GemfileLock   bool
	CargoToml     bool
	PomXML        bool
	Requirements  bool
	Pyproject     bool
	UvLock        bool
	PoetryLock    bool
	Csproj        bool
	PodfileLock   bool
}

// Regenerable reports whether rel is a dependency or build folder that
// should be excluded, and the rule name. A folder with no regenerating
// marker is not excluded.
func Regenerable(rel string, m Markers) (bool, string) {
	rel = path.Clean(strings.TrimSpace(rel))
	rel = strings.TrimPrefix(rel, "./")
	base := rel
	if i := strings.IndexAny(rel, `/\`); i >= 0 {
		base = rel[:i]
	}
	base = strings.TrimSuffix(base, "/")
	switch base {
	case "node_modules", "bower_components", ".pnpm-store":
		if m.PackageJSON || m.PnpmLock || m.NpmLock || m.YarnLock {
			return true, "default:node"
		}
	case "vendor":
		if (m.GoMod && m.VendorModules) || m.ComposerLock || m.GemfileLock {
			return true, "default:vendor"
		}
	case ".venv", "venv", "__pycache__":
		if m.Requirements || m.Pyproject || m.UvLock || m.PoetryLock {
			return true, "default:python"
		}
	case "target":
		if m.CargoToml || m.PomXML {
			return true, "default:target"
		}
	case "dist", "build", "out", ".next", ".nuxt", ".turbo":
		if m.PackageJSON || m.PnpmLock || m.NpmLock || m.YarnLock {
			return true, "default:build"
		}
	case ".gradle":
		if m.PomXML {
			return true, "default:gradle"
		}
	case ".cache", "coverage", ".pytest_cache":
		if m.Requirements || m.Pyproject || m.UvLock || m.PoetryLock {
			return true, "default:cache"
		}
	case "DerivedData":
		if m.PodfileLock {
			return true, "default:xcode"
		}
	case "bin", "obj":
		if m.Csproj {
			return true, "default:dotnet"
		}
	case "Pods":
		if m.PodfileLock {
			return true, "default:pods"
		}
	}
	if strings.HasSuffix(base, ".pyc") {
		return true, "default:python"
	}
	return false, ""
}

// VersionStamp is one stored session version for retention.
type VersionStamp struct {
	ID string
	At time.Time
}

// RetainVersions keeps every version from the last 90 days and, before
// that, the latest version on each UTC day (D10).
func RetainVersions(in []VersionStamp, now time.Time) []string {
	cutoff := now.UTC().Add(-90 * 24 * time.Hour)
	keep := map[string]bool{}
	bestOlder := map[string]VersionStamp{}
	for _, v := range in {
		at := v.At.UTC()
		if !at.Before(cutoff) {
			keep[v.ID] = true
			continue
		}
		day := at.Format("2006-01-02")
		cur, ok := bestOlder[day]
		if !ok || at.After(cur.At) {
			bestOlder[day] = VersionStamp{ID: v.ID, At: at}
		}
	}
	for _, v := range bestOlder {
		keep[v.ID] = true
	}
	out := make([]string, 0, len(keep))
	for id := range keep {
		out = append(out, id)
	}
	return out
}

// UsageState is ok, warn (80% and above), or full (at the cap).
func UsageState(used, cap int64) string {
	if cap <= 0 || used < 0 {
		return "ok"
	}
	if used >= cap {
		return "full"
	}
	if used*100 >= cap*80 {
		return "warn"
	}
	return "ok"
}

// FreePlanBytes is the proposed Free cap (D19).
const FreePlanBytes int64 = 5 * 1024 * 1024 * 1024
