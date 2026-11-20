-- Local administrative profile; WorkOS remains the identity source.
ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
