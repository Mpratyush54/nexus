package diagnostics

import (
	"regexp"
)

// Redacted is the placeholder left where a secret was.
const Redacted = "[redacted]"

type rule struct {
	re   *regexp.Regexp
	repl string
}

// Order matters: specific token shapes run before the generic key=value
// rule, and paths run last so a token inside a path is already gone.
var redactRules = []rule{
	// Authorization headers and "Bearer <token>" anywhere.
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "$1 " + Redacted},
	// JSON Web Tokens (header.payload.signature, base64url).
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}`), Redacted},
	// Nexus API tokens.
	{regexp.MustCompile(`\bnxs_[A-Za-z0-9_-]{6,}`), "nxs_" + Redacted},
	// OpenAI / Anthropic / OpenRouter style keys (sk-..., sk-ant-..., sk-or-...).
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{12,}`), "sk-" + Redacted},
	// GitHub tokens.
	{regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`), Redacted},
	// AWS access key IDs.
	{regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), Redacted},
	// Slack tokens.
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), Redacted},
	// Private key blocks.
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)`), Redacted},
	// Credentials in URLs: scheme://user:pass@host.
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`), "$1" + Redacted + "@"},
	// Secret-looking query parameters.
	{regexp.MustCompile(`(?i)([?&](?:token|access_token|refresh_token|id_token|api_key|apikey|key|secret|password|sig|signature|code|otp|auth)=)[^&#\s"']+`), "${1}" + Redacted},
	// key=value / "key": "value" pairs whose key names a secret.
	{regexp.MustCompile(`(?i)("?(?:api[_-]?key|access[_-]?token|refresh[_-]?token|auth[_-]?token|client[_-]?secret|secret|password|passwd|pwd|authorization|cookie|set-cookie|session[_-]?token|private[_-]?key)"?\s*[:=]\s*"?)[^\s"',;&]{3,}`), "${1}" + Redacted},
	// Email addresses.
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), "[email]"},
	// Absolute home directories → ~ (Windows, escaped Windows, macOS, Linux).
	// Windows user names may contain spaces: take everything up to the next
	// separator (over-redacting a trailing sentence beats leaking a surname).
	{regexp.MustCompile(`(?i)\b[A-Z]:\\\\(?:Users|Documents and Settings)\\\\[^\\"'<>|\r\n]+`), "~"},
	{regexp.MustCompile(`(?i)\b[A-Z]:[\\/](?:Users|Documents and Settings)[\\/][^\\/"'<>|:\r\n]+`), "~"},
	{regexp.MustCompile(`(^|[^A-Za-z0-9._~-])/(?:Users|home)/[^/\s"'<>:]+`), "$1~"},
	{regexp.MustCompile(`(^|[\s"'(=:])/root\b`), "$1~"},
}

// Redact strips anything that looks like a credential, an email address or
// a user's home directory from s. It is idempotent.
func Redact(s string) string {
	if s == "" {
		return s
	}
	for _, r := range redactRules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}
