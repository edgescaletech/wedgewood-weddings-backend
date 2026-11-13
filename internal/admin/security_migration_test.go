package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"wedding-project/internal/authorization"
)

// TestSecurityMigrationIntegration verifies real migration rollback failures and
// direct SQL updates against a disposable schema, preserving all history.
func TestSecurityMigrationIntegration(t *testing.T) {
	dsn := os.Getenv("RBAC_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("RBAC_TEST_DATABASE_URL not configured")
	}
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "security_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = adminDB.Exec(`CREATE SCHEMA ` + schema); err != nil {
		adminDB.Close()
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

	migrationPath, err := filepath.Abs("../migrations")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := migrate.New("file://"+migrationPath, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Close()
	if err := runner.Up(); err != nil {
		t.Fatal(err)
	}
	userID, otherID, assignmentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{userID, otherID} {
		if _, err := db.Exec(`INSERT INTO users(id,workos_id,email,is_active) VALUES($1::uuid,$1::text,$1::text,true)`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO admin_bootstrap(singleton,user_id) VALUES(true,$1)`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO audit_logs(id,actor_user_id,action) VALUES($1,$2,'test.history')`, uuid.NewString(), userID); err != nil {
		t.Fatal(err)
	}
	if err := runner.Steps(-1); err == nil {
		t.Fatal("security rollback unexpectedly succeeded")
	}
	version, dirty, err := runner.Version()
	if err != nil || version != 4 || !dirty {
		t.Fatalf("unexpected failed rollback state: %d %v %v", version, dirty, err)
	}
	// Reproduce documented recovery: inspect data first, then clear only the dirty
	// metadata to the actual retained schema version and resume forward migration.
	assertHistory := func() {
		t.Helper()
		var bootstrapCount, auditCount int
		if err := db.QueryRow(`SELECT (SELECT count(*) FROM admin_bootstrap),(SELECT count(*) FROM audit_logs)`).Scan(&bootstrapCount, &auditCount); err != nil || bootstrapCount != 1 || auditCount != 1 {
			t.Fatalf("history lost: %d %d %v", bootstrapCount, auditCount, err)
		}
	}
	assertHistory()
	if err := runner.Force(5); err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(); !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("forward resume: %v", err)
	}
	historicalDown, err := os.ReadFile("../migrations/003_admin.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(historicalDown)); err == nil {
		t.Fatal("historical destructive rollback succeeded")
	}
	assertHistory()
	if _, err := db.Exec(`INSERT INTO admin_bootstrap(singleton,user_id) VALUES(true,$1)`, otherID); err == nil {
		t.Fatal("bootstrap reopened")
	}
	if _, err := db.Exec(`INSERT INTO venues(id,name) VALUES('venue-a','A'),('venue-b','B')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_roles(id,user_id,role_id,venue_id) VALUES($1,$2,'00000000-0000-4000-8000-000000000003','venue-a')`, assignmentID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_permission_overrides(id,user_id,permission_id,venue_id,effect) SELECT $1,$2,id,'venue-a','allow' FROM permissions WHERE key='schedules.publish'`, uuid.NewString(), userID); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct{ column, value string }{
		{"id", uuid.NewString()}, {"user_id", otherID}, {"role_id", "00000000-0000-4000-8000-000000000002"}, {"venue_id", "venue-b"},
	} {
		t.Run(change.column, func(t *testing.T) {
			if _, err := db.Exec(`UPDATE user_roles SET `+change.column+`=$1 WHERE id=$2`, change.value, assignmentID); err == nil {
				t.Fatal("identity update accepted")
			}
		})
	}
	// Delete-and-create removes the old ALLOW instead of reviving it when the
	// user receives a fresh assignment at the original venue.
	if _, err := db.Exec(`DELETE FROM user_roles WHERE id=$1`, assignmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_roles(id,user_id,role_id,venue_id) VALUES($1,$2,'00000000-0000-4000-8000-000000000003','venue-a')`, uuid.NewString(), userID); err != nil {
		t.Fatal(err)
	}
	venue := "venue-a"
	access := authorization.Service{DB: db}
	allowed, err := access.HasPermission(context.Background(), userID, "schedules.publish", &venue)
	if err != nil || allowed {
		t.Fatalf("stale ALLOW revived: %v %v", allowed, err)
	}
	assertHistory()
}
