-- Reconcile databases initialized from either version 1 migration.
-- Phase 1: Core Database Schema for Page 1 (All Events) & Page 2 (Staffing Plan)
-- Enables sub-50ms reads, offline reliability, and Pub/Sub CDC real-time synchronization.

CREATE TABLE IF NOT EXISTS venues (
    id VARCHAR(64) PRIMARY KEY,              -- Salesforce Account ID (e.g. 0011P000012oMy9QAE)
    name VARCHAR(255) NOT NULL,
    property_name VARCHAR(255),
    general_manager VARCHAR(255),
    timezone VARCHAR(64) DEFAULT 'America/Denver',
    is_active BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS events (
    id VARCHAR(64) PRIMARY KEY,              -- Salesforce Opportunity ID (e.g. 006Pq00000bXfjpIAC)
    venue_id VARCHAR(64) REFERENCES venues(id) ON DELETE SET NULL,
    name VARCHAR(255) NOT NULL,
    event_date DATE NOT NULL,
    day_of_week VARCHAR(20),
    start_time VARCHAR(30),
    end_time VARCHAR(30),
    event_type VARCHAR(100),
    guest_count INT DEFAULT 0,
    under_21_count NUMERIC(6,1),
    over_21_count NUMERIC(6,1),
    stage_name VARCHAR(100),
    contract_signed BOOLEAN DEFAULT FALSE,
    venue_name VARCHAR(255),
    venue_property VARCHAR(255),
    venue_gm VARCHAR(255),
    ceremony_location VARCHAR(255),
    ceremony_start_time VARCHAR(50),
    ceremony_start_datetime VARCHAR(50),
    meal_service_time VARCHAR(50),
    meal_service_datetime VARCHAR(50),
    cocktail_hour_start_time VARCHAR(50),
    cocktail_hour_start_datetime VARCHAR(50),
    has_bar BOOLEAN DEFAULT FALSE,
    bar_location VARCHAR(255),
    bar_start_time VARCHAR(50),
    bar_start_datetime VARCHAR(50),
    bar_close_time VARCHAR(50),
    bar_close_datetime VARCHAR(50),
    bar_last_call_time VARCHAR(50),
    bar_last_call_datetime VARCHAR(50),
    anticipated_bar_activity TEXT,
    other_bar_notes TEXT,
    beverage_cart_details TEXT,
    signature_drinks TEXT,
    charger_plates TEXT,
    photo_booth BOOLEAN,
    photobooth_staffed BOOLEAN DEFAULT FALSE,
    package_name VARCHAR(100),
    coordinator_name VARCHAR(255),
    dj_name VARCHAR(255),
    decor_setup_time VARCHAR(50),
    beo_notes TEXT,
    notes TEXT,
    catering_details JSONB,                   -- CPQ Quote, Line items, Food & Beverage specifications
    raw_payload JSONB,                        -- Full Salesforce record snapshot for audit/replay
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- Indexes for ultra-fast queries on Page 1 and Page 2
CREATE INDEX IF NOT EXISTS idx_events_date ON events(event_date);
CREATE INDEX IF NOT EXISTS idx_events_venue_id ON events(venue_id);
CREATE INDEX IF NOT EXISTS idx_events_date_venue ON events(event_date, venue_id);

CREATE TABLE IF NOT EXISTS event_planning_notes (
    id BIGSERIAL PRIMARY KEY,
    event_id VARCHAR(64) NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    notes TEXT NOT NULL,
    updated_by VARCHAR(255),
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_event_notes_event_id ON event_planning_notes(event_id);

-- Pub/Sub CDC idempotency inbox and sync cursor tracking
CREATE TABLE IF NOT EXISTS integration_inbox (
    id BIGSERIAL PRIMARY KEY,
    source_system VARCHAR(50) NOT NULL DEFAULT 'salesforce_pubsub',
    message_key VARCHAR(255),
    replay_id BYTEA,
    entity_name VARCHAR(100),
    change_type VARCHAR(50),
    source_record_id VARCHAR(64),
    payload JSONB,
    status VARCHAR(50) DEFAULT 'processed',
    error_message TEXT,
    received_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS integration_sync_state (
    source_system VARCHAR(50) NOT NULL,
    subscription_key VARCHAR(100) NOT NULL,
    replay_id BYTEA,
    last_synced_at TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (source_system, subscription_key)
);


CREATE TABLE IF NOT EXISTS users (
 id UUID PRIMARY KEY,
 workos_id TEXT NOT NULL UNIQUE,
 email TEXT NOT NULL,
 is_active BOOLEAN NOT NULL DEFAULT FALSE,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS auth_sessions (
 id_hash BYTEA PRIMARY KEY,
 workos_id TEXT NOT NULL REFERENCES users(workos_id),
 sealed_session TEXT NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS auth_sessions_expires_at_idx ON auth_sessions(expires_at);
