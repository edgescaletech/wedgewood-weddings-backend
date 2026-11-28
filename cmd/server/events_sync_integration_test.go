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

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"wedding-project/internal/adapters/salesforce"
	"wedding-project/internal/jobs"
)

func TestEventSyncAndCachedRequests(t *testing.T) {
	dsn := os.Getenv("RBAC_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("RBAC_TEST_DATABASE_URL not configured")
	}
	base, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "event_sync_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.Exec("CREATE SCHEMA " + schema); err != nil {
		base.Close()
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
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("postgres", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, name := range []string{"001_phase1_tables.up.sql", "003_event_sync_tracking.up.sql", "005_venue_number_and_region.up.sql", "011_unresolved_sync_records.up.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "internal", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO venues(id,name) VALUES('venue-1','Test venue');
		INSERT INTO events(id,venue_id,name,event_date,stage_name,guest_count,
		under_21_count,over_21_count,contract_signed,has_bar,photo_booth,photobooth_staffed)
		VALUES('event-1','venue-1','Cached event','2026-11-17','Booked',0,0,0,false,false,false,false)`); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	requests := make(chan struct{}, 10)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		requests <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalSize":0,"done":true,"records":[]}`))
	}))
	t.Cleanup(upstream.Close)
	client, err := salesforce.NewClient(salesforce.Config{MyDomainURL: upstream.URL, AccessToken: "test"})
	if err != nil {
		t.Fatal(err)
	}
	worker := jobs.NewEventSyncWorker(db, client)
	eventRequest := func(path string) *http.Request {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		return request.WithContext(context.WithValue(request.Context(), venueAccessKey{}, map[string]bool{"venue-1": true}))
	}
	handler := eventsHandler(db, client, worker)
	for _, suffix := range []string{"", "&refresh=true"} {
		response := httptest.NewRecorder()
		handler(response, eventRequest("/api/events?week=2026-11-16"+suffix))
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || body["source"] != "database" || len(body["events"].([]any)) != 1 {
			t.Fatalf("cached response %s: %d %s", suffix, response.Code, response.Body)
		}
		if _, exists := body["syncStatus"]; exists {
			t.Fatal("cached response reports a manual sync")
		}
	}
	select {
	case <-requests:
		t.Fatal("cached request triggered a Salesforce sync")
	case <-time.After(200 * time.Millisecond):
	}
	fallback := httptest.NewRecorder()
	eventsHandler(nil, client, nil)(fallback, eventRequest("/api/events?week=2026-11-16"))
	if fallback.Code != http.StatusOK || !strings.Contains(fallback.Body.String(), `"source":"salesforce"`) {
		t.Fatalf("Salesforce fallback: %d %s", fallback.Code, fallback.Body)
	}
	<-requests
	cron := httptest.NewRecorder()
	syncEventsHandler(worker)(cron, httptest.NewRequest(http.MethodPost, "/api/sync/events?week=2026-11-16", nil))
	if cron.Code != http.StatusAccepted {
		t.Fatalf("cron endpoint: %d %s", cron.Code, cron.Body)
	}
	select {
	case <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("cron did not start Salesforce synchronization")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		err := db.QueryRow(`SELECT status FROM integration_sync_state WHERE subscription_key='sf_events_week_2026-11-16'`).Scan(&status)
		if err == nil && status != "syncing" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("cron synchronization did not complete")
}
