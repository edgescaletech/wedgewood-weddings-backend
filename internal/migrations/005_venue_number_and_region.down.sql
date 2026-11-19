-- Migration 005 Down: Drop venue_number and region from venues table

DROP INDEX IF EXISTS ix_venues_region;
DROP INDEX IF EXISTS ix_venues_venue_number;

ALTER TABLE venues DROP COLUMN IF EXISTS region;
ALTER TABLE venues DROP COLUMN IF EXISTS venue_number;
