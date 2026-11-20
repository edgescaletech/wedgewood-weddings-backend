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
	"testing"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"wedding-project/internal/adapters/salesforce"
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
		VALUES('event-a','a','A event','2026-11-17','Booked',0,0,0,false,false,false,false),
		      ('event-b','b','B event','2026-11-17','Booked',0,0,0,false,false,false,false)`)
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
	if response.Code != http.StatusForbidden {
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
