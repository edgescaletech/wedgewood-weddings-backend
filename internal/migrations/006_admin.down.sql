-- Drop audit history first so the migration 008 dependency blocks this rollback.
DROP TABLE IF EXISTS audit_logs;
DROP TRIGGER IF EXISTS venue_rbac_resource ON venues;
DROP FUNCTION IF EXISTS sync_venue_rbac_resource();
DROP TABLE IF EXISTS user_roles;
DROP FUNCTION IF EXISTS require_active_assignment_resource();
DROP TABLE IF EXISTS rbac_resources;
DROP FUNCTION IF EXISTS protect_rbac_resource_identity();
DROP FUNCTION IF EXISTS audit_rbac_resource_delete();
DROP TABLE IF EXISTS role_permissions;
DROP TABLE IF EXISTS permissions;
DROP TABLE IF EXISTS roles;
ALTER TABLE venues ALTER COLUMN is_active DROP NOT NULL;
