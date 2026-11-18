-- Local administrative profile and archival state; WorkOS remains the identity source.
ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN archived_at TIMESTAMPTZ;

-- Store a stable operation ID before contacting WorkOS. Never store passwords.
CREATE TABLE user_provisioning (
    id UUID PRIMARY KEY,
    email TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    workos_id TEXT UNIQUE REFERENCES users(workos_id)
);
