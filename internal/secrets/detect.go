package secrets

import (
	"path"
	"strings"
)

// DetectSecretPath reports whether a relative workspace path should be
// treated as a secret file for capture (D8 filename rules).
func DetectSecretPath(rel string) bool {
	rel = strings.ReplaceAll(strings.TrimSpace(rel), `\`, "/")
	rel = path.Clean(rel)
	if rel == "." || rel == "" {
		return false
	}
	base := path.Base(rel)
	lower := strings.ToLower(base)
	dir := strings.ToLower(path.Dir(rel))

	switch lower {
	case ".env.example", ".env.sample", ".env.template":
		return false
	case ".npmrc", ".pypirc", "credentials", "kubeconfig", "service-account.json":
		return true
	}

	if strings.HasPrefix(lower, ".env") {
		return true
	}
	if strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key") ||
		strings.HasSuffix(lower, ".p12") || strings.HasSuffix(lower, ".pfx") {
		return true
	}
	if strings.HasPrefix(lower, "id_rsa") || strings.HasPrefix(lower, "id_ed25519") {
		return true
	}
	if strings.Contains(lower, "credentials") && strings.HasSuffix(lower, ".json") {
		return true
	}
	if strings.Contains(lower, "service-account") && strings.HasSuffix(lower, ".json") {
		return true
	}
	if strings.Contains(dir, "/.aws") && lower == "credentials" {
		return true
	}
	if lower == "config" && (strings.Contains(dir, "/.kube") || strings.HasSuffix(dir, ".kube")) {
		return true
	}
	return false
}

// DetectSecretPaths returns the subset of paths that DetectSecretPath flags.
func DetectSecretPaths(paths []string) []string {
	out := make([]string, 0)
	for _, p := range paths {
		if DetectSecretPath(p) {
			out = append(out, p)
		}
	}
	return out
}
