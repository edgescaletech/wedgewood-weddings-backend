package admin

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"wedding-project/internal/authorization"
)

func (server *Server) setOverride(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Permission string  `json:"permission"`
		Effect     string  `json:"effect"`
		VenueID    *string `json:"venueId"`
	}
	if !decodeMutation(writer, request, &input) {
		return
	}
	userID := request.PathValue("id")
	server.mutate(writer, request, "users.manage_permissions", func(ctx context.Context, tx *sql.Tx, actor string) error {
		if !validID(userID) || input.Permission == "" || (input.Effect != "allow" && input.Effect != "deny") || (input.VenueID != nil && (*input.VenueID == "" || len(*input.VenueID) > 64)) {
			return errInvalid
		}
		if sameUser(actor, userID) {
			return errForbidden
		}
		if input.VenueID != nil && (strings.HasPrefix(input.Permission, "users.") || strings.HasPrefix(input.Permission, "roles.") || input.Permission == "venues.manage_access") {
			return errInvalid
		}
		var permissionID string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM permissions WHERE key=$1`, input.Permission).Scan(&permissionID); err != nil {
			return err
		}
		allowed, err := authorization.HasPermissionTx(ctx, tx, actor, input.Permission, input.VenueID)
		if err != nil {
			return err
		}
		if !allowed {
			return errForbidden
		}
		// The migration's scope trigger requires an existing matching role assignment.
		var overrideID string
		err = tx.QueryRowContext(ctx, `SELECT id FROM user_permission_overrides WHERE user_id=$1 AND permission_id=$2 AND venue_id IS NOT DISTINCT FROM $3`, userID, permissionID, input.VenueID).Scan(&overrideID)
		if err == sql.ErrNoRows {
			overrideID = uuid.NewString()
			_, err = tx.ExecContext(ctx, `INSERT INTO user_permission_overrides(id,user_id,permission_id,venue_id,effect,assigned_by) VALUES($1,$2,$3,$4,$5,$6)`, overrideID, userID, permissionID, input.VenueID, input.Effect, actor)
		} else if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE user_permission_overrides SET effect=$1,assigned_by=$2,updated_at=now() WHERE id=$3`, input.Effect, actor, overrideID)
		}
		if err != nil {
			return constraintError(err)
		}
		return audit(ctx, tx, actor, "permission.override.set", "user", userID, input.VenueID, map[string]any{"permission": input.Permission, "effect": input.Effect, "overrideId": overrideID, "outcome": "success"})
	})
}

func (server *Server) removeOverride(writer http.ResponseWriter, request *http.Request) {
	userID, overrideID := request.PathValue("id"), request.PathValue("overrideId")
	server.mutate(writer, request, "users.manage_permissions", func(ctx context.Context, tx *sql.Tx, actor string) error {
		if !validID(userID) || !validID(overrideID) {
			return errInvalid
		}
		if sameUser(actor, userID) {
			return errForbidden
		}
		var key string
		var venue sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT p.key,o.venue_id FROM user_permission_overrides o JOIN permissions p ON p.id=o.permission_id WHERE o.id=$1 AND o.user_id=$2`, overrideID, userID).Scan(&key, &venue); err != nil {
			return err
		}
		var venueID *string
		if venue.Valid {
			venueID = &venue.String
		}
		allowed, err := authorization.HasPermissionTx(ctx, tx, actor, key, venueID)
		if err != nil {
			return err
		}
		if !allowed {
			return errForbidden
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_permission_overrides WHERE id=$1 AND user_id=$2`, overrideID, userID); err != nil {
			return err
		}
		return audit(ctx, tx, actor, "permission.override.remove", "user", userID, venueID, map[string]any{"permission": key, "overrideId": overrideID, "outcome": "success"})
	})
}
