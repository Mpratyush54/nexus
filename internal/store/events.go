// Event store: append-only log with real-time fan-out (issue #9, plan §§2.1-2.2).
//
// The events table is the source of truth for everything the daemon observes:
// tool interceptions (Layer 1), transcript harvests (Layer 2), instruction
// file changes (Layer 3), and explicit memory/task/episode actions (Layer 4+).
// The Memory Processor (issue #10) consumes this stream; the WebSocket hub
// (Phase 3) fans it out to live collaborators.
//
// Concurrency: EventStore holds only a DBTX (pool-backed *DB in production).
// AppendEvent is a single INSERT ... RETURNING per call, so the pool
// multiplexes concurrent callers across connections — one shared EventStore
// is safe for concurrent use with no mutex (a mutex would only serialize what
// the pool already parallelizes).
//
// Real-time: Postgres LISTEN/NOTIFY via Subscribe, one dedicated pooled
// connection per subscriber, released on context cancel. Notifications carry
// only (id, project_id, event_type); subscribers hydrate full rows via
// GetEventByID and backfill missed history via ListEvents on (re)connect.
// Keeping NOTIFY payloads tiny makes redelivery idempotent (replay by id)
// and keeps per-message overhead constant regardless of payload size.
//
// OWNERSHIP NOTE (parallel-agent constraint): db.go, projects.go,
// workspaces.go and memory.go are NOT touched here. EventStore depends only
// on the DBTX/Querier/Rows seams those files already expose, and Subscribe
// takes *pgxpool.Pool directly because *DB keeps its pool private (no
// accessor exists and adding one would mean editing db.go). See ADR-009.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EventChannel is the Postgres NOTIFY channel the notify_event() trigger
// (migrations/002_events.up.sql) publishes to. One channel carries all
// projects; subscribers filter client-side by project so a single LISTEN
// connection multiplexes every project the process cares about.
const EventChannel = "events"

// Event types (plan §2.1 — the complete registry; AppendEvent rejects
// anything outside it so producer typos fail fast instead of polluting the
// log with unprocessable rows).
//
// NOTE: EventFileRead, EventFileModified, EventCommandExecuted and
// EventGitCommitted are declared in episodes.go (issue #11, which landed
// first and consumes them for arc detection); they are reused here, not
// redeclared, so the package has exactly one definition per type.
const (
	// Agent activity, continued (Layer 1 — tool interception).
	EventGitDiffViewed = "GIT_DIFF_VIEWED"

	// Conversation (Layer 2 — transcript harvesting).
	EventConversationTurn          = "CONVERSATION_TURN"
	EventSessionTranscriptComplete = "SESSION_TRANSCRIPT_COMPLETE"

	// Instruction files (Layer 3 — file watcher).
	EventInstructionFileChanged = "INSTRUCTION_FILE_CHANGED"

	// Explicit actions (Layer 4 — piggyback + voluntary).
	EventMemoryProposed   = "MEMORY_PROPOSED"
	EventMemoryConfirmed  = "MEMORY_CONFIRMED"
	EventMemoryRejected   = "MEMORY_REJECTED"
	EventMemorySuperseded = "MEMORY_SUPERSEDED"

	// Tasks.
	EventTaskCreated   = "TASK_CREATED"
	EventTaskUpdated   = "TASK_UPDATED"
	EventTaskCompleted = "TASK_COMPLETED"

	// Episodes.
	EventEpisodeOpened   = "EPISODE_OPENED"
	EventEpisodeUpdated  = "EPISODE_UPDATED"
	EventEpisodeResolved = "EPISODE_RESOLVED"

	// Lifecycle.
	EventMessageSent         = "MESSAGE_SENT"
	EventSessionStarted      = "SESSION_STARTED"
	EventSessionEnded        = "SESSION_ENDED"
	EventWorkspaceRegistered = "WORKSPACE_REGISTERED"
	EventWorkspaceOffline    = "WORKSPACE_OFFLINE"

	// Steering (live agent steering — issue #42, Phase 3 WS protocol §3.2
	// extension; canonical strings proposed by internal/steer).
	EventAgentInterruptRequested = "AGENT_INTERRUPT_REQUESTED"
	EventAgentSteerPrompt        = "AGENT_STEER_PROMPT"
	EventAgentPaused             = "AGENT_PAUSED"
	EventAgentResumed            = "AGENT_RESUMED"
)

// ValidEventTypes is the §2.1 registry backing IsValidEventType.
var ValidEventTypes = map[string]struct{}{
	EventFileRead: {}, EventFileModified: {}, EventCommandExecuted: {},
	EventGitCommitted: {}, EventGitDiffViewed: {},
	EventConversationTurn: {}, EventSessionTranscriptComplete: {},
	EventInstructionFileChanged: {},
	EventMemoryProposed:         {}, EventMemoryConfirmed: {},
	EventMemoryRejected: {}, EventMemorySuperseded: {},
	EventTaskCreated: {}, EventTaskUpdated: {}, EventTaskCompleted: {},
	EventEpisodeOpened: {}, EventEpisodeUpdated: {}, EventEpisodeResolved: {},
	EventMessageSent: {}, EventSessionStarted: {}, EventSessionEnded: {},
	EventWorkspaceRegistered: {}, EventWorkspaceOffline: {},
	EventAgentInterruptRequested: {}, EventAgentSteerPrompt: {},
	EventAgentPaused: {}, EventAgentResumed: {},
}

// IsValidEventType reports whether t is a known §2.1 event type. Matching is
// exact (case-sensitive): producers must use the constants above.
func IsValidEventType(t string) bool {
	_, ok := ValidEventTypes[t]
	return ok
}

// Event mirrors an events row (plan §2.1). Empty SessionID/UserID/AgentID/
// WorkspaceID/EpisodeID mean SQL NULL. Payload is always valid JSON
// (AppendEvent normalizes nil to {}).
type Event struct {
	ID          int64
	ProjectID   string
	SessionID   string
	UserID      string
	AgentID     string
	WorkspaceID string
	EpisodeID   string
	EventType   string
	Payload     json.RawMessage
	CreatedAt   time.Time
}

// AppendEventParams carries one event append. ProjectID and EventType are
// required; EventType must be a §2.1 constant. Payload is marshaled with
// MarshalEventPayload (nil becomes {}); all other IDs are optional (""
// means SQL NULL).
type AppendEventParams struct {
	ProjectID   string
	SessionID   string
	UserID      string
	AgentID     string
	WorkspaceID string
	EpisodeID   string
	EventType   string
	Payload     any
}

// EventFilter scopes a ListEvents replay. Zero value lists everything (with
// the default limit). Times are inclusive bounds on created_at.
type EventFilter struct {
	ProjectID  string
	SessionID  string
	EpisodeID  string
	EventTypes []string
	Since      *time.Time
	Until      *time.Time
	Limit      int
	Offset     int
}

// Replay bounds: ListEvents defaults to a bounded window and refuses
// unbounded scans so a runaway consumer cannot OOM the server; paginate with
// Offset for full history.
const (
	DefaultEventLimit = 100
	MaxEventLimit     = 1000
)

// LimitOrDefault clamps Limit into [1, MaxEventLimit].
func (f EventFilter) LimitOrDefault() int {
	if f.Limit <= 0 {
		return DefaultEventLimit
	}
	if f.Limit > MaxEventLimit {
		return MaxEventLimit
	}
	return f.Limit
}

// EventNotification is the parsed pg_notify payload from notify_event():
// just enough to route (project filter) and hydrate (GetEventByID) without
// paying full-row cost per message.
type EventNotification struct {
	ID        int64
	ProjectID string
	EventType string
}

// MarshalEventPayload normalizes an append payload to valid JSON for the
// JSONB column: nil becomes {} (the column is NOT NULL), []byte and
// json.RawMessage pass through after a validity check, everything else goes
// through json.Marshal (so unmarshalable values fail here, not in SQL).
func MarshalEventPayload(v any) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
	}
	switch t := v.(type) {
	case []byte:
		if len(t) == 0 {
			return []byte("{}"), nil
		}
		if !json.Valid(t) {
			return nil, errors.New("store: event payload bytes are not valid JSON")
		}
		return t, nil
	case json.RawMessage:
		if len(t) == 0 {
			return []byte("{}"), nil
		}
		if !json.Valid(t) {
			return nil, errors.New("store: event payload RawMessage is not valid JSON")
		}
		return []byte(t), nil
	case string:
		// A bare string is data, not raw JSON: quote it so the column
		// always holds a JSON value, never a bare Postgres string that
		// only sometimes parses.
		b, err := json.Marshal(t)
		if err != nil {
			return nil, fmt.Errorf("store: marshal event payload: %w", err)
		}
		return b, nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("store: marshal event payload: %w", err)
		}
		if string(b) == "null" {
			return []byte("{}"), nil
		}
		return b, nil
	}
}

// ParseEventNotification parses one pg_notify payload
// ({"id":N,"project_id":"...","event_type":"..."}) into an
// EventNotification. It is pure (no DB) so subscriber routing is unit
// testable; malformed payloads are skipped by Subscribe, never fatal.
func ParseEventNotification(payload string) (EventNotification, error) {
	var raw struct {
		ID        int64  `json:"id"`
		ProjectID string `json:"project_id"`
		EventType string `json:"event_type"`
	}
	dec := json.NewDecoder(strings.NewReader(payload))
	if err := dec.Decode(&raw); err != nil {
		return EventNotification{}, fmt.Errorf("store: parse event notification: %w", err)
	}
	if raw.ID <= 0 {
		return EventNotification{}, fmt.Errorf("store: parse event notification: missing or invalid id in %q", payload)
	}
	if strings.TrimSpace(raw.ProjectID) == "" {
		return EventNotification{}, fmt.Errorf("store: parse event notification: missing project_id in %q", payload)
	}
	if strings.TrimSpace(raw.EventType) == "" {
		return EventNotification{}, fmt.Errorf("store: parse event notification: missing event_type in %q", payload)
	}
	return EventNotification{ID: raw.ID, ProjectID: raw.ProjectID, EventType: raw.EventType}, nil
}

// deliverNotification is the client-side project filter for Subscribe: an
// empty filter receives every project, otherwise only exact matches.
func deliverNotification(n EventNotification, projectFilter string) bool {
	return projectFilter == "" || n.ProjectID == projectFilter
}

// eventColumns selects events with NULL UUIDs coalesced to "" and the JSONB
// payload cast to TEXT server-side, so rows scan into plain Go values with
// no driver-level JSON/UUID decoding involved.
const eventColumns = `id, ` +
	`project_id::TEXT AS project_id, ` +
	`COALESCE(session_id::TEXT, '') AS session_id, ` +
	`COALESCE(user_id::TEXT, '') AS user_id, ` +
	`COALESCE(agent_id::TEXT, '') AS agent_id, ` +
	`COALESCE(workspace_id::TEXT, '') AS workspace_id, ` +
	`COALESCE(episode_id::TEXT, '') AS episode_id, ` +
	`event_type, ` +
	`payload::TEXT AS payload, ` +
	`created_at`

// scanEvent scans a full eventColumns row.
func scanEvent(row pgx.Row) (*Event, error) {
	var e Event
	var payload string
	if err := row.Scan(
		&e.ID, &e.ProjectID, &e.SessionID, &e.UserID, &e.AgentID,
		&e.WorkspaceID, &e.EpisodeID, &e.EventType, &payload, &e.CreatedAt,
	); err != nil {
		return nil, err
	}
	if payload == "" {
		payload = "{}"
	}
	e.Payload = json.RawMessage(payload)
	return &e, nil
}

// eventNullText maps "" to SQL NULL for nullable UUID columns. Named locally
// (not reusing projects.go's nullUUID) so this file stays self-contained
// under the parallel-ownership constraint.
func eventNullUUID(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}

// EventStore is event append + replay. It holds only a DBTX, so it shares
// the process pool and is safe for concurrent use (see package doc).
type EventStore struct {
	db DBTX
}

// NewEventStore wires an EventStore to any DBTX (pool, transaction, fake).
func NewEventStore(db DBTX) *EventStore {
	return &EventStore{db: db}
}

// AppendEvent validates params, marshals the payload, and inserts one row,
// returning the stored event (id + created_at assigned by the database).
// Validation happens before any SQL so callers get fast deterministic errors
// without a round-trip.
func (s *EventStore) AppendEvent(ctx context.Context, params AppendEventParams) (*Event, error) {
	projectID := strings.TrimSpace(params.ProjectID)
	if projectID == "" {
		return nil, errors.New("store: append event requires a project id")
	}
	if strings.TrimSpace(params.EventType) == "" {
		return nil, errors.New("store: append event requires an event type")
	}
	if !IsValidEventType(params.EventType) {
		return nil, fmt.Errorf("store: unknown event type %q", params.EventType)
	}
	payload, err := MarshalEventPayload(params.Payload)
	if err != nil {
		return nil, err
	}
	e, err := scanEvent(s.db.QueryRow(ctx,
		`INSERT INTO events
		 (project_id, session_id, user_id, agent_id, workspace_id, episode_id, event_type, payload)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8::JSONB)
		 RETURNING `+eventColumns,
		projectID,
		eventNullUUID(params.SessionID),
		eventNullUUID(params.UserID),
		eventNullUUID(params.AgentID),
		eventNullUUID(params.WorkspaceID),
		eventNullUUID(params.EpisodeID),
		params.EventType,
		string(payload)))
	if err != nil {
		return nil, fmt.Errorf("store: append event: %w", err)
	}
	return e, nil
}

// BuildListEventsSQL renders the replay query: one AND-arm per set filter,
// event types as a single = ANY($n) array match, inclusive created_at
// bounds, stable ORDER BY id ASC (append order — the only order replay may
// rely on), and clamped LIMIT/OFFSET. Placeholders are numbered sequentially.
func BuildListEventsSQL(f EventFilter) (string, []any) {
	var b strings.Builder
	b.WriteString(`SELECT ` + eventColumns + ` FROM events`)
	var conds []string
	var args []any
	if strings.TrimSpace(f.ProjectID) != "" {
		args = append(args, strings.TrimSpace(f.ProjectID))
		conds = append(conds, fmt.Sprintf("project_id = $%d", len(args)))
	}
	if strings.TrimSpace(f.SessionID) != "" {
		args = append(args, strings.TrimSpace(f.SessionID))
		conds = append(conds, fmt.Sprintf("session_id = $%d", len(args)))
	}
	if strings.TrimSpace(f.EpisodeID) != "" {
		args = append(args, strings.TrimSpace(f.EpisodeID))
		conds = append(conds, fmt.Sprintf("episode_id = $%d", len(args)))
	}
	if len(f.EventTypes) > 0 {
		args = append(args, f.EventTypes)
		conds = append(conds, fmt.Sprintf("event_type = ANY($%d)", len(args)))
	}
	if f.Since != nil {
		args = append(args, *f.Since)
		conds = append(conds, fmt.Sprintf("created_at >= $%d", len(args)))
	}
	if f.Until != nil {
		args = append(args, *f.Until)
		conds = append(conds, fmt.Sprintf("created_at <= $%d", len(args)))
	}
	if len(conds) > 0 {
		b.WriteString(" WHERE " + strings.Join(conds, " AND "))
	}
	b.WriteString(" ORDER BY id ASC")
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, f.LimitOrDefault())
	fmt.Fprintf(&b, " LIMIT $%d", len(args))
	args = append(args, offset)
	fmt.Fprintf(&b, " OFFSET $%d", len(args))
	return b.String(), args
}

// ListEvents replays the log through the filter (id-ascending). Callers
// resuming a subscription pass Since = last seen created_at (or paginate by
// id via repeated bounded windows) to backfill what NOTIFY may have dropped
// while they were disconnected.
func (s *EventStore) ListEvents(ctx context.Context, f EventFilter) ([]Event, error) {
	query, args := BuildListEventsSQL(f)
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var payload string
		if err := rows.Scan(
			&e.ID, &e.ProjectID, &e.SessionID, &e.UserID, &e.AgentID,
			&e.WorkspaceID, &e.EpisodeID, &e.EventType, &payload, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("store: list events scan: %w", err)
		}
		if payload == "" {
			payload = "{}"
		}
		e.Payload = json.RawMessage(payload)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list events rows: %w", err)
	}
	return out, nil
}

// GetEventByID fetches one event (the Subscribe hydration path) or a wrapped
// ErrNotFound.
func (s *EventStore) GetEventByID(ctx context.Context, id int64) (*Event, error) {
	e, err := scanEvent(s.db.QueryRow(ctx,
		`SELECT `+eventColumns+` FROM events WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("store: event %d: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get event: %w", err)
	}
	return e, nil
}

// Subscribe LISTENs on EventChannel and streams parsed notifications until
// ctx is canceled. Each call holds one dedicated pooled connection (LISTEN
// is per-connection state, so sharing would entangle filters); the
// connection is UNLISTENed and released when ctx ends, and the channel is
// then closed — range terminates exactly when the subscription does.
//
// Delivery contract: at-least-once per commit is NOT guaranteed (a
// notification sent while disconnected is lost), so callers MUST replay via
// ListEvents on (re)connect and treat notifications as wake-ups, hydrating
// via GetEventByID. Malformed payloads are skipped without killing the
// stream; a broken connection ends the stream (caller resubscribes).
// projectFilter == "" receives every project.
func Subscribe(ctx context.Context, pool *pgxpool.Pool, projectFilter string) (<-chan EventNotification, error) {
	if pool == nil {
		return nil, errors.New("store: subscribe requires a non-nil pool")
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: subscribe acquire: %w", err)
	}
	if _, err := conn.Exec(ctx, `LISTEN `+EventChannel); err != nil {
		conn.Release()
		return nil, fmt.Errorf("store: subscribe listen: %w", err)
	}
	out := make(chan EventNotification, 64)
	go func() {
		defer func() {
			// Best-effort UNLISTEN: a released connection that still
			// LISTENs accumulates server-side notifications for nobody.
			_, _ = conn.Exec(context.Background(), `UNLISTEN `+EventChannel)
			conn.Release()
			close(out)
		}()
		pgConn := conn.Conn()
		for {
			n, err := pgConn.WaitForNotification(ctx)
			if err != nil {
				return // ctx canceled or connection lost; caller resubscribes + replays
			}
			if n.Channel != EventChannel {
				continue
			}
			ev, err := ParseEventNotification(n.Payload)
			if err != nil {
				continue // malformed payload never blocks the stream
			}
			if !deliverNotification(ev, projectFilter) {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}
