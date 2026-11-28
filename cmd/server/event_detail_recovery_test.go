package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	sf "wedding-project/internal/adapters/salesforce"
	"wedding-project/internal/database"
)

func TestEventDetailRecoveryDoesNotReserveWaitingConnections(t *testing.T) {
	dsn := os.Getenv("CDC_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CDC_TEST_DATABASE_URL not configured")
	}
	base, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "cache_request_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = base.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer base.Exec("DROP SCHEMA " + schema + " CASCADE")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	db, err := sql.Open("postgres", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(3)
	for _, name := range []string{"001_phase1_tables.up.sql", "003_event_sync_tracking.up.sql", "005_venue_number_and_region.up.sql", "011_unresolved_sync_records.up.sql"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "internal", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(data)); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	recovery, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recovery.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext('sf_event_cache_recovery'))"); err != nil {
		t.Fatal(err)
	}
	defer database.ReleaseAdvisoryLock(recovery, "SELECT pg_advisory_unlock(hashtext('sf_event_cache_recovery'))", "")
	const requests = 12
	arrived := make(chan struct{}, requests)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("q")
		if strings.Contains(query, "FROM SBQQ__Quote__c") {
			fmt.Fprint(w, `{"done":true,"records":[]}`)
			return
		}
		arrived <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		id := "006000000000001AAA"
		venue := "venue-a"
		if strings.Contains(query, "006000000000002AAA") {
			id = "006000000000002AAA"
			venue = "venue-b"
		}
		fmt.Fprintf(w, `{"done":true,"records":[{"Id":%q,"Venue__c":%q,"Name":"Live event","StageName":"Booked","Event_Start_Date_Only__c":"2026-11-27","SystemModstamp":"2026-11-27T10:00:00Z"}]}`, id, venue)
	}))
	defer upstream.Close()
	client, err := sf.NewClient(sf.Config{MyDomainURL: upstream.URL, AccessToken: "test"})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan int, requests)
	for i := 0; i < requests; i++ {
		go func(i int) {
			id := "006000000000001AAA"
			if i == requests-1 {
				id = "006000000000002AAA"
			}
			request := httptest.NewRequest(http.MethodGet, "/api/events/"+id, nil).WithContext(context.WithValue(ctx, venueAccessKey{}, map[string]bool{"venue-a": true}))
			response := httptest.NewRecorder()
			eventByIDHandler(db, client)(response, request)
			results <- response.Code
		}(i)
	}
	// All requests must reach Salesforce while recovery still owns its lock.
	for i := 0; i < requests; i++ {
		select {
		case <-arrived:
		case <-ctx.Done():
			unblock()
			t.Fatal("requests waited on recovery instead of serving uncached data")
		}
	}
	if inUse := db.Stats().InUse; inUse != 1 {
		unblock()
		t.Fatalf("requests retained connections during Salesforce fallback: inUse=%d", inUse)
	}
	readCtx, stop := context.WithTimeout(ctx, 500*time.Millisecond)
	defer stop()
	var one int
	if err = db.QueryRowContext(readCtx, "SELECT 1").Scan(&one); err != nil {
		unblock()
		t.Fatalf("unrelated read blocked: %v", err)
	}
	unblock()
	ok, denied := 0, 0
	for i := 0; i < requests; i++ {
		select {
		case status := <-results:
			if status == 200 {
				ok++
			} else if status == 404 {
				denied++
			} else {
				t.Errorf("unexpected status %d", status)
			}
		case <-ctx.Done():
			t.Fatal("request did not finish")
		}
	}
	if ok != requests-1 || denied != 1 {
		t.Fatalf("authorization changed: ok=%d denied=%d", ok, denied)
	}
	var cached int
	if err = db.QueryRow("SELECT count(*) FROM events").Scan(&cached); err != nil || cached != 0 {
		t.Fatalf("fallback wrote during recovery: %d %v", cached, err)
	}
}
