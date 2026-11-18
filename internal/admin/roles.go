package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"wedding-project/internal/authorization"
)

// registerMutations mounts each action with the corresponding global permission.
func (server *Server) registerMutations(mux *http.ServeMux) {
	register := func(pattern, permission string, handler http.HandlerFunc) {
		mux.Handle(pattern, server.Auth.Middleware(server.Access.HasPermissionMiddleware(permission, authenticatedUserID, nil, handler)))
	}
	register("POST /api/admin/users/{id}/activate", "users.manage", server.activate)
	register("POST /api/admin/users", "users.manage", server.createUser)
	register("POST /api/admin/users/{id}/invitation/resend", "users.manage", server.resendInvitation)
	register("POST /api/admin/users/{id}/deactivate", "users.manage", server.deactivate)
	register("DELETE /api/admin/users/{id}", "users.manage", server.archive)
	register("PATCH /api/admin/users/{id}", "users.manage", server.updateUser)
	register("POST /api/admin/users/{id}/roles", "roles.assign", server.assignRole)
	register("DELETE /api/admin/users/{id}/roles/{assignmentId}", "roles.assign", server.removeRole)
	register("GET /api/admin/users/{id}/roles", "users.view", server.userRoles)
	register("GET /api/admin/users/{id}/permissions", "users.view", server.effectivePermissions)
	register("GET /api/admin/venues/{id}/users", "venues.manage_access", server.venueUsers)
}

func (server *Server) assignRole(writer http.ResponseWriter, request *http.Request) {
	var input roleGrant
	if !decodeMutation(writer, request, &input) {
		return
	}
	userID := request.PathValue("id")
	server.mutate(writer, request, "roles.assign", func(ctx context.Context, tx *sql.Tx, actor string) error {
		return assignGrant(ctx, tx, actor, userID, input, nil)
	})
}

type roleGrant struct {
	RoleID  string  `json:"roleId"`
	VenueID *string `json:"venueId"`
}

// assignGrant applies the same authorization and audit rules to standalone and invitation grants.
func assignGrant(ctx context.Context, tx *sql.Tx, actor, userID string, grant roleGrant, resourceIDBinding *string) error {
	if !validID(userID) || !validID(grant.RoleID) {
		return errInvalid
	}
	if sameUser(actor, userID) {
		return errForbidden
	}
	scope, resource, err := validateGrant(ctx, tx, actor, grant.RoleID, grant.VenueID, resourceIDBinding)
	if err != nil {
		return err
	}
	assignmentID := uuid.NewString()
	var resourceID *string
	if resource != nil {
		resourceID = &resource.id
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO user_roles(id,user_id,role_id,assignment_type,resource_id,assigned_by)
			VALUES($1,$2,$3,$4,$5,$6)`, assignmentID, userID, grant.RoleID, scope, resourceID, actor)
	if err != nil {
		return constraintError(err)
	}
	return audit(ctx, tx, actor, "role.assign", "user", userID, resource, map[string]any{"roleId": grant.RoleID, "assignmentId": assignmentID, "outcome": "success"})
}

// validateGrant resolves and locks the resource before checking the actor's ceiling.
func validateGrant(ctx context.Context, tx *sql.Tx, actor, roleID string, venueID, resourceIDBinding *string) (string, *resourceScope, error) {
	if venueID != nil && (*venueID == "" || len(*venueID) > 64) {
		return "", nil, errInvalid
	}
	var scope string
	if err := tx.QueryRowContext(ctx, `SELECT assignment_type FROM roles WHERE id=$1`, roleID).Scan(&scope); err != nil {
		return "", nil, err
	}
	if (venueID == nil && scope != "global") || (venueID != nil && scope != "venue") {
		return "", nil, errInvalid
	}
	var resource *resourceScope
	if venueID != nil {
		allowed, err := authorization.HasGlobalPermissionTx(ctx, tx, actor, "venues.manage_access")
		if err != nil {
			return "", nil, err
		}
		if !allowed {
			return "", nil, errForbidden
		}
		resource = &resourceScope{resourceType: scope, externalID: *venueID}
		var active bool
		err = tx.QueryRowContext(ctx, `SELECT id, is_active FROM rbac_resources
                WHERE resource_type=$1 AND external_id=$2
                  AND ($3::uuid IS NULL OR id=$3) FOR SHARE`,
			scope, resource.externalID, resourceIDBinding).Scan(&resource.id, &active)
		if resourceIDBinding != nil && errors.Is(err, sql.ErrNoRows) {
			return "", nil, errInvitationVenueChanged
		}
		if err != nil {
			return "", nil, err
		}
		if !active {
			return "", nil, errInvalid
		}
	}
	if err := checkGrantCeiling(ctx, tx, actor, roleID, resource); err != nil {
		return "", nil, err
	}
	return scope, resource, nil
}

func (server *Server) removeRole(writer http.ResponseWriter, request *http.Request) {
	userID, assignmentID := request.PathValue("id"), request.PathValue("assignmentId")
	server.mutate(writer, request, "roles.assign", func(ctx context.Context, tx *sql.Tx, actor string) error {
		if !validID(userID) || !validID(assignmentID) {
			return errInvalid
		}
		if sameUser(actor, userID) {
			return errForbidden
		}
		var roleID string
		var resourceID, resourceType, externalID sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT ur.role_id, rr.id, rr.resource_type, rr.external_id
			FROM user_roles ur LEFT JOIN rbac_resources rr ON rr.id=ur.resource_id
			WHERE ur.id=$1 AND ur.user_id=$2 AND ur.assignment_type IN ('global','venue')`,
			assignmentID, userID).Scan(&roleID, &resourceID, &resourceType, &externalID); err != nil {
			return err
		}
		var resource *resourceScope
		if resourceID.Valid {
			resource = &resourceScope{id: resourceID.String, resourceType: resourceType.String, externalID: externalID.String}
			allowed, err := authorization.HasGlobalPermissionTx(ctx, tx, actor, "venues.manage_access")
			if err != nil {
				return err
			}
			if !allowed {
				return errForbidden
			}
		}
		// With role-only access, removing a grant can only reduce permissions.
		result, err := tx.ExecContext(ctx, `DELETE FROM user_roles WHERE id=$1 AND user_id=$2`, assignmentID, userID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return errNotFound
		}
		return audit(ctx, tx, actor, "role.remove", "user", userID, resource, map[string]any{"roleId": roleID, "assignmentId": assignmentID, "outcome": "success"})
	})
}

func (server *Server) setRolePermissions(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Permissions []string `json:"permissions"`
	}
	if !decodeMutation(writer, request, &input) {
		return
	}
	roleID := request.PathValue("id")
	server.mutate(writer, request, "roles.manage", func(ctx context.Context, tx *sql.Tx, actor string) error {
		if !validID(roleID) || input.Permissions == nil || len(input.Permissions) > 100 {
			return errInvalid
		}
		var scope string
		if err := tx.QueryRowContext(ctx, `SELECT assignment_type FROM roles WHERE id=$1`, roleID).Scan(&scope); err != nil {
			return err
		}
		var assigned bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM user_roles WHERE user_id=$1 AND role_id=$2)`, actor, roleID).Scan(&assigned); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT p.key FROM role_permissions rp JOIN permissions p ON p.id=rp.permission_id WHERE rp.role_id=$1 ORDER BY p.key`, roleID)
		if err != nil {
			return err
		}
		oldPermissions := []string{}
		old := map[string]bool{}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return err
			}
			old[key] = true
			oldPermissions = append(oldPermissions, key)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, key := range input.Permissions {
			if seen[key] || key == "" {
				return errInvalid
			}
			seen[key] = true
			if assigned && !old[key] && administrativePermission(key) {
				return errForbidden
			}
			if scope != "global" && administrativePermission(key) {
				return errInvalid
			}
			var exists bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM permissions WHERE key=$1)`, key).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return errInvalid
			}
			allowed, err := authorization.HasGlobalPermissionTx(ctx, tx, actor, key)
			if err != nil {
				return err
			}
			if !allowed {
				return errForbidden
			}
		}
		// An actor may not change their own inherited management capabilities.
		if assigned {
			for _, key := range oldPermissions {
				if !seen[key] && administrativePermission(key) {
					return errForbidden
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM role_permissions WHERE role_id=$1`, roleID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO role_permissions(role_id,permission_id) SELECT $1,id FROM permissions WHERE key=ANY($2)`, roleID, pq.Array(input.Permissions)); err != nil {
			return err
		}
		return audit(ctx, tx, actor, "role.permissions.update", "role", roleID, nil, map[string]any{"previousPermissions": oldPermissions, "permissions": input.Permissions, "outcome": "success"})
	})
}

func constraintError(err error) error {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && (pqErr.Code == "23503" || pqErr.Code == "23505" || pqErr.Code == "23514" || pqErr.Code == "P0001") {
		return errInvalid
	}
	return err
}

func (server *Server) userRoles(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if !validID(id) {
		mutationError(writer, errInvalid)
		return
	}
	server.queryJSON(writer, request, `SELECT COALESCE(jsonb_agg(to_jsonb(t)),'[]'::jsonb) FROM (SELECT ur.id,ur.role_id,rr.external_id AS venue_id,r.name,r.assignment_type AS scope FROM user_roles ur JOIN roles r ON r.id=ur.role_id LEFT JOIN rbac_resources rr ON rr.id=ur.resource_id AND rr.resource_type='venue' WHERE ur.user_id=$1 ORDER BY r.name,rr.external_id) t`, id)
}

func (server *Server) venueUsers(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if id == "" || len(id) > 64 {
		mutationError(writer, errInvalid)
		return
	}
	limit, offset, err := pagination(request)
	if err != nil {
		mutationError(writer, errInvalid)
		return
	}
	server.queryJSON(writer, request, `SELECT COALESCE(jsonb_agg(to_jsonb(t)),'[]'::jsonb) FROM (SELECT u.id,u.email,r.name,ur.id AS assignment_id FROM user_roles ur JOIN users u ON u.id=ur.user_id JOIN roles r ON r.id=ur.role_id JOIN rbac_resources rr ON rr.id=ur.resource_id WHERE rr.resource_type='venue' AND rr.external_id=$1 ORDER BY u.email,ur.id LIMIT $2 OFFSET $3) t`, id, limit, offset)
}
