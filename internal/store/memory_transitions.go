// Memory lifecycle transitions (issue #35, plan §§2.7–2.8).
//
// This file backs the daemon Memory Processor's end-to-end wiring without
// touching memory.go (frozen by ownership): the PROPOSED auto-confirm sweep
// (plan §2.8), the SESSION → PROJECT promotion write (plan §2.7), and
// validated single-row status transitions. Every CHECK constraint from
// migrations/001 (content 20–2000 chars, level/scope/status sets,
// confidence 0–1) is mirrored by a pure validator so violations surface as
// Go errors before SQL ever runs.
//
// DB OWNERSHIP NOTE: this file defines the narrow MemoryStore over the
// shared DBTX interface (db.go) — *DB satisfies it with no adapter, and unit
// tests run on scripted fakes. The daemon package (internal/daemon) never
// imports internal/store: daemon.ProcessorStore declares ConfirmDue /
// CountKeySessions / PromoteKey with identical stdlib-only signatures, so
// *MemoryStore (ConfirmDue, PromoteKey) and *SessionStore
// (CountKeySessions, PromoteKey) satisfy those methods structurally; the
// thin daemon↔store adapter composing them lives in the server wiring
// (follow-up, see ADR-035).
//
// CONFIRM-TIER NOTE (no-migration constraint): memory_items has no
// confirm_due_at column, so SaveProposed encodes the processor's per-item
// ConfirmAfter (1h explicit / 4h high-confidence / 24h default) into the
// source tag ("processor:confirm_after=<go-duration>", see
// FormatConfirmSource) and ConfirmDue parses it back per row
// (ParseConfirmAfter, defaulting to ConfirmDueAfterDefault). A dedicated
// confirm_due_at TIMESTAMPTZ column is the follow-up migration; until then
// the source tag is the durable due record and survives restarts.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ConfirmDueAfterDefault is the conservative auto-confirm tier (plan §2.8:
// PROPOSED items confirm after 24h unless fast-tracked). It mirrors
// daemon.ConfirmAfterDefault without importing it (same decoupling as the
// Level constants). Rows whose source tag carries no parseable tier fall
// back to this.
const ConfirmDueAfterDefault = 24 * time.Hour

// ValidMemoryScopes mirrors the memory_items scope CHECK (migrations/001,
// plan §1.1). Levels and statuses reuse Level* / Status* from memory.go.
var ValidMemoryScopes = []string{"fact", "preference", "decision", "constraint", "pattern", "episode_summary"}

// allowedStatusTransitions is the lifecycle DAG: PROPOSED items are
// confirmed or rejected; CONFIRMED items are superseded (never edited
// in place — lineage); terminal states accept no outgoing edge.
var allowedStatusTransitions = map[string]map[string]bool{
	StatusProposed:  {StatusConfirmed: true, StatusRejected: true},
	StatusConfirmed: {StatusSuperseded: true},
}

// ValidateMemoryContent mirrors the content CHECK (20–2000 chars).
func ValidateMemoryContent(content string) error {
	n := len([]rune(content))
	if n < 20 || n > 2000 {
		return fmt.Errorf("store: content length %d outside 20-2000 CHECK window", n)
	}
	return nil
}

// ValidateMemoryLevel mirrors the level CHECK (migrations/001 plus the
// ephemeral 5th tier from migration 009, issue #30).
func ValidateMemoryLevel(level string) error {
	switch level {
	case LevelOrganization, LevelProject, LevelPersonal, LevelSession, LevelEphemeral:
		return nil
	default:
		return fmt.Errorf("store: invalid level %q (want organization|project|personal|session|ephemeral)", level)
	}
}

// ValidateMemoryScope mirrors the scope CHECK.
func ValidateMemoryScope(scope string) error {
	for _, s := range ValidMemoryScopes {
		if scope == s {
			return nil
		}
	}
	return fmt.Errorf("store: invalid scope %q (want fact|preference|decision|constraint|pattern|episode_summary)", scope)
}

// ValidateMemoryConfidence mirrors the confidence CHECK (0–1).
func ValidateMemoryConfidence(c float64) error {
	if c < 0 || c > 1 {
		return fmt.Errorf("store: confidence %v outside 0-1 CHECK window", c)
	}
	return nil
}

// normalizeStatus upper-cases and trims a lifecycle status for CHECK
// comparison (the CHECK set is uppercase; callers may pass anything).
func normalizeStatus(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// IsValidMemoryStatus reports whether s is in the memory_items status CHECK
// set (migrations/001: PROPOSED, CONFIRMED, REJECTED, SUPERSEDED).
func IsValidMemoryStatus(s string) bool {
	switch normalizeStatus(s) {
	case StatusProposed, StatusConfirmed, StatusRejected, StatusSuperseded:
		return true
	default:
		return false
	}
}

// ValidateStatusTransition enforces the lifecycle DAG and mirrors the CHECK
// set on both endpoints: unknown statuses and illegal edges both fail with
// errors naming the CHECK set, so callers can distinguish "bad value" from
// "bad edge" without touching SQL.
func ValidateStatusTransition(cur, next string) error {
	c, n := normalizeStatus(cur), normalizeStatus(next)
	if !IsValidMemoryStatus(c) {
		return fmt.Errorf("store: invalid current status %q (want PROPOSED|CONFIRMED|REJECTED|SUPERSEDED)", cur)
	}
	if !IsValidMemoryStatus(n) {
		return fmt.Errorf("store: invalid next status %q (want PROPOSED|CONFIRMED|REJECTED|SUPERSEDED)", next)
	}
	if allowedStatusTransitions[c][n] {
		return nil
	}
	return fmt.Errorf("store: illegal status transition %s -> %s (allowed: PROPOSED->{CONFIRMED,REJECTED}, CONFIRMED->SUPERSEDED)", c, n)
}

// FormatConfirmSource encodes the processor's ConfirmAfter tier into the
// source tag so the due instant survives restarts without a schema change.
// A non-positive tier records the plain "processor" source (ConfirmDue
// falls back to ConfirmDueAfterDefault for such rows).
func FormatConfirmSource(after time.Duration) string {
	if after <= 0 {
		return "processor"
	}
	return "processor:confirm_after=" + after.String()
}

// ParseConfirmAfter decodes the tier from a source tag. It returns
// (tier, true) on success, (0, false) when the tag carries no (or an
// invalid) tier — callers fall back to ConfirmDueAfterDefault.
func ParseConfirmAfter(source string) (time.Duration, bool) {
	const prefix = "confirm_after="
	i := strings.Index(source, prefix)
	if i < 0 {
		return 0, false
	}
	d, err := time.ParseDuration(strings.TrimSpace(source[i+len(prefix):]))
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

// IsConfirmDue is the pure per-row due predicate: a PROPOSED item proposed
// at proposedAt with tier after is due when proposedAt+after <= now.
func IsConfirmDue(proposedAt time.Time, after time.Duration, now time.Time) bool {
	if after <= 0 {
		after = ConfirmDueAfterDefault
	}
	if proposedAt.IsZero() {
		return false // unknown age: staleness must be proven, never assumed
	}
	return !proposedAt.Add(after).After(now)
}

// ProposedInput is the store-native write shape for one processor-extracted
// candidate. ConfirmAfter rides into the source tag (FormatConfirmSource);
// Embedding travels as a pgvector text literal (FormatEmbedding) or NULL.
type ProposedInput struct {
	ProjectID      string
	SessionID      string // "" = NULL (project/org/personal item)
	Key            string
	Content        string
	ContextSnippet string
	Level          string
	Scope          string
	Tags           []string
	Confidence     float64
	Embedding      []float32
	ConfirmAfter   time.Duration
}

// Validate mirrors every memory_items CHECK the INSERT must satisfy.
func (in ProposedInput) Validate() error {
	if strings.TrimSpace(in.ProjectID) == "" {
		return errors.New("store: proposed memory project id is required")
	}
	if strings.TrimSpace(in.Key) == "" {
		return errors.New("store: proposed memory key is required")
	}
	if err := ValidateMemoryContent(in.Content); err != nil {
		return err
	}
	if err := ValidateMemoryLevel(in.Level); err != nil {
		return err
	}
	if err := ValidateMemoryScope(in.Scope); err != nil {
		return err
	}
	if err := ValidateMemoryConfidence(in.Confidence); err != nil {
		return err
	}
	return nil
}

// BuildSaveProposedSQL renders the PROPOSED INSERT: status is always
// 'PROPOSED' (CHECK literal, never caller-supplied), the confirm tier is
// encoded in source, and a missing embedding stores NULL for later
// backfill. SessionID "" stores NULL.
func BuildSaveProposedSQL(in ProposedInput) (string, []any) {
	var emb any
	if len(in.Embedding) > 0 {
		emb = FormatEmbedding(in.Embedding)
	}
	return `INSERT INTO memory_items (project_id, session_id, key, content, context_snippet, ` +
			`level, scope, embedding, tags, confidence, status, source) ` +
			`VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'PROPOSED', $11) ` +
			`RETURNING id::TEXT AS id`,
		[]any{
			in.ProjectID, nullUUID(strings.TrimSpace(in.SessionID)), in.Key,
			in.Content, nullText(strings.TrimSpace(in.ContextSnippet)),
			in.Level, in.Scope, emb, in.Tags, in.Confidence,
			FormatConfirmSource(in.ConfirmAfter),
		}
}

// BuildListProposedSQL renders the sweep's candidate select: every PROPOSED
// row with the fields ConfirmDue needs (id, source tier tag, created_at as
// the proposal instant). Bounded (issue #131): an unbounded select loads
// the whole proposal backlog into memory and can exceed Postgres parameter
// limits on the follow-up UPDATE ... = ANY($1). Oldest first so the most
// overdue confirm; leftovers ride the next sweep tick.
func BuildListProposedSQL() string {
	return `SELECT id::TEXT AS id, COALESCE(source, '') AS source, created_at ` +
		`FROM memory_items WHERE status = 'PROPOSED' ` +
		`ORDER BY created_at ASC LIMIT 500`
}

// BuildConfirmIDsSQL renders the sweep's confirm write: PROPOSED → CONFIRMED
// on the due id set (the status predicate keeps the write idempotent under
// concurrent sweepers). ids compare as TEXT so plain []string args work.
func BuildConfirmIDsSQL() string {
	return `UPDATE memory_items SET status = 'CONFIRMED', updated_at = now() ` +
		`WHERE id::TEXT = ANY($1) AND status = 'PROPOSED'`
}

// BuildPromoteKeySQL renders the plan §2.7 promotion write: every
// session-scoped row under key becomes project-scoped by NULL-ing
// session_id and flipping level to 'project'. The session_id IS NOT NULL
// guard keeps the write idempotent (re-promotion touches zero rows).
func BuildPromoteKeySQL() string {
	// Guards (issue #131): only live session rows promote — REJECTED /
	// SUPERSEDED rows stay terminal (no resurrection), and only
	// level='session' rows qualify (project rows are already there).
	return `UPDATE memory_items SET session_id = NULL, level = 'project', updated_at = now() ` +
		`WHERE project_id = $1::uuid AND key = $2 AND session_id IS NOT NULL ` +
		`AND level = 'session' AND status IN ('PROPOSED','CONFIRMED')`
}

// BuildSetStatusSQL renders a validated single-row transition. Both the id
// and the expected current status are predicates: a concurrent transition
// touches zero rows and surfaces as ErrNotFound instead of a silent
// overwrite.
func BuildSetStatusSQL() string {
	return `UPDATE memory_items SET status = $1, updated_at = now() ` +
		`WHERE id::TEXT = $2 AND status = $3`
}

// MemoryStore is memory-lifecycle writes over any DBTX (pool, transaction,
// fake). Reads (Search) stay in memory.go; session scoping predicates stay
// in sessions.go.
type MemoryStore struct {
	db DBTX
}

// NewMemoryStore wires a MemoryStore to any DBTX.
func NewMemoryStore(db DBTX) *MemoryStore {
	return &MemoryStore{db: db}
}

// SaveProposed validates (CHECK-mirroring) and inserts one PROPOSED row,
// returning its id.
func (s *MemoryStore) SaveProposed(ctx context.Context, in ProposedInput) (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	query, args := BuildSaveProposedSQL(in)
	var id string
	if err := s.db.QueryRow(ctx, query, args...).Scan(&id); err != nil {
		return "", fmt.Errorf("store: save proposed: %w", err)
	}
	return id, nil
}

// ConfirmDue sweeps PROPOSED → CONFIRMED for rows whose created_at + tier
// <= now (tier from the source tag, default ConfirmDueAfterDefault). It
// returns the confirmed count. With no due rows it issues no UPDATE.
func (s *MemoryStore) ConfirmDue(ctx context.Context, now time.Time) (int64, error) {
	rows, err := s.db.Query(ctx, BuildListProposedSQL())
	if err != nil {
		return 0, fmt.Errorf("store: list proposed: %w", err)
	}
	defer rows.Close()
	var due []string
	for rows.Next() {
		var id, source string
		var createdAt time.Time
		if err := rows.Scan(&id, &source, &createdAt); err != nil {
			return 0, fmt.Errorf("store: list proposed scan: %w", err)
		}
		after, ok := ParseConfirmAfter(source)
		if !ok {
			after = ConfirmDueAfterDefault
		}
		if IsConfirmDue(createdAt, after, now) {
			due = append(due, id)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("store: list proposed rows: %w", err)
	}
	if len(due) == 0 {
		return 0, nil
	}
	tag, err := s.db.Exec(ctx, BuildConfirmIDsSQL(), due)
	if err != nil {
		return 0, fmt.Errorf("store: confirm due: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PromoteKey flips every session-scoped row under key to project scope
// (session_id → NULL, level → 'project') and returns the touched row
// count. Empty key/project fail before any SQL.
func (s *MemoryStore) PromoteKey(ctx context.Context, projectID, key string) (int64, error) {
	if strings.TrimSpace(projectID) == "" {
		return 0, errors.New("store: promote requires a project id")
	}
	if strings.TrimSpace(key) == "" {
		return 0, errors.New("store: promote requires a key")
	}
	tag, err := s.db.Exec(ctx, BuildPromoteKeySQL(), projectID, key)
	if err != nil {
		return 0, fmt.Errorf("store: promote key: %w", err)
	}
	return tag.RowsAffected(), nil
}

// SetStatus applies one validated lifecycle transition (PROPOSED →
// CONFIRMED/REJECTED, CONFIRMED → SUPERSEDED). Validation runs before SQL;
// zero touched rows (unknown id or concurrent transition) wrap ErrNotFound.
func (s *MemoryStore) SetStatus(ctx context.Context, id, from, to string) error {
	if err := ValidateStatusTransition(from, to); err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("store: transition requires a memory id")
	}
	tag, err := s.db.Exec(ctx, BuildSetStatusSQL(), normalizeStatus(to), id, normalizeStatus(from))
	if err != nil {
		return fmt.Errorf("store: transition status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("store: memory %s: %w", id, ErrNotFound)
	}
	return nil
}
