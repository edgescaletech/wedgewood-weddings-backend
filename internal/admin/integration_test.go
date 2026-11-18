package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	workos "github.com/workos/workos-go/v7"
	"wedding-project/internal/auth"
	"wedding-project/internal/authorization"
)

type testTransport func(*http.Request) (*http.Response, error)

type testIdentityProvider struct {
	send       func(context.Context, string, string) (*workos.UserInvite, error)
	priorState workos.UserInviteState
}

func (provider testIdentityProvider) Send(ctx context.Context, email, key string) (*workos.UserInvite, error) {
	return provider.send(ctx, email, key)
}
func (provider testIdentityProvider) Get(_ context.Context, id string) (*workos.UserInvite, error) {
	return &workos.UserInvite{ID: id, Email: "new@example.com", State: provider.priorState}, nil
}
func (provider testIdentityProvider) Resend(_ context.Context, id, _ string) (*workos.UserInvite, error) {
	return &workos.UserInvite{ID: id, Email: "new@example.com"}, nil
}
func (transport testTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// TestConcurrentAdminRemoval exercises the same lock and invariant as mutations.
func TestConcurrentAdminRemoval(t *testing.T) {
	dsn := os.Getenv("RBAC_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("RBAC_TEST_DATABASE_URL not configured")
	}
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "admin_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	for _, file := range []string{"001_phase1_tables.up.sql", "002_auth.up.sql", "003_event_sync_tracking.up.sql", "004_admin.up.sql", "005_user_admin.up.sql", "006_security_guards.up.sql", "007_invitations.up.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(migration)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	ids := []string{uuid.NewString(), uuid.NewString()}
	for index, id := range ids {
		if _, err = db.Exec(`INSERT INTO users(id,workos_id,email,is_active,workos_invitation_id) VALUES($1,$2,$2,true,'inv-' || $2)`, id, "workos-"+string(rune('a'+index))); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO user_roles(id,user_id,role_id,assignment_type) VALUES($1,$2,'00000000-0000-4000-8000-000000000001','global')`, uuid.NewString(), id); err != nil {
			t.Fatal(err)
		}
	}

	// Exercise actual authentication, permission middleware, CSRF and audit wiring.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keySet, err := json.Marshal(map[string]any{"keys": []any{map[string]string{
		"kty": "RSA", "kid": "test-key", "n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}), "alg": "RS256", "use": "sig",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	previousTransport := http.DefaultTransport
	http.DefaultTransport = testTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(keySet)), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	secret := strings.Repeat("z", 32)
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"sub": "workos-a", "sid": "session-id", "iss": "https://issuer.example", "client_id": "client_test", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	token.Header["kid"] = "test-key"
	accessToken, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := workos.SealSessionFromAuthResponse(accessToken, "refresh", &workos.User{ID: "workos-a", Email: "workos-a"}, nil, secret)
	if err != nil {
		t.Fatal(err)
	}
	sessionHash := sha256.Sum256([]byte("browser-session"))
	if _, err = db.Exec(`INSERT INTO auth_sessions(id_hash,workos_id,sealed_session,expires_at) VALUES($1,'workos-a',$2,now()+interval '1 hour')`, sessionHash[:], sealed); err != nil {
		t.Fatal(err)
	}
	authServer, err := auth.New(auth.Config{ClientID: "client_test", APIKey: "test-key", Issuer: "https://issuer.example", CookiePassword: secret, RedirectURI: "http://localhost:8080/auth/callback", AppURL: "http://localhost:3000"}, db)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{DB: db, Auth: authServer, Access: &authorization.Service{DB: db}, AppURL: "http://localhost:3000"}
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest(http.MethodPatch, "/api/admin/users/"+ids[1], strings.NewReader(`{"displayName":"Updated"}`))
	request.Header.Set("Origin", "http://localhost:3000")
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "session", Value: "browser-session"})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("admin update status=%d body=%s", response.Code, response.Body.String())
	}
	var auditCount int
	if err := db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='user.update' AND actor_user_id=$1`, ids[0]).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit count=%d err=%v", auditCount, err)
	}

	// Assign a venue role and grant, then revoke, a missing permission via HTTP.
	targetID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO users(id,workos_id,email,is_active,workos_invitation_id) VALUES($1,'workos-scheduler','scheduler@example.com',true,'inv-scheduler')`, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO venues(id,name) VALUES('venue-test','Test')`); err != nil {
		t.Fatal(err)
	}
	callWithKey := func(method, path, body, key string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Idempotency-Key", key)
		request.Header.Set("Origin", "http://localhost:3000")
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: "session", Value: "browser-session"})
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		return callWithKey(method, path, body, uuid.NewString())
	}

	// UUID text casing must not bypass self-modification protection.
	response = call(http.MethodPost, "/api/admin/users/"+strings.ToUpper(ids[0])+"/roles", `{"roleId":"00000000-0000-4000-8000-000000000003","venueId":"venue-test"}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("uppercase self assignment: %d", response.Code)
	}
	response = call(http.MethodPost, "/api/admin/users/"+strings.ToUpper(ids[0])+"/deactivate", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("uppercase self deactivation: %d", response.Code)
	}
	response = call(http.MethodDelete, "/api/admin/users/"+strings.ToUpper(ids[0]), "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("uppercase self archival: %d", response.Code)
	}
	response = call(http.MethodPost, "/api/admin/users/not-a-uuid/deactivate", "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("malformed UUID: %d", response.Code)
	}

	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/admin/roles"},
		{http.MethodDelete, "/api/admin/roles/00000000-0000-4000-8000-000000000001"},
		{http.MethodPut, "/api/admin/roles/00000000-0000-4000-8000-000000000001/permissions"},
	} {
		if response := call(route.method, route.path, "{}"); response.Code != http.StatusNotFound {
			t.Fatalf("disabled route %s %s: %d", route.method, route.path, response.Code)
		}
	}

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	const newUser = `{"email":"new@example.com","displayName":"New user","roles":[{"roleId":"00000000-0000-4000-8000-000000000003","venueId":"venue-test"}]}`
	invitationKey := uuid.NewString()
	deliveries := 0
	server.Identity = testIdentityProvider{priorState: "pending", send: func(_ context.Context, email, key string) (*workos.UserInvite, error) {
		deliveries++
		if email != "new@example.com" {
			return nil, &workos.APIError{StatusCode: 422}
		}
		if key != "admin-invite-"+invitationKey {
			return nil, errors.New("unexpected idempotency key")
		}
		return &workos.UserInvite{ID: "inv-new", Email: email}, nil
	}}
	exec(`ALTER TABLE audit_logs ADD CONSTRAINT test_fail_provision CHECK (action <> 'user.invite')`)
	if response := callWithKey(http.MethodPost, "/api/admin/users", newUser, invitationKey); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("forced local failure: %d %s", response.Code, response.Body)
	}
	exec(`ALTER TABLE audit_logs DROP CONSTRAINT test_fail_provision`)
	for range 2 {
		if response := callWithKey(http.MethodPost, "/api/admin/users", newUser, invitationKey); response.Code != http.StatusNoContent {
			t.Fatalf("retry status: %d %s", response.Code, response.Body)
		}
	}
	if deliveries != 1 {
		t.Fatalf("provider deliveries=%d", deliveries)
	}
	if response := call(http.MethodPost, "/api/admin/users", newUser); response.Code != http.StatusConflict {
		t.Fatalf("independent create: %d", response.Code)
	}
	if response := callWithKey(http.MethodPost, "/api/admin/users", strings.Replace(newUser, "New user", "Different name", 1), invitationKey); response.Code != http.StatusConflict {
		t.Fatalf("changed replay: %d", response.Code)
	}

	var invitedID string
	if err := db.QueryRow(`SELECT id FROM users WHERE workos_invitation_id='inv-new' AND workos_id IS NULL
    AND NOT is_active AND display_name='New user' AND EXISTS (SELECT 1 FROM user_roles WHERE user_id=users.id)`).Scan(&invitedID); err != nil {
		t.Fatal(err)
	}
	if response := call(http.MethodPost, "/api/admin/users/"+invitedID+"/activate", ""); response.Code != http.StatusNotFound {
		t.Fatalf("unaccepted activation: %d", response.Code)
	}
	resendPath := "/api/admin/users/" + invitedID + "/invitation/resend"
	if response := call(http.MethodPost, resendPath, ""); response.Code != http.StatusNoContent {
		t.Fatalf("resend: %d %s", response.Code, response.Body)
	}
	server.Identity = testIdentityProvider{priorState: "expired", send: func(_ context.Context, email, _ string) (*workos.UserInvite, error) {
		return &workos.UserInvite{ID: "inv-replacement", Email: email}, nil
	}}
	if response := call(http.MethodPost, resendPath, ""); response.Code != http.StatusNoContent {
		t.Fatalf("expired resend: %d %s", response.Code, response.Body)
	}
	exec(`UPDATE users SET archived_at=now() WHERE id=$1`, invitedID)
	if _, err := db.Exec(`UPDATE users SET is_active=true,workos_id='test-archived' WHERE id=$1`, invitedID); err == nil {
		t.Fatal("archived activation bypassed database check")
	}
	server.Identity = testIdentityProvider{send: func(_ context.Context, email, _ string) (*workos.UserInvite, error) {
		return nil, &workos.APIError{StatusCode: 422}
	}}
	failure := "/api/admin/users"
	for _, name := range []string{"First", "Corrected"} {
		if response := call(http.MethodPost, failure, `{"email":"failed@example.com","displayName":"`+name+`"}`); response.Code != http.StatusBadRequest {
			t.Fatalf("correctable rejection: %d", response.Code)
		}
	}
	if response := call(http.MethodPost, failure, `{"email":"password@example.com","displayName":"Password","password":"test"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("admin password accepted: %d", response.Code)
	}

	response = call(http.MethodPost, "/api/admin/users/"+targetID+"/roles", `{"roleId":"00000000-0000-4000-8000-000000000003","venueId":"venue-test"}`)
	if response.Code != http.StatusNoContent {
		t.Fatalf("assign role: %d %s", response.Code, response.Body.String())
	}
	venue, err := venueResource(context.Background(), db, "venue-test")
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := server.Access.HasResourcePermission(context.Background(), targetID, "schedules.approve", venue)
	if err != nil || allowed {
		t.Fatalf("unexpected inherited approval: %v %v", allowed, err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		path := "/api/admin/users/" + targetID + "/permissions/overrides"
		if method == http.MethodDelete {
			path += "/" + uuid.NewString()
		}
		if result := call(method, path, ""); result.Code != http.StatusNotFound {
			t.Errorf("retired override route %s returned %d", method, result.Code)
		}
	}
	var assignedRoleID string
	if err := db.QueryRow(`SELECT id FROM user_roles WHERE user_id=$1`, targetID).Scan(&assignedRoleID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE venues SET is_active=false WHERE id='venue-test'`); err != nil {
		t.Fatal(err)
	}
	response = call(http.MethodGet, "/api/admin/venues/venue-test/users", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), assignedRoleID) {
		t.Fatalf("suspended assignment list: %d %s", response.Code, response.Body.String())
	}
	response = call(http.MethodPost, "/api/admin/users/"+targetID+"/roles", `{"roleId":"00000000-0000-4000-8000-000000000002","venueId":"venue-test"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("grant at inactive venue: %d %s", response.Code, response.Body.String())
	}
	response = call(http.MethodGet, "/api/admin/users/"+targetID+"/permissions?venueId=unknown", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("unresolved venue: %d %s", response.Code, response.Body.String())
	}
	response = call(http.MethodDelete, "/api/admin/users/"+targetID+"/roles/"+assignedRoleID, "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("remove role: %d %s", response.Code, response.Body.String())
	}
	if _, err := db.Exec(`UPDATE venues SET is_active=true WHERE id='venue-test'`); err != nil {
		t.Fatal(err)
	}
	allowed, err = server.Access.HasResourcePermission(context.Background(), targetID, "schedules.view", venue)
	if err != nil || allowed {
		t.Fatalf("revoked role still grants viewing: %v %v", allowed, err)
	}
	if _, err := db.Exec(`UPDATE users SET is_active=false WHERE id=$1`, targetID); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/admin/users/"+ids[1]+"/deactivate", nil)
	request.Header.Set("Origin", "http://untrusted.example")
	request.AddCookie(&http.Cookie{Name: "session", Value: "browser-session"})
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("untrusted origin status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/admin/users/"+ids[0]+"/roles", strings.NewReader(`{"roleId":"00000000-0000-4000-8000-000000000001"}`))
	request.Header.Set("Origin", "http://localhost:3000")
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "session", Value: "browser-session"})
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("self-assignment status=%d", response.Code)
	}
	// A limited global user manager must not reactivate an Admin's retained role.
	const adminRole = "00000000-0000-4000-8000-000000000001"
	managerRole := uuid.NewString()
	exec(`INSERT INTO roles(id,name,description,assignment_type) VALUES($1,'User manager','Test role','global')`, managerRole)
	exec(`INSERT INTO role_permissions(role_id,permission_id) SELECT $1,id FROM permissions WHERE key='users.manage'`, managerRole)
	exec(`DELETE FROM user_roles WHERE user_id=$1 AND role_id=$2`, ids[0], adminRole)
	exec(`INSERT INTO user_roles(id,user_id,role_id,assignment_type) VALUES($1,$2,$3,'global')`, uuid.NewString(), ids[0], managerRole)
	exec(`UPDATE users SET is_active=false WHERE id=$1`, ids[1])
	response = call(http.MethodPost, "/api/admin/users/"+ids[1]+"/activate", "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("limited manager reactivated Admin grants: %d", response.Code)
	}
	exec(`UPDATE users SET is_active=true WHERE id=$1`, ids[1])
	exec(`DELETE FROM user_roles WHERE user_id=$1 AND role_id=$2`, ids[0], managerRole)
	exec(`INSERT INTO user_roles(id,user_id,role_id,assignment_type) VALUES($1,$2,$3,'global')`, uuid.NewString(), ids[0], adminRole)

	started, resume := make(chan struct{}), make(chan struct{})
	server.Identity = testIdentityProvider{send: func(ctx context.Context, email, _ string) (*workos.UserInvite, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-resume:
			return &workos.UserInvite{ID: "inv-slow", Email: email}, nil
		}
	}}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- call(http.MethodPost, "/api/admin/users", `{"email":"slow@example.com","displayName":"Slow"}`)
	}()
	select {
	case <-started:
	case response := <-result:
		t.Fatalf("provider did not start: %d", response.Code)
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not start in time")
	}
	lockCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	revokeErr := func() error {
		tx, err := db.BeginTx(lockCtx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(lockCtx, `SELECT pg_advisory_xact_lock(87239651)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(lockCtx, `DELETE FROM user_roles WHERE user_id=$1 AND role_id=$2`, ids[0], adminRole); err != nil {
			return err
		}
		return tx.Commit()
	}()
	close(resume)
	response = <-result
	if revokeErr != nil || response.Code != http.StatusForbidden {
		t.Fatalf("slow WorkOS call blocked revocation or bypassed final check: %v %d", revokeErr, response.Code)
	}
	var finalized bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE workos_invitation_id='inv-slow')`).Scan(&finalized); err != nil || finalized {
		t.Fatalf("revoked administrator finalized the user: %v %v", finalized, err)
	}
	exec(`INSERT INTO user_roles(id,user_id,role_id,assignment_type) VALUES($1,$2,$3,'global')`, uuid.NewString(), ids[0], adminRole)
	var committed atomic.Int32
	var workers sync.WaitGroup
	for _, id := range ids {
		workers.Add(1)
		go func(userID string) {
			defer workers.Done()
			ctx := context.Background()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer tx.Rollback()
			if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(87239651)`); err != nil {
				t.Error(err)
				return
			}
			if _, err = tx.ExecContext(ctx, `UPDATE users SET is_active=false WHERE id=$1`, userID); err != nil {
				t.Error(err)
				return
			}
			if err = ensureAdministrator(ctx, tx); err == errConflict {
				return
			} else if err != nil {
				t.Error(err)
				return
			}
			if err = tx.Commit(); err != nil {
				t.Error(err)
				return
			}
			committed.Add(1)
		}(id)
	}
	workers.Wait()
	if committed.Load() != 1 {
		t.Fatalf("committed %d changes, want exactly one", committed.Load())
	}
	var active int
	if err := db.QueryRow(`SELECT count(*) FROM users WHERE is_active`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active=%d err=%v", active, err)
	}
}
