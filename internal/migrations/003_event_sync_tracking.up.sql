-- Migration 003: Add fields to support Async Event Sync & Health Tracking

-- 1. Extend integration_sync_state to track async job execution and health
ALTER TABLE integration_sync_state
    ADD COLUMN IF NOT EXISTS status VARCHAR(50) DEFAULT 'healthy',
    ADD COLUMN IF NOT EXISTS last_attempt_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_success_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_error TEXT,
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ DEFAULT NOW();

-- 2. Extend events to track sync freshness and active status
ALTER TABLE events
    ADD COLUMN IF NOT EXISTS last_synced_at TIMESTAMPTZ DEFAULT NOW(),
    ADD COLUMN IF NOT EXISTS salesforce_last_modified_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS is_active BOOLEAN DEFAULT TRUE;

-- Index for quickly filtering active events during planning
CREATE INDEX IF NOT EXISTS idx_events_is_active ON events(is_active) WHERE is_active = TRUE;
