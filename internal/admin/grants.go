package admin

import (
	"context"
	"database/sql"
)

// checkGrantCeiling verifies all permissions carried by a proposed role assignment.
func checkGrantCeiling(ctx context.Context, tx *sql.Tx, actor, roleID string, resource *resourceScope) error {
	var resourceID *string
	if resource != nil {
		resourceID = &resource.id
	}
	return checkRequiredPermissions(ctx, tx, actor, `
		SELECT p.key, $3::uuid AS resource_id
		FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
		WHERE rp.role_id = $2`, roleID, resourceID)
}

func checkRetainedGrants(ctx context.Context, tx *sql.Tx, actor, userID string) error {
	return checkRequiredPermissions(ctx, tx, actor, `
		SELECT p.key, CASE WHEN rr.is_active THEN rr.id END AS resource_id
		FROM user_roles ur
		JOIN role_permissions rp ON rp.role_id = ur.role_id
		JOIN permissions p ON p.id = rp.permission_id
		LEFT JOIN rbac_resources rr ON rr.id = ur.resource_id
		WHERE ur.user_id = $2
		UNION
		SELECT 'venues.manage_access', NULL::uuid
		WHERE EXISTS (
			SELECT 1 FROM user_roles ur
			JOIN rbac_resources rr ON rr.id = ur.resource_id
			WHERE ur.user_id = $2 AND rr.resource_type = 'venue'
		)`, userID)
}

// checkRequiredPermissions reads all decisions for an assignment or reactivation
// in one snapshot. The caller supplies a static SELECT of permission keys and scope IDs.
func checkRequiredPermissions(ctx context.Context, tx *sql.Tx, actor string, requiredSQL string, args ...any) error {
	rows, err := tx.QueryContext(ctx, `
		WITH required AS (`+requiredSQL+`), grants AS (`+activePermissionGrants+`)
		SELECT required.key, required.resource_id IS NOT NULL,
		       EXISTS (
		           SELECT 1 FROM grants g
		           WHERE g.permission_id = p.id AND g.resource_id IS NULL
		       ),
		       COALESCE(rr.is_active, false) AND EXISTS (
		           SELECT 1 FROM grants g
		           WHERE g.permission_id = p.id
		             AND (g.resource_id IS NULL OR g.resource_id = required.resource_id)
		       )
		FROM required
		LEFT JOIN permissions p ON p.key = required.key
		LEFT JOIN rbac_resources rr ON rr.id = required.resource_id`, append([]any{actor}, args...)...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var key string
		var scoped, globalGrant, scopedGrant bool
		if err := rows.Scan(&key, &scoped, &globalGrant, &scopedGrant); err != nil {
			return err
		}
		allowed := globalGrant
		if scoped && !administrativePermission(key) {
			allowed = scopedGrant
		}
		if !allowed {
			return errForbidden
		}
	}
	return rows.Err()
}
