package extract

import (
	"strings"
)

// BuildPrompt renders the extraction instructions for an LLM (turn-batch path).
func BuildPrompt(project string, existing []Existing, turns []Turn) string {
	var sb strings.Builder
	sb.WriteString("Given these conversation turns from project \"")
	sb.WriteString(project)
	sb.WriteString("\", extract durable memories.\n\n")
	sb.WriteString("HARD RULES:\n")
	sb.WriteString("- Capture project decisions, preferences, constraints, codebase facts, AND concrete actions.\n")
	sb.WriteString("- Actions: files created/edited/deleted, commands run, configs changed, packages added — keep path and package names.\n")
	sb.WriteString("- Outcomes: what worked, failed, or was left pending.\n")
	sb.WriteString("- Each memory content must be a standalone sentence a teammate can read without chat context.\n")
	sb.WriteString("- NEVER write markdown tables, | PROPOSED | lines, keys, or UI telemetry as content.\n")
	sb.WriteString("- Ignore skill dumps, agent instructions, tool schemas, system prompts, and meta chatter about extractors/portals.\n")
	sb.WriteString("- Do not drop concrete nouns (paths, APIs, package names, error strings).\n")
	sb.WriteString("- Prefer empty memories[] only when the batch is pure noise / skill dumps with no work product.\n\n")
	sb.WriteString("Classify each memory's level (pick exactly one):\n")
	sb.WriteString("- organization — company/org-wide policy across many projects\n")
	sb.WriteString("- project — team decision, architecture, or codebase fact (DEFAULT for durable work)\n")
	sb.WriteString("- personal — one person's taste (\"I prefer…\", \"my editor…\")\n")
	sb.WriteString("- session — throwaway note for this task only (rare; prefer project)\n\n")
	sb.WriteString("Classify each memory's scope (pick exactly one):\n")
	sb.WriteString("- decision — agreed choice (\"we decided…\", \"agreed to use…\")\n")
	sb.WriteString("- constraint — must / must-not / forbidden / required\n")
	sb.WriteString("- preference — personal taste or style\n")
	sb.WriteString("- fact — neutral codebase or product fact (including actions/outcomes)\n")
	sb.WriteString("- pattern — recurring convention\n")
	sb.WriteString("- episode_summary — incident / postmortem / session work summary\n\n")
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

// BuildCompressPrompt asks for one rich session episode_summary plus sharp decisions.
func BuildCompressPrompt(project, sessionID string, existing []Existing, turns []Turn) string {
	var sb strings.Builder
	sb.WriteString("Compress this chat session into durable memory for project \"")
	sb.WriteString(project)
	sb.WriteString("\".\n")
	if sessionID != "" {
		sb.WriteString("session_id=")
		sb.WriteString(sessionID)
		sb.WriteString("\n")
	}
	sb.WriteString("\nProduce ONE rich session_summary (max ~1800 characters) covering:\n")
	sb.WriteString("- Goal / what the user asked for\n")
	sb.WriteString("- Actions taken (files created/edited/deleted, commands, configs, packages — keep concrete paths/names)\n")
	sb.WriteString("- Outcomes (what worked, failed, left pending)\n")
	sb.WriteString("- Key decisions and open questions\n\n")
	sb.WriteString("Also extract decisions[]: only explicit durable project decisions (short standalone sentences).\n")
	sb.WriteString("Empty decisions[] is fine. Empty session_summary ONLY if the transcript is pure noise.\n")
	sb.WriteString("Never invent work that is not in the turns. Never drop concrete nouns.\n\n")
	sb.WriteString("Respond with JSON only:\n")
	sb.WriteString(`{"session_summary":"structured prose under 1800 chars","decisions":[{"key":"short/slug","content":"standalone sentence","level":"project","scope":"decision","confidence":0.0-1.0,"explicit":true}]}`)
	sb.WriteString("\n\nExisting memories (do not duplicate decisions):\n")
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
	sb.WriteString("\nSession turns:\n")
	for _, t := range CapCompressTurns(turns) {
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
