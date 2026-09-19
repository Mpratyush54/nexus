package extract

import (
	"strings"
)

var junkMarkers = []string{
	"subagent_type", "skill.md", "use this skill", "launch exactly",
	"full repository path:", "custom instructions:", "by default, the review",
	"when launching this subagent", "run_in_background",
	"agent transcripts", "do not dump entire chat",
	"prefer nexus mcp", "memory_search", "memory_write",
	"always_applied_workspace_rule", "available_skills",
	"you are an ai coding", "follow the user's instructions",
	"diff: branch changes", "change description:",
	"bugbot", "security review", "review-bugbot",
	"<user_query>", "<communication>", "citing_code",
	"tool call", "function calls to help you",
	"<timestamp>", "</timestamp>", "<user_info>", "<git_status>",
	"agent-transcripts", "open_and_recently_viewed", "todo_update",
	"conversation_summary", "calldynamictool", "getdynamictools",
	"you must read the tool schemas", "always inspect a tool",
	"please always cite", "never write a or d",
	"this subagent is single-shot", "model family",
	"namespace and single-tool", "[truncated]",
	"use when ", "use for ", "use this ", "use the ",
	"unless the user explicitly", "browser automation",
	"when speaking to the user", "kebab-case model",
	"available_subagent", "best-of-n", "exact prompt shape",
}

var durableMarkers = []string{
	"i prefer", "i like", "i want", "i decided", "we decided", "we chose",
	"let's use", "lets use", "we're using", "we are using", "we use ",
	"going with", "switched to", "migrate to", "migrated to",
	"decision recorded", "decision:", "agreed to",
	"never use", "always use", "must not", "must never", "we must ",
	"preference", "my style",
	"use redis", "use postgres", "use sqlite",
	"harvested cursor", "auto-sync", "auto sync",
}

var explicitMarkers = []string{
	"i prefer", "i like", "i want", "i decided", "we decided",
	"let's use", "lets use", "use redis", "we must ", "i must ",
	"never use", "always use", "must not", "must never",
	"don't use", "do not use",
}

// Heuristic extracts durable proposals from turns (no network).
func Heuristic(project string, turns []Turn, _ []Existing) []Proposal {
	_ = project
	var out []Proposal
	for _, t := range CapTurns(turns) {
		for _, sent := range splitSentences(t.Content) {
			sent = strings.TrimSpace(sent)
			if len(sent) < 20 {
				continue
			}
			if len(sent) > 2000 {
				sent = sent[:2000]
			}
			if !isDurable(sent, t.Speaker) {
				continue
			}
			explicit := isExplicit(sent)
			conf := 0.75
			if explicit {
				conf = 0.95
			}
			out = append(out, Proposal{
				Key:        KeyFromContent(sent),
				Content:    sent,
				Level:      classifyLevel(sent),
				Scope:      classifyScope(sent),
				Confidence: conf,
				Explicit:   explicit,
				Source:     "processor:heuristic",
			})
		}
	}
	return out
}

func splitSentences(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return r == '.' || r == '!' || r == '?' || r == '\n'
	})
}

func isJunk(text string) bool {
	lowered := strings.ToLower(strings.TrimSpace(text))
	if lowered == "" {
		return true
	}
	for _, m := range junkMarkers {
		if strings.Contains(lowered, m) {
			return true
		}
	}
	if strings.HasPrefix(lowered, "- `") || strings.HasPrefix(lowered, "- **") {
		return true
	}
	if looksLikePath(text) {
		return true
	}
	if strings.Count(text, "`") >= 4 && !hasDurable(lowered) {
		return true
	}
	return false
}

func looksLikePath(text string) bool {
	t := strings.TrimSpace(text)
	if len(t) < 8 {
		return false
	}
	if (len(t) >= 3 && t[1] == ':' && (t[2] == '\\' || t[2] == '/')) ||
		strings.HasPrefix(t, "/") || strings.HasPrefix(t, `\\`) {
		if strings.Count(t, " ") <= 2 && (strings.Contains(t, `\`) || strings.Contains(t, "/")) {
			return true
		}
	}
	return strings.Contains(t, `\.cursor\`) || strings.Contains(t, "/.cursor/") ||
		strings.Contains(t, "skills-cursor") || strings.Contains(t, "SKILL.md")
}

func hasDurable(lowered string) bool {
	for _, m := range durableMarkers {
		if strings.Contains(lowered, m) {
			return true
		}
	}
	return false
}

func isExplicit(text string) bool {
	lowered := strings.ToLower(text)
	for _, m := range explicitMarkers {
		if strings.Contains(lowered, m) {
			return true
		}
	}
	return false
}

func isDurable(text, speaker string) bool {
	if isJunk(text) {
		return false
	}
	sp := strings.ToLower(strings.TrimSpace(speaker))
	if sp == "tool" || sp == "system" {
		return false
	}
	lowered := strings.ToLower(text)
	durable := hasDurable(lowered) || isExplicit(text)
	switch sp {
	case "user", "human":
		if durable {
			return true
		}
		return isStackChoice(lowered)
	case "assistant", "ai", "model", "bot":
		for _, m := range []string{
			"we decided", "i decided", "we chose", "agreed to",
			"going with", "we'll use", "we will use", "decision recorded", "decision:",
		} {
			if strings.Contains(lowered, m) {
				return true
			}
		}
		return false
	default:
		return durable
	}
}

func isStackChoice(lowered string) bool {
	if !strings.HasPrefix(lowered, "use ") || len(lowered) > 120 {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(lowered, "use "))
	words := strings.Fields(rest)
	if len(words) == 0 {
		return false
	}
	switch words[0] {
	case "when", "this", "the", "a", "an", "for", "to", "in", "with", "exactly":
		return false
	}
	if strings.HasPrefix(words[0], "`") || strings.HasPrefix(words[0], "[") {
		return false
	}
	return true
}

func classifyLevel(text string) string {
	lowered := strings.ToLower(text)
	for _, m := range []string{"i prefer", "i like", "i always", "my style"} {
		if strings.Contains(lowered, m) {
			return "personal"
		}
	}
	for _, m := range []string{"for now", "right now", "during this", "in this task"} {
		if strings.Contains(lowered, m) {
			return "session"
		}
	}
	for _, m := range []string{"all apis", "company policy", "every service", "organization-wide"} {
		if strings.Contains(lowered, m) {
			return "organization"
		}
	}
	return "project"
}

func classifyScope(text string) string {
	lowered := strings.ToLower(text)
	for _, m := range []string{"must not", "must never", "never use", "required", "forbidden"} {
		if strings.Contains(lowered, m) {
			return "constraint"
		}
	}
	for _, m := range []string{"root cause", "bug fix", "incident", "postmortem"} {
		if strings.Contains(lowered, m) {
			return "episode_summary"
		}
	}
	for _, m := range []string{"decided", "decision", "chose ", "chosen", "agreed"} {
		if strings.Contains(lowered, m) {
			return "decision"
		}
	}
	for _, m := range []string{"prefer", "preference", "i like", "i love", "my style"} {
		if strings.Contains(lowered, m) {
			return "preference"
		}
	}
	for _, m := range []string{"pattern", "convention", "typically", "usually", "best practice"} {
		if strings.Contains(lowered, m) {
			return "pattern"
		}
	}
	return "fact"
}
