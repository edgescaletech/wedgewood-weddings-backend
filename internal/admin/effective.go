package admin

import (
	"database/sql"
	"net/http"
)

// activePermissionGrants reads the current role-derived grants for user $1.
const activePermissionGrants = `
	SELECT ur.resource_id, rp.permission_id
	FROM users u
	JOIN user_roles ur ON ur.user_id = u.id
	JOIN role_permissions rp ON rp.role_id = ur.role_id
	WHERE u.id = $1 AND u.is_active`

const effectivePermissionsQuery = `
WITH venue AS (
	SELECT id, is_active FROM rbac_resources
	WHERE resource_type = 'venue' AND external_id = $2
),
grants AS (
	SELECT g.permission_id,
	       bool_or(g.resource_id IS NULL) AS global_grant,
	       bool_or(g.resource_id IS NULL OR g.resource_id = v.id) AS venue_grant
	FROM (` + activePermissionGrants + `) g
	LEFT JOIN venue v ON true
	GROUP BY g.permission_id
)
SELECT v.id IS NOT NULL, p.key, COALESCE(g.global_grant, false),
       COALESCE(v.is_active AND g.venue_grant, false)
FROM users u
LEFT JOIN venue v ON true
LEFT JOIN permissions p ON true
LEFT JOIN grants g ON g.permission_id = p.id
WHERE u.id = $1
ORDER BY p.key`

// effectivePermissions reports role-derived access for one scope.
func (server *Server) effectivePermissions(writer http.ResponseWriter, request *http.Request) {
	userID := request.PathValue("id")
	if !validID(userID) {
		mutationError(writer, errInvalid)
		return
	}

	venue := request.URL.Query().Get("venueId")
	rows, err := server.DB.QueryContext(request.Context(), effectivePermissionsQuery, userID, venue)
	if err != nil {
		mutationError(writer, err)
		return
	}
	defer rows.Close()

	if !rows.Next() {
		err := rows.Err()
		if err == nil {
			err = errNotFound
		}
		mutationError(writer, err)
		return
	}
	if len(venue) > 64 {
		mutationError(writer, errInvalid)
		return
	}

	type entry struct {
		Permission string `json:"permission"`
		Inherited  bool   `json:"inherited"`
		Effective  bool   `json:"effective"`
	}
	result := []entry{}
	for {
		var foundVenue, globalGrant, venueGrant bool
		var key sql.NullString
		if err := rows.Scan(&foundVenue, &key, &globalGrant, &venueGrant); err != nil {
			mutationError(writer, err)
			return
		}
		if venue != "" && !foundVenue {
			mutationError(writer, errNotFound)
			return
		}
		// The left join still returns the user when the permission catalog is empty.
		if key.Valid {
			effective := globalGrant
			if venue != "" && !administrativePermission(key.String) {
				effective = venueGrant
			}
			result = append(result, entry{Permission: key.String, Inherited: effective, Effective: effective})
		}
		if !rows.Next() {
			break
		}
	}
	if err := rows.Err(); err != nil {
		mutationError(writer, err)
		return
	}
	respond(writer, http.StatusOK, result)
}
