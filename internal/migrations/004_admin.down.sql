-- RBAC rollback removes assignments, overrides and audit history; back up first.
DROP TABLE IF EXISTS admin_bootstrap;
DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS user_permission_overrides;
DROP TABLE IF EXISTS user_roles;
DROP FUNCTION IF EXISTS cleanup_role_overrides();
DROP FUNCTION IF EXISTS check_override_scope();
DROP FUNCTION IF EXISTS check_user_role_scope();
DROP TABLE IF EXISTS role_permissions;
DROP TABLE IF EXISTS permissions;
DROP TABLE IF EXISTS roles;
