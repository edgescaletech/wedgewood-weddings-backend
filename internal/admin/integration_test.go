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

type testIdentityProvider func(context.Context, string, string, string, string) (string, error)

func (provider testIdentityProvider) Create(ctx context.Context, operationID, email, name, password string) (string, error) {
	return provider(ctx, operationID, email, name, password)
}

func provisionTestUser(_ context.Context, _ string, email, name, password string) (string, error) {
	if email == "failure@example.com" {
		return "", errors.New("upstream exposed " + password)
	}
	if email != "new@example.com" || name != "New user" || password != "test-password-123" {
		return "", errors.New("unexpected provisioning input")
	}
	return "workos-new", nil
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
	for _, file := range []string{"001_phase1_tables.up.sql", "002_auth.up.sql", "004_admin.up.sql", "005_user_admin.up.sql", "006_security_guards.up.sql"} {
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
		if _, err = db.Exec(`INSERT INTO users(id,workos_id,email,is_active) VALUES($1,$2,$2,true)`, id, "workos-"+string(rune('a'+index))); err != nil {
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
	server := &Server{DB: db, Auth: authServer, Access: &authorization.Service{DB: db}, AppURL: "http://localhost:3000", Identity: testIdentityProvider(provisionTestUser)}
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
	if _, err := db.Exec(`INSERT INTO users(id,workos_id,email,is_active) VALUES($1,'workos-scheduler','scheduler@example.com',true)`, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO venues(id,name) VALUES('venue-test','Test')`); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Origin", "http://localhost:3000")
		request.Header.Set("Content-Type", "application/json")
		request.AddCookie(&http.Cookie{Name: "session", Value: "browser-session"})
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
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
	const newUser = `{"email":"new@example.com","displayName":"New user","password":"test-password-123"}`
	var operationIDs []string
	server.Identity = testIdentityProvider(func(ctx context.Context, operationID, email, name, password string) (string, error) {
		operationIDs = append(operationIDs, operationID)
		return provisionTestUser(ctx, operationID, email, name, password)
	})
	exec(`ALTER TABLE audit_logs ADD CONSTRAINT test_fail_provision CHECK (action <> 'user.create')`)
	if response := call(http.MethodPost, "/api/admin/users", newUser); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("forced local failure: %d", response.Code)
	}
	exec(`ALTER TABLE audit_logs DROP CONSTRAINT test_fail_provision`)
	response = call(http.MethodPost, "/api/admin/users", newUser)
	if response.Code != http.StatusNoContent || len(operationIDs) != 2 || operationIDs[0] != operationIDs[1] {
		t.Fatalf("retry did not reuse the WorkOS operation: %d %v", response.Code, operationIDs)
	}
	if response := call(http.MethodPost, "/api/admin/users", newUser); response.Code != http.StatusNoContent || len(operationIDs) != 2 {
		t.Fatalf("completed retry called WorkOS again: %d %v", response.Code, operationIDs)
	}
	var usersCreated, auditsCreated int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM users WHERE workos_id='workos-new'),
		(SELECT count(*) FROM audit_logs WHERE action='user.create' AND metadata->>'workosId'='workos-new')`).Scan(&usersCreated, &auditsCreated); err != nil || usersCreated != 1 || auditsCreated != 1 {
		t.Fatalf("retry produced duplicate or missing records: users=%d audits=%d err=%v", usersCreated, auditsCreated, err)
	}
	server.Identity = testIdentityProvider(provisionTestUser)
	if response.Code != http.StatusNoContent {
		t.Fatalf("provision user: %d %s", response.Code, response.Body.String())
	}
	var newActive bool
	if err := db.QueryRow(`SELECT is_active FROM users WHERE workos_id='workos-new'`).Scan(&newActive); err != nil || newActive {
		t.Fatalf("new user active=%v err=%v", newActive, err)
	}
	var passwordStored bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE to_jsonb(users)::text LIKE '%test-password-123%') OR EXISTS(SELECT 1 FROM audit_logs WHERE metadata::text LIKE '%test-password-123%')`).Scan(&passwordStored); err != nil || passwordStored {
		t.Fatalf("password persisted in user or audit data: %v; error=%v", passwordStored, err)
	}
	response = call(http.MethodPost, "/api/admin/users", `{"email":"failure@example.com","displayName":"Failed user","password":"private-test-password"}`)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "private-test-password") {
		t.Fatalf("unexpected failure response: %d", response.Code)
	}
	var failedRows int
	if err := db.QueryRow(`SELECT count(*) FROM users WHERE email='failure@example.com'`).Scan(&failedRows); err != nil || failedRows != 0 {
		t.Fatalf("failed provisioning persisted user: count=%d err=%v", failedRows, err)
	}
	response = call(http.MethodPost, "/api/admin/users", `{"email":"empty@example.com","displayName":"Empty password","password":""}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("empty password status: %d", response.Code)
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
	server.Identity = testIdentityProvider(func(ctx context.Context, _, _, _, _ string) (string, error) {
		close(started)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-resume:
			return "workos-slow", nil
		}
	})
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- call(http.MethodPost, "/api/admin/users", `{"email":"slow@example.com","displayName":"Slow","password":"test-password"}`)
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
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE workos_id='workos-slow')`).Scan(&finalized); err != nil || finalized {
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
