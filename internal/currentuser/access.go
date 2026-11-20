// Package currentuser exposes Wedgewood access information for the signed-in user.
package currentuser

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/lib/pq"
	"wedding-project/internal/auth"
	"wedding-project/internal/authorization"
)

// Migration 004 seeds this global role. Its identity is stable across renames.
const globalAdminRoleID = "00000000-0000-4000-8000-000000000001"

type server struct {
	access *authorization.Service
}

// Register protects both current-user routes with the existing session middleware.
func Register(mux *http.ServeMux, sessions *auth.Server, access *authorization.Service) {
	handler := server{access: access}
	mux.Handle("GET /auth/access", sessions.Middleware(http.HandlerFunc(handler.getAccess)))
	mux.Handle("GET /api/me/venues", sessions.Middleware(http.HandlerFunc(handler.getVenues)))
}

func (server server) loadAccess(writer http.ResponseWriter, request *http.Request) (authorization.Access, bool) {
	user, ok := auth.User(request.Context())
	if !ok {
		respond(writer, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return authorization.Access{}, false
	}
	access, err := server.access.ListAccess(request.Context(), user.ID, "venue", globalAdminRoleID)
	if err != nil {
		respond(writer, http.StatusServiceUnavailable, map[string]string{"error": "authorization unavailable"})
		return authorization.Access{}, false
	}
	return access, true
}

func (server server) getAccess(writer http.ResponseWriter, request *http.Request) {
	access, ok := server.loadAccess(writer, request)
	if !ok {
		return
	}
	venues := make(map[string]authorization.Grants, len(access.Resources))
	for _, venue := range access.Resources {
		// Roles stay at the scope where assigned. Permission keys at a venue
		// include the union of direct grants and inherited global grants.
		grants := venue.Grants
		if len(access.Global.Permissions) != 0 {
			permissions := append(slices.Clone(access.Global.Permissions), grants.Permissions...)
			slices.Sort(permissions)
			grants.Permissions = slices.Compact(permissions)
		}
		venues[venue.ExternalID] = grants
	}
	respond(writer, http.StatusOK, struct {
		Global authorization.Grants            `json:"global"`
		Venues map[string]authorization.Grants `json:"venues"`
	}{access.Global, venues})
}

type venueDetails struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
}

func (server server) getVenues(writer http.ResponseWriter, request *http.Request) {
	access, ok := server.loadAccess(writer, request)
	if !ok {
		return
	}
	venues := make([]venueDetails, 0, len(access.Resources))
	if len(access.Resources) == 0 {
		respond(writer, http.StatusOK, venues)
		return
	}

	resourceIDs := make([]string, 0, len(access.Resources))
	roles := make(map[string][]string, len(access.Resources))
	for _, resource := range access.Resources {
		resourceIDs = append(resourceIDs, resource.ID)
		roles[resource.ID] = resource.Grants.Roles
	}
	// Resolve names by registry UUID so a venue recreated with the same
	// Salesforce ID cannot be returned under a grant for its previous lifetime.
	rows, err := server.access.DB.QueryContext(request.Context(), `
		SELECT rr.id, v.id, v.name FROM rbac_resources rr
		JOIN venues v ON v.id = rr.external_id
		WHERE rr.id = ANY($1::uuid[]) AND rr.resource_type = 'venue' AND rr.is_active AND v.is_active
		ORDER BY v.name, v.id`, pq.Array(resourceIDs))
	if err != nil {
		respond(writer, http.StatusServiceUnavailable, map[string]string{"error": "venues unavailable"})
		return
	}
	defer rows.Close()
	for rows.Next() {
		var resourceID string
		var venue venueDetails
		if err := rows.Scan(&resourceID, &venue.ID, &venue.Name); err != nil {
			respond(writer, http.StatusServiceUnavailable, map[string]string{"error": "venues unavailable"})
			return
		}
		venue.Roles = roles[resourceID]
		venues = append(venues, venue)
	}
	if rows.Err() != nil {
		respond(writer, http.StatusServiceUnavailable, map[string]string{"error": "venues unavailable"})
		return
	}
	respond(writer, http.StatusOK, venues)
}

func respond(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}
