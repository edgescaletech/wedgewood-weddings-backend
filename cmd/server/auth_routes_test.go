package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"wedding-project/internal/auth"
)

func TestServerRoutesPreserveAccessBoundaries(t *testing.T) {
	database, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	authServer, err := auth.New(auth.Config{
		ClientID:       "client_test",
		APIKey:         "test-key",
		RedirectURI:    "http://localhost:8080/auth/callback",
		CookiePassword: strings.Repeat("x", 32),
		Issuer:         "https://example.com",
		AppURL:         "http://localhost:3000",
		Secure:         false,
	}, database)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	registerRoutes(mux, database, nil, nil, authServer)

	for _, path := range []string{
		"/api/events", "/api/events/event_1", "/api/labor-goals/summary",
		"/api/admin/labor-goals/upload", "/api/admin/accounting-periods/upload",
		"/api/admin/users", "/auth/access",
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", path, response.Code)
		}
	}

	health := httptest.NewRecorder()
	mux.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Errorf("health check: got %d, want 200", health.Code)
	}

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/auth/signup", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("signup status = %d, want 404", response.Code)
	}

	invite := httptest.NewRecorder()
	mux.ServeHTTP(invite, httptest.NewRequest(http.MethodGet, "/auth/invite", nil))
	if invite.Code != http.StatusMethodNotAllowed {
		t.Fatalf("invitation GET status = %d, want 405", invite.Code)
	}
}
