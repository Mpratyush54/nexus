-- 002_events.down.sql: Rollback the event store + real-time bus.
-- Reverse order: trigger -> function -> FKs -> indexes (via table drop) -> table.

DROP TRIGGER IF EXISTS events_notify ON events;
DROP FUNCTION IF EXISTS notify_event();

ALTER TABLE episode_events DROP CONSTRAINT IF EXISTS fk_episode_events_event;
ALTER TABLE memory_items DROP CONSTRAINT IF EXISTS fk_memory_source_event;

DROP TABLE IF EXISTS events;
