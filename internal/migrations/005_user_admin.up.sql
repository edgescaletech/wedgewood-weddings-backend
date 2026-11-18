-- Local administrative profile and archival state; WorkOS remains the identity source.
ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN archived_at TIMESTAMPTZ;
