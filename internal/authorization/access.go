package authorization

import (
	"context"
	"database/sql"

	"github.com/lib/pq"
)

// Grants holds the roles and permissions assigned directly at a scope.
type Grants struct {
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

type ResourceGrants struct {
	ID         string
	ExternalID string
	Grants     Grants
}

type Access struct {
	Global    Grants
	Resources []ResourceGrants
}

// ListAccess reads direct grants and active resources of one type in a single
// snapshot. The supplied global role may enumerate resources without a grant
// on each one. Other global grants are returned separately for inheritance.
func (service *Service) ListAccess(ctx context.Context, userID, resourceType, allResourcesRoleID string) (Access, error) {
	rows, err := service.DB.QueryContext(ctx, `
WITH grants AS (`+activeUserGrants+`),
scopes AS (
    SELECT NULL::uuid AS resource_id, NULL::text AS external_id, role_id
    FROM grants WHERE resource_id IS NULL
    UNION ALL
    SELECT rr.id, rr.external_id, direct.role_id
    FROM rbac_resources rr
    LEFT JOIN grants direct ON direct.resource_id = rr.id
    WHERE rr.resource_type = $2 AND rr.is_active
      AND (direct.role_id IS NOT NULL OR EXISTS (
          SELECT 1 FROM grants WHERE resource_id IS NULL AND role_id = $3
      ))
)
SELECT s.resource_id, s.external_id,
       COALESCE(array_agg(DISTINCT r.name ORDER BY r.name) FILTER (WHERE r.name IS NOT NULL), '{}'),
       COALESCE(array_agg(DISTINCT p.key ORDER BY p.key) FILTER (WHERE p.key IS NOT NULL), '{}')
FROM scopes s
LEFT JOIN roles r ON r.id = s.role_id
LEFT JOIN role_permissions rp ON rp.role_id = r.id
LEFT JOIN permissions p ON p.id = rp.permission_id
GROUP BY s.resource_id, s.external_id
ORDER BY s.external_id NULLS FIRST`, userID, resourceType, allResourcesRoleID)
	if err != nil {
		return Access{}, err
	}
	defer rows.Close()

	access := Access{Global: Grants{Roles: []string{}, Permissions: []string{}}}
	for rows.Next() {
		var resourceID, externalID sql.NullString
		var roles, permissions pq.StringArray
		if err := rows.Scan(&resourceID, &externalID, &roles, &permissions); err != nil {
			return Access{}, err
		}
		grants := Grants{Roles: roles, Permissions: permissions}
		if !resourceID.Valid {
			access.Global = grants
			continue
		}
		access.Resources = append(access.Resources, ResourceGrants{
			ID: resourceID.String, ExternalID: externalID.String, Grants: grants,
		})
	}
	return access, rows.Err()
}
