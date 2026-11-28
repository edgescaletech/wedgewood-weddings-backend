CREATE TABLE unresolved_sync_records (
    id BIGSERIAL PRIMARY KEY,
    source_system VARCHAR(50) NOT NULL,
    subscription_key VARCHAR(100) NOT NULL,
    entity_name VARCHAR(100) NOT NULL DEFAULT 'Opportunity',
    source_record_id VARCHAR(64) NOT NULL,
    reason TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'unresolved'
        CHECK (status IN ('unresolved', 'restored', 'confirmed_deleted')),
    first_detected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_detected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    retry_count INTEGER NOT NULL DEFAULT 0 CHECK (retry_count >= 0),
    last_retried_at TIMESTAMPTZ,
    next_retry_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ,
    resolved_by TEXT,
    resolution_notes TEXT,
    UNIQUE (source_system, subscription_key, entity_name, source_record_id),
    CHECK ((status = 'unresolved' AND resolved_at IS NULL AND resolved_by IS NULL)
        OR (status <> 'unresolved' AND resolved_at IS NOT NULL AND resolved_by IS NOT NULL))
);
CREATE INDEX unresolved_sync_records_open ON unresolved_sync_records
    (source_system, subscription_key, next_retry_at, source_record_id) WHERE status = 'unresolved';
-- Event list/detail metadata lookups are by record, across subscriptions.
CREATE INDEX unresolved_sync_records_event ON unresolved_sync_records
    (source_system, entity_name, source_record_id) WHERE status = 'unresolved';
