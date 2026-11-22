package store

// ProjectBriefKey is the key of the one pinned per-project brief memory: a
// short markdown summary of what the project is, its stack, and its most
// important decisions. The harvest worker maintains it and agents receive
// it on MCP initialize, project_knowledge, and empty memory_search results.
const ProjectBriefKey = "project/brief"

// ProjectBriefTag marks the brief row; brief source sessions are tagged
// "brief-source:<session id>" (not "session:<id>", so the brief never shows
// up as a memory harvested from those chats).
const (
	ProjectBriefTag       = "project-brief"
	ProjectBriefSourceTag = "brief-source:"
)
