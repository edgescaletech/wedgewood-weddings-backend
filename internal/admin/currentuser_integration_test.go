package admin

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/google/uuid"
	"wedding-project/internal/authorization"
)

func testCurrentUserAccess(t *testing.T, db *sql.DB, mux *http.ServeMux, schedulerID string) {
	t.Helper()
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	const (
		managerRole   = "00000000-0000-4000-8000-000000000002"
		schedulerRole = "00000000-0000-4000-8000-000000000003"
		assignedVenue = "access-assigned"
		otherVenue    = "access-other"
		inactiveVenue = "access-inactive"
	)
	exec(`INSERT INTO venues(id, name) VALUES ($1,'Assigned'),($2,'Other'),($3,'Inactive')`,
		assignedVenue, otherVenue, inactiveVenue)
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM venues WHERE id IN ($1,$2,$3)`, assignedVenue, otherVenue, inactiveVenue); err != nil {
			t.Error(err)
		}
	})
	for _, roleID := range []string{managerRole, schedulerRole} {
		exec(`INSERT INTO user_roles(id,user_id,role_id,assignment_type,resource_id)
			SELECT $1,$2,$3,'venue',id FROM rbac_resources WHERE resource_type='venue' AND external_id=$4`,
			uuid.NewString(), schedulerID, roleID, assignedVenue)
	}
	exec(`INSERT INTO user_roles(id,user_id,role_id,assignment_type,resource_id)
		SELECT $1,$2,$3,'venue',id FROM rbac_resources WHERE resource_type='venue' AND external_id=$4`,
		uuid.NewString(), schedulerID, schedulerRole, inactiveVenue)
	exec(`UPDATE venues SET is_active=false WHERE id=$1`, inactiveVenue)

	// A limited global role contributes permissions on assigned venues, but
	// does not grant venue discovery across the application.
	globalRoleID := uuid.NewString()
	exec(`INSERT INTO roles(id,name,description,assignment_type) VALUES($1,'Test Global','', 'global')`, globalRoleID)
	exec(`INSERT INTO role_permissions(role_id,permission_id)
		SELECT $1,id FROM permissions WHERE key='labor_goals.manage'`, globalRoleID)
	exec(`INSERT INTO user_roles(id,user_id,role_id,assignment_type) VALUES($1,$2,$3,'global')`,
		uuid.NewString(), schedulerID, globalRoleID)

	call := func(path, session string, status int) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if session != "" {
			request.AddCookie(&http.Cookie{Name: "session", Value: session})
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s status=%d, want %d. Body=%s", path, response.Code, status, response.Body)
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s missing no-store", path)
		}
		return response
	}
	type accessResponse struct {
		Global authorization.Grants
		Venues map[string]authorization.Grants
	}
	type venueResponse struct {
		ID, Name string
		Roles    []string
	}
	getAccess := func(session string) accessResponse {
		t.Helper()
		var access accessResponse
		response := call("/auth/access", session, http.StatusOK)
		if err := json.Unmarshal(response.Body.Bytes(), &access); err != nil {
			t.Fatal(err)
		}
		return access
	}
	getVenues := func(session string) []venueResponse {
		t.Helper()
		var venues []venueResponse
		// Query-string user IDs cannot select a different user's grants.
		response := call("/api/me/venues?userId="+schedulerID, session, http.StatusOK)
		if err := json.Unmarshal(response.Body.Bytes(), &venues); err != nil {
			t.Fatal(err)
		}
		return venues
	}
	for _, path := range []string{"/auth/access", "/api/me/venues"} {
		call(path, "", http.StatusUnauthorized)
	}

	adminAccess := getAccess("browser-session")
	if !slices.Equal(adminAccess.Global.Roles, []string{"Admin"}) ||
		!slices.Contains(adminAccess.Global.Permissions, "users.manage") ||
		!slices.Contains(adminAccess.Venues[otherVenue].Permissions, "schedules.view") ||
		adminAccess.Venues[otherVenue].Roles == nil || len(adminAccess.Venues[otherVenue].Roles) != 0 {
		t.Fatalf("global access or inheritance is incorrect: %+v", adminAccess)
	}
	if _, listed := adminAccess.Venues[inactiveVenue]; listed {
		t.Fatal("inactive venue returned for global Admin")
	}
	adminVenues := getVenues("browser-session")
	if !slices.ContainsFunc(adminVenues, func(venue venueResponse) bool { return venue.ID == otherVenue }) ||
		slices.ContainsFunc(adminVenues, func(venue venueResponse) bool { return venue.ID == inactiveVenue }) {
		t.Fatalf("unexpected Admin venue list: %+v", adminVenues)
	}

	scopedAccess := getAccess("scheduler-session")
	wantRoles := []string{"General Manager", "Venue Scheduler"}
	assigned := scopedAccess.Venues[assignedVenue]
	if !slices.Equal(scopedAccess.Global.Roles, []string{"Test Global"}) ||
		!slices.Equal(scopedAccess.Global.Permissions, []string{"labor_goals.manage"}) ||
		len(scopedAccess.Venues) != 1 || !slices.Equal(assigned.Roles, wantRoles) ||
		!slices.Contains(assigned.Permissions, "schedules.approve") ||
		!slices.Contains(assigned.Permissions, "labor_goals.manage") ||
		slices.Contains(assigned.Permissions, "users.manage") {
		t.Fatalf("unexpected scheduler grants: %+v", scopedAccess)
	}
	venues := getVenues("scheduler-session")
	if len(venues) != 1 || venues[0].ID != assignedVenue || venues[0].Name != "Assigned" ||
		!slices.Equal(venues[0].Roles, wantRoles) {
		t.Fatalf("unexpected scoped venue list: %+v", venues)
	}

	exec(`DELETE FROM user_roles WHERE user_id=$1 AND role_id=$2`, schedulerID, managerRole)
	exec(`DELETE FROM role_permissions WHERE role_id=$1`, globalRoleID)
	updated := getAccess("scheduler-session")
	if !slices.Equal(updated.Venues[assignedVenue].Roles, []string{"Venue Scheduler"}) ||
		slices.Contains(updated.Venues[assignedVenue].Permissions, "schedules.approve") ||
		slices.Contains(updated.Venues[assignedVenue].Permissions, "labor_goals.manage") {
		t.Fatalf("revoked grants still visible: %+v", updated)
	}
	exec(`DELETE FROM user_roles WHERE user_id=$1`, schedulerID)
	if access := getAccess("scheduler-session"); len(access.Venues) != 0 || len(access.Global.Roles) != 0 {
		t.Fatalf("revoked venue assignment still visible: %+v", access)
	}
	if venues := getVenues("scheduler-session"); venues == nil || len(venues) != 0 {
		t.Fatalf("expected an empty venue array: %+v", venues)
	}
	exec(`UPDATE users SET is_active=false WHERE id=$1`, schedulerID)
	for _, path := range []string{"/auth/access", "/api/me/venues"} {
		call(path, "scheduler-session", http.StatusForbidden)
	}
	exec(`UPDATE users SET is_active=true WHERE id=$1`, schedulerID)
	exec(`DELETE FROM roles WHERE id=$1`, globalRoleID)
}
