-- Store WorkOS identities and encrypted server-side sessions.
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
