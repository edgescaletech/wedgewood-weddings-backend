BEGIN;

-- Freeze venue writes until existing venues are registered and the trigger is installed.
LOCK TABLE venues IN ACCESS EXCLUSIVE MODE;
UPDATE venues SET is_active = TRUE WHERE is_active IS NULL;
ALTER TABLE venues ALTER COLUMN is_active SET NOT NULL;

CREATE TABLE rbac_resources (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    resource_type TEXT NOT NULL CHECK (resource_type <> '' AND resource_type <> 'global'),
    external_id VARCHAR(64) NOT NULL CHECK (external_id <> ''),
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (resource_type, external_id),
    UNIQUE (id, resource_type)
);

INSERT INTO rbac_resources(resource_type, external_id, is_active, created_at)
SELECT 'venue', id, is_active, COALESCE(created_at, now()) FROM venues;

CREATE TABLE roles (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL,
    assignment_type TEXT NOT NULL CHECK (assignment_type <> ''),
    UNIQUE (id, assignment_type),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE permissions (
    id UUID PRIMARY KEY,
    key TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE role_permissions (
    role_id UUID NOT NULL REFERENCES roles(id),
    permission_id UUID NOT NULL REFERENCES permissions(id),
    PRIMARY KEY (role_id, permission_id)
);

CREATE TABLE user_roles (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id),
    role_id UUID NOT NULL,
    assignment_type TEXT NOT NULL,
    resource_id UUID,
    assigned_by UUID REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((resource_id IS NULL) = (assignment_type = 'global')),
    FOREIGN KEY (role_id, assignment_type) REFERENCES roles(id, assignment_type),
    FOREIGN KEY (resource_id, assignment_type) REFERENCES rbac_resources(id, resource_type) ON DELETE CASCADE
);

CREATE UNIQUE INDEX user_roles_global_unique ON user_roles(user_id, role_id) WHERE resource_id IS NULL;

CREATE UNIQUE INDEX user_roles_venue_unique ON user_roles(user_id, resource_id)
    WHERE assignment_type = 'venue';

CREATE INDEX user_roles_user_idx ON user_roles(user_id);

CREATE INDEX user_roles_resource_idx ON user_roles(resource_id);

CREATE INDEX user_roles_role_idx ON user_roles(role_id, assignment_type);

CREATE TABLE audit_logs (
    id UUID PRIMARY KEY,
    actor_user_id UUID REFERENCES users(id),
    action TEXT NOT NULL,
    target_type TEXT,
    target_id TEXT,
    -- Keep historical scope even after the resource is deleted.
    resource_type TEXT,
    resource_external_id VARCHAR(64),
    resource_id UUID,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Support newest-first audit listing, with the ID breaking timestamp ties.
CREATE INDEX audit_logs_created_at_id_idx
    ON audit_logs (created_at DESC, id DESC);

CREATE FUNCTION require_active_assignment_resource() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.resource_id IS NOT NULL THEN
        -- Hold a shared row lock until commit, serializing against status changes and deletion.
        PERFORM 1 FROM rbac_resources WHERE id = NEW.resource_id
            AND resource_type = NEW.assignment_type AND is_active FOR SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'assignment requires an active resource' USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER active_assignment_resource BEFORE INSERT ON user_roles
FOR EACH ROW EXECUTE FUNCTION require_active_assignment_resource();


CREATE FUNCTION protect_rbac_resource_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF ROW(NEW.id, NEW.resource_type, NEW.external_id)
       IS DISTINCT FROM ROW(OLD.id, OLD.resource_type, OLD.external_id) THEN
        RAISE EXCEPTION 'resource identity is immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER immutable_resource_identity BEFORE UPDATE ON rbac_resources
FOR EACH ROW EXECUTE FUNCTION protect_rbac_resource_identity();


CREATE FUNCTION audit_rbac_resource_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO audit_logs(id, action, target_type, target_id, resource_type,
                           resource_external_id, resource_id, metadata)
    SELECT gen_random_uuid(), 'role.remove.resource_deleted', 'user', user_id::text,
           OLD.resource_type, OLD.external_id, OLD.id,
           jsonb_build_object('assignmentId', id, 'roleId', role_id, 'assignedBy', assigned_by)
    FROM user_roles WHERE resource_id = OLD.id;
    RETURN OLD;
END $$;

CREATE TRIGGER audit_resource_delete BEFORE DELETE ON rbac_resources
FOR EACH ROW EXECUTE FUNCTION audit_rbac_resource_delete();


-- WedgeWood-specific adapter. The registry and assignment triggers above are generic.
CREATE FUNCTION sync_venue_rbac_resource() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM rbac_resources WHERE resource_type = 'venue' AND external_id = OLD.id;
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE' AND OLD.id IS DISTINCT FROM NEW.id THEN
        RAISE EXCEPTION 'venue identity is immutable' USING ERRCODE = '23514';
    END IF;
    INSERT INTO rbac_resources(resource_type, external_id, is_active)
    VALUES ('venue', NEW.id, NEW.is_active)
    ON CONFLICT (resource_type, external_id) DO UPDATE SET is_active = EXCLUDED.is_active;
    RETURN NEW;
END $$;

CREATE TRIGGER venue_rbac_resource AFTER INSERT OR UPDATE OF id, is_active OR DELETE ON venues
FOR EACH ROW EXECUTE FUNCTION sync_venue_rbac_resource();


INSERT INTO roles (id, name, description, assignment_type) VALUES
('00000000-0000-4000-8000-000000000001', 'Admin', 'Global administration', 'global'),
('00000000-0000-4000-8000-000000000002', 'General Manager', 'Venue management', 'venue'),
('00000000-0000-4000-8000-000000000003', 'Venue Scheduler', 'Venue scheduling', 'venue');


INSERT INTO permissions (id, key, description) VALUES
('10000000-0000-4000-8000-000000000001','schedules.view','View schedules'),
('10000000-0000-4000-8000-000000000002','schedules.create','Create schedules'),
('10000000-0000-4000-8000-000000000003','schedules.edit','Edit schedules'),
('10000000-0000-4000-8000-000000000004','schedules.approve','Approve schedules'),
('10000000-0000-4000-8000-000000000005','schedules.publish','Publish schedules'),
('10000000-0000-4000-8000-000000000006','schedules.cancel','Cancel schedules'),
('10000000-0000-4000-8000-000000000007','labor_goals.view','View labor goals'),
('10000000-0000-4000-8000-000000000008','labor_goals.manage','Manage labor goals'),
('10000000-0000-4000-8000-000000000009','users.view','View users'),
('10000000-0000-4000-8000-000000000010','users.manage','Manage users'),
('10000000-0000-4000-8000-000000000012','roles.view','View roles'),
('10000000-0000-4000-8000-000000000013','roles.manage','Manage role permissions'),
('10000000-0000-4000-8000-000000000014','roles.assign','Assign roles'),
('10000000-0000-4000-8000-000000000015','venues.manage_access','Manage venue access');


INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p WHERE
    r.name = 'Admin' OR
    (r.name = 'General Manager' AND p.key IN ('schedules.view','schedules.create','schedules.edit','schedules.approve','schedules.publish','schedules.cancel','labor_goals.view')) OR
    (r.name = 'Venue Scheduler' AND p.key IN ('schedules.view','schedules.create','schedules.edit','labor_goals.view'));

COMMIT;
