package capture

import (
	"path"

	"central-memory/internal/secrets"
)

// FileEntry is one path the capture walk saw, relative to the workspace root.
type FileEntry struct {
	Rel  string
	Size int64
}

// TreePlan is the upload decision for one working tree (D8, D19).
type TreePlan struct {
	Include []string
	Secrets []string
	Exclude []string
	Rebuild []string
	Refuse  []string
}

// MarkersFromNames sets the regenerable markers from a flat list of relative paths.
func MarkersFromNames(names []string) Markers {
	var m Markers
	for _, name := range names {
		base := path.Base(path.Clean(name))
		switch base {
		case "package.json":
			m.PackageJSON = true
		case "pnpm-lock.yaml":
			m.PnpmLock = true
		case "package-lock.json":
			m.NpmLock = true
		case "yarn.lock":
			m.YarnLock = true
		case "go.mod":
			m.GoMod = true
		case "modules.txt":
			if path.Base(path.Dir(path.Clean(name))) == "vendor" || path.Clean(name) == "vendor/modules.txt" {
				m.VendorModules = true
			}
		case "composer.lock":
			m.ComposerLock = true
		case "Gemfile.lock":
			m.GemfileLock = true
		case "Cargo.toml":
			m.CargoToml = true
		case "pom.xml":
			m.PomXML = true
		case "requirements.txt":
			m.Requirements = true
		case "pyproject.toml":
			m.Pyproject = true
		case "uv.lock":
			m.UvLock = true
		case "poetry.lock":
			m.PoetryLock = true
		case "Podfile.lock":
			m.PodfileLock = true
		default:
			if len(base) > 7 && base[len(base)-7:] == ".csproj" {
				m.Csproj = true
			}
		}
	}
	return m
}

// PlanTree decides which files are uploaded, which regenerable folders are
// skipped, and which files are over the 10 GiB cap.
func PlanTree(files []FileEntry, m Markers) TreePlan {
	var plan TreePlan
	seenRule := map[string]bool{}
	for _, f := range files {
		rel := path.Clean(f.Rel)
		if rel == "." || rel == "" {
			continue
		}
		if f.Size > 10<<30 {
			plan.Refuse = append(plan.Refuse, rel)
			continue
		}
		if ok, rule := Regenerable(rel, m); ok {
			plan.Exclude = append(plan.Exclude, rel)
			if rule != "" && !seenRule[rule] {
				seenRule[rule] = true
				plan.Rebuild = append(plan.Rebuild, rule)
			}
			continue
		}
		if secrets.DetectSecretPath(rel) {
			plan.Secrets = append(plan.Secrets, rel)
			continue
		}
		plan.Include = append(plan.Include, rel)
	}
	if plan.Include == nil {
		plan.Include = []string{}
	}
	if plan.Secrets == nil {
		plan.Secrets = []string{}
	}
	if plan.Exclude == nil {
		plan.Exclude = []string{}
	}
	if plan.Rebuild == nil {
		plan.Rebuild = []string{}
	}
	if plan.Refuse == nil {
		plan.Refuse = []string{}
	}
	return plan
}
