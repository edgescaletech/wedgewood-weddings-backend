package admin

import (
	"context"
	"database/sql"
	"strings"

	"wedding-project/internal/auth"
)

// resourceScope carries a verified registry identity into the audit transaction.
type resourceScope struct {
	id, resourceType, externalID string
}

func venueResource(ctx context.Context, db *sql.DB, venueID string) (string, error) {
	var id string
	err := db.QueryRowContext(ctx, `SELECT id FROM rbac_resources
		WHERE resource_type='venue' AND external_id=$1`, venueID).Scan(&id)
	return id, err
}

// These keys are global management operations in the WedgeWood API.
func administrativePermission(key string) bool {
	return strings.HasPrefix(key, "users.") || strings.HasPrefix(key, "roles.") || key == "venues.manage_access"
}

// authenticatedUserID adapts the WorkOS session context to the RBAC middleware.
func authenticatedUserID(ctx context.Context) (string, bool) {
	user, ok := auth.User(ctx)
	return user.ID, ok
}
