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
	sb.WriteString("- Extract clear project decisions, preferences, constraints, and codebase facts.\n")
	sb.WriteString("- Each memory content must be a standalone sentence a teammate can read without chat context.\n")
	sb.WriteString("- NEVER write markdown tables, | PROPOSED | lines, keys, or UI telemetry as content.\n")
	sb.WriteString("- Ignore skill dumps, agent instructions, tool schemas, path fragments, system prompts.\n")
	sb.WriteString("- Do not quote assistant narration unless it clearly records a team decision.\n")
	sb.WriteString("- Prefer empty memories[] over low-signal or meta text about extractors/portals.\n\n")
	sb.WriteString("Classify each memory's level (pick exactly one):\n")
	sb.WriteString("- organization — company/org-wide policy across many projects\n")
	sb.WriteString("- project — team decision, architecture, or codebase fact (DEFAULT for durable work)\n")
	sb.WriteString("- personal — one person's taste (\"I prefer…\", \"my editor…\")\n")
	sb.WriteString("- session — throwaway note for this task only (rare; prefer project)\n\n")
	sb.WriteString("Classify each memory's scope (pick exactly one):\n")
	sb.WriteString("- decision — agreed choice (\"we decided…\", \"agreed to use…\")\n")
	sb.WriteString("- constraint — must / must-not / forbidden / required\n")
	sb.WriteString("- preference — personal taste or style\n")
	sb.WriteString("- fact — neutral codebase or product fact\n")
	sb.WriteString("- pattern — recurring convention\n")
	sb.WriteString("- episode_summary — incident / postmortem summary\n\n")
	sb.WriteString("If unsure between session and project, choose project.\n")
	sb.WriteString("If unsure between fact and decision, choose decision when the chat records an agreement.\n\n")
	sb.WriteString("Respond with JSON only: {\"memories\":[{\"key\":\"short/slug\",\"content\":\"standalone sentence\",\"level\":\"project\",\"scope\":\"decision\",\"confidence\":0.0-1.0,\"explicit\":true|false}]}\n\n")
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
