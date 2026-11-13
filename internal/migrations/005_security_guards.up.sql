-- Keep a dependency on permanent security history so even the historical 003
-- down script cannot DROP these tables without an explicit destructive CASCADE.
CREATE VIEW security_history_rollback_guard AS
SELECT b.singleton, b.completed_at, b.user_id, a.id AS audit_id
FROM admin_bootstrap b CROSS JOIN audit_logs a;

-- An assignment is an immutable grant. Moving it would skip DELETE cleanup and
-- could revive overrides later. Use the audited Admin delete/create operations.
CREATE FUNCTION reject_assignment_identity_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.user_id IS DISTINCT FROM OLD.user_id
       OR NEW.role_id IS DISTINCT FROM OLD.role_id
       OR NEW.venue_id IS DISTINCT FROM OLD.venue_id THEN
        RAISE EXCEPTION 'assignment identity is immutable; use authorized delete and create operations'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER immutable_assignment_identity BEFORE UPDATE ON user_roles
FOR EACH ROW EXECUTE FUNCTION reject_assignment_identity_update();
