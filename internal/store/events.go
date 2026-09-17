package store

// events.go — Postgres append-only event log + LISTEN/NOTIFY bus (issue #9).
//
// Table + trigger come from migrations/002_events.up.sql. Writes are plain
// INSERTs (BIGSERIAL id, trigger pg_notifies on channel 'events'); reads are
// range scans by (project_id, id). Subscribe opens a dedicated connection —
// LISTEN cannot share a pooled conn — and re-fetches each notified row so
// subscribers always see the full payload, not just the notify header.

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

const eventColumns = `id, project_id, session_id, user_id, agent_id,
	workspace_id, episode_id, event_type, payload, created_at`

func scanEvent(row pgx.Row) (*Event, error) {
	var ev Event
	var sessionID, userID, agentID, workspaceID, episodeID *string
	var payload []byte
	if err := row.Scan(&ev.ID, &ev.ProjectID, &sessionID, &userID, &agentID,
		&workspaceID, &episodeID, &ev.EventType, &payload, &ev.CreatedAt); err != nil {
		return nil, err
	}
	if sessionID != nil {
		ev.SessionID = *sessionID
	}
	if userID != nil {
		ev.UserID = *userID
	}
	if workspaceID != nil {
		ev.WorkspaceID = *workspaceID
	}
	if agentID != nil {
		ev.AgentID = *agentID
	}
	if episodeID != nil {
		ev.EpisodeID = *episodeID
	}
	ev.Payload = unmarshalPayload(payload)
	return &ev, nil
}

// AppendEvent inserts one immutable event; id + created_at come back.
func (s *PostgresStore) AppendEvent(ctx context.Context, ev *Event) error {
	row := s.pool.QueryRow(ctx,
		`INSERT INTO events
			(project_id, session_id, user_id, agent_id,
			 workspace_id, episode_id, event_type, payload)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
		         $5::uuid, $6::uuid, $7, $8)
		 RETURNING id, created_at`,
		ev.ProjectID, nullText(ev.SessionID), nullText(ev.UserID),
		nullText(ev.AgentID), nullText(ev.WorkspaceID), nullText(ev.EpisodeID),
		ev.EventType, marshalPayload(ev.Payload))
	return row.Scan(&ev.ID, &ev.CreatedAt)
}

// ListEvents returns up to `limit` events for a project after sinceID,
// oldest first. limit <= 0 means "no cap" for backfills.
func (s *PostgresStore) ListEvents(ctx context.Context, projectID string, sinceID int64, limit int) ([]*Event, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+eventColumns+` FROM events
		  WHERE project_id = $1::uuid AND id > $2
		  ORDER BY id ASC
		  LIMIT $3`,
		projectID, sinceID, nilLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func nilLimit(limit int) any {
	if limit <= 0 {
		return nil // LIMIT NULL == no limit
	}
	return limit
}

// eventNotice is the JSON body fanned out by notify_event().
type eventNotice struct {
	ID        int64  `json:"id"`
	ProjectID string `json:"project_id"`
	EventType string `json:"event_type"`
}

// Subscribe LISTENs on 'events' and forwards full rows for one project.
// A dedicated connection is required (LISTEN is per-connection state), so
// the dsn retained by NewPostgresStore is reused here. Delivery is
// at-most-once per notification with the same non-blocking/drop contract as
// MemStore.Subscribe; gaps are backfilled via ListEvents.
func (s *PostgresStore) Subscribe(ctx context.Context, projectID string) (<-chan *Event, func(), error) {
	conn, err := pgx.Connect(ctx, s.dsn)
	if err != nil {
		return nil, nil, err
	}
	if _, err := conn.Exec(ctx, `LISTEN events`); err != nil {
		_ = conn.Close(ctx)
		return nil, nil, err
	}
	out := make(chan *Event, 64)
	life, cancel := context.WithCancel(context.Background())
	// Tie listener lifetime to the caller's ctx too.
	go func() {
		<-ctx.Done()
		cancel()
	}()
	go func() {
		defer close(out)
		defer cancel()
		defer conn.Close(life)
		for {
			notice, err := conn.WaitForNotification(life)
			if err != nil {
				return // cancelled or connection lost; caller resubscribes
			}
			var n eventNotice
			if err := json.Unmarshal([]byte(notice.Payload), &n); err != nil {
				continue
			}
			if projectID != "" && n.ProjectID != projectID {
				continue
			}
			ev, err := s.getEventByID(life, n.ID)
			if err != nil {
				continue
			}
			select {
			case out <- ev:
			case <-life.Done():
				return
			default: // slow subscriber: drop, backfill via ListEvents
			}
		}
	}()
	return out, cancel, nil
}

func (s *PostgresStore) getEventByID(ctx context.Context, id int64) (*Event, error) {
	return scanEvent(s.pool.QueryRow(ctx,
		`SELECT `+eventColumns+` FROM events WHERE id = $1`, id))
}
