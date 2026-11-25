package daemon

// Claude Code saves each sub-agent run as its own transcript beside its
// parent chat: <project>/<parent session>/subagents/agent-<hex>.jsonl. The
// run is captured as a session of its own (the desktop opens it with "Open
// as a chat") linked to its parent by parent_native_id.

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// errSubagentTranscriptGone: a sub-agent run's transcript no longer exists
// (its parent chat was deleted), so there is nothing to snapshot.
var errSubagentTranscriptGone = errors.New("snapshot: sub-agent transcript not found")

// claudeSubagentID is the file stem of a Claude Code sub-agent transcript.
var claudeSubagentID = regexp.MustCompile(`(?i)^agent-[0-9a-f]{8,}$`)

// isClaudeSubagentID reports a Claude Code sub-agent run's native id.
func isClaudeSubagentID(nativeID string) bool {
	return claudeSubagentID.MatchString(strings.TrimSpace(nativeID))
}

// subagentParentNativeID returns the parent session's native id for a Claude
// Code sub-agent transcript, <project>/<parent session>/subagents/agent-<hex>.jsonl,
// or "" for any other path.
func subagentParentNativeID(path string) string {
	parts := strings.Split(strings.ReplaceAll(strings.TrimSpace(path), `\`, "/"), "/")
	if len(parts) < 3 || !strings.EqualFold(parts[len(parts)-2], "subagents") {
		return ""
	}
	if !isClaudeSubagentID(sessionID(parts[len(parts)-1])) {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-3])
}

// findClaudeSubagentTranscript finds <project>/<parent>/subagents/<id>.jsonl
// in the given project folders ("" when there is none).
func findClaudeSubagentTranscript(projects []string, id string) string {
	for _, proj := range projects {
		matches, _ := filepath.Glob(filepath.Join(proj, "*", "subagents", id+".jsonl"))
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && !st.IsDir() {
				return m
			}
		}
	}
	return ""
}
