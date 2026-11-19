package admin

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	workos "github.com/workos/workos-go/v7"
)

func testInvitationSafeguards(t *testing.T, db *sql.DB, server *Server, call func(string, string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	const (
		invitePath    = "/api/admin/users"
		venueID       = "invitation-lifecycle"
		schedulerRole = "00000000-0000-4000-8000-000000000003"
	)
	post := func(path, body, key string, want int) {
		t.Helper()
		response := call(http.MethodPost, path, body, key)
		if response.Code != want {
			t.Fatalf("%s status=%d, want %d. Body=%s", path, response.Code, want, response.Body)
		}
	}
	invitation := func(email string) string {
		return fmt.Sprintf(`{"email":%q,"displayName":"Test Invitee","roles":[{"roleId":%q,"venueId":%q}]}`,
			email, schedulerRole, venueID)
	}
	exec(`INSERT INTO venues(id,name) VALUES($1,'Original')`, venueID)
	replacedBody, replacedKey := invitation("replaced@example.com"), uuid.NewString()
	replacedDeliveries := 0
	server.Identity = testIdentityProvider{send: func(_ context.Context, email, _ string) (*workos.UserInvite, error) {
		replacedDeliveries++
		exec(`DELETE FROM venues WHERE id=$1`, venueID)
		exec(`INSERT INTO venues(id,name) VALUES($1,'Replacement')`, venueID)
		return &workos.UserInvite{ID: "inv-replaced", Email: email}, nil
	}}
	post(invitePath, replacedBody, replacedKey, http.StatusConflict)
	post(invitePath, replacedBody, replacedKey, http.StatusConflict)
	var scopedInviteCommitted bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE workos_invitation_id='inv-replaced')`).Scan(&scopedInviteCommitted); err != nil || scopedInviteCommitted || replacedDeliveries != 1 {
		t.Fatalf("replaced venue invitation persisted=%v deliveries=%d error=%v", scopedInviteCommitted, replacedDeliveries, err)
	}

	// Removing the dormant permission must still permit all three live V1 writes.
	exec(`DELETE FROM role_permissions WHERE permission_id=(SELECT id FROM permissions WHERE key='roles.manage')`)
	t.Cleanup(func() {
		exec(`INSERT INTO role_permissions(role_id,permission_id)
			SELECT r.id,p.id FROM roles r CROSS JOIN permissions p
			WHERE r.name='Admin' AND p.key='roles.manage'`)
	})
	const reusableEmail = "reusable@example.com"
	body, firstKey := invitation(reusableEmail), uuid.NewString()
	deliveries := 0
	server.Identity = testIdentityProvider{send: func(_ context.Context, email, _ string) (*workos.UserInvite, error) {
		deliveries++
		if deliveries == 1 {
			exec(`UPDATE venues SET is_active=false WHERE id=$1`, venueID)
		}
		return &workos.UserInvite{ID: fmt.Sprintf("inv-reusable-%d", deliveries), Email: email}, nil
	}}
	post(invitePath, body, firstKey, http.StatusBadRequest)
	exec(`UPDATE venues SET is_active=true WHERE id=$1`, venueID)
	post(invitePath, body, uuid.NewString(), http.StatusConflict) // The first operation is still in flight.
	post(invitePath, body, firstKey, http.StatusNoContent)
	post(invitePath, body, uuid.NewString(), http.StatusConflict) // A pending account now owns the email.

	var userID string
	if err := db.QueryRow(`SELECT id FROM users WHERE workos_invitation_id='inv-reusable-1'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE users SET workos_id='workos-reusable-test', email='changed@example.com' WHERE id=$1`, userID)
	post("/api/admin/users/"+userID+"/activate", "", uuid.NewString(), http.StatusNoContent)
	post("/api/admin/users/"+userID+"/roles", `{"roleId":"00000000-0000-4000-8000-000000000002","venueId":"invitation-lifecycle"}`, uuid.NewString(), http.StatusConflict)

	var assignmentID string
	if err := db.QueryRow(`SELECT id FROM user_roles WHERE user_id=$1`, userID).Scan(&assignmentID); err != nil {
		t.Fatal(err)
	}
	response := call(http.MethodPut, "/api/admin/users/"+userID+"/roles/"+assignmentID,
		`{"roleId":"00000000-0000-4000-8000-000000000002"}`, "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("replace venue role: %d %s", response.Code, response.Body)
	}
	var roleName string
	if err := db.QueryRow(`SELECT r.name FROM user_roles ur JOIN roles r ON r.id=ur.role_id WHERE ur.user_id=$1`, userID).Scan(&roleName); err != nil || roleName != "General Manager" {
		t.Fatalf("unexpected replacement: %q %v", roleName, err)
	}
	if _, err := db.Exec(`INSERT INTO user_roles(id,user_id,role_id,assignment_type,resource_id)
		SELECT gen_random_uuid(),user_id,$2,assignment_type,resource_id FROM user_roles WHERE user_id=$1`, userID, schedulerRole); err == nil {
		t.Fatal("database accepted a second role at the same venue")
	}
	duplicate := fmt.Sprintf(`{"email":"duplicate-venue@example.com","displayName":"Duplicate","roles":[{"roleId":%q,"venueId":%q},{"roleId":"00000000-0000-4000-8000-000000000002","venueId":%q}]}`,
		schedulerRole, venueID, venueID)
	post(invitePath, duplicate, uuid.NewString(), http.StatusBadRequest)

	post(invitePath, body, firstKey, http.StatusNoContent) // A replay never sends a new invitation.
	post(invitePath, body, uuid.NewString(), http.StatusNoContent)
	if deliveries != 2 {
		t.Fatalf("WorkOS deliveries=%d, want 2", deliveries)
	}
}
