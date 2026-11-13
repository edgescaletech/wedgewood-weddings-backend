package auth

import (
	"context"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/golang-jwt/jwt/v5"
	workos "github.com/workos/workos-go/v7"
)

type resilienceProvider struct {
	fakeProvider
	refreshErr error
	revokedID  string
	revokeErr  error
}

func (provider *resilienceProvider) Refresh(context.Context, string) (*workos.AuthenticateResponse, error) {
	return provider.response, provider.refreshErr
}

func (provider *resilienceProvider) Revoke(_ context.Context, sessionID string) error {
	provider.revokedID = sessionID
	return provider.revokeErr
}

// TestLogoutRevokesExpiredSession confirms token expiry does not skip WorkOS revocation.
func TestLogoutRevokesExpiredSession(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	secret := strings.Repeat("a", 32)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sid": "session_123", "exp": time.Now().Add(-time.Hour).Unix()})
	signed, err := token.SignedString([]byte("test-key"))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := workos.SealSessionFromAuthResponse(signed, "refresh", &workos.User{ID: "user_1"}, nil, secret)
	if err != nil {
		t.Fatal(err)
	}
	provider := &resilienceProvider{}
	server := &Server{database: database, config: Config{CookiePassword: secret, AppURL: "http://localhost:3000"}, provider: provider}
	mock.ExpectQuery("DELETE FROM auth_sessions").WillReturnRows(sqlmock.NewRows([]string{"sealed_session"}).AddRow(sealed))
	request := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	request.Header.Set("Origin", "http://localhost:3000")
	request.AddCookie(&http.Cookie{Name: "session", Value: "opaque"})
	recorder := httptest.NewRecorder()
	server.Logout(recorder, request)
	if recorder.Code != http.StatusNoContent || provider.revokedID != "session_123" {
		t.Fatalf("status %d; revoked session %q", recorder.Code, provider.revokedID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestRefreshFailure preserves the session for transient errors and reports
// revoked refresh tokens as unauthenticated.
func TestRefreshFailure(t *testing.T) {
	tests := []struct {
		name       string
		refreshErr error
		status     int
	}{
		{"network", errors.New("network unavailable"), http.StatusServiceUnavailable},
		{"rate limited", &workos.APIError{StatusCode: 429}, http.StatusServiceUnavailable},
		{"revoked", &workos.APIError{StatusCode: 400, ErrorCode: "invalid_grant"}, http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			secret := strings.Repeat("a", 32)
			sealed, err := workos.SealSessionFromAuthResponse("expired", "refresh", &workos.User{ID: "user_1"}, nil, secret)
			if err != nil {
				t.Fatal(err)
			}
			provider := &resilienceProvider{refreshErr: test.refreshErr}
			server := &Server{database: database, config: Config{CookiePassword: secret}, provider: provider,
				verify: func(context.Context, string) (*claims, error) { return nil, jwt.ErrTokenExpired },
			}
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT users.id").WillReturnRows(sqlmock.NewRows([]string{"id", "workos_id", "email", "is_active", "sealed_session"}).AddRow("id", "user_1", "a@example.com", true, sealed))
			mock.ExpectRollback()
			request := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
			request.AddCookie(&http.Cookie{Name: "session", Value: "opaque"})
			recorder := httptest.NewRecorder()
			server.Middleware(http.HandlerFunc(server.Me)).ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status %d, want %d", recorder.Code, test.status)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestRotatedTokenSavedDuringKeyOutage confirms a refresh is saved before JWKS
// verification and no protected response is served while keys are unavailable.
func TestRotatedTokenSavedDuringKeyOutage(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	secret := strings.Repeat("a", 32)
	user := &workos.User{ID: "user_1"}
	sealed, err := workos.SealSessionFromAuthResponse("expired", "old", user, nil, secret)
	if err != nil {
		t.Fatal(err)
	}
	provider := &resilienceProvider{fakeProvider: fakeProvider{response: &workos.AuthenticateResponse{User: user, AccessToken: "new", RefreshToken: "rotated"}}}
	server := &Server{database: database, config: Config{CookiePassword: secret}, provider: provider,
		verify: func(_ context.Context, token string) (*claims, error) {
			if token == "expired" {
				return nil, jwt.ErrTokenExpired
			}
			return nil, errJWKSUnavailable
		},
	}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT users.id").WillReturnRows(sqlmock.NewRows([]string{"id", "workos_id", "email", "is_active", "sealed_session"}).AddRow("id", "user_1", "a@example.com", true, sealed))
	mock.ExpectExec("UPDATE auth_sessions").WithArgs(sealedRefreshMatcher{secret: secret, refresh: "rotated"}, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	request := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: "opaque"})
	recorder := httptest.NewRecorder()
	server.Middleware(http.HandlerFunc(server.Me)).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", recorder.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// sealedRefreshMatcher verifies that the replacement refresh token is encrypted before storage.
type sealedRefreshMatcher struct{ secret, refresh string }

func (matcher sealedRefreshMatcher) Match(value driver.Value) bool {
	sealed, ok := value.(string)
	if !ok {
		return false
	}
	data, err := workos.Unseal[workos.SessionData](sealed, matcher.secret)
	return err == nil && data.RefreshToken == matcher.refresh
}

// TestLogoutReportsRevocationFailure makes a failed WorkOS revocation visible
// while still clearing the local session cookie.
func TestLogoutReportsRevocationFailure(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	secret := strings.Repeat("a", 32)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sid": "session_123"})
	signed, err := token.SignedString([]byte("test-key"))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := workos.SealSessionFromAuthResponse(signed, "refresh", &workos.User{ID: "user_1"}, nil, secret)
	if err != nil {
		t.Fatal(err)
	}
	provider := &resilienceProvider{revokeErr: errors.New("unavailable")}
	server := &Server{database: database, config: Config{CookiePassword: secret, AppURL: "http://localhost:3000"}, provider: provider}
	mock.ExpectQuery("DELETE FROM auth_sessions").WillReturnRows(sqlmock.NewRows([]string{"sealed_session"}).AddRow(sealed))
	request := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	request.Header.Set("Origin", "http://localhost:3000")
	request.AddCookie(&http.Cookie{Name: "session", Value: "opaque"})
	recorder := httptest.NewRecorder()
	server.Logout(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || provider.revokedID != "session_123" {
		t.Fatalf("status %d, revoked %q", recorder.Code, provider.revokedID)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 || cookies[0].MaxAge >= 0 {
		t.Fatal("session cookie not cleared")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestSigningKeyOutageIsRetryable checks the JWT library retains the JWKS error.
func TestSigningKeyOutageIsRetryable(t *testing.T) {
	verifier := &verifier{url: "https://jwks.example/keys", issuer: "issuer", clientID: "client", client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("network failure") })}}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "user_1"})
	// Build an RS256-shaped JWT; the key lookup fails before signature verification.
	token.Method = jwt.SigningMethodRS256
	token.Header["alg"] = "RS256"
	token.Header["kid"] = "key_1"
	header, err := token.SigningString()
	if err != nil {
		t.Fatal(err)
	}
	_, err = verifier.verify(context.Background(), header+".c2ln")
	if !errors.Is(err, errJWKSUnavailable) {
		t.Fatalf("expected key outage, got %v", err)
	}
}
