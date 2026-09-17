-- 002_events.up.sql: Append-only event store + real-time bus (Phase 2.1).
--
-- Depends on: 001_initial (projects, workspaces, episodes, episode_events).
-- Every message, tool call, git action, and decision produces one immutable row.
-- Real-time fan-out uses Postgres LISTEN/NOTIFY on channel 'events'.

-- ============================================================
-- EVENTS — immutable append-only log
-- ============================================================
CREATE TABLE IF NOT EXISTS events (
    id              BIGSERIAL PRIMARY KEY,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id      UUID,
    user_id         UUID REFERENCES users(id) ON DELETE SET NULL,
    agent_id        UUID,
    workspace_id    UUID REFERENCES workspaces(id) ON DELETE SET NULL,
    episode_id      UUID REFERENCES episodes(id) ON DELETE SET NULL,
    event_type      TEXT NOT NULL,
    payload         JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_events_project_time ON events(project_id, created_at);
CREATE INDEX IF NOT EXISTS idx_events_project_type ON events(project_id, event_type);
CREATE INDEX IF NOT EXISTS idx_events_episode ON events(episode_id) WHERE episode_id IS NOT NULL;

-- Now that events exists, wire the FK that 001 deliberately left deferred.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_episode_events_event'
    ) THEN
        ALTER TABLE episode_events
            ADD CONSTRAINT fk_episode_events_event
            FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE;
    END IF;
END
$$;

-- Link memory_items back to their originating event (001 left this as a bare BIGINT).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'fk_memory_source_event'
    ) THEN
        ALTER TABLE memory_items
            ADD CONSTRAINT fk_memory_source_event
            FOREIGN KEY (source_event_id) REFERENCES events(id) ON DELETE SET NULL;
    END IF;
END
$$;

-- ============================================================
-- REAL-TIME BUS — LISTEN/NOTIFY trigger
-- ============================================================
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

DROP TRIGGER IF EXISTS events_notify ON events;
CREATE TRIGGER events_notify AFTER INSERT ON events
    FOR EACH ROW EXECUTE FUNCTION notify_event();
