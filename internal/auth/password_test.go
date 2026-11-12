package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/golang-jwt/jwt/v5"
	workos "github.com/workos/workos-go/v7"
)

type passwordMock struct {
	fakeProvider
	calls int
}

func (provider *passwordMock) Password(_ context.Context, email, password string) (*workos.AuthenticateResponse, error) {
	provider.calls++
	return provider.response, nil
}

func TestPasswordLoginRejectsUntrustedOrigin(t *testing.T) {
	provider := &passwordMock{}
	server := &Server{config: Config{AppURL: "http://localhost:5173"}, provider: provider}
	request := httptest.NewRequest(http.MethodPost, "/auth/password", strings.NewReader(`{"email":"a@example.com","password":"secret"}`))
	request.Header.Set("Origin", "http://evil.example")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.PasswordLogin(recorder, request)
	if recorder.Code != http.StatusForbidden || provider.calls != 0 {
		t.Fatalf("status %d, WorkOS calls %d", recorder.Code, provider.calls)
	}
}

func TestPasswordLoginCreatesSession(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	provider := &passwordMock{fakeProvider: fakeProvider{response: &workos.AuthenticateResponse{
		User: &workos.User{ID: "user_1", Email: "a@example.com"}, AccessToken: "access", RefreshToken: "refresh",
	}}}
	server := &Server{
		config:   Config{AppURL: "http://localhost:5173", CookiePassword: strings.Repeat("a", 32)},
		database: database,
		provider: provider,
		verify: func(context.Context, string) (*claims, error) {
			return &claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "user_1"}}, nil
		},
	}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO users").WithArgs(sqlmock.AnyArg(), "user_1", "a@example.com").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO auth_sessions").WithArgs(sqlmock.AnyArg(), "user_1", sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	request := httptest.NewRequest(http.MethodPost, "/auth/password", strings.NewReader(`{"email":"a@example.com","password":"secret"}`))
	request.Header.Set("Origin", "http://localhost:5173")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.PasswordLogin(recorder, request)
	if recorder.Code != http.StatusNoContent || provider.calls != 1 {
		t.Fatalf("status %d, WorkOS calls %d", recorder.Code, provider.calls)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "session" || !cookies[0].HttpOnly {
		t.Fatal("missing HTTP-only session cookie")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
