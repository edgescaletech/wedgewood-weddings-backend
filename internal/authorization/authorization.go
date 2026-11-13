// Package authorization resolves current PostgreSQL permissions for authenticated users.
package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"wedding-project/internal/auth"
)

// Service resolves permissions without caching role assignments or overrides.
type Service struct{ DB *sql.DB }

// HasPermission enforces active status, scope, and deny-before-allow precedence.
// Venue IDs are Salesforce IDs, matching the existing venues primary key.
func (service *Service) HasPermission(ctx context.Context, userID, permission string, venueID *string) (bool, error) {
	return hasPermission(ctx, service.DB, userID, permission, venueID)
}

// HasPermissionTx evaluates permissions inside the caller's transaction.
func HasPermissionTx(ctx context.Context, tx *sql.Tx, userID, permission string, venueID *string) (bool, error) {
	return hasPermission(ctx, tx, userID, permission, venueID)
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func hasPermission(ctx context.Context, db queryRower, userID, permission string, venueID *string) (bool, error) {
	// Administrative capabilities are always global, even when a venue is supplied.
	if strings.HasPrefix(permission, "users.") || strings.HasPrefix(permission, "roles.") || permission == "venues.manage_access" {
		venueID = nil
	}
	var allowed bool
	err := db.QueryRowContext(ctx, `
WITH membership AS (
 SELECT EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id
   WHERE ur.user_id=$1 AND r.scope='global' AND ur.venue_id IS NULL) AS global_access,
        EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id
   WHERE ur.user_id=$1 AND r.scope='venue' AND ur.venue_id=$3) AS venue_access
), eligible AS (
 SELECT global_access, venue_access FROM membership
 WHERE global_access OR ($3::text IS NOT NULL AND venue_access)
), overrides AS (
 SELECT o.effect FROM user_permission_overrides o JOIN permissions p ON p.id=o.permission_id CROSS JOIN eligible e
 WHERE o.user_id=$1 AND p.key=$2 AND
   ((o.venue_id IS NULL AND e.global_access) OR (o.venue_id=$3 AND e.venue_access))
), inherited AS (
 SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id
 JOIN role_permissions rp ON rp.role_id=r.id JOIN permissions p ON p.id=rp.permission_id
 CROSS JOIN eligible e WHERE ur.user_id=$1 AND p.key=$2 AND
   ((r.scope='global' AND ur.venue_id IS NULL AND e.global_access) OR
    ($3::text IS NOT NULL AND r.scope='venue' AND ur.venue_id=$3 AND e.venue_access))
)
SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND is_active)
 AND EXISTS(SELECT 1 FROM eligible)
 AND NOT EXISTS(SELECT 1 FROM overrides WHERE effect='deny')
 AND (EXISTS(SELECT 1 FROM overrides WHERE effect='allow') OR EXISTS(SELECT 1 FROM inherited))`, userID, permission, venueID).Scan(&allowed)
	return allowed, err
}

// HasPermissionMiddleware requires an authenticated identity and effective permission.
// resolveVenue must derive a resource's venue from trusted data when needed.
func (service *Service) HasPermissionMiddleware(permission string, resolveVenue func(*http.Request) (*string, error), next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		identity, ok := auth.User(request.Context())
		if !ok {
			writeError(writer, http.StatusUnauthorized, "authentication required")
			return
		}
		var venueID *string
		if resolveVenue != nil {
			var err error
			venueID, err = resolveVenue(request)
			if err != nil || venueID == nil || *venueID == "" {
				writeError(writer, http.StatusBadRequest, "invalid venue")
				return
			}
		}
		allowed, err := service.HasPermission(request.Context(), identity.ID, permission, venueID)
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
