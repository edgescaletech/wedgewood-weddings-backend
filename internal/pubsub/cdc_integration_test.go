package pubsub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	goavro "github.com/linkedin/goavro/v2"
	"google.golang.org/grpc/metadata"
	sf "wedding-project/internal/adapters/salesforce"
	"wedding-project/internal/database"
	"wedding-project/internal/models"
	"wedding-project/internal/repositories"
	proto "wedding-project/proto"
)

func cdcTestDatabase(t *testing.T) *sql.DB {
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

func TestCDCPostgresStaleSnapshotsCannotResurrectDeletedEvents(t *testing.T) {
	db := cdcTestDatabase(t)
	ctx := context.Background()
	initial := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	event := models.Event{ID: "006000000000001", Name: "Original", EventDate: "2026-11-27", StageName: "Booked", SalesforceModifiedAt: initial}
	if err := repositories.UpsertEvent(ctx, db, &event); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	deletedAt := initial.Add(time.Second)
	payload := map[string]any{"ChangeEventHeader": map[string]any{"entityName": "Opportunity", "changeType": "DELETE", "commitTimestamp": deletedAt.UnixMilli(), "recordIds": []any{event.ID, "006000000000002"}}}
	if err = processCDCMessage(ctx, conn, nil, "test-key", []byte{1}, payload); err != nil {
		t.Fatal(err)
	}
	// This batch represents a weekly fetch begun before the CDC deletion.
	if err = repositories.UpsertEvents(ctx, db, []models.Event{event}); err != nil {
		t.Fatal(err)
	}
	var active bool
	var name string
	if err = db.QueryRow("SELECT is_active,name FROM events WHERE id=$1", event.ID).Scan(&active, &name); err != nil || active {
		t.Fatalf("stale batch resurrected event: active=%v err=%v", active, err)
	}
	// An equal timestamp cannot undo a deletion either.
	event.SalesforceModifiedAt = deletedAt
	if err = repositories.UpsertEvent(ctx, db, &event); err != nil {
		t.Fatal(err)
	}
	if _, err = repositories.GetEventByID(ctx, db, event.ID); err != sql.ErrNoRows {
		t.Fatalf("deleted event visible: %v", err)
	}
	has, err := repositories.HasEventsForWeek(ctx, db, initial.AddDate(0, 0, -1), initial.AddDate(0, 0, 1))
	if err != nil || has {
		t.Fatalf("tombstones counted as live events: %v %v", has, err)
	}
	// A genuinely newer UNDELETE/current Salesforce record is allowed.
	event.Name = "Restored"
	event.SalesforceModifiedAt = deletedAt.Add(time.Second)
	if err = repositories.UpsertEvents(ctx, db, []models.Event{event}); err != nil {
		t.Fatal(err)
	}
	// A duplicate or delayed old delete must not deactivate that newer record.
	if err = processCDCMessage(ctx, conn, nil, "test-key", []byte{2}, payload); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT is_active,name FROM events WHERE id=$1", event.ID).Scan(&active, &name); err != nil || !active || name != "Restored" {
		t.Fatalf("newer event overwritten: active=%v name=%s err=%v", active, name, err)
	}
	var bookmark []byte
	if err = db.QueryRow("SELECT replay_id FROM integration_sync_state WHERE subscription_key='test-key'").Scan(&bookmark); err != nil || len(bookmark) != 1 || bookmark[0] != 2 {
		t.Fatalf("bookmark=%v err=%v", bookmark, err)
	}
	// The unseen second ID also retains a tombstone against a later stale insert.
	event.ID = "006000000000002"
	event.SalesforceModifiedAt = initial
	if err = repositories.UpsertEvent(ctx, db, &event); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT is_active FROM events WHERE id=$1", event.ID).Scan(&active); err != nil || active {
		t.Fatalf("unknown deleted ID resurrected: %v %v", active, err)
	}
}

func TestCDCPostgresCheckpointFailureRollsBackDeletion(t *testing.T) {
	db := cdcTestDatabase(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO events(id,name,event_date) VALUES('006000000000001','Keep me','2026-11-27');
 ALTER TABLE integration_sync_state ADD CONSTRAINT reject_test_bookmark CHECK (subscription_key <> 'reject')`); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payload := map[string]any{"ChangeEventHeader": map[string]any{"entityName": "Opportunity", "changeType": "DELETE", "commitTimestamp": time.Now().UnixMilli(), "recordIds": []any{"006000000000001"}}}
	if err = processCDCMessage(ctx, conn, nil, "reject", []byte{1}, payload); err == nil {
		t.Fatal("checkpoint failure accepted")
	}
	var active bool
	if err = db.QueryRow("SELECT is_active FROM events WHERE id='006000000000001'").Scan(&active); err != nil || !active {
		t.Fatalf("event mutation escaped rollback: %v %v", active, err)
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM integration_inbox").Scan(&count); err != nil || count != 0 {
		t.Fatalf("audit escaped rollback: %d %v", count, err)
	}
}

func TestCDCPostgresBrokenLeaderConnectionCannotUseAnotherPoolConnection(t *testing.T) {
	db := cdcTestDatabase(t)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var pid int
	if err = conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	var killed bool
	if err = db.QueryRow("SELECT pg_terminate_backend($1)", pid).Scan(&killed); err != nil || !killed {
		t.Fatalf("terminate leader: %v", err)
	}
	payload := map[string]any{"ChangeEventHeader": map[string]any{"entityName": "Opportunity", "changeType": "DELETE", "commitTimestamp": time.Now().UnixMilli(), "recordIds": []any{"006000000000001"}}}
	if err = processCDCMessage(ctx, conn, nil, "test-key", []byte{1}, payload); err == nil {
		t.Fatal("broken leader wrote through another pool connection")
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM integration_sync_state").Scan(&count); err != nil || count != 0 {
		t.Fatal(fmt.Sprintf("unexpected checkpoint count=%d err=%v", count, err))
	}
}

// The first buffered message deletes an event still present in the older
// snapshot. Recovery must consume the anchor first, load the snapshot, then
// apply that message, and only then save the newer cursor.
func TestCDCPostgresRecoveryAppliesBufferedDeletionAfterSnapshot(t *testing.T) {
	db := cdcTestDatabase(t)
	ctx := context.Background()
	t.Setenv("SF_TO_DB_SYNC_WEEKS_AHEAD", "1")
	schemaID := "recovery-test-schema"
	codec, err := goavro.NewCodec(`{"type":"record","name":"TestChange","fields":[{"name":"ChangeEventHeader","type":{"type":"record","name":"Header","fields":[{"name":"entityName","type":"string"},{"name":"changeType","type":"string"},{"name":"recordIds","type":{"type":"array","items":"string"}},{"name":"commitTimestamp","type":"long"}]}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	schemaCache.Store(schemaID, codec)
	defer schemaCache.Delete(schemaID)
	version := time.Now().UTC().Truncate(time.Second)
	payload := map[string]any{"ChangeEventHeader": map[string]any{"entityName": "Opportunity", "changeType": "DELETE", "recordIds": []any{"006000000000001"}, "commitTimestamp": version.Add(time.Second).UnixMilli()}}
	binary, err := codec.BinaryFromNative(nil, payload)
	if err != nil {
		t.Fatal(err)
	}
	stream := &cdcTestStream{response: &proto.FetchResponse{Events: []*proto.ConsumerEvent{{ReplayId: []byte{1}, Event: &proto.ProducerEvent{SchemaId: schemaID, Payload: binary}}}, LatestReplayId: []byte{2}, PendingNumRequested: 1}}
	snapshots := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !stream.received {
			t.Error("snapshot started before stream anchor")
		}
		if strings.Contains(r.URL.Query().Get("q"), "FROM Opportunity") {
			snapshots++
			fmt.Fprintf(w, `{"done":true,"records":[{"Id":"006000000000001","Name":"Older snapshot","SystemModstamp":%q,"Event_Start_Date_Only__c":%q,"StageName":"Booked"}]}`, version.Format(time.RFC3339), version.Format("2006-01-02"))
			return
		}
		fmt.Fprint(w, `{"done":true,"records":[]}`)
	}))
	defer server.Close()
	client, err := sf.NewClient(sf.Config{MyDomainURL: server.URL, AccessToken: "test"})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = consumeCDCStream(ctx, conn, client, "recovery", true, nil, stream); !errors.Is(err, io.EOF) {
		t.Fatalf("consume: %v", err)
	}
	if snapshots != 1 {
		t.Fatalf("snapshots=%d", snapshots)
	}
	var active bool
	if err = db.QueryRow("SELECT is_active FROM events WHERE id='006000000000001'").Scan(&active); err != nil || active {
		t.Fatalf("snapshot overrode buffered deletion: %v %v", active, err)
	}
	var cursor []byte
	if err = db.QueryRow("SELECT replay_id FROM integration_sync_state WHERE subscription_key='recovery'").Scan(&cursor); err != nil || len(cursor) != 1 || cursor[0] != 2 {
		t.Fatalf("cursor=%v err=%v", cursor, err)
	}
	if stream.sends != 0 {
		t.Fatal("requested more events while outstanding demand remained")
	}
}

type cdcTestStream struct {
	proto.PubSub_SubscribeClient
	response *proto.FetchResponse
	received bool
	sends    int
}

func (s *cdcTestStream) Recv() (*proto.FetchResponse, error) {
	if s.received {
		return nil, io.EOF
	}
	s.received = true
	return s.response, nil
}
func (s *cdcTestStream) Send(*proto.FetchRequest) error { s.sends++; return nil }
func (s *cdcTestStream) Trailer() metadata.MD           { return nil }

func TestCDCPostgresRecoveryWaitsForInflightCacheWriter(t *testing.T) {
	db := cdcTestDatabase(t)
	ctx := context.Background()
	writer, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	recovery, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Close()
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	// The writer has fetched before recovery starts, but has not saved yet.
	go func() {
		finished <- database.WithEventCacheGate(ctx, writer, false, func() error {
			close(entered)
			<-release
			return repositories.UpsertEvent(ctx, writer, &models.Event{ID: "006000000000001", Name: "Late old fetch", EventDate: time.Now().UTC().Format("2006-01-02"), SalesforceModifiedAt: time.Now().UTC().Add(-time.Hour)})
		})
	}()
	<-entered
	// Guarantee cleanup if a test assertion fails before releasing the writer.
	released := false
	defer func() {
		if !released {
			close(release)
			<-finished
		}
	}()
	deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	err = database.WithEventCacheGate(deadline, recovery, true, func() error { t.Error("recovery ran before in-flight writer finished"); return nil })
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("recovery did not wait: %v", err)
	}
	close(release)
	released = true
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	t.Setenv("SF_TO_DB_SYNC_WEEKS_AHEAD", "1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "queryAll") {
			fmt.Fprintf(w, `{"done":true,"records":[{"Id":"006000000000001","IsDeleted":true,"SystemModstamp":%q}]}`, time.Now().UTC().Format(time.RFC3339Nano))
			return
		}
		fmt.Fprint(w, `{"done":true,"records":[]}`)
	}))
	defer server.Close()
	client, err := sf.NewClient(sf.Config{MyDomainURL: server.URL, AccessToken: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err = reconcileCDCCache(ctx, recovery, client, "gate-test"); err != nil {
		t.Fatal(err)
	}
	var active bool
	if err = db.QueryRow("SELECT is_active FROM events WHERE id='006000000000001'").Scan(&active); err != nil || active {
		t.Fatalf("late cache write escaped recovery: %v %v", active, err)
	}
	// The exclusive gate must also prevent a new fetch from starting mid-recovery.
	if err = database.WithEventCacheGate(ctx, recovery, true, func() error {
		deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		returnErr := database.WithEventCacheGate(deadline, writer, false, func() error { t.Error("new writer started during recovery"); return nil })
		if !errors.Is(returnErr, context.DeadlineExceeded) {
			return fmt.Errorf("writer did not wait: %v", returnErr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
