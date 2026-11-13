package auth

import (
	"context"
	"encoding/json"
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

type recoveryMock struct {
	fakeProvider
	passwordErr       error
	resetErr          error
	resetCalls        int
	verificationCalls int
}

func (provider *recoveryMock) Password(context.Context, string, string) (*workos.AuthenticateResponse, error) {
	return nil, provider.passwordErr
}
func (provider *recoveryMock) VerifyEmail(_ context.Context, token, code string) (*workos.AuthenticateResponse, error) {
	provider.verificationCalls++
	if token != "private-pending-token" || code != "123456" {
		return nil, errors.New("invalid code")
	}
	return provider.response, nil
}
func (provider *recoveryMock) RequestPasswordReset(context.Context, string) error {
	provider.resetCalls++
	return provider.resetErr
}
func (provider *recoveryMock) ConfirmPasswordReset(_ context.Context, token, password string) (*workos.ResetPasswordResponse, error) {
	if token != "valid-token" || password != "new-password" {
		return nil, errors.New("invalid token")
	}
	return &workos.ResetPasswordResponse{User: &workos.User{ID: "user_1"}}, nil
}
func recoveryRequest(path, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Origin", "http://localhost:3000")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestPasswordLoginStartsEmailVerification(t *testing.T) {
	provider := &recoveryMock{passwordErr: &workos.EmailVerificationRequiredError{APIError: &workos.APIError{PendingAuthenticationToken: "private-pending-token"}}}
	server := &Server{config: Config{AppURL: "http://localhost:3000", CookiePassword: strings.Repeat("a", 32), Secure: true}, provider: provider}
	recorder := httptest.NewRecorder()
	server.PasswordLogin(recorder, recoveryRequest("/auth/password", `{"email":"person@example.com","password":"secret"}`))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "private-pending-token") {
		t.Fatal("pending token exposed")
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body["status"] != "email_verification_required" {
		t.Fatal("missing verification status")
	}
	cookies := recorder.Result().Cookies()
	var pendingCookie *http.Cookie
	for _, cookie := range cookies {
		if cookie.Name == verificationCookie && cookie.MaxAge > 0 {
			pendingCookie = cookie
		}
	}
	if pendingCookie == nil || !pendingCookie.HttpOnly || !pendingCookie.Secure || pendingCookie.Value == "private-pending-token" {
		t.Fatal("pending cookie is not protected")
	}
}

func TestVerifyEmailCompletesSession(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	provider := &recoveryMock{fakeProvider: fakeProvider{response: &workos.AuthenticateResponse{User: &workos.User{ID: "user_1", Email: "person@example.com"}, AccessToken: "access", RefreshToken: "refresh"}}}
	secret := strings.Repeat("a", 32)
	server := &Server{database: database, config: Config{AppURL: "http://localhost:3000", CookiePassword: secret}, provider: provider, verify: func(context.Context, string) (*claims, error) {
		return &claims{RegisteredClaims: jwt.RegisteredClaims{Subject: "user_1"}}, nil
	}}
	sealed, err := workos.Seal(verificationState{Token: "private-pending-token", Expires: time.Now().Add(time.Minute).Unix()}, secret)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO users").WithArgs(sqlmock.AnyArg(), "user_1", "person@example.com").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO auth_sessions").WithArgs(sqlmock.AnyArg(), "user_1", sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	request := recoveryRequest("/auth/verify-email", `{"code":"123456"}`)
	request.AddCookie(&http.Cookie{Name: verificationCookie, Value: sealed})
	recorder := httptest.NewRecorder()
	server.VerifyEmail(recorder, request)
	if recorder.Code != http.StatusNoContent || provider.verificationCalls != 1 {
		t.Fatalf("status %d", recorder.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryRejectsBadOriginAndExpiredChallenge(t *testing.T) {
	provider := &recoveryMock{}
	secret := strings.Repeat("a", 32)
	server := &Server{config: Config{AppURL: "http://localhost:3000", CookiePassword: secret}, provider: provider}
	request := recoveryRequest("/auth/forgot-password", `{"email":"person@example.com"}`)
	request.Header.Set("Origin", "http://other.example")
	recorder := httptest.NewRecorder()
	server.ForgotPassword(recorder, request)
	if recorder.Code != http.StatusForbidden || provider.resetCalls != 0 {
		t.Fatal("untrusted origin accepted")
	}
	sealed, err := workos.Seal(verificationState{Token: "private-pending-token", Expires: time.Now().Add(-time.Minute).Unix()}, secret)
	if err != nil {
		t.Fatal(err)
	}
	request = recoveryRequest("/auth/verify-email", `{"code":"123456"}`)
	request.AddCookie(&http.Cookie{Name: verificationCookie, Value: sealed})
	recorder = httptest.NewRecorder()
	server.VerifyEmail(recorder, request)
	if recorder.Code != http.StatusUnauthorized || provider.verificationCalls != 0 {
		t.Fatal("expired challenge accepted")
	}
}

func TestPasswordResetKeepsAccountExistencePrivate(t *testing.T) {
	for _, resetErr := range []error{nil, &workos.APIError{StatusCode: 404}} {
		provider := &recoveryMock{resetErr: resetErr}
		server := &Server{config: Config{AppURL: "http://localhost:3000"}, provider: provider}
		recorder := httptest.NewRecorder()
		server.ForgotPassword(recorder, recoveryRequest("/auth/forgot-password", `{"email":"person@example.com"}`))
		if recorder.Code != http.StatusNoContent || recorder.Body.Len() != 0 {
			t.Fatalf("status %d", recorder.Code)
		}
	}
}

func TestResetPasswordDeletesLocalSessions(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	server := &Server{database: database, config: Config{AppURL: "http://localhost:3000"}, provider: &recoveryMock{}}
	mock.ExpectExec("DELETE FROM auth_sessions").WithArgs("user_1").WillReturnResult(sqlmock.NewResult(0, 1))
	recorder := httptest.NewRecorder()
	server.ResetPassword(recorder, recoveryRequest("/auth/reset-password", `{"token":"valid-token","newPassword":"new-password"}`))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status %d", recorder.Code)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
