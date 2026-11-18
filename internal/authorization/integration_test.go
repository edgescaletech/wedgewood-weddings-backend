package authorization

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

// TestPermissionIntegration uses a disposable schema and never modifies existing tables.
func TestPermissionIntegration(t *testing.T) {
	dsn := os.Getenv("RBAC_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("RBAC_TEST_DATABASE_URL not configured")
	}
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "rbac_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = adminDB.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := adminDB.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Error(err)
		}
		adminDB.Close()
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("postgres", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, file := range []string{"001_phase1_tables.up.sql", "002_auth.up.sql", "004_admin.up.sql", "005_user_admin.up.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(migration)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	userID := uuid.NewString()
	roleID := "00000000-0000-4000-8000-000000000003"
	_, err = db.Exec(`INSERT INTO users(id,workos_id,email,is_active) VALUES($1,'user_test','test@example.com',true)`, userID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO venues(id,name) VALUES('venue-a','A'),('venue-b','B')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO user_roles(id,user_id,role_id,venue_id) VALUES($1,$2,$3,'venue-a')`, uuid.NewString(), userID, roleID)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{DB: db}
	ctx := context.Background()
	venueA, venueB := "venue-a", "venue-b"
	check := func(key string, venue *string, want bool) {
		t.Helper()
		got, err := service.HasPermission(ctx, userID, key, venue)
		if err != nil || got != want {
			t.Fatalf("%s venue %v: got %v, want %v, err %v", key, venue, got, want, err)
		}
	}
	check("schedules.view", &venueA, true)
	check("schedules.view", &venueB, false)
	check("schedules.approve", &venueA, false)
	check("users.manage", nil, false)
	_, err = db.Exec(`INSERT INTO role_permissions(role_id,permission_id) SELECT $1,id FROM permissions WHERE key='schedules.approve'`, roleID)
	if err != nil {
		t.Fatal(err)
	}
	check("schedules.approve", &venueA, true)
	_, err = db.Exec(`DELETE FROM role_permissions WHERE role_id=$1 AND permission_id=(SELECT id FROM permissions WHERE key='schedules.approve')`, roleID)
	if err != nil {
		t.Fatal(err)
	}
	check("schedules.approve", &venueA, false)
	_, err = db.Exec(`INSERT INTO user_roles(id,user_id,role_id) VALUES($1,$2,'00000000-0000-4000-8000-000000000001')`, uuid.NewString(), userID)
	if err != nil {
		t.Fatal(err)
	}
	check("schedules.approve", &venueB, true)
	check("users.manage", nil, true)
	_, err = db.Exec(`DELETE FROM user_roles WHERE user_id=$1 AND venue_id IS NULL`, userID)
	if err != nil {
		t.Fatal(err)
	}
	check("schedules.approve", &venueA, false)
	check("schedules.view", &venueA, true)
	_, err = db.Exec(`UPDATE users SET is_active=false WHERE id=$1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	check("schedules.view", &venueA, false)
	_, err = db.Exec(`UPDATE users SET is_active=true WHERE id=$1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`DELETE FROM user_roles WHERE user_id=$1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	check("schedules.approve", &venueA, false)
	check("schedules.view", &venueA, false)
}
