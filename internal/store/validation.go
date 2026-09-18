package store

// validation.go — shared CHECK-mirroring validators + defensive-copy helpers
// (issues #30, #102, #110, #119).
//
// Additive only: no existing symbol is renamed or removed. Validators here
// mirror the SQL CHECK constraints so MemStore (no database) rejects exactly
// what Postgres rejects, instead of permitting states production cannot
// represent.

import (
	"fmt"
	"strings"
	"time"
)

// LevelEphemeral is the 5th memory tier (issue #30). The plan promises five
// tiers (organization | project | personal | session | ephemeral); migration
// 009 adds it to the level CHECK. Ephemeral rows are never inherited by new
// sessions and never promoted — they expire instead (see lifecycle.go).
const LevelEphemeral = "ephemeral"

// EmbeddingDim is the pgvector contract: memory_items.embedding and
// episodes.embedding are vector(1536) (migrations/001). Empty means "no
// embedding yet" (NULL); anything else must be exactly 1536 wide, or
// Postgres fails the query at runtime (issue #105).
const EmbeddingDim = 1536

// ValidateEmbeddingDim rejects wrong-width vectors before SQL. Nil/empty is
// the legitimate "no embedding" state and passes.
func ValidateEmbeddingDim(vec []float32) error {
	if len(vec) == 0 {
		return nil
	}
	if len(vec) != EmbeddingDim {
		return fmt.Errorf("store: embedding has %d dimensions, want %d (vector(1536) schema)", len(vec), EmbeddingDim)
	}
	return nil
}

// ValidEpisodeTypes mirrors the episode_type CHECK (migrations/001): six
// values including onboarding, which the old Go lists omitted (issue #119).
var ValidEpisodeTypes = []string{
	"bug_fix", "feature", "refactor", "incident", "investigation", "onboarding",
}

// IsValidEpisodeType reports whether t is in the episode_type CHECK set.
func IsValidEpisodeType(t string) bool {
	t = strings.ToLower(strings.TrimSpace(t))
	for _, v := range ValidEpisodeTypes {
		if t == v {
			return true
		}
	}
	return false
}

// ValidateEpisodeType rejects unknown or empty episode types before SQL.
func ValidateEpisodeType(t string) error {
	if !IsValidEpisodeType(t) {
		return fmt.Errorf("store: invalid episode_type %q (want bug_fix|feature|refactor|incident|investigation|onboarding)", t)
	}
	return nil
}

// ValidEpisodeStatuses mirrors the episode status CHECK (migrations/001).
var ValidEpisodeStatuses = []string{"OPEN", "INVESTIGATING", "RESOLVED", "WONT_FIX"}

// IsValidEpisodeStatus reports whether s is a known episode status.
func IsValidEpisodeStatus(s string) bool {
	s = strings.ToUpper(strings.TrimSpace(s))
	for _, v := range ValidEpisodeStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// ValidateEpisodeStatus rejects unknown episode statuses ("" is handled by
// callers defaulting to OPEN before calling this).
func ValidateEpisodeStatus(s string) error {
	if !IsValidEpisodeStatus(s) {
		return fmt.Errorf("store: invalid episode status %q (want OPEN|INVESTIGATING|RESOLVED|WONT_FIX)", s)
	}
	return nil
}

// LevelRank orders memory levels from broadest to narrowest scope. Used for
// deterministic tie-breaks and scope comparisons; higher = narrower.
// Unknown levels rank below every known tier.
func LevelRank(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case LevelOrganization:
		return 0
	case LevelProject:
		return 1
	case LevelPersonal:
		return 2
	case LevelSession:
		return 3
	case LevelEphemeral:
		return 4
	default:
		return -1
	}
}

// IsValidMemoryLevel reports whether level is a known tier, including the
// ephemeral 5th tier (issue #30). Prefer this over ValidateMemoryLevel when
// only a boolean is needed.
func IsValidMemoryLevel(level string) bool {
	return LevelRank(level) >= 0
}

// ValidateMemoryScopeRules enforces the level/session/user relational
// invariants that no single-column CHECK can express (issues #102, #119):
//
//   - level='session' requires session_id set, and any row carrying a
//     session_id must be level='session' (otherwise the row is invisible to
//     ListSessionVisibleMemories inheritance yet claims another scope).
//   - level='personal' requires user_id set (otherwise the row is
//     ownerless and the search scope predicate leaks it globally).
func ValidateMemoryScopeRules(level, sessionID, userID string) error {
	lvl := strings.ToLower(strings.TrimSpace(level))
	hasSession := strings.TrimSpace(sessionID) != ""
	if lvl == LevelSession && !hasSession {
		return fmt.Errorf("store: level 'session' requires session_id (invisible row otherwise)")
	}
	if hasSession && lvl != LevelSession {
		return fmt.Errorf("store: session_id set requires level 'session' (got %q)", level)
	}
	if lvl == LevelPersonal && strings.TrimSpace(userID) == "" {
		return fmt.Errorf("store: level 'personal' requires user_id (ownerless personal rows leak globally)")
	}
	return nil
}

// validateMemoryItemForCreate mirrors every memory_items CHECK the INSERT
// must satisfy (content 20-2000, level/scope/status sets, confidence 0-1,
// key required) plus the scope-combination rules. Both MemStore and
// PostgresStore call it before writing so violations surface as Go errors.
func validateMemoryItemForCreate(item *MemoryItem) error {
	if strings.TrimSpace(item.Key) == "" {
		return fmt.Errorf("store: memory key is required")
	}
	if err := ValidateMemoryContent(item.Content); err != nil {
		return err
	}
	level := item.Level
	if strings.TrimSpace(level) == "" {
		level = LevelProject
	}
	if !IsValidMemoryLevel(level) {
		return fmt.Errorf("store: invalid level %q (want organization|project|personal|session|ephemeral)", item.Level)
	}
	scope := item.Scope
	if strings.TrimSpace(scope) == "" {
		scope = "fact"
	}
	validScope := false
	for _, s := range ValidMemoryScopes {
		if scope == s {
			validScope = true
			break
		}
	}
	if !validScope {
		return fmt.Errorf("store: invalid scope %q (want fact|preference|decision|constraint|pattern|episode_summary)", item.Scope)
	}
	if item.Confidence != 0 {
		if err := ValidateMemoryConfidence(float64(item.Confidence)); err != nil {
			return err
		}
	}
	if err := ValidateEmbeddingDim(item.Embedding); err != nil {
		return err
	}
	status := item.Status
	if strings.TrimSpace(status) == "" {
		status = StatusProposed
	}
	if !IsValidMemoryStatus(status) {
		return fmt.Errorf("store: invalid status %q (want PROPOSED|CONFIRMED|REJECTED|SUPERSEDED)", item.Status)
	}
	return ValidateMemoryScopeRules(level, item.SessionID, item.UserID)
}

// ---- defensive copies (issue #110) ----

// cloneMemoryItem deep-copies a memory row (slices included) so getters can
// hand out values that callers may mutate freely. Empty slices stay non-nil
// (Postgres normalizes [] the same way).
func cloneMemoryItem(m *MemoryItem) *MemoryItem {
	if m == nil {
		return nil
	}
	cp := *m
	cp.Tags = append([]string{}, m.Tags...)
	cp.Embedding = append([]float32{}, m.Embedding...)
	return &cp
}

// cloneProject copies a project row.
func cloneProject(p *Project) *Project {
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

// cloneWorkspace copies a workspace row.
func cloneWorkspace(w *Workspace) *Workspace {
	if w == nil {
		return nil
	}
	cp := *w
	return &cp
}

// cloneEpisode deep-copies an episode row (slices included).
func cloneEpisode(e *Episode) *Episode {
	if e == nil {
		return nil
	}
	cp := *e
	cp.Tags = append([]string{}, e.Tags...)
	cp.Embedding = append([]float32{}, e.Embedding...)
	cp.FilesInvolved = append([]string{}, e.FilesInvolved...)
	cp.ErrorPatterns = append([]string{}, e.ErrorPatterns...)
	return &cp
}

// cloneEvent deep-copies an event row (payload map included).
func cloneEvent(e *Event) *Event {
	if e == nil {
		return nil
	}
	cp := *e
	if e.Payload != nil {
		cp.Payload = make(map[string]any, len(e.Payload))
		for k, v := range e.Payload {
			cp.Payload[k] = v
		}
	}
	return &cp
}

// nullTime maps the zero time to NULL for nullable TIMESTAMPTZ columns.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
