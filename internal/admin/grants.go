package admin

import (
	"context"
	"database/sql"

	"wedding-project/internal/authorization"
)

// checkGrantCeiling applies the same policy to new grants and retained grants.
func checkGrantCeiling(ctx context.Context, tx *sql.Tx, actor, roleID string, resource *resourceScope) error {
	rows, err := tx.QueryContext(ctx, `SELECT p.key FROM role_permissions rp
		JOIN permissions p ON p.id=rp.permission_id WHERE rp.role_id=$1`, roleID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, key := range keys {
		var allowed bool
		if resource == nil || administrativePermission(key) {
			allowed, err = authorization.HasGlobalPermissionTx(ctx, tx, actor, key)
		} else {
			allowed, err = authorization.HasResourcePermissionTx(ctx, tx, actor, key, resource.id)
		}
		if err != nil {
			return err
		}
		if !allowed {
			return errForbidden
		}
	}
	return nil
}

func checkRetainedGrants(ctx context.Context, tx *sql.Tx, actor, userID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT ur.role_id, ur.resource_id, rr.resource_type, COALESCE(rr.is_active, false)
		FROM user_roles ur LEFT JOIN rbac_resources rr ON rr.id=ur.resource_id WHERE ur.user_id=$1`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type retainedGrant struct {
		roleID                   string
		resourceID, resourceType sql.NullString
		active                   bool
	}
	var grants []retainedGrant
	for rows.Next() {
		var grant retainedGrant
		if err := rows.Scan(&grant.roleID, &grant.resourceID, &grant.resourceType, &grant.active); err != nil {
			return err
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, grant := range grants {
		if grant.resourceType.String == "venue" {
			allowed, err := authorization.HasGlobalPermissionTx(ctx, tx, actor, "venues.manage_access")
			if err != nil {
				return err
			}
			if !allowed {
				return errForbidden
			}
		}
		var resource *resourceScope
		if grant.resourceID.Valid && grant.active {
			resource = &resourceScope{id: grant.resourceID.String}
		}
		// A suspended grant can resume later. Require global authority for its keys.
		if err := checkGrantCeiling(ctx, tx, actor, grant.roleID, resource); err != nil {
			return err
		}
	}
	return nil
}
