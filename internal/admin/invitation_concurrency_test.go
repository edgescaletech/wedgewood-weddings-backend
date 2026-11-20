package admin

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	workos "github.com/workos/workos-go/v7"
)

type adminTestRequest func(method, path, body, key string) *httptest.ResponseRecorder

func testConcurrentInvitationDelivery(t *testing.T, db *sql.DB, server *Server, call adminTestRequest) {
	const body = `{"email":"concurrent@example.com","displayName":"Concurrent","roles":[{"roleId":"00000000-0000-4000-8000-000000000003","venueId":"slow-delivery"}]}`
	const scheduler = "00000000-0000-4000-8000-000000000003"
	target, assignment := uuid.NewString(), uuid.NewString()
	execute := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	execute(`INSERT INTO venues(id,name) VALUES ('slow-delivery','Original')`)
	execute(`INSERT INTO users(id,workos_id,email,is_active,workos_invitation_id) VALUES($1,'unrelated','unrelated@example.com',true,'inv-unrelated')`, target)
	execute(`INSERT INTO user_roles(id,user_id,role_id,assignment_type,resource_id)
		SELECT $1,$2,$3,'venue',id FROM rbac_resources WHERE external_id='venue-test'`, assignment, target, scheduler)
	started, release := make(chan struct{}), make(chan struct{})
	server.Identity = testIdentityProvider{send: func(ctx context.Context, email, key string) (*workos.UserInvite, error) {
		close(started)
		select {
		case <-release:
			return &workos.UserInvite{ID: "inv-concurrent", Email: email}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	key := uuid.NewString()
	result := make(chan int, 1)
	go func() { result <- call(http.MethodPost, "/api/admin/users", body, key).Code }()
	defer func() {
		close(release)
		if code := <-result; code != http.StatusConflict {
			t.Errorf("deleted venue finalization: %d", code)
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider never started")
	}
	execute(`DELETE FROM venues WHERE id='slow-delivery'`)
	// Keep delivery paused until the retry, revocation and deactivation all finish.
	done := make(chan []int, 1)
	go func() {
		done <- []int{call(http.MethodPost, "/api/admin/users", body, key).Code,
			call(http.MethodDelete, "/api/admin/users/"+target+"/roles/"+assignment, "", uuid.NewString()).Code,
			call(http.MethodPost, "/api/admin/users/"+target+"/deactivate", "", uuid.NewString()).Code}
	}()
	select {
	case codes := <-done:
		if codes[0] != 503 || codes[1] != 204 || codes[2] != 204 {
			t.Fatalf("retry, revoke, deactivate: %v", codes)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow delivery blocked unrelated Admin writes")
	}
}

func testConcurrentEmailOwnership(t *testing.T, db *sql.DB, server *Server, call adminTestRequest) {
	const body = `{"email":"ownership@example.com","displayName":"Ownership"}`
	id, key := uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO users(id,email,workos_id,workos_invitation_id) VALUES($1,'old@example.com','linked-owner','inv-owner')`, id); err != nil {
		t.Fatal(err)
	}
	// Leave the sign-in email update uncommitted: the Admin precheck still sees the old email.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE users SET email='Ownership@Example.com' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	server.Identity = testIdentityProvider{send: func(_ context.Context, email, key string) (*workos.UserInvite, error) {
		return &workos.UserInvite{ID: "inv-contested", Email: email}, nil
	}}
	result := make(chan int, 1)
	go func() { result <- call(http.MethodPost, "/api/admin/users", body, key).Code }()
	// Confirm the invitation INSERT is waiting on this exact backend's transaction,
	// then let the sign-in win. Timing alone cannot guarantee this interleaving.
	var blockerPID int
	if err := tx.QueryRow(`SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		var waiting bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE 'INSERT INTO users(id,email,display_name%')`, blockerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("invitation did not wait for the sign-in update")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if code := <-result; code != 409 {
		t.Fatalf("ownership race: %d", code)
	}
	var delivered string
	var rejected int
	if err := db.QueryRow(`SELECT delivered_id,rejected_status FROM user_invitation_operations WHERE id=$1`, key).Scan(&delivered, &rejected); err != nil || delivered != "inv-contested" || rejected != 409 {
		t.Fatalf("reconciliation record: %q %d %v", delivered, rejected, err)
	}
	if response := call(http.MethodPost, "/api/admin/users", body, key); response.Code != 409 {
		t.Fatalf("conflict replay: %d", response.Code)
	}
	// In the opposite order, a pending invite owns the address before sign-in.
	if _, err := db.Exec(`INSERT INTO users(id,email,workos_invitation_id) VALUES(gen_random_uuid(),'pending-owner@example.com','inv-pending-owner')`); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE users SET email='PENDING-OWNER@example.com' WHERE id=$1`, id)
	if pgErr, ok := err.(*pq.Error); !ok || pgErr.Constraint != "users_email_owner" {
		t.Fatalf("sign-in ownership conflict: %v", err)
	}
}
