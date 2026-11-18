BEGIN;

ALTER TABLE users ALTER COLUMN workos_id DROP NOT NULL;
ALTER TABLE users ADD COLUMN workos_invitation_id TEXT NOT NULL UNIQUE;
ALTER TABLE users ADD CONSTRAINT users_active_identity_required
    CHECK (NOT is_active OR (archived_at IS NULL AND workos_id IS NOT NULL));
CREATE UNIQUE INDEX users_pending_invitation_email ON users (lower(email))
    WHERE workos_id IS NULL AND archived_at IS NULL;

CREATE TABLE user_invitation_operations (
    id UUID PRIMARY KEY,
    email TEXT NOT NULL,
    request JSONB NOT NULL,
    resend_user_id UUID REFERENCES users(id),
    prior_invitation_id TEXT,
    invitation_id TEXT,
    delivered_id TEXT,
    delivery_mode TEXT CHECK (delivery_mode IN ('send', 'resend')),
    rejected_status INTEGER CHECK (rejected_status IN (400, 409)),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (invitation_id IS NULL OR rejected_status IS NULL)
);
CREATE UNIQUE INDEX user_invitation_creation_email ON user_invitation_operations(email)
    WHERE resend_user_id IS NULL AND rejected_status IS NULL;
CREATE UNIQUE INDEX user_invitation_resend_in_flight ON user_invitation_operations(resend_user_id)
    WHERE resend_user_id IS NOT NULL AND rejected_status IS NULL AND invitation_id IS NULL;

COMMIT;
