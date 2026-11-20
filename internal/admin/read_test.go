package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"wedding-project/internal/auth"
	"wedding-project/internal/authorization"
)

func TestAdminRoutesRequireAuthentication(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authServer, err := auth.New(auth.Config{
		ClientID: "client", APIKey: "key", CookiePassword: strings.Repeat("x", 32),
		Issuer: "https://example.com", RedirectURI: "http://localhost:8080/auth/callback", AppURL: "http://localhost:3000",
	}, db)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	server := &Server{DB: db, Auth: authServer, Access: &authorization.Service{DB: db}}
	server.Register(mux)
	for _, path := range []string{"/api/admin/users", "/api/admin/roles", "/api/admin/venues"} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", path, response.Code)
		}
	}
	for _, path := range []string{"/api/admin/roles/00000000-0000-4000-8000-000000000001", "/api/admin/permissions"} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", path, response.Code)
		}
	}
	for _, endpoint := range []struct{ method, path string }{
		{http.MethodPost, "/api/admin/users"},
		{http.MethodPost, "/api/admin/users/00000000-0000-4000-8000-000000000001/roles"},
		{http.MethodDelete, "/api/admin/users/00000000-0000-4000-8000-000000000001/roles/00000000-0000-4000-8000-000000000002"},
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(endpoint.method, endpoint.path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: got %d, want 401", endpoint.method, endpoint.path, response.Code)
		}
	}
}

func TestPaginationRejectsInvalidValues(t *testing.T) {
	for _, query := range []string{"?limit=101", "?limit=0", "?offset=-1", "?limit=abc"} {
		request := httptest.NewRequest(http.MethodGet, "/api/admin/users"+query, nil)
		if _, _, err := pagination(request); err == nil {
			t.Errorf("accepted %s", query)
		}
	}
}
