-- Rollback Migration 003: Remove fields added for Async Event Sync Tracking

DROP INDEX IF EXISTS idx_events_is_active;

ALTER TABLE events
    DROP COLUMN IF EXISTS last_synced_at,
    DROP COLUMN IF EXISTS salesforce_last_modified_at,
    DROP COLUMN IF EXISTS is_active;

ALTER TABLE integration_sync_state
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS last_attempt_at,
    DROP COLUMN IF EXISTS last_success_at,
    DROP COLUMN IF EXISTS last_error,
    DROP COLUMN IF EXISTS updated_at;
