-- Rollback Phase 1 tables (reverse order to respect FK constraints)
DROP TABLE IF EXISTS integration_sync_state;
DROP TABLE IF EXISTS integration_inbox;
DROP TABLE IF EXISTS event_planning_notes;
DROP INDEX IF EXISTS idx_events_date_venue;
DROP INDEX IF EXISTS idx_events_venue_id;
DROP INDEX IF EXISTS idx_events_date;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS venues;
