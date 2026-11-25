package agentsetup

import (
	"log"
	"strings"
)

// The desktop shows Share and RemoveMCP results to people as they are.
// Internal reasons ("unsafe skill path", "invalid target MCP map") are
// logged and replaced with one plain sentence that says what to do.

// agentLabel is the agent's everyday name ("Gemini", not "gemini" or
// "Gemini CLI": the desktop hides sentences that mention a CLI).
func agentLabel(id string) string {
	for _, h := range harnesses {
		if h.id == id {
			return strings.TrimSuffix(h.name, " CLI")
		}
	}
	if id == "" {
		return "This agent"
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

// kindLabel names an item kind in the plural, as people say it.
func kindLabel(kind string) string {
	switch kind {
	case "mcp":
		return "tools"
	case "subagent":
		return "sub-agents"
	case "skill":
		return "skills"
	case "command":
		return "commands"
	case "rules":
		return "rules"
	}
	return "items"
}

func incompatibleSkip(to, from, kind string) string {
	return agentLabel(to) + " can't use " + agentLabel(from) + " " + kindLabel(kind) + ", so it was skipped."
}

func conflictSkip(to, kind, name string) string {
	return agentLabel(to) + " already has a different " + strings.TrimSuffix(kindLabel(kind), "s") + " named " + name + ", so it was left alone."
}

// plainShareError turns a Share failure into a sentence with one action and
// logs the original.
func plainShareError(err error, o ShareOptions) error {
	if err == nil {
		return nil
	}
	raw := err.Error()
	log.Printf("agentsetup: share %s %q from %s: %s", o.Item.Kind, o.Item.Name, o.From, raw)
	switch {
	case strings.Contains(raw, "requires Workspace"):
		return plainError("Choose a project folder first, then try again.")
	case strings.Contains(raw, "Codex") && strings.Contains(raw, "user scope only"):
		return plainError("Codex only takes this for all projects. Choose All projects, then try again.")
	case strings.Contains(raw, "Cursor rules") && strings.Contains(raw, "project scope only"):
		return plainError("Cursor only takes rules for one project. Choose a project, then try again.")
	case strings.Contains(raw, "not found in inventory"), strings.Contains(raw, "no longer exists"):
		return plainError("This item changed since the list was loaded. Refresh the list, then try again.")
	case strings.Contains(raw, "symlink"), strings.Contains(raw, "unsafe"), strings.Contains(raw, "outside allowed roots"):
		return plainError("This item's files are outside its folder, so Nexus didn't copy them, for safety.")
	}
	return plainError("Nexus couldn't copy this safely, so nothing was changed. Refresh the list, then try again.")
}

// plainRemoveError does the same for removing a tool from an agent.
func plainRemoveError(err error, agent, name string) error {
	if err == nil {
		return nil
	}
	raw := err.Error()
	log.Printf("agentsetup: remove %s %q: %s", agent, name, raw)
	if strings.Contains(raw, "not in the agent inventory") || strings.Contains(raw, "is no longer in") {
		return plainError(name + " is no longer in " + agentLabel(agent) + "'s settings. Refresh the list.")
	}
	return plainError("Nexus couldn't safely change " + agentLabel(agent) + "'s settings file, so it was left as it was.")
}

type plainError string

func (e plainError) Error() string { return string(e) }
