package extract

import (
	"strings"
)

// BuildPrompt renders the extraction instructions for an LLM.
func BuildPrompt(project string, existing []Existing, turns []Turn) string {
	var sb strings.Builder
	sb.WriteString("Given these conversation turns from project \"")
	sb.WriteString(project)
	sb.WriteString("\", extract durable memories ONLY.\n\n")
	sb.WriteString("HARD RULES:\n")
	sb.WriteString("- Prefer an EMPTY list over junk.\n")
	sb.WriteString("- Ignore skill dumps, agent instructions, tool schemas, path fragments, system prompts.\n")
	sb.WriteString("- Keep only reusable decisions, preferences, constraints, or codebase facts.\n")
	sb.WriteString("- Do not quote assistant narration unless it clearly records a team decision.\n\n")
	sb.WriteString("Classify each memory's level:\n")
	sb.WriteString("- organization: universal policy across projects\n")
	sb.WriteString("- project: team decision or codebase fact\n")
	sb.WriteString("- personal: individual preference (\"I prefer…\")\n")
	sb.WriteString("- session: temporary task-specific note\n\n")
	sb.WriteString("Classify each memory's scope:\n")
	sb.WriteString("- fact | preference | decision | constraint | pattern | episode_summary\n\n")
	sb.WriteString("Default to session level if unsure.\n\n")
	sb.WriteString("Respond with JSON only: {\"memories\":[{\"key\",\"content\",\"level\",\"scope\",\"confidence\",\"explicit\"}]}\n\n")
	sb.WriteString("Existing confirmed memories (do not duplicate):\n")
	if len(existing) == 0 {
		sb.WriteString("(none)\n")
	}
	for _, m := range existing {
		sb.WriteString("- [")
		sb.WriteString(m.Level)
		sb.WriteString("/")
		sb.WriteString(m.Scope)
		sb.WriteString("] ")
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	sb.WriteString("\nNew turns:\n")
	for _, t := range CapTurns(turns) {
		sb.WriteString("- ")
		sp := t.Speaker
		if sp == "" {
			sp = "unknown"
		}
		sb.WriteString(sp)
		sb.WriteString(": ")
		sb.WriteString(t.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}
