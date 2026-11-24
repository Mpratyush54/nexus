package scan

import "regexp"

// RedactedPlaceholder replaces secret matches in text leaving this machine.
const RedactedPlaceholder = "[REDACTED]"

// ExtraSecretPatterns supplements NeverPatterns with common token shapes
// (sk-, slack xox, PEM private keys, github_pat_, Bearer/JWT, AWS/client
// secrets). Kept in step with the daemon's hardening corpus.
var ExtraSecretPatterns = func() []*regexp.Regexp {
	patterns := []string{
		`sk-[A-Za-z0-9]{20,}`,
		`sk-proj-[A-Za-z0-9_\-]{20,}`,
		`xox[baprs]-[A-Za-z0-9\-]{8,}`,
		`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`,
		`github_pat_[A-Za-z0-9_]{20,}`,
		`(?i)bearer\s+[A-Za-z0-9_\-\.~\+/]{20,}={0,2}`,
		`eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}`,
		`(?i)aws_secret_access_key\s*[:=]\s*['"]?[A-Za-z0-9/\+]{30,}['"]?`,
		`(?i)client_secret\s*[:=]\s*['"]?[A-Za-z0-9_\-]{16,}['"]?`,
	}
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, regexp.MustCompile(p))
	}
	return out
}()

// RedactSecrets replaces every NeverPattern and ExtraSecretPattern match in
// s with RedactedPlaceholder and reports whether anything changed.
func RedactSecrets(s string) (string, bool) {
	redacted := false
	for _, set := range [][]*regexp.Regexp{NeverPatterns, ExtraSecretPatterns} {
		for _, re := range set {
			if re.MatchString(s) {
				s = re.ReplaceAllString(s, RedactedPlaceholder)
				redacted = true
			}
		}
	}
	return s, redacted
}
