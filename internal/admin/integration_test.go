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

type testIdentityProvider struct{}

func (testIdentityProvider) Create(_ context.Context, email, name, password string) (string, error) {
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
		if _, err = db.Exec(`INSERT INTO user_roles(id,user_id,role_id) VALUES($1,$2,'00000000-0000-4000-8000-000000000001')`, uuid.NewString(), id); err != nil {
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
	server := &Server{DB: db, Auth: authServer, Access: &authorization.Service{DB: db}, AppURL: "http://localhost:3000", Identity: testIdentityProvider{}}
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

	response = call(http.MethodPost, "/api/admin/users", `{"email":"new@example.com","displayName":"New user","password":"test-password-123"}`)
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
	venue := "venue-test"
	allowed, err := server.Access.HasPermission(context.Background(), targetID, "schedules.approve", &venue)
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
	response = call(http.MethodDelete, "/api/admin/users/"+targetID+"/roles/"+assignedRoleID, "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("remove role: %d %s", response.Code, response.Body.String())
	}
	allowed, err = server.Access.HasPermission(context.Background(), targetID, "schedules.view", &venue)
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
