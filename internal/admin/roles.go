package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"wedding-project/internal/authorization"
)

// registerMutations mounts each action with the corresponding global permission.
func (server *Server) registerMutations(mux *http.ServeMux) {
	register := func(pattern, permission string, handler http.HandlerFunc) {
		mux.Handle(pattern, server.Auth.Middleware(server.Access.HasPermissionMiddleware(permission, nil, handler)))
	}
	register("POST /api/admin/users/{id}/activate", "users.manage", server.activate)
	register("POST /api/admin/users", "users.manage", server.createUser)
	register("POST /api/admin/users/{id}/deactivate", "users.manage", server.deactivate)
	register("DELETE /api/admin/users/{id}", "users.manage", server.archive)
	register("PATCH /api/admin/users/{id}", "users.manage", server.updateUser)
	register("POST /api/admin/users/{id}/roles", "roles.assign", server.assignRole)
	register("DELETE /api/admin/users/{id}/roles/{assignmentId}", "roles.assign", server.removeRole)
	register("PUT /api/admin/roles/{id}/permissions", "roles.manage", server.setRolePermissions)
	register("GET /api/admin/users/{id}/roles", "users.view", server.userRoles)
	register("GET /api/admin/users/{id}/permissions", "users.view", server.effectivePermissions)
	register("GET /api/admin/venues/{id}/users", "venues.manage_access", server.venueUsers)
}

func (server *Server) assignRole(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		RoleID  string  `json:"roleId"`
		VenueID *string `json:"venueId"`
	}
	if !decodeMutation(writer, request, &input) {
		return
	}
	userID := request.PathValue("id")
	server.mutate(writer, request, "roles.assign", func(ctx context.Context, tx *sql.Tx, actor string) error {
		if !validID(userID) || !validID(input.RoleID) || (input.VenueID != nil && (*input.VenueID == "" || len(*input.VenueID) > 64)) {
			return errInvalid
		}
		if sameUser(actor, userID) {
			return errForbidden
		}
		var scope string
		if err := tx.QueryRowContext(ctx, `SELECT scope FROM roles WHERE id=$1`, input.RoleID).Scan(&scope); err != nil {
			return err
		}
		if (scope == "global") != (input.VenueID == nil) {
			return errInvalid
		}
		if input.VenueID != nil {
			allowed, err := authorization.HasPermissionTx(ctx, tx, actor, "venues.manage_access", nil)
			if err != nil {
				return err
			}
			if !allowed {
				return errForbidden
			}
		}
		// An administrator may only grant permissions they possess themselves.
		rows, err := tx.QueryContext(ctx, `SELECT p.key FROM role_permissions rp JOIN permissions p ON p.id=rp.permission_id WHERE rp.role_id=$1`, input.RoleID)
		if err != nil {
			return err
		}
		var keys []string
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return err
			}
			keys = append(keys, key)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, key := range keys {
			allowed, err := authorization.HasPermissionTx(ctx, tx, actor, key, input.VenueID)
			if err != nil {
				return err
			}
			if !allowed {
				return errForbidden
			}
		}
		assignmentID := uuid.NewString()
		_, err = tx.ExecContext(ctx, `INSERT INTO user_roles(id,user_id,role_id,venue_id,assigned_by) VALUES($1,$2,$3,$4,$5)`, assignmentID, userID, input.RoleID, input.VenueID, actor)
		if err != nil {
			return constraintError(err)
		}
		return audit(ctx, tx, actor, "role.assign", "user", userID, input.VenueID, map[string]any{"roleId": input.RoleID, "assignmentId": assignmentID, "outcome": "success"})
	})
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
		var venue sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT role_id,venue_id FROM user_roles WHERE id=$1 AND user_id=$2`, assignmentID, userID).Scan(&roleID, &venue); err != nil {
			return err
		}
		var venueID *string
		if venue.Valid {
			venueID = &venue.String
			allowed, err := authorization.HasPermissionTx(ctx, tx, actor, "venues.manage_access", nil)
			if err != nil {
				return err
			}
			if !allowed {
				return errForbidden
			}
		}
		// With role-only access, removing a grant can only reduce permissions.
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_roles WHERE id=$1 AND user_id=$2`, assignmentID, userID); err != nil {
			return err
		}
		return audit(ctx, tx, actor, "role.remove", "user", userID, venueID, map[string]any{"roleId": roleID, "assignmentId": assignmentID, "outcome": "success"})
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
		if err := tx.QueryRowContext(ctx, `SELECT scope FROM roles WHERE id=$1`, roleID).Scan(&scope); err != nil {
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
			if assigned && !old[key] && (strings.HasPrefix(key, "users.") || strings.HasPrefix(key, "roles.") || key == "venues.manage_access") {
				return errForbidden
			}
			if scope == "venue" && (strings.HasPrefix(key, "users.") || strings.HasPrefix(key, "roles.") || key == "venues.manage_access") {
				return errInvalid
			}
			var exists bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM permissions WHERE key=$1)`, key).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return errInvalid
			}
			allowed, err := authorization.HasPermissionTx(ctx, tx, actor, key, nil)
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
				if !seen[key] && (strings.HasPrefix(key, "users.") || strings.HasPrefix(key, "roles.") || key == "venues.manage_access") {
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
	server.queryJSON(writer, request, `SELECT COALESCE(jsonb_agg(to_jsonb(t)),'[]'::jsonb) FROM (SELECT ur.id,ur.role_id,ur.venue_id,r.name,r.scope FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=$1 ORDER BY r.name,ur.venue_id) t`, id)
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
	server.queryJSON(writer, request, `SELECT COALESCE(jsonb_agg(to_jsonb(t)),'[]'::jsonb) FROM (SELECT u.id,u.email,r.name,ur.id AS assignment_id FROM user_roles ur JOIN users u ON u.id=ur.user_id JOIN roles r ON r.id=ur.role_id WHERE ur.venue_id=$1 ORDER BY u.email,ur.id LIMIT $2 OFFSET $3) t`, id, limit, offset)
}
