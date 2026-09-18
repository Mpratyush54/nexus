// Package adapters defines the per-agent transcript-locator contract.
// Each agent implements discover/classify with BACKUP (sessions, plans,
// todos, instructions, skills) / IGNORE (cache, logs, tmp) / NEVER
// (credentials, tokens, keys — fail closed) handling. Discover/Classify are
// the live harvester path: they locate transcript files for ingestion.
//
// Export/Restore/Normalize are the legacy vault-backup shim (Issue #117):
// retained for backward compatibility because internal/migrate imports
// legacy vaults, but new code should treat adapters as locators, not backup
// agents. Restore enforces same-absolute-path (<root>\X -> <root>\X) under
// the configured project roots; mismatched layouts restore side-by-side
// with manual re-link steps, never silent rewrite.
package adapters

type Classification int

const (
	Backup Classification = iota
	Ignore
	Never
)

type Artifact struct {
	Agent      string
	Kind       string // session|plan|todo|skill|config
	NativePath string
	RawPath    string // vault copy location (set on export)
	Project    string // leaf ID, e.g. "gitlab-test/Campus-Navigator"
	Was        string // stale original location (move fallback), "" if direct
	Repo       string // git origin URL (move-proof identity, may be "")
	Root       string // git root-commit hash (move-proof identity, may be "")
}

type Adapter interface {
	Name() string
	Discover() ([]Artifact, error)
	Classify(a Artifact) Classification
	// Export is the legacy vault-backup shim (Issue #117, deprecated):
	// retained for migrate compatibility. New code uses Discover/Classify.
	Export(vault string) error
	// Restore is the legacy vault-backup shim (Issue #117, deprecated):
	// retained for migrate compatibility.
	Restore(vault, project, at string) error
	// Normalize is the legacy vault-backup shim (Issue #117, deprecated):
	// retained for migrate compatibility. The harvester transcript index
	// shape is preserved.
	Normalize(vault string) error
}
