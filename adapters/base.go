// Package adapters defines the per-agent backup contract (P2+).
// Each agent implements discover/classify/export/restore/normalize with
// BACKUP (sessions, plans, todos, instructions, skills) / IGNORE (cache,
// logs, tmp) / NEVER (credentials, tokens, keys — fail closed) handling.
// Restore enforces same-absolute-path (<project root>\<project> -> the same
// path); mismatched layouts restore side-by-side with manual re-link steps,
// never silent rewrite.
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
	Export(vault string) error
	Restore(vault, project, at string) error
	Normalize(vault string) error
}
