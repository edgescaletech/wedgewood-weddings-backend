package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/golang-jwt/jwt/v5"
	workos "github.com/workos/workos-go/v7"
)

type fakeProvider struct {
	exchanges int
	response  *workos.AuthenticateResponse
}

func (p *fakeProvider) Start(string) (*workos.AuthKitPKCEAuthorizationURLResult, error) {
	return &workos.AuthKitPKCEAuthorizationURLResult{URL: "https://example.com/login", State: "state", CodeVerifier: "verifier"}, nil
}
func (p *fakeProvider) Exchange(context.Context, string, string) (*workos.AuthenticateResponse, error) {
	p.exchanges++
	return p.response, nil
}
func (p *fakeProvider) Refresh(context.Context, string) (*workos.AuthenticateResponse, error) {
	return p.response, nil
}
func (*fakeProvider) Revoke(context.Context, string) error { return nil }

func TestLoginAndInvalidState(t *testing.T) {
	p := &fakeProvider{}
	s := &Server{config: Config{CookiePassword: strings.Repeat("a", 32), Secure: true}, provider: p}
	w := httptest.NewRecorder()
	s.Login(w, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("login status: %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("state cookie is not secure")
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?code=secret&state=wrong", nil)
	req.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	s.Callback(w, req)
	if w.Code != http.StatusBadRequest || p.exchanges != 0 {
		t.Fatalf("invalid state accepted: %d, exchanges %d", w.Code, p.exchanges)
	}
}
func TestMiddlewareRejectsMissingAndInactiveSessions(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Server{database: db}
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unexpected access") })
	w := httptest.NewRecorder()
	s.Middleware(next).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != 401 {
		t.Fatalf("missing session: %d", w.Code)
	}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT users.id").WillReturnRows(sqlmock.NewRows([]string{"id", "workos_id", "email", "is_active", "sealed_session"}).AddRow("id", "user_1", "a@example.com", false, "sealed"))
	mock.ExpectRollback()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "session"})
	w = httptest.NewRecorder()
	s.Middleware(next).ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("inactive session: %d", w.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func TestVerifySessionToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := json.Marshal(map[string]any{"keys": []any{map[string]string{
			"kty": "RSA", "kid": "key1", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})
	v := verifier{url: "https://jwks.example/keys", issuer: "https://issuer", clientID: "client", client: &http.Client{Transport: transport}}
	cases := []struct {
		name     string
		issuer   string
		client   string
		audience []string
		expires  time.Time
		valid    bool
	}{
		{"valid", "https://issuer", "client", nil, time.Now().Add(time.Minute), true},
		{"expired", "https://issuer", "client", nil, time.Now().Add(-time.Minute), false},
		{"wrong issuer", "https://wrong", "client", nil, time.Now().Add(time.Minute), false},
		{"wrong client", "https://issuer", "other", nil, time.Now().Add(time.Minute), false},
		{"wrong audience", "https://issuer", "client", []string{"other"}, time.Now().Add(time.Minute), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := claims{ClientID: tc.client, SessionID: "session", RegisteredClaims: jwt.RegisteredClaims{Issuer: tc.issuer, Subject: "user", Audience: tc.audience, ExpiresAt: jwt.NewNumericDate(tc.expires), IssuedAt: jwt.NewNumericDate(time.Now())}}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
			token.Header["kid"] = "key1"
			signed, err := token.SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.verify(context.Background(), signed)
			if (err == nil) != tc.valid {
				t.Fatalf("verification error: %v", err)
			}
		})
	}
}

// roundTripFunc supplies deterministic JWKS responses without opening a network socket.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestCallbackCreatesInactiveUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider := &fakeProvider{response: &workos.AuthenticateResponse{User: &workos.User{ID: "user_1", Email: "person@example.com"}, AccessToken: "access", RefreshToken: "refresh"}}
	server := &Server{database: db, config: Config{CookiePassword: strings.Repeat("a", 32), AppURL: "https://app.example", Secure: true}, provider: provider,
		verify: func(context.Context, string) (*claims, error) {
			return &claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "user_1"}}, nil
		},
	}
	pendingCookie, err := workos.Seal(pending{State: "valid", Verifier: "verifier", Expires: time.Now().Add(time.Minute).Unix()}, server.config.CookiePassword)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO users").WithArgs(sqlmock.AnyArg(), "user_1", "person@example.com").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO auth_sessions").WithArgs(sqlmock.AnyArg(), "user_1", sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	request := httptest.NewRequest(http.MethodGet, "/auth/callback?code=code&state=valid", nil)
	request.AddCookie(&http.Cookie{Name: "auth_state", Value: pendingCookie})
	recorder := httptest.NewRecorder()
	server.Callback(recorder, request)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("callback status: %d", recorder.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshExpiredAccessToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user := &workos.User{ID: "user_1", Email: "person@example.com"}
	secret := strings.Repeat("a", 32)
	sealed, err := workos.SealSessionFromAuthResponse("expired", "old-refresh", user, nil, secret)
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakeProvider{response: &workos.AuthenticateResponse{User: user, AccessToken: "fresh", RefreshToken: "new-refresh"}}
	server := &Server{database: db, config: Config{CookiePassword: secret}, provider: provider,
		verify: func(_ context.Context, token string) (*claims, error) {
			if token == "expired" {
				return nil, jwt.ErrTokenExpired
			}
			return &claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "user_1"}}, nil
		},
	}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT users.id").WillReturnRows(sqlmock.NewRows([]string{"id", "workos_id", "email", "is_active", "sealed_session"}).AddRow("local-id", "user_1", user.Email, true, sealed))
	mock.ExpectExec("UPDATE auth_sessions").WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	request := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: "session"})
	recorder := httptest.NewRecorder()
	server.Middleware(http.HandlerFunc(server.Me)).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("refresh status: %d", recorder.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
