package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	workos "github.com/workos/workos-go/v7"
)

type signupMock struct {
	fakeProvider
	email    string
	password string
	err      error
}

func (provider *signupMock) CreateUser(_ context.Context, email, password string) error {
	provider.email, provider.password = email, password
	return provider.err
}

func TestSignupCreatesWorkOSUser(t *testing.T) {
	provider := &signupMock{}
	server := &Server{config: Config{AppURL: "http://localhost:3000"}, provider: provider}
	response := httptest.NewRecorder()
	server.Signup(response, recoveryRequest("/auth/signup", `{"email":" person@example.com ","password":"secret"}`))

	if response.Code != http.StatusNoContent || provider.email != "person@example.com" || provider.password != "secret" {
		t.Fatalf("signup failed: status %d, email %q", response.Code, provider.email)
	}
}

func TestSignupRejectsUntrustedOrigin(t *testing.T) {
	provider := &signupMock{}
	server := &Server{config: Config{AppURL: "http://localhost:3000"}, provider: provider}
	request := recoveryRequest("/auth/signup", `{"email":"person@example.com","password":"secret"}`)
	request.Header.Set("Origin", "https://untrusted.example")
	response := httptest.NewRecorder()
	server.Signup(response, request)
	if response.Code != http.StatusForbidden || provider.email != "" {
		t.Fatal("signup accepted an untrusted origin")
	}
}

func TestSignupHidesProviderErrors(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{
		{&workos.APIError{StatusCode: 422, Message: "sensitive details: secret"}, http.StatusBadRequest},
		{errors.New("sensitive details"), http.StatusServiceUnavailable},
	} {
		provider := &signupMock{err: test.err}
		server := &Server{config: Config{AppURL: "http://localhost:3000"}, provider: provider}
		response := httptest.NewRecorder()
		server.Signup(response, recoveryRequest("/auth/signup", `{"email":"person@example.com","password":"secret"}`))
		if response.Code != test.status || strings.Contains(response.Body.String(), "sensitive details") {
			t.Fatalf("unexpected signup response: %d %q", response.Code, response.Body.String())
		}
	}
}

func TestSignupExplainsPasswordAndConfigurationErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     *workos.APIError
		status  int
		message string
	}{
		{"password", &workos.APIError{StatusCode: 422, RawBody: `{"message":"Validation failed","errors":[{"field":"password","message":"Password must be at least 12 characters"}]}`}, http.StatusBadRequest, "Password must be at least 12 characters"},
		{"password code", &workos.APIError{StatusCode: 400, Code: "password_contains_email", Message: "Password must not contain your email"}, http.StatusBadRequest, "Password must not contain your email"},
		{"configuration", &workos.APIError{StatusCode: 401}, http.StatusServiceUnavailable, "temporarily unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{config: Config{AppURL: "http://localhost:3000"}, provider: &signupMock{err: test.err}}
			response := httptest.NewRecorder()
			server.Signup(response, recoveryRequest("/auth/signup", `{"email":"person@example.com","password":"secret"}`))
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.message) {
				t.Fatalf("unexpected signup response: %d %q", response.Code, response.Body.String())
			}
		})
	}
}
