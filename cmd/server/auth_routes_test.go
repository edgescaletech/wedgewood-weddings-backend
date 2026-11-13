package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"wedding-project/internal/auth"
)

// Event routes must reject anonymous requests before reaching their handlers.
func TestEventRoutesRequireSession(t *testing.T) {
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
	protected := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("anonymous request reached event handler")
	})
	mux.Handle("/api/events", authServer.Middleware(protected))
	mux.Handle("/api/events/", authServer.Middleware(protected))
	mux.HandleFunc("/healthz", healthzHandler)

	for _, path := range []string{"/api/events", "/api/events/event_1"} {
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
}
