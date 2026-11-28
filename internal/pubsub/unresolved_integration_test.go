package pubsub

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sf "wedding-project/internal/adapters/salesforce"
	"wedding-project/internal/models"
	"wedding-project/internal/repositories"
	proto "wedding-project/proto"
)

func TestUnresolvedRecoveryContinuesAndCheckpointsPreserveStatus(t *testing.T) {
	db := cdcTestDatabase(t)
	ctx := context.Background()
	t.Setenv("SF_TO_DB_SYNC_WEEKS_AHEAD", "1")
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	old := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	newer := old.Add(time.Minute)
	ids := []string{"006000000000001", "006000000000002", "006000000000003"}
	for _, id := range ids {
		if err = repositories.UpsertEvent(ctx, conn, &models.Event{ID: id, Name: id, StageName: "Booked", EventDate: time.Now().UTC().Format("2006-01-02"), SalesforceModifiedAt: old}); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		if strings.Contains(r.URL.Path, "queryAll") {
			if strings.Contains(q, ids[0]) {
				fmt.Fprint(w, `{"done":true,"records":[]}`)
				return
			}
			id := ids[2]
			deleted := false
			if strings.Contains(q, ids[1]) {
				id = ids[1]
				deleted = true
			}
			fmt.Fprintf(w, `{"done":true,"records":[{"Id":%q,"IsDeleted":%t,"SystemModstamp":%q}]}`, id, deleted, newer.Format(time.RFC3339))
			return
		}
		if strings.Contains(q, "WHERE Id = '"+ids[2]+"'") {
			fmt.Fprintf(w, `{"done":true,"records":[{"Id":%q,"Name":"Refreshed event","StageName":"Booked","Event_Start_Date_Only__c":%q,"SystemModstamp":%q}]}`, ids[2], time.Now().UTC().Format("2006-01-02"), newer.Format(time.RFC3339))
			return
		}
		fmt.Fprint(w, `{"done":true,"records":[]}`)
	}))
	defer server.Close()
	client, _ := sf.NewClient(sf.Config{MyDomainURL: server.URL, AccessToken: "test"})
	if err = reconcileCDCCache(ctx, conn, client, "issues"); err != nil {
		t.Fatal(err)
	}
	issues, err := ListSyncIssues(ctx, db, "issues")
	if err != nil || len(issues) != 1 || issues[0].RecordID != ids[0] {
		t.Fatalf("issues=%v err=%v", issues, err)
	}
	// Both a normal event and an empty keepalive must leave recovery incomplete.
	payload := map[string]any{"ChangeEventHeader": map[string]any{"entityName": "Account", "changeType": "UPDATE", "recordIds": []any{"001000000000001"}}}
	if err = processCDCMessage(ctx, conn, nil, "issues", []byte{1}, payload); err != nil {
		t.Fatal(err)
	}
	if err = saveCDCCheckpoint(ctx, conn, "issues", []byte{2}); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = db.QueryRow("SELECT status FROM integration_sync_state WHERE subscription_key='issues'").Scan(&status); err != nil || status != "recovery_incomplete" {
		t.Fatalf("status=%s err=%v", status, err)
	}
	missing, err := repositories.GetEventByID(ctx, db, ids[0])
	if err != nil || missing.SyncStatus != "unverified" || missing.LastSyncedAt == nil {
		t.Fatalf("missing warning metadata: %+v %v", missing, err)
	}
	live, err := repositories.GetEventByID(ctx, db, ids[2])
	if err != nil || live.Name != "Refreshed event" || live.SyncStatus != "cached" {
		t.Fatalf("other record not refreshed: %+v %v", live, err)
	}
	var active bool
	if err = db.QueryRow("SELECT is_active FROM events WHERE id=$1", ids[1]).Scan(&active); err != nil || active {
		t.Fatalf("confirmed deletion not applied: %v %v", active, err)
	}
}

func TestUnresolvedPersistenceFailureDoesNotAdvanceReplay(t *testing.T) {
	db := cdcTestDatabase(t)
	ctx := context.Background()
	t.Setenv("SF_TO_DB_SYNC_WEEKS_AHEAD", "1")
	_, err := db.Exec(`INSERT INTO events(id,name,event_date,stage_name) VALUES('006000000000001','Legacy event',current_date,'Booked');
 ALTER TABLE unresolved_sync_records ADD CONSTRAINT reject_issue CHECK(subscription_key <> 'failure');
 INSERT INTO integration_sync_state(source_system,subscription_key,replay_id,status) VALUES('salesforce_pubsub','failure',decode('01','hex'),'recovery_required')`)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"done":true,"records":[]}`) }))
	defer server.Close()
	client, _ := sf.NewClient(sf.Config{MyDomainURL: server.URL, AccessToken: "test"})
	conn, _ := db.Conn(ctx)
	defer conn.Close()
	stream := &cdcTestStream{response: &proto.FetchResponse{LatestReplayId: []byte{2}}}
	if err = consumeCDCStream(ctx, conn, client, "failure", true, nil, stream); err == nil {
		t.Fatal("failed issue persistence was ignored")
	}
	var cursor []byte
	if err = db.QueryRow("SELECT replay_id FROM integration_sync_state WHERE subscription_key='failure'").Scan(&cursor); err != nil || len(cursor) != 1 || cursor[0] != 1 {
		t.Fatalf("checkpoint advanced: %v %v", cursor, err)
	}
}

func TestAutomaticResolutionIsScopedAuditedAndReopenable(t *testing.T) {
	db := cdcTestDatabase(t)
	ctx := context.Background()
	t.Setenv("SF_PUBSUB_TOPIC", "/data/ChangeEvents")
	key := cdcSubscriptionKey("org-test", pubsubTopic())
	other := "other-subscription"
	conn, _ := db.Conn(ctx)
	defer conn.Close()
	stamp := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	first, second := "006000000000001", "006000000000002"
	for _, id := range []string{first, second} {
		if err := repositories.UpsertEvent(ctx, conn, &models.Event{ID: id, Name: id, EventDate: "2026-11-27", SalesforceModifiedAt: stamp}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.RecordUnresolved(ctx, conn, key, id, "missing_from_queryall"); err != nil {
			t.Fatal(err)
		}
	}
	if err := repositories.RecordUnresolved(ctx, conn, other, first, "missing_from_queryall"); err != nil {
		t.Fatal(err)
	}
	var secondLive atomic.Bool
	var deletionVisible atomic.Bool
	deletion := stamp.Add(time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "userinfo") {
			fmt.Fprint(w, `{"organization_id":"org-test"}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/deleted/") {
			records := "[]"
			if deletionVisible.Load() {
				records = fmt.Sprintf(`[{"id":%q,"deletedDate":%q}]`, first, deletion.Format(time.RFC3339))
			}
			fmt.Fprintf(w, `{"deletedRecords":%s,"earliestDateAvailable":%q,"latestDateCovered":%q}`, records, r.URL.Query().Get("start"), r.URL.Query().Get("end"))
			return
		}
		query := r.URL.Query().Get("q")
		if secondLive.Load() && strings.Contains(query, second) {
			version := stamp.Add(2 * time.Minute).Format(time.RFC3339)
			if strings.Contains(r.URL.Path, "queryAll") {
				fmt.Fprintf(w, `{"done":true,"records":[{"Id":%q,"IsDeleted":false,"SystemModstamp":%q}]}`, second, version)
			} else {
				fmt.Fprintf(w, `{"done":true,"records":[{"Id":%q,"Name":"Access restored","StageName":"Booked","Event_Start_Date_Only__c":"2026-11-27","SystemModstamp":%q}]}`, second, version)
			}
			return
		}

		fmt.Fprint(w, `{"done":true,"records":[]}`)
	}))
	defer server.Close()
	client, _ := sf.NewClient(sf.Config{MyDomainURL: server.URL, AccessToken: "test"})
	if err := RetryUnresolved(ctx, conn, client); err != nil {
		t.Fatal(err)
	}
	var activeBefore bool
	db.QueryRow("SELECT is_active FROM events WHERE id=$1", first).Scan(&activeBefore)
	if !activeBefore {
		t.Fatal("missing record treated as deleted")
	}
	deletionVisible.Store(true)
	db.Exec("UPDATE unresolved_sync_records SET next_retry_at=now()")
	if err := RetryUnresolved(ctx, conn, client); err != nil {
		t.Fatal(err)
	}
	var status string
	db.QueryRow("SELECT status FROM integration_sync_state WHERE subscription_key=$1", key).Scan(&status)
	if status != "recovery_incomplete" {
		t.Fatalf("second open issue hidden: %s", status)
	}
	var otherStatus string
	db.QueryRow("SELECT status FROM unresolved_sync_records WHERE subscription_key=$1", other).Scan(&otherStatus)
	if otherStatus != "unresolved" {
		t.Fatal("other subscription was resolved")
	}
	secondLive.Store(true)
	db.Exec("UPDATE unresolved_sync_records SET next_retry_at=now()")
	if err := RetryUnresolved(ctx, conn, client); err != nil {
		t.Fatal(err)
	}
	db.QueryRow("SELECT status FROM integration_sync_state WHERE subscription_key=$1", key).Scan(&status)
	if status != "healthy" {
		t.Fatalf("last issue not cleared: %s", status)
	}
	restored, err := repositories.GetEventByID(ctx, db, second)
	if err != nil || restored.SyncStatus != "cached" || restored.Name != "Access restored" {
		t.Fatalf("retry did not restore clean metadata: %+v %v", restored, err)
	}
	var audits int
	if err := db.QueryRow("SELECT count(*) FROM audit_logs WHERE action='sync.resolve'").Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audits=%d err=%v", audits, err)
	}
	if err := repositories.RecordUnresolved(ctx, conn, key, first, "missing_again"); err != nil {
		t.Fatal(err)
	}
	issues, err := ListSyncIssues(ctx, db, key)
	if err != nil || len(issues) != 1 {
		t.Fatalf("reopen failed: %v %v", issues, err)
	}
	db.QueryRow("SELECT count(*) FROM audit_logs WHERE action='sync.resolve'").Scan(&audits)
	if audits != 2 {
		t.Fatal("reopening erased audit history")
	}
	// A newer local record must not be overwritten by older deletion evidence.
	if _, err = db.Exec("UPDATE events SET is_active=TRUE,salesforce_last_modified_at=$2 WHERE id=$1", first, deletion.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = RetryUnresolved(ctx, conn, client); err != nil {
		t.Fatal(err)
	}
	var active bool
	db.QueryRow("SELECT is_active FROM events WHERE id=$1", first).Scan(&active)
	if !active {
		t.Fatal("newer record deleted")
	}
}

func TestAutomaticAuditFailureRollsBackResolutionAndData(t *testing.T) {
	db := cdcTestDatabase(t)
	ctx := context.Background()
	conn, _ := db.Conn(ctx)
	defer conn.Close()
	old := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	id := "006000000000001"
	if err := repositories.UpsertEvent(ctx, conn, &models.Event{ID: id, Name: "Keep me", EventDate: "2026-11-27", SalesforceModifiedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.RecordUnresolved(ctx, conn, "audit-failure", id, "missing_from_queryall"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE audit_logs"); err != nil {
		t.Fatal(err)
	}
	if err := applyResolvedSource(ctx, conn, "audit-failure", id, nil, old.Add(time.Minute), "operator", "Verified deletion", "ticket"); err == nil {
		t.Fatal("resolution succeeded without audit persistence")
	}
	var active bool
	if err := db.QueryRow("SELECT is_active FROM events WHERE id=$1", id).Scan(&active); err != nil || !active {
		t.Fatalf("data mutation escaped rollback: %v %v", active, err)
	}
	issues, err := ListSyncIssues(ctx, db, "audit-failure")
	if err != nil || len(issues) != 1 {
		t.Fatalf("issue resolution escaped rollback: %v %v", issues, err)
	}
}

func TestCDCMissingRecordIsPersistedBeforeCheckpoint(t *testing.T) {
	db := cdcTestDatabase(t)
	ctx := context.Background()
	conn, _ := db.Conn(ctx)
	defer conn.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"done":true,"records":[]}`) }))
	defer server.Close()
	client, _ := sf.NewClient(sf.Config{MyDomainURL: server.URL, AccessToken: "test"})
	payload := map[string]any{"ChangeEventHeader": map[string]any{"entityName": "Opportunity", "changeType": "UPDATE", "recordIds": []any{"006000000000001"}}}
	if err := processCDCMessage(ctx, conn, client, "live-missing", []byte{1}, payload); err != nil {
		t.Fatal(err)
	}
	issues, err := ListSyncIssues(ctx, db, "live-missing")
	if err != nil || len(issues) != 1 {
		t.Fatalf("missing live message not tracked: %v %v", issues, err)
	}
	if _, err = db.Exec(`ALTER TABLE unresolved_sync_records ADD CONSTRAINT reject_next CHECK(subscription_key <> 'failed-live')`); err != nil {
		t.Fatal(err)
	}
	if err = processCDCMessage(ctx, conn, client, "failed-live", []byte{2}, payload); err == nil {
		t.Fatal("failed persistence ignored")
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM integration_sync_state WHERE subscription_key='failed-live'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("checkpoint escaped rollback: %d %v", count, err)
	}
}

func TestAutomaticRetryRotatesAndPreservesBackoff(t *testing.T) {
	db := cdcTestDatabase(t)
	ctx := context.Background()
	conn, _ := db.Conn(ctx)
	defer conn.Close()
	key := cdcSubscriptionKey("org-test", pubsubTopic())
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("006%012d", i)
		if err := repositories.RecordUnresolved(ctx, conn, key, id, "missing"); err != nil {
			t.Fatal(err)
		}
	}
	var checks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "userinfo") {
			fmt.Fprint(w, `{"organization_id":"org-test"}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/deleted/") {
			w.WriteHeader(503)
			return
		}
		checks.Add(1)
		w.WriteHeader(503)
	}))
	defer server.Close()
	client, _ := sf.NewClient(sf.Config{MyDomainURL: server.URL, AccessToken: "test"})
	if err := RetryUnresolved(ctx, conn, client); err != nil {
		t.Fatal(err)
	}
	if checks.Load() != 20 {
		t.Fatalf("batch size: %d", checks.Load())
	}
	var next time.Time
	db.QueryRow("SELECT next_retry_at FROM unresolved_sync_records WHERE source_record_id='006000000000000'").Scan(&next)
	if err := repositories.RecordUnresolved(ctx, conn, key, "006000000000000", "detected again"); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	db.QueryRow("SELECT next_retry_at FROM unresolved_sync_records WHERE source_record_id='006000000000000'").Scan(&after)
	if !after.Equal(next) {
		t.Fatal("repeat detection reset backoff")
	}
	if err := RetryUnresolved(ctx, conn, client); err != nil {
		t.Fatal(err)
	}
	if checks.Load() != 25 {
		t.Fatalf("later records starved: %d", checks.Load())
	}
	var attempted int
	db.QueryRow("SELECT count(*) FROM unresolved_sync_records WHERE retry_count=1 AND last_retried_at IS NOT NULL").Scan(&attempted)
	if attempted != 25 {
		t.Fatalf("attempts not persisted: %d", attempted)
	}
}

func TestAutomaticRetrySkipsRecoveryAndFailsOnLostConnection(t *testing.T) {
	db := cdcTestDatabase(t)
	ctx := context.Background()
	holder, _ := db.Conn(ctx)
	defer holder.Close()
	holder.ExecContext(ctx, "SELECT pg_advisory_lock(hashtext('sf_event_cache_recovery'))")
	conn, _ := db.Conn(ctx)
	defer conn.Close()
	// A nil client deliberately ensures no Salesforce call occurs while recovery owns the gate.
	if err := RetryUnresolved(ctx, conn, nil); err != nil {
		t.Fatal(err)
	}
	holder.ExecContext(ctx, "SELECT pg_advisory_unlock(hashtext('sf_event_cache_recovery'))")
	var pid int
	conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid)
	if _, err := holder.ExecContext(ctx, "SELECT pg_terminate_backend($1)", pid); err != nil {
		t.Fatal(err)
	}
	if err := RetryUnresolved(ctx, conn, nil); err == nil {
		t.Fatal("lost sweep connection accepted")
	}
}

type SyncIssue struct {
	SubscriptionKey string    `json:"subscriptionKey"`
	RecordID        string    `json:"recordId"`
	Reason          string    `json:"reason"`
	Status          string    `json:"status"`
	FirstDetected   time.Time `json:"firstDetectedAt"`
	LastDetected    time.Time `json:"lastDetectedAt"`
}

func ListSyncIssues(ctx context.Context, db *sql.DB, key string) ([]SyncIssue, error) {
	rows, err := db.QueryContext(ctx, `SELECT subscription_key,source_record_id,reason,status,first_detected_at,last_detected_at FROM unresolved_sync_records WHERE source_system=$1 AND subscription_key=$2 AND status='unresolved' ORDER BY first_detected_at`, repositories.CDCSource, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	issues := []SyncIssue{}
	for rows.Next() {
		var issue SyncIssue
		if err = rows.Scan(&issue.SubscriptionKey, &issue.RecordID, &issue.Reason, &issue.Status, &issue.FirstDetected, &issue.LastDetected); err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}
