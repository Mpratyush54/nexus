-- Migration 002_events — Phase 2 event store (append-only log + fan-out).
-- Source of truth: implementation-plan.md §2.1 (transcribed exactly).
-- Cross-platform: pure DDL, no filesystem paths, no OS-specific constructs.
-- Target: AWS RDS Aurora Serverless v2 (PostgreSQL + pgvector).
-- Applies on top of 001_initial: events references projects/users/workspaces/
-- episodes from 001, and completes the deferred FK on episode_events.event_id
-- (001 left it as BIGINT with the comment "REFERENCES events(id), added in
-- migration 002"). session_id/agent_id stay plain UUID here: their parent
-- tables (sessions, agents) land in migrations 003/004.
-- Forward-only step 2 of N; rollback in 002_events.down.sql.

CREATE TABLE events (
    id              BIGSERIAL PRIMARY KEY,
    project_id      UUID NOT NULL REFERENCES projects(id),
    session_id      UUID,
    user_id         UUID REFERENCES users(id),
    agent_id        UUID,
    workspace_id    UUID REFERENCES workspaces(id),
    episode_id      UUID REFERENCES episodes(id),
    event_type      TEXT NOT NULL,
    payload         JSONB NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_events_project_time ON events(project_id, created_at);
CREATE INDEX idx_events_project_type ON events(project_id, event_type);
CREATE INDEX idx_events_episode ON events(episode_id);

-- Add FK to episode_events now that events table exists
ALTER TABLE episode_events
    ADD CONSTRAINT fk_episode_events_event
    FOREIGN KEY (event_id) REFERENCES events(id);

-- Real-time notification
CREATE OR REPLACE FUNCTION notify_event() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('events', json_build_object(
        'id', NEW.id,
        'project_id', NEW.project_id,
        'event_type', NEW.event_type
    )::text);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER events_notify AFTER INSERT ON events
    FOR EACH ROW EXECUTE FUNCTION notify_event();
