package store

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// placeholderTitlePattern matches the generic titles a capture stores before
// anyone names the chat: "Captured codex session", "New chat", "Codex chat".
// It is written so Go's regexp and PostgreSQL's ~* read it the same way.
const placeholderTitlePattern = `^\s*(captured\s+\S+\s+(session|snapshot)|new chat|untitled chat|session|(claude|claude code|codex|cursor|opencode|gemini|kimi|copilot|antigravity|windsurf|agent)\s+chat)\s*$`

var placeholderTitle = regexp.MustCompile(`(?i)` + placeholderTitlePattern)

// chatTitleMax is the length a derived title is cut to (at a word boundary).
const chatTitleMax = 60

// IsPlaceholderTitle reports whether a stored chat title names nothing: empty,
// or a generic placeholder a capture wrote. A title a person set is never one.
func IsPlaceholderTitle(title string) bool {
	title = strings.TrimSpace(title)
	return title == "" || placeholderTitle.MatchString(title)
}

// TitleFromRequest is a chat title from one user turn: the request without
// its harness wrapper, first line, cut near chatTitleMax at a word boundary.
// "" when the turn is only wrapper.
func TitleFromRequest(raw string) string {
	text := strings.TrimSpace(requestText(raw))
	for _, line := range strings.Split(text, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		return cutTitle(line)
	}
	return ""
}

func cutTitle(line string) string {
	chars := []rune(line)
	if len(chars) <= chatTitleMax {
		return line
	}
	cut := string(chars[:chatTitleMax])
	if i := strings.LastIndex(cut, " "); i >= chatTitleMax/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:-") + "…"
}

// titleFromTurns derives a title from the leading user turns (raw previews in
// idx order): the first of timelineFallbackTurns that has request text.
func titleFromTurns(raw []string) string {
	for i, text := range raw {
		if i >= timelineFallbackTurns {
			break
		}
		if t := TitleFromRequest(text); t != "" {
			return t
		}
	}
	return ""
}

// PresentTitle is the title a reader sees: the stored one, unless it is a
// placeholder and the first user requests name the chat better.
func PresentTitle(stored string, firstUserTurns []string) string {
	if !IsPlaceholderTitle(stored) {
		return stored
	}
	if derived := titleFromTurns(firstUserTurns); derived != "" {
		return derived
	}
	return stored
}

// SessionTitleSource gives the leading user turns of a chat (raw previews in
// idx order, at most timelineFallbackTurns) for PresentTitle.
type SessionTitleSource interface {
	FirstUserTurns(ctx context.Context, sessionID string) ([]string, error)
}

func (m *MemStore) FirstUserTurns(ctx context.Context, sessionID string) ([]string, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	var users []SessionTurn
	for _, turn := range m.sessionTurns[sessionID] {
		if turn.Role == "user" && strings.TrimSpace(turn.TextPreview) != "" {
			users = append(users, turn)
		}
	}
	sort.SliceStable(users, func(i, j int) bool { return users[i].Idx < users[j].Idx })
	out := make([]string, 0, timelineFallbackTurns)
	for i := 0; i < len(users) && i < timelineFallbackTurns; i++ {
		out = append(out, users[i].TextPreview)
	}
	return out, nil
}

func (s *PostgresStore) FirstUserTurns(ctx context.Context, sessionID string) ([]string, error) {
	if !looksLikeUUID(sessionID) {
		return nil, ErrNotFound
	}
	var out []string
	err := s.pool.QueryRow(ctx, `SELECT ARRAY(
		SELECT LEFT(t.text_preview, 4000) FROM session_turns t
		WHERE t.session_id = $1::uuid AND t.role = 'user'
		  AND NULLIF(BTRIM(t.text_preview), '') IS NOT NULL
		ORDER BY t.idx LIMIT $2)`, sessionID, timelineFallbackTurns).Scan(&out)
	return out, err
}
