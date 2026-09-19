// Package buildinfo holds ldflag-injected version metadata shared by the
// API server, nexus CLI, and workspace daemon.
package buildinfo

// Version is the product semver (default "dev"; CI sets -X ...Version=vX.Y.Z).
var Version = "dev"

// Commit is the git SHA (CI sets -X ...Commit=$GITHUB_SHA).
var Commit = "unknown"

// BuiltAt is an RFC3339 timestamp when the binary was produced.
var BuiltAt = "unknown"

// App names used in the release registry.
const (
	AppAPI     = "api"
	AppPWA     = "pwa"
	AppCLI     = "cli"
	AppDaemon  = "daemon"
	AppDesktop = "desktop"
)

// Info is the JSON shape for GET /version and GET /healthz.
type Info struct {
	App     string `json:"app"`
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	BuiltAt string `json:"built_at,omitempty"`
}

// Current returns this binary's identity.
func Current(app string) Info {
	return Info{App: app, Version: Version, Commit: Commit, BuiltAt: BuiltAt}
}
