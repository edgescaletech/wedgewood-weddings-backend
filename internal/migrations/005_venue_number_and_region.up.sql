-- Migration 005: Add venue_number and region to venues table

ALTER TABLE venues ADD COLUMN IF NOT EXISTS venue_number VARCHAR(20);
ALTER TABLE venues ADD COLUMN IF NOT EXISTS region VARCHAR(100);

CREATE INDEX IF NOT EXISTS ix_venues_venue_number ON venues(venue_number);
CREATE INDEX IF NOT EXISTS ix_venues_region ON venues(region);
