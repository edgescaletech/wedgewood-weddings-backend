package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	sf "wedding-project/internal/adapters/salesforce"
)

func retryTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("CDC_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("CDC_TEST_DATABASE_URL not configured")
	}
	base, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "cdc_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = base.Exec("CREATE SCHEMA " + schema); err != nil {
		base.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := base.Exec("DROP SCHEMA " + schema + " CASCADE")
		if err != nil {
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
	for _, name := range []string{"001_phase1_tables.up.sql", "002_auth.up.sql", "003_event_sync_tracking.up.sql", "005_venue_number_and_region.up.sql", "006_admin.up.sql", "011_unresolved_sync_records.up.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// Separate workers share only PostgreSQL, just as separate application instances do.
func TestScheduledRetryUsesSweepLockAndStopsAfterConnectionLoss(t *testing.T) {
	db := retryTestDatabase(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		fmt.Fprint(w, `{"organization_id":"org-test"}`)
	}))
	defer server.Close()
	client, _ := sf.NewClient(sf.Config{MyDomainURL: server.URL, AccessToken: "test"})
	first, second := NewEventSyncWorker(db, client), NewEventSyncWorker(db, client)
	done := make(chan struct{})
	go func() { defer close(done); first.sweepUpcomingWeeks(context.Background(), 0) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("retry not reached")
	}
	second.sweepUpcomingWeeks(context.Background(), 0)
	if calls.Load() != 1 {
		close(release)
		t.Fatal("second instance duplicated retry")
	}
	// Terminate the lock-owning session while its source request is in flight.
	var pid int
	err := db.QueryRow(`SELECT pid FROM pg_locks WHERE locktype='advisory' AND granted AND objid=hashtext('sf_sweep_scheduled_sync')::bigint & 4294967295 LIMIT 1`).Scan(&pid)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if _, err = db.Exec("SELECT pg_terminate_backend($1)", pid); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("lost connection did not stop retry")
	}
	// A subsequent sweep can acquire the released lock and run normally.
	second.sweepUpcomingWeeks(context.Background(), 0)
	if calls.Load() != 2 {
		t.Fatalf("subsequent sweep did not run: %d", calls.Load())
	}
}
