package extract

import "strings"

// NormalizeLevel maps model aliases onto the store vocabulary.
// Harvest / durable facts default to project (not session).
func NormalizeLevel(level, content string) string {
	l := strings.ToLower(strings.TrimSpace(level))
	switch l {
	case "organization", "org", "org-wide", "company":
		return "organization"
	case "personal", "user", "individual", "private":
		return "personal"
	case "session", "temporary", "ephemeral", "task":
		return "session"
	case "project", "team", "codebase":
		return "project"
	case "":
		return classifyLevel(content)
	default:
		return classifyLevel(content)
	}
}

// NormalizeScope maps model aliases onto allowed scopes.
func NormalizeScope(scope, content string) string {
	s := strings.ToLower(strings.TrimSpace(scope))
	switch s {
	case "fact", "facts":
		return "fact"
	case "preference", "pref", "style":
		return "preference"
	case "decision", "decided", "choice":
		return "decision"
	case "constraint", "rule", "policy", "must":
		return "constraint"
	case "pattern", "convention":
		return "pattern"
	case "episode_summary", "episode", "incident", "summary":
		return "episode_summary"
	case "":
		return classifyScope(content)
	default:
		return classifyScope(content)
	}
}
