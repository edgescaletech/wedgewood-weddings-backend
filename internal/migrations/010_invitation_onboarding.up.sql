CREATE TABLE invitation_onboarding (
    invitation_id TEXT PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_sent_at TIMESTAMPTZ NOT NULL,
    workos_id TEXT,
    session_hash BYTEA UNIQUE,
    expires_at TIMESTAMPTZ,
    password_started_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX invitation_onboarding_one_password_attempt
    ON invitation_onboarding (user_id) WHERE password_started_at IS NOT NULL;
