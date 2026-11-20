package main

import (
	"context"
	"database/sql"
	"net/http"

	"wedding-project/internal/auth"
	"wedding-project/internal/models"
)

type venueAccessKey struct{}

// venueAccess loads active venues where the user has the requested permission,
// including permissions inherited from a global role.
func venueAccess(db *sql.DB, permission string, requireVenue bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := auth.User(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		venue := r.URL.Query().Get("venueId")
		if requireVenue && venue == "" {
			writeError(w, http.StatusBadRequest, "venueId is required")
			return
		}
		allowed, err := authorizedVenues(r.Context(), db, user.ID, permission)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "authorization unavailable")
			return
		}
		if venue != "" && !allowed[venue] {
			writeError(w, http.StatusForbidden, "permission denied")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), venueAccessKey{}, allowed)))
	})
}

func authorizedVenues(ctx context.Context, db *sql.DB, userID, permission string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `
			SELECT rr.external_id
			FROM rbac_resources rr
			JOIN venues v ON v.id=rr.external_id AND v.is_active
			WHERE rr.resource_type='venue' AND rr.is_active
			  AND EXISTS (
			    SELECT 1 FROM users u
			    JOIN user_roles ur ON ur.user_id=u.id
			    JOIN role_permissions rp ON rp.role_id=ur.role_id
			    JOIN permissions p ON p.id=rp.permission_id
			    WHERE u.id=$1 AND u.is_active AND p.key=$2
			      AND (ur.resource_id IS NULL OR ur.resource_id=rr.id)
			  )`, userID, permission)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	allowed := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		allowed[id] = true
	}
	return allowed, rows.Err()
}

func eventVenueAccess(r *http.Request) map[string]bool {
	allowed, _ := r.Context().Value(venueAccessKey{}).(map[string]bool)
	return allowed
}

func filterEventsByVenue(allowed map[string]bool, events []models.Event) []models.Event {
	filtered := make([]models.Event, 0, len(events))
	for _, event := range events {
		if allowed[event.VenueID] {
			filtered = append(filtered, event)
		}
	}
	return filtered
}
