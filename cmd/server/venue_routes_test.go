package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/golang-jwt/jwt/v5"
	workos "github.com/workos/workos-go/v7"
	"wedding-project/internal/auth"
)

type venueRouteTransport func(*http.Request) (*http.Response, error)

func (f venueRouteTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise registerRoutes and the real session/permission middleware without
// external WorkOS requests or a shared development database.
func venueRouteHarness(t *testing.T) (*http.ServeMux, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := json.Marshal(map[string]any{"keys": []any{map[string]string{
		"kty": "RSA", "kid": "venue-route-test", "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	original := http.DefaultTransport
	http.DefaultTransport = venueRouteTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(jwks))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	secret := strings.Repeat("x", 32)
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "user-route", "sid": "session-route", "iss": "https://issuer.example",
		"client_id": "client_test", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	token.Header["kid"] = "venue-route-test"
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := workos.SealSessionFromAuthResponse(signed, "refresh", &workos.User{ID: "user-route", Email: "test@example.test"}, nil, secret)
	if err != nil {
		t.Fatal(err)
	}
	server, err := auth.New(auth.Config{ClientID: "client_test", APIKey: "test-key", RedirectURI: "http://localhost:8080/auth/callback", CookiePassword: secret, Issuer: "https://issuer.example", AppURL: "http://localhost:3000"}, db)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerRoutes(mux, db, nil, nil, server)
	expectSession := func() {
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT users.id").WillReturnRows(sqlmock.NewRows([]string{"id", "workos_id", "email", "display_name", "is_active", "sealed_session"}).AddRow("local-route", "user-route", "test@example.test", "Test User", true, sealed))
		mock.ExpectCommit()
	}
	return mux, mock, expectSession
}

func venueRouteRequest(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "session", Value: "route-cookie"})
	r.Header.Set("Origin", "http://localhost:3000")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestRegisteredVenuesRouteListsOnlyAuthorizedVenues(t *testing.T) {
	mux, mock, session := venueRouteHarness(t)
	for _, allowed := range []bool{true, false} {
		session()
		grants := sqlmock.NewRows([]string{"external_id"})
		if allowed {
			grants.AddRow("venue-a")
		}
		mock.ExpectQuery("SELECT rr.external_id").WithArgs("local-route", "labor_goals.view").WillReturnRows(grants)
		mock.ExpectQuery("SELECT id, name, COALESCE").WillReturnRows(sqlmock.NewRows([]string{"id", "name", "venue_number", "region", "timezone"}).AddRow("venue-a", "Allowed venue", "101", "West", "America/Denver").AddRow("venue-b", "Private venue", "102", "East", "America/Denver"))
		response := venueRouteRequest(mux, http.MethodGet, "/api/venues", "")
		var body struct {
			Count  int `json:"count"`
			Venues []struct {
				ID string `json:"id"`
			} `json:"venues"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 200 {
			t.Fatalf("list: %d %s (%v)", response.Code, response.Body, err)
		}
		want := 0
		if allowed {
			want = 1
		}
		if body.Count != want || len(body.Venues) != want || strings.Contains(response.Body.String(), "Private venue") {
			t.Fatalf("leaked venue or wrong count: %s", response.Body)
		}
		if allowed && body.Venues[0].ID != "venue-a" {
			t.Fatalf("wrong venue: %s", response.Body)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRegisteredVenueAdminRoutesInvokeIntendedHandlers(t *testing.T) {
	for _, tc := range []struct{ name, method, path, body, field string }{
		{"overview", http.MethodGet, "/api/admin/venues-overview?week=2027-01-04", "", "periodKey"},
		{"region list", http.MethodGet, "/api/admin/regions", "", "regions"},
		{"region update", http.MethodPost, "/api/admin/regions", `{"regionName":"West","venueIds":["venue-a"]}`, "updated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, mock, session := venueRouteHarness(t)
			session()
			mock.ExpectQuery("SELECT EXISTS").WithArgs("local-route", "labor_goals.manage", nil).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(true))
			switch tc.name {
			case "overview":
				mock.ExpectQuery("SELECT id, year, period_number").WillReturnError(sql.ErrNoRows)
				mock.ExpectQuery("SELECT v.id").WillReturnRows(sqlmock.NewRows([]string{"id", "name", "venue_number", "region", "foh_target", "boh_target"}).AddRow("venue-a", "Venue A", "101", "West", 2.5, nil))
			case "region list":
				mock.ExpectQuery("SELECT COALESCE").WillReturnRows(sqlmock.NewRows([]string{"r_name", "venue_count"}).AddRow("West", 1))
			case "region update":
				mock.ExpectExec("UPDATE venues").WithArgs("West", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			response := venueRouteRequest(mux, tc.method, tc.path, tc.body)
			var result map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != 200 || result[tc.field] == nil {
				t.Fatalf("%s: %d %s (%v)", tc.path, response.Code, response.Body, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRegisteredVenueAdminRoutesRejectMissingManagementPermission(t *testing.T) {
	mux, mock, session := venueRouteHarness(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/admin/venues-overview"},
		{http.MethodGet, "/api/admin/regions"},
		{http.MethodPost, "/api/admin/regions"},
	} {
		session()
		mock.ExpectQuery("SELECT EXISTS").WithArgs("local-route", "labor_goals.manage", nil).WillReturnRows(sqlmock.NewRows([]string{"allowed"}).AddRow(false))
		response := venueRouteRequest(mux, tc.method, tc.path, `{"regionName":"West","venueIds":["venue-a"]}`)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s: %d %s", tc.path, response.Code, response.Body)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRegisteredLaborTargetsScopeQueriesToAuthorizedVenues(t *testing.T) {
	for _, tc := range []struct {
		name, path            string
		venue, region         string
		allowed               []string
		wantStatus, wantCount int
	}{
		{name: "year across venues", path: "/api/labor-goals/targets?year=2027", allowed: []string{"venue-a"}, wantStatus: 200, wantCount: 1},
		{name: "region across venues", path: "/api/labor-goals/targets?year=2027&region=West", region: "West", allowed: []string{"venue-a"}, wantStatus: 200, wantCount: 1},
		{name: "specific permitted venue", path: "/api/labor-goals/targets?year=2027&venueId=venue-a", venue: "venue-a", allowed: []string{"venue-a"}, wantStatus: 200, wantCount: 1},
		{name: "specific denied venue", path: "/api/labor-goals/targets?year=2027&venueId=venue-b", venue: "venue-b", allowed: []string{"venue-a"}, wantStatus: 403},
		{name: "no permitted venues", path: "/api/labor-goals/targets?year=2027", wantStatus: 200, wantCount: 0},
		{name: "missing year", path: "/api/labor-goals/targets?region=West", allowed: []string{"venue-a"}, wantStatus: 400},
		{name: "invalid year", path: "/api/labor-goals/targets?year=invalid", allowed: []string{"venue-a"}, wantStatus: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, mock, session := venueRouteHarness(t)
			session()
			grants := sqlmock.NewRows([]string{"external_id"})
			for _, id := range tc.allowed {
				grants.AddRow(id)
			}
			mock.ExpectQuery("SELECT rr.external_id").WithArgs("local-route", "labor_goals.view").WillReturnRows(grants)
			if tc.wantStatus == http.StatusOK {
				rows := sqlmock.NewRows([]string{"id", "venue_id", "name", "region", "venue_number", "year", "period_key", "foh_target", "boh_target"}).AddRow("target-a", "venue-a", "Allowed venue", "West", "101", 2027, "2027-01", 2.5, nil)
				if tc.venue == "" {
					rows.AddRow("target-b", "venue-b", "Private venue", "West", "102", 2027, "2027-01", 9.9, 9.9)
				}
				switch {
				case tc.region != "":
					mock.ExpectQuery(`SELECT t.id.*AND TRIM\(v.region\) = \$2`).WithArgs(2027, tc.region).WillReturnRows(rows)
				case tc.venue != "":
					mock.ExpectQuery(`SELECT t.id.*AND t.venue_id = \$2`).WithArgs(2027, tc.venue).WillReturnRows(rows)
				default:
					mock.ExpectQuery("SELECT t.id").WithArgs(2027).WillReturnRows(rows)
				}
			}
			response := venueRouteRequest(mux, http.MethodGet, tc.path, "")
			if response.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.wantStatus, response.Body)
			}
			if tc.wantStatus == http.StatusOK {
				var body struct {
					Year    int `json:"year"`
					Targets []struct {
						VenueID string `json:"venueId"`
					} `json:"targets"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Year != 2027 || body.Targets == nil || len(body.Targets) != tc.wantCount || strings.Contains(response.Body.String(), "Private venue") {
					t.Fatalf("wrong or unfiltered response: %s", response.Body)
				}
				for _, target := range body.Targets {
					if target.VenueID != "venue-a" {
						t.Fatalf("unauthorized target: %+v", target)
					}
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
