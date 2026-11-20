\set ON_ERROR_STOP on

BEGIN;

-- Serialize with Admin API mutations and other runs of this script.
SELECT pg_advisory_xact_lock(87239651);

-- psql quotes every operator-supplied value as a SQL string literal.
CREATE TEMP TABLE first_admin_input ON COMMIT DROP AS
SELECT
    btrim(:'workos_user_id') AS workos_user_id,
    btrim(:'workos_invitation_id') AS workos_invitation_id,
    lower(btrim(:'email')) AS email,
    btrim(:'display_name') AS display_name;

DO $seed$
DECLARE
    seed_input first_admin_input%ROWTYPE;
    admin_role_id UUID;
    seeded_user_id UUID := gen_random_uuid();
BEGIN
    SELECT * INTO STRICT seed_input FROM first_admin_input;

    IF seed_input.workos_user_id = '' OR seed_input.workos_invitation_id = ''
        OR seed_input.email = '' OR seed_input.display_name = '' THEN
        RAISE EXCEPTION 'WorkOS user ID, invitation ID, email, and display name are required';
    END IF;

    SELECT id INTO admin_role_id FROM roles
    WHERE name = 'Admin' AND assignment_type = 'global';

    IF admin_role_id IS NULL THEN
        RAISE EXCEPTION 'Global Admin role is missing. Apply the database migrations first.';
    END IF;

    IF EXISTS (SELECT 1 FROM user_roles WHERE role_id = admin_role_id) THEN
        RAISE EXCEPTION 'The global Admin role is already assigned. Use the Admin API for subsequent accounts.';
    END IF;

    IF EXISTS (
        SELECT 1 FROM users
        WHERE workos_id = seed_input.workos_user_id
            OR workos_invitation_id = seed_input.workos_invitation_id
            OR lower(email) = seed_input.email
    ) THEN
        RAISE EXCEPTION 'A local account already uses this WorkOS user ID, invitation ID, or email.';
    END IF;

    INSERT INTO users (id, workos_id, workos_invitation_id, email, display_name, is_active)
    VALUES (seeded_user_id, seed_input.workos_user_id, seed_input.workos_invitation_id,
            seed_input.email, seed_input.display_name, TRUE);

    INSERT INTO user_roles (id, user_id, role_id, assignment_type)
    VALUES (gen_random_uuid(), seeded_user_id, admin_role_id, 'global');

    -- The actor is NULL because this is a private database operator action.
    INSERT INTO audit_logs (id, action, target_type, target_id, metadata)
    VALUES (gen_random_uuid(), 'admin.seed', 'user', seeded_user_id::text,
            jsonb_build_object('roleId', admin_role_id, 'source', 'operator_sql'));
END
$seed$;

COMMIT;
