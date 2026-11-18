package authorization

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wedding-project/internal/repositories"

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
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	apply := func(files ...string) {
		t.Helper()
		for _, file := range files {
			migration, err := os.ReadFile(filepath.Join("..", "migrations", file))
			if err != nil {
				t.Fatal(err)
			}
			exec(string(migration))
		}
	}
	apply("001_phase1_tables.up.sql", "002_auth.up.sql")
	userID := uuid.NewString()
	exec(`INSERT INTO users(id,workos_id,email,is_active) VALUES($1,'user_test','test@example.com',true)`, userID)
	exec(`INSERT INTO venues(id,name,is_active) VALUES('venue-a','A',NULL),('venue-b','B',TRUE),('closed','Closed',FALSE)`)
	exec(`INSERT INTO events(id,venue_id,name,event_date) VALUES('historic','venue-a','Historic event',current_date)`)
	apply("004_admin.up.sql", "005_user_admin.up.sql", "006_security_guards.up.sql")
	registryID := func(externalID string) string {
		t.Helper()
		var id string
		if err := db.QueryRow(`SELECT id FROM rbac_resources WHERE resource_type='venue' AND external_id=$1`, externalID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	venueA, venueB := registryID("venue-a"), registryID("venue-b")
	var preserved bool
	if err := db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM events WHERE id='historic' AND venue_id='venue-a'
	) AND EXISTS (
		SELECT 1 FROM rbac_resources WHERE resource_type='venue' AND external_id='closed' AND NOT is_active
	)`).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("migration changed historical events or lost inactive status: %v %v", preserved, err)
	}
	assignmentID := uuid.NewString()
	roleID := "00000000-0000-4000-8000-000000000003"
	exec(`INSERT INTO user_roles(id,user_id,role_id,assignment_type,resource_id)
		VALUES($1,$2,$3,'venue',$4)`, assignmentID, userID, roleID, venueA)

	service := &Service{DB: db}
	ctx := context.Background()
	check := func(key string, resource *string, want bool) {
		t.Helper()
		var got bool
		var err error
		if resource == nil {
			got, err = service.HasGlobalPermission(ctx, userID, key)
		} else {
			got, err = service.HasResourcePermission(ctx, userID, key, *resource)
		}
		if err != nil || got != want {
			t.Fatalf("%s: got %v, want %v, err %v", key, got, want, err)
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
	_, err = db.Exec(`INSERT INTO user_roles(id,user_id,role_id,assignment_type) VALUES($1,$2,'00000000-0000-4000-8000-000000000001','global')`, uuid.NewString(), userID)
	if err != nil {
		t.Fatal(err)
	}
	check("schedules.approve", &venueB, true)
	check("users.manage", nil, true)
	// Global grants apply only to existing, active targets in resource checks.
	missing := uuid.NewString()
	check("schedules.view", &missing, false)
	exec(`UPDATE venues SET is_active=false WHERE id='venue-a'`)
	if _, err := db.Exec(`UPDATE venues SET is_active=NULL WHERE id='venue-a'`); err == nil {
		t.Fatal("NULL reopened a suspended venue")
	}
	check("schedules.view", &venueA, false)
	if err := repositories.UpsertVenue(ctx, db, "venue-a", "Renamed", "", ""); err != nil {
		t.Fatal(err)
	}
	check("schedules.view", &venueA, false)
	exec(`UPDATE venues SET is_active=true WHERE id='venue-a'`)
	if registryID("venue-a") != venueA {
		t.Fatal("venue status change changed registry identity")
	}
	check("schedules.view", &venueA, true)

	_, err = db.Exec(`DELETE FROM user_roles WHERE user_id=$1 AND resource_id IS NULL`, userID)
	if err != nil {
		t.Fatal(err)
	}
	check("schedules.approve", &venueA, false)
	check("schedules.view", &venueA, true)
	projectID := uuid.NewString()
	exec(`INSERT INTO rbac_resources(id,resource_type,external_id) VALUES($1,'project','venue-a')`, projectID)
	check("schedules.view", &projectID, false)
	const grant = `INSERT INTO user_roles(id,user_id,role_id,assignment_type,resource_id)
		VALUES($1,$2,'00000000-0000-4000-8000-000000000002','venue',$3)`
	if _, err := db.Exec(grant, uuid.NewString(), userID, projectID); err == nil {
		t.Fatal("venue policy accepted a project target")
	}
	// A project role uses the same tables and evaluator as a venue role.
	projectRole := uuid.NewString()
	exec(`INSERT INTO roles(id,name,description,assignment_type)
		VALUES($1,'Project Reader','Read assigned projects','project')`, projectRole)
	exec(`INSERT INTO role_permissions(role_id,permission_id)
		SELECT $1,id FROM permissions WHERE key='schedules.view'`, projectRole)
	exec(`INSERT INTO user_roles(id,user_id,role_id,assignment_type,resource_id)
		VALUES($1,$2,$3,'project',$4)`, uuid.NewString(), userID, projectRole, projectID)
	check("schedules.view", &projectID, true)
	if _, err := db.Exec(`INSERT INTO user_roles(id,user_id,role_id,assignment_type,resource_id)
		VALUES($1,$2,$3,'venue',$4)`, uuid.NewString(), userID, projectRole, venueA); err == nil {
		t.Fatal("project role accepted a venue assignment policy")
	}
	managerID := uuid.NewString()
	exec(grant, managerID, userID, venueA)
	check("schedules.approve", &venueA, true)
	exec(`DELETE FROM user_roles WHERE id=$1`, managerID)
	check("schedules.approve", &venueA, false)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE venues SET is_active=false WHERE id='venue-a'`); err != nil {
		t.Fatal(err)
	}
	raceContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	grantResult := make(chan error, 1)
	go func() {
		_, err := db.ExecContext(raceContext, grant, uuid.NewString(), userID, venueA)
		grantResult <- err
	}()
	select {
	case err := <-grantResult:
		t.Fatalf("assignment did not wait for venue update: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-grantResult; err == nil || !strings.Contains(err.Error(), "active resource") {
		t.Fatalf("assignment against suspended resource: %v", err)
	}
	exec(`UPDATE venues SET is_active=true WHERE id='venue-a'`)
	check("schedules.view", &venueA, true)
	check("schedules.approve", &venueA, false)
	exec(`DELETE FROM venues WHERE id='venue-a'`)
	check("schedules.view", &venueA, false)
	if err := repositories.UpsertVenue(ctx, db, "venue-a", "New venue", "", ""); err != nil {
		t.Fatal(err)
	}
	recreated := registryID("venue-a")
	if recreated == venueA {
		t.Fatal("venue recreation reused registry identity")
	}
	check("schedules.view", &recreated, false)
	if err := db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM audit_logs WHERE action='role.remove.resource_deleted'
		  AND resource_id=$1 AND metadata->>'assignmentId'=$2
	) AND EXISTS (SELECT 1 FROM events WHERE id='historic')`, venueA, assignmentID).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("hard delete lost grant audit or historical event: %v %v", preserved, err)
	}
	exec(grant, uuid.NewString(), userID, recreated)
	check("schedules.view", &recreated, true)
	_, err = db.Exec(`UPDATE users SET is_active=false WHERE id=$1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	check("schedules.view", &recreated, false)
	_, err = db.Exec(`UPDATE users SET is_active=true WHERE id=$1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`DELETE FROM user_roles WHERE user_id=$1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	check("schedules.approve", &recreated, false)
	check("schedules.view", &recreated, false)
}
