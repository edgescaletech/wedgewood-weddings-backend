package admin

import (
	"database/sql"
	"net/http"
	"strings"
)

// effectivePermissions reports inherited, explicit and final access for one scope.
func (server *Server) effectivePermissions(writer http.ResponseWriter, request *http.Request) {
	userID := request.PathValue("id")
	if !validID(userID) {
		mutationError(writer, errInvalid)
		return
	}
	var exists bool
	if err := server.DB.QueryRowContext(request.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, userID).Scan(&exists); err != nil {
		mutationError(writer, err)
		return
	}
	if !exists {
		mutationError(writer, errNotFound)
		return
	}
	var venueID *string
	if venue := request.URL.Query().Get("venueId"); venue != "" {
		if len(venue) > 64 {
			mutationError(writer, errInvalid)
			return
		}
		venueID = &venue
	}
	rows, err := server.DB.QueryContext(request.Context(), `SELECT key FROM permissions ORDER BY key`)
	if err != nil {
		mutationError(writer, err)
		return
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			mutationError(writer, err)
			return
		}
		keys = append(keys, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		mutationError(writer, err)
		return
	}
	type entry struct {
		Permission string  `json:"permission"`
		Inherited  bool    `json:"inherited"`
		Override   *string `json:"override"`
		Effective  bool    `json:"effective"`
	}
	result := make([]entry, 0, len(keys))
	for _, key := range keys {
		item := entry{Permission: key}
		scopeVenue := venueID
		if strings.HasPrefix(key, "users.") || strings.HasPrefix(key, "roles.") || key == "venues.manage_access" {
			scopeVenue = nil
		}
		var override sql.NullString
		// Global denies and venue denies take precedence over either allow.
		err := server.DB.QueryRowContext(request.Context(), `
SELECT EXISTS(SELECT 1 FROM user_roles ur JOIN roles r ON r.id=ur.role_id JOIN role_permissions rp ON rp.role_id=r.id JOIN permissions p ON p.id=rp.permission_id
 WHERE ur.user_id=$1 AND p.key=$2 AND ((r.scope='global' AND ur.venue_id IS NULL) OR ($3::text IS NOT NULL AND r.scope='venue' AND ur.venue_id=$3))),
 (SELECT o.effect FROM user_permission_overrides o JOIN permissions p ON p.id=o.permission_id WHERE o.user_id=$1 AND p.key=$2 AND (o.venue_id IS NULL OR o.venue_id=$3) ORDER BY (o.effect='deny') DESC LIMIT 1)`, userID, key, scopeVenue).Scan(&item.Inherited, &override)
		if err != nil {
			mutationError(writer, err)
			return
		}
		if override.Valid {
			item.Override = &override.String
		}
		item.Effective, err = server.Access.HasPermission(request.Context(), userID, key, scopeVenue)
		if err != nil {
			mutationError(writer, err)
			return
		}
		result = append(result, item)
	}
	respond(writer, http.StatusOK, result)
}
