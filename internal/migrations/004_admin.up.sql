-- Preserve Salesforce venue identifiers and existing authentication data.
CREATE TABLE roles (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL,
    scope TEXT NOT NULL CHECK (scope IN ('global', 'venue')),
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
    role_id UUID NOT NULL REFERENCES roles(id),
    venue_id VARCHAR(64) REFERENCES venues(id),
    assigned_by UUID REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX user_roles_global_unique ON user_roles(user_id, role_id) WHERE venue_id IS NULL;
CREATE UNIQUE INDEX user_roles_venue_unique ON user_roles(user_id, role_id, venue_id) WHERE venue_id IS NOT NULL;
CREATE INDEX user_roles_scope_idx ON user_roles(user_id, venue_id);
CREATE TABLE user_permission_overrides (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id),
    permission_id UUID NOT NULL REFERENCES permissions(id),
    venue_id VARCHAR(64) REFERENCES venues(id),
    effect TEXT NOT NULL CHECK (effect IN ('allow', 'deny')),
    assigned_by UUID REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX user_overrides_global_unique ON user_permission_overrides(user_id, permission_id) WHERE venue_id IS NULL;
CREATE UNIQUE INDEX user_overrides_venue_unique ON user_permission_overrides(user_id, permission_id, venue_id) WHERE venue_id IS NOT NULL;
CREATE INDEX user_overrides_scope_idx ON user_permission_overrides(user_id, venue_id);
CREATE TABLE audit_logs (
    id UUID PRIMARY KEY,
    actor_user_id UUID REFERENCES users(id),
    action TEXT NOT NULL,
    target_type TEXT,
    target_id TEXT,
    venue_id VARCHAR(64) REFERENCES venues(id),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE admin_bootstrap (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    completed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_id UUID NOT NULL REFERENCES users(id)
);

-- A role assignment must match the role's immutable global/venue scope.
CREATE FUNCTION check_user_role_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM roles WHERE id = NEW.role_id AND
        ((scope = 'global' AND NEW.venue_id IS NULL) OR (scope = 'venue' AND NEW.venue_id IS NOT NULL))) THEN
        RAISE EXCEPTION 'role scope does not match assignment';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER user_role_scope BEFORE INSERT OR UPDATE ON user_roles FOR EACH ROW EXECUTE FUNCTION check_user_role_scope();

-- Overrides cannot create global or venue membership on their own.
CREATE FUNCTION check_override_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id
        WHERE ur.user_id=NEW.user_id AND
        ((NEW.venue_id IS NULL AND r.scope='global' AND ur.venue_id IS NULL) OR
         (NEW.venue_id IS NOT NULL AND r.scope='venue' AND ur.venue_id=NEW.venue_id))) THEN
        RAISE EXCEPTION 'override requires a matching role assignment';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER override_scope BEFORE INSERT OR UPDATE ON user_permission_overrides FOR EACH ROW EXECUTE FUNCTION check_override_scope();

-- Revoking the last assignment for a scope also removes its overrides.
CREATE FUNCTION cleanup_role_overrides() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM user_roles WHERE user_id=OLD.user_id AND venue_id IS NOT DISTINCT FROM OLD.venue_id) THEN
        DELETE FROM user_permission_overrides WHERE user_id=OLD.user_id AND venue_id IS NOT DISTINCT FROM OLD.venue_id;
    END IF;
    RETURN OLD;
END $$;
CREATE TRIGGER cleanup_overrides AFTER DELETE ON user_roles FOR EACH ROW EXECUTE FUNCTION cleanup_role_overrides();

INSERT INTO roles (id, name, description, scope) VALUES
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
('10000000-0000-4000-8000-000000000011','users.manage_permissions','Manage user permissions'),
('10000000-0000-4000-8000-000000000012','roles.view','View roles'),
('10000000-0000-4000-8000-000000000013','roles.manage','Manage role permissions'),
('10000000-0000-4000-8000-000000000014','roles.assign','Assign roles'),
('10000000-0000-4000-8000-000000000015','venues.manage_access','Manage venue access');
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p WHERE
    r.name = 'Admin' OR
    (r.name = 'General Manager' AND p.key IN ('schedules.view','schedules.create','schedules.edit','schedules.approve','schedules.publish','schedules.cancel','labor_goals.view')) OR
    (r.name = 'Venue Scheduler' AND p.key IN ('schedules.view','schedules.create','schedules.edit','labor_goals.view'));
