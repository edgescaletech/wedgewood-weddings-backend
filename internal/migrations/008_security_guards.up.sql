-- Keep a dependency on audit history so the historical 006 down script
-- cannot drop audit_logs without an explicit destructive CASCADE.
CREATE VIEW security_history_rollback_guard AS
SELECT id AS audit_id FROM audit_logs;

-- Assignment identity is immutable. Change access with audited delete/create operations.
CREATE FUNCTION reject_assignment_identity_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.id IS DISTINCT FROM OLD.id
       OR NEW.user_id IS DISTINCT FROM OLD.user_id
       OR NEW.role_id IS DISTINCT FROM OLD.role_id
       OR NEW.assignment_type IS DISTINCT FROM OLD.assignment_type
       OR NEW.resource_id IS DISTINCT FROM OLD.resource_id THEN
        RAISE EXCEPTION 'assignment identity is immutable; use authorized delete and create operations'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER immutable_assignment_identity BEFORE UPDATE ON user_roles
FOR EACH ROW EXECUTE FUNCTION reject_assignment_identity_update();
