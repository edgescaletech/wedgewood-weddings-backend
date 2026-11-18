// Package authorization resolves current PostgreSQL permissions for authenticated users.
package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
)

// Service resolves permissions without caching role assignments.
type Service struct{ DB *sql.DB }

// HasGlobalPermission checks grants without a resource.
func (service *Service) HasGlobalPermission(ctx context.Context, userID, permission string) (bool, error) {
	return HasGlobalPermissionTx(ctx, service.DB, userID, permission)
}

// HasResourcePermission checks an active resource, considering global and direct grants.
func (service *Service) HasResourcePermission(ctx context.Context, userID, permission, resourceID string) (bool, error) {
	return HasResourcePermissionTx(ctx, service.DB, userID, permission, resourceID)
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func HasGlobalPermissionTx(ctx context.Context, db queryRower, userID, permission string) (bool, error) {
	return hasPermission(ctx, db, userID, permission, nil)
}

func HasResourcePermissionTx(ctx context.Context, db queryRower, userID, permission, resourceID string) (bool, error) {
	if resourceID == "" {
		return false, nil
	}
	return hasPermission(ctx, db, userID, permission, resourceID)
}

func hasPermission(ctx context.Context, db queryRower, userID, permission string, resourceID any) (bool, error) {
	var allowed bool
	err := db.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1 FROM users u
    JOIN user_roles ur ON ur.user_id = u.id
    JOIN role_permissions rp ON rp.role_id = ur.role_id
    JOIN permissions p ON p.id = rp.permission_id
    WHERE u.id = $1 AND u.is_active AND p.key = $2
      AND (ur.resource_id IS NULL OR ur.resource_id = $3::uuid)
) AND ($3::uuid IS NULL OR EXISTS (
    SELECT 1 FROM rbac_resources WHERE id = $3 AND is_active
))`, userID, permission, resourceID).Scan(&allowed)
	return allowed, err
}

// HasPermissionMiddleware resolves the user from the application's authentication
// context. A nil resource resolver runs a global check. Scoped routes must pass
// a resolver that derives the registry ID from trusted resource data.
func (service *Service) HasPermissionMiddleware(
	permission string,
	resolveUser func(context.Context) (string, bool),
	resolveResource func(*http.Request) (string, error),
	next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		userID, ok := resolveUser(request.Context())
		if !ok || userID == "" {
			writeError(writer, http.StatusUnauthorized, "authentication required")
			return
		}
		var resourceID string
		if resolveResource != nil {
			var err error
			resourceID, err = resolveResource(request)
			if err != nil || resourceID == "" {
				writeError(writer, http.StatusBadRequest, "invalid resource")
				return
			}
		}
		var allowed bool
		var err error
		if resolveResource == nil {
			allowed, err = service.HasGlobalPermission(request.Context(), userID, permission)
		} else {
			allowed, err = service.HasResourcePermission(request.Context(), userID, permission, resourceID)
		}
		if err != nil {
			writeError(writer, http.StatusServiceUnavailable, "authorization unavailable")
			return
		}
		if !allowed {
			writeError(writer, http.StatusForbidden, "permission denied")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"error": message})
}
