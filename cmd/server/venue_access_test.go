package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"wedding-project/internal/adapters/salesforce"
	"wedding-project/internal/repositories"
)

func TestVenueAuthorizationIntegration(t *testing.T) {
	dsn := os.Getenv("RBAC_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("RBAC_TEST_DATABASE_URL not configured")
	}
	base, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "venue_auth_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := base.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Error(err)
		}
		base.Close()
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	params := parsed.Query()
	params.Set("search_path", schema)
	parsed.RawQuery = params.Encode()
	db, err := sql.Open("postgres", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	files, err := filepath.Glob("../../internal/migrations/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(data)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	userID, adminID := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO users(id,workos_id,workos_invitation_id,email,is_active)
		VALUES($1,'user-test','inv-test','user@example.test',true),
		      ($2,'admin-test','inv-admin','admin@example.test',true)`, userID, adminID)
	exec(`INSERT INTO venues(id,name,is_active) VALUES('a','A',true),('b','B',true),('closed','Closed',false)`)
	exec(`INSERT INTO user_roles(id,user_id,role_id,assignment_type,resource_id)
		SELECT $1,$2,r.id,'venue',rr.id FROM roles r,rbac_resources rr
		WHERE r.name='Venue Scheduler' AND rr.external_id='a'`, uuid.NewString(), userID)
	exec(`INSERT INTO user_roles(id,user_id,role_id,assignment_type)
		SELECT $1,$2,id,'global' FROM roles WHERE name='Admin'`, uuid.NewString(), adminID)
	for _, test := range []struct {
		user, permission string
		a, b, closed     bool
	}{
		{userID, "schedules.view", true, false, false},
		{userID, "labor_goals.view", true, false, false},
		{userID, "schedules.publish", false, false, false},
		{adminID, "schedules.view", true, true, false},
	} {
		allowed, err := authorizedVenues(context.Background(), db, test.user, test.permission)
		if err != nil || allowed["a"] != test.a || allowed["b"] != test.b || allowed["closed"] != test.closed {
			t.Fatalf("%s %s: %v, %v", test.user, test.permission, allowed, err)
		}
	}
	exec(`INSERT INTO events(id,venue_id,name,event_date,stage_name,guest_count,under_21_count,over_21_count,contract_signed,has_bar,photo_booth,photobooth_staffed)
		VALUES('event-a','a','A event','2026-11-17','Booked',20,0,0,false,false,false,false),
		      ('event-b','b','B event','2026-11-17','Booked',30,0,0,false,false,false,false),
		      ('event-unassigned',NULL,'Unassigned event','2026-11-17','Booked',40,0,0,false,false,false,false)`)
	date := time.Date(2026, time.November, 17, 0, 0, 0, 0, time.UTC)
	guests, count, err := repositories.GetEventGuestCountAndCount(context.Background(), db, "a", date, date)
	if err != nil || guests != 20 || count != 1 {
		t.Fatalf("venue a labor totals: guests=%d events=%d err=%v", guests, count, err)
	}
	allowed, err := authorizedVenues(context.Background(), db, userID, "schedules.view")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/events?week=2026-11-16", nil)
	request = request.WithContext(context.WithValue(request.Context(), venueAccessKey{}, allowed))
	response := httptest.NewRecorder()
	eventsHandler(db, nil, nil)(response, request)
	var body struct {
		Events []struct {
			ID string `json:"id"`
		} `json:"events"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 200 ||
		len(body.Events) != 1 || body.Events[0].ID != "event-a" {
		t.Fatalf("event filtering: %d %s (%v)", response.Code, response.Body, err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/events/event-b", nil)
	request = request.WithContext(context.WithValue(request.Context(), venueAccessKey{}, allowed))
	response = httptest.NewRecorder()
	eventByIDHandler(db, nil)(response, request)
	if response.Code != http.StatusNotFound || response.Body.String() != "{\"error\":\"event not found\"}\n" {
		t.Fatalf("unauthorized event: %d %s", response.Code, response.Body)
	}
	exec(`UPDATE users SET is_active=false WHERE id=$1`, userID)
	allowed, err = authorizedVenues(context.Background(), db, userID, "schedules.view")
	if err != nil || len(allowed) != 0 {
		t.Fatalf("deactivated user's access: %v, %v", allowed, err)
	}
}

func TestSalesforceFallbackFiltersVenues(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalSize":2,"done":true,"records":[{"Id":"event-a","Name":"Allowed","Venue__c":"a","Event_Start_Date_Only__c":"2026-11-17","StageName":"Booked"},{"Id":"event-b","Name":"Denied","Venue__c":"b","Event_Start_Date_Only__c":"2026-11-17","StageName":"Booked"}]}`))
	}))
	defer upstream.Close()
	client, err := salesforce.NewClient(salesforce.Config{MyDomainURL: upstream.URL, AccessToken: "test"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/events?week=2026-11-16", nil)
	request = request.WithContext(context.WithValue(request.Context(), venueAccessKey{}, map[string]bool{"a": true}))
	response := httptest.NewRecorder()
	eventsHandler(nil, client, nil)(response, request)
	var body struct {
		Events []struct {
			ID string `json:"id"`
		} `json:"events"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusOK ||
		len(body.Events) != 1 || body.Events[0].ID != "event-a" {
		t.Fatalf("Salesforce fallback filtering: %d %s (%v)", response.Code, response.Body, err)
	}
}

func TestEventDetailSkipsSalesforceForMalformedIDs(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("malformed event ID reached Salesforce")
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer upstream.Close()
	client, err := salesforce.NewClient(salesforce.Config{MyDomainURL: upstream.URL, AccessToken: "test"})
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"e2e-missing-2d4b3e1b-b103-437f-8f68-b2b875e75abe", "00600000000000", "00600000000000-"} {
		mock.ExpectQuery(`FROM events\s+LEFT JOIN venues.*\s+WHERE events.id = \$1`).
			WithArgs(id).
			WillReturnRows(sqlmock.NewRows([]string{"id"}))
		request := httptest.NewRequest(http.MethodGet, "/api/events/"+id, nil)
		request = request.WithContext(context.WithValue(request.Context(), venueAccessKey{}, map[string]bool{"a": true}))
		response := httptest.NewRecorder()
		eventByIDHandler(db, client)(response, request)
		if response.Code != http.StatusNotFound || response.Body.String() != "{\"error\":\"event not found\"}\n" {
			t.Errorf("event detail for %q: %d %s", id, response.Code, response.Body)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSalesforceEventDetailFallbackEnforcesVenueAccess(t *testing.T) {
	const eventID = "006000000000001AAA"
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`FROM events\s+LEFT JOIN venues.*\s+WHERE events.id = \$1`).
		WithArgs(eventID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	var lookups atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(query, "FROM SBQQ__Quote__c") {
			_, _ = w.Write([]byte(`{"totalSize":0,"done":true,"records":[]}`))
			return
		}
		if !strings.Contains(query, "WHERE Id = '"+eventID+"'") {
			t.Error("Salesforce did not receive the expected event detail query")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		lookups.Add(1)
		_, _ = w.Write([]byte(`{"totalSize":1,"done":true,"records":[{"Id":"` + eventID + `","Name":"Private Venue B event","Venue__c":"b","Event_Start_Date_Only__c":"2026-11-17","StageName":"Booked"}]}`))
	}))
	defer upstream.Close()
	client, err := salesforce.NewClient(salesforce.Config{MyDomainURL: upstream.URL, AccessToken: "test"})
	if err != nil {
		t.Fatal(err)
	}

	requestForVenue := func(venueID string) *http.Request {
		request := httptest.NewRequest(http.MethodGet, "/api/events/"+eventID, nil)
		return request.WithContext(context.WithValue(request.Context(), venueAccessKey{}, map[string]bool{venueID: true}))
	}
	denied := httptest.NewRecorder()
	eventByIDHandler(db, client)(denied, requestForVenue("a"))
	if denied.Code != http.StatusNotFound || denied.Body.String() != "{\"error\":\"event not found\"}\n" {
		t.Fatalf("unauthorized Salesforce event detail: %d %s", denied.Code, denied.Body)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("expected Salesforce lookup after cache miss, got %d", got)
	}

	// The same Salesforce record is accessible to a user authorized at Venue B.
	allowed := httptest.NewRecorder()
	eventByIDHandler(nil, client)(allowed, requestForVenue("b"))
	var event struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		VenueID string `json:"venueId"`
	}
	if err := json.Unmarshal(allowed.Body.Bytes(), &event); err != nil || allowed.Code != http.StatusOK ||
		event.ID != eventID || event.Name != "Private Venue B event" || event.VenueID != "b" {
		t.Fatalf("authorized Salesforce event detail: %d %s (%v)", allowed.Code, allowed.Body, err)
	}
}
