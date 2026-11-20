package admin

import (
	"net/http"
)

// effectivePermissions reports role-derived access for one scope.
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
	var resourceID string
	if venue := request.URL.Query().Get("venueId"); venue != "" {
		if len(venue) > 64 {
			mutationError(writer, errInvalid)
			return
		}
		var err error
		resourceID, err = venueResource(request.Context(), server.DB, venue)
		if err != nil {
			mutationError(writer, err)
			return
		}
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
		Permission string `json:"permission"`
		Inherited  bool   `json:"inherited"`
		Effective  bool   `json:"effective"`
	}
	result := make([]entry, 0, len(keys))
	for _, key := range keys {
		item := entry{Permission: key}
		if resourceID == "" || administrativePermission(key) {
			item.Effective, err = server.Access.HasGlobalPermission(request.Context(), userID, key)
		} else {
			item.Effective, err = server.Access.HasResourcePermission(request.Context(), userID, key, resourceID)
		}
		item.Inherited = item.Effective
		if err != nil {
			mutationError(writer, err)
			return
		}
		result = append(result, item)
	}
	respond(writer, http.StatusOK, result)
}
