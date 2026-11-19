package admin

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
)

func testFilteredDirectory(t *testing.T, db *sql.DB, call adminTestRequest) {
	t.Helper()
	execute := func(query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	execute(`INSERT INTO venues(id,name,is_active) VALUES
		('directory-east','Directory East',true),('directory-west','Directory West',false)`)
	execute(`INSERT INTO users(id,workos_id,email,display_name,is_active,archived_at,workos_invitation_id)
		VALUES (gen_random_uuid(),'directory-active','aaa-directory@example.test','Directory Person',true,NULL,'directory-active'),
		       (gen_random_uuid(),NULL,'bbb-directory@example.test','Directory Person',false,NULL,'directory-invited'),
		       (gen_random_uuid(),'directory-inactive','ccc-directory@example.test','Directory Person',false,NULL,'directory-inactive'),
		       (gen_random_uuid(),'directory-archived','ddd-directory@example.test','Directory Person',false,now(),'directory-archived')`)
	execute(`INSERT INTO user_roles(id,user_id,role_id,assignment_type,resource_id)
		SELECT gen_random_uuid(),u.id,r.id,'venue',rr.id FROM users u
		CROSS JOIN roles r CROSS JOIN rbac_resources rr WHERE u.workos_invitation_id IN ('directory-active','directory-invited')
		AND r.name='General Manager' AND rr.resource_type='venue' AND rr.external_id='directory-east'`)

	type record struct {
		Email      string
		ID         string
		CanPublish bool
		Roles      []struct{ Name, VenueID string }
	}
	for _, example := range []struct {
		path, value string
		total       int
		publish     bool
	}{
		{"/users?search=directory%20person&limit=1&offset=1", "bbb-directory@example.test", 4, false},
		{"/users?search=directory%20person&limit=1&offset=20", "", 4, false},
		{"/users?search=directory%20person&status=archived", "ddd-directory@example.test", 1, false},
		{"/users?search=directory%20person&status=inactive", "ccc-directory@example.test", 1, false},
		{"/users?status=invited&role=General+Manager&venueId=directory-east", "bbb-directory@example.test", 1, false},
		{"/users?role=General+Manager&venueId=directory-east&canPublish=true", "aaa-directory@example.test", 1, true},
		{"/users?role=General+Manager&venueId=directory-west&canPublish=true", "", 0, false},
		{"/venues?search=directory&active=false&limit=1", "directory-west", 1, false},
		{"/venues?search=directory&limit=1&offset=1", "directory-west", 2, false},
	} {
		response := call(http.MethodGet, "/api/admin"+example.path+"&view=directory", "", "")
		var page struct {
			Total int
			Items []record
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK || page.Total != example.total || page.Items == nil {
			t.Fatalf("%s: status=%d body=%s error=%v", example.path, response.Code, response.Body, err)
		}
		if example.value == "" {
			if len(page.Items) != 0 {
				t.Fatalf("%s: expected an empty page", example.path)
			}
			continue
		}
		if len(page.Items) != 1 || (page.Items[0].Email != example.value && page.Items[0].ID != example.value) || page.Items[0].CanPublish != example.publish {
			t.Fatalf("%s: unexpected page %s", example.path, response.Body)
		}
		if example.publish && (len(page.Items[0].Roles) != 1 || page.Items[0].Roles[0].VenueID != "directory-east") {
			t.Fatalf("%s: missing scoped assignment in %s", example.path, response.Body)
		}
	}
	for _, path := range []string{"/users?limit=1", "/venues?limit=1"} {
		response := call(http.MethodGet, "/api/admin"+path, "", "")
		var items []json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &items); err != nil || response.Code != http.StatusOK || len(items) != 1 {
			t.Fatalf("legacy response %s: %d %s", path, response.Code, response.Body)
		}
	}
	for _, query := range []string{"status=invalid", "canPublish=true", "view=invalid"} {
		response := call(http.MethodGet, "/api/admin/users?"+query, "", "")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid query %s: %d", query, response.Code)
		}
	}
}
