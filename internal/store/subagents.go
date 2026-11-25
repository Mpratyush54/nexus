package store

import (
	"fmt"
	"regexp"
	"strings"
)

// Sub-agent runs are chats a harness started on an agent's behalf, not chats
// a person had. Claude Code saves each one as its own transcript,
// <project>/<parent session>/subagents/agent-<hex>.jsonl, so it is captured
// as a separate session with native id "agent-<hex>". Other harnesses mark
// them in the title ("Explore the repo (@general subagent)") or the native id.
//
// The timeline lists only the chats people had unless the caller opts in
// (include_subagents=1). The rules below match the clients'
// SessionLineage.IsSubagentSession, and the SQL patterns are the same
// expressions for Postgres (~* is case-insensitive like (?i)).
const (
	subagentNativeIDPattern     = `^agent-[0-9a-f]{8,}$`
	subagentNativeMarkerPattern = `(^|[^a-z0-9])sub-?agent([^a-z0-9]|$)`
	subagentTitlePattern        = `\(\s*(@[a-z0-9_.-]+\s+)?sub-?agent\s*\)\s*$`
)

var (
	subagentNativeID     = regexp.MustCompile(`(?i)` + subagentNativeIDPattern)
	subagentNativeMarker = regexp.MustCompile(`(?i)` + subagentNativeMarkerPattern)
	subagentTitle        = regexp.MustCompile(`(?i)` + subagentTitlePattern)
)

// IsSubagentRun reports whether a session is a sub-agent run: it has a
// recorded parent, a Claude Code "agent-<hex>" native id, a sub-agent marker
// in its native id, or a "(… subagent)" title suffix.
func IsSubagentRun(nativeID, title, parentNativeID string) bool {
	if strings.TrimSpace(parentNativeID) != "" {
		return true
	}
	nativeID = strings.TrimSpace(nativeID)
	if nativeID != "" && !strings.ContainsAny(nativeID, `/\`) &&
		(subagentNativeID.MatchString(nativeID) || subagentNativeMarker.MatchString(nativeID)) {
		return true
	}
	return subagentTitle.MatchString(title)
}

// subagentSQL is the IsSubagentRun rule over agent_sessions alias s. $a, $b
// and $c are the native id, native marker and title patterns.
func subagentSQL(a, b, c int) string {
	return fmt.Sprintf(`(s.parent_native_id IS NOT NULL
	      OR (STRPOS(s.native_id, '/') = 0 AND STRPOS(s.native_id, chr(92)) = 0 AND (s.native_id ~* $%d OR s.native_id ~* $%d))
	      OR COALESCE(s.title, '') ~* $%d)`, a, b, c)
}
