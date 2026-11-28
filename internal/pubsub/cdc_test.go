package pubsub

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/metadata"
)

func TestCDCDeletesEveryRecordAndCommitsBookmarkAtomically(t *testing.T) {
	for _, failure := range []string{"", "delete", "checkpoint"} {
		t.Run(failure, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			ids := []any{"006000000000001", "006000000000002"}
			payload := map[string]any{"ChangeEventHeader": map[string]any{"entityName": "Opportunity", "changeType": "DELETE", "recordIds": ids, "commitTimestamp": int64(1795800000000)}}
			mock.ExpectBegin()
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
			first := mock.ExpectExec("INSERT INTO events").WithArgs(ids[0], time.UnixMilli(1795800000000).UTC())
			if failure == "delete" {
				first.WillReturnError(errors.New("write failed"))
				mock.ExpectRollback()
			} else {
				// Deleting an already absent record must still allow replay progress.
				first.WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectQuery("SELECT is_active").WillReturnRows(sqlmock.NewRows([]string{"matches"}).AddRow(false))
				mock.ExpectExec("INSERT INTO events").WithArgs(ids[1], time.UnixMilli(1795800000000).UTC()).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery("SELECT is_active").WillReturnRows(sqlmock.NewRows([]string{"matches"}).AddRow(false))
				mock.ExpectExec("INSERT INTO integration_inbox").WillReturnResult(sqlmock.NewResult(1, 1))
				checkpoint := mock.ExpectExec("INSERT INTO integration_sync_state").WithArgs(cdcSource, "key", []byte{1, 2})
				if failure == "checkpoint" {
					checkpoint.WillReturnError(errors.New("checkpoint failed"))
					mock.ExpectRollback()
				} else {
					checkpoint.WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectCommit()
				}
			}
			err = processCDCMessage(context.Background(), conn, nil, "key", []byte{1, 2}, payload)
			if (err != nil) != (failure != "") {
				t.Fatalf("failure=%s error=%v", failure, err)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCDCCheckpointLoadDoesNotMaskDatabaseErrors(t *testing.T) {
	for _, test := range []struct {
		name                      string
		noRows, recovery, wantErr bool
	}{
		{name: "first startup", noRows: true, recovery: true},
		{name: "database unavailable", wantErr: true},
		{name: "saved checkpoint"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, _ := sqlmock.New()
			defer db.Close()
			conn, _ := db.Conn(context.Background())
			defer conn.Close()
			query := mock.ExpectQuery("SELECT replay_id, status").WithArgs(cdcSource, "key")
			if test.noRows {
				query.WillReturnError(sql.ErrNoRows)
			} else if test.wantErr {
				query.WillReturnError(errors.New("database unavailable"))
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"replay_id", "status"}).AddRow([]byte{9}, "healthy"))
			}
			_, recovery, err := loadCDCCheckpoint(context.Background(), conn, "key")
			if (err != nil) != test.wantErr || recovery != test.recovery {
				t.Fatalf("recovery=%v err=%v", recovery, err)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCDCGapRequiresRecoveryWithoutRecordIDs(t *testing.T) {
	_, _, _, err := parseCDCHeader(map[string]any{"ChangeEventHeader": map[string]any{"entityName": "Opportunity", "changeType": "GAP_OVERFLOW"}})
	if !errors.Is(err, errRecoveryRequired) {
		t.Fatal(err)
	}
}

func TestCDCPermissionFailureDoesNotResetReplay(t *testing.T) {
	if replayRejected(errors.New("permission denied"), nil) {
		t.Fatal("permission failure reset checkpoint")
	}
	trailers := metadata.Pairs("error-code", "sfdc.platform.eventbus.grpc.subscription.fetch.replayid.corrupted")
	if !replayRejected(errors.New("invalid argument"), trailers) {
		t.Fatal("rejected replay not detected")
	}
}

func TestCDCSubscriptionScope(t *testing.T) {
	first := cdcSubscriptionKey("orgA", "/data/ChangeEvents")
	if first == cdcSubscriptionKey("orgB", "/data/ChangeEvents") || first == cdcSubscriptionKey("orgA", "/data/OpportunityChangeEvent") || len(first) > 100 {
		t.Fatal("checkpoint scope collision or oversized key")
	}
}

func TestCDCMalformedRecordIDsDoNotStartTransaction(t *testing.T) {
	for _, ids := range []any{nil, "006000000000001", []any{}, []any{nil}, []any{""}} {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		payload := map[string]any{"ChangeEventHeader": map[string]any{"entityName": "Opportunity", "changeType": "DELETE", "recordIds": ids, "commitTimestamp": int64(1795800000000)}}
		if err = processCDCMessage(context.Background(), conn, nil, "test", []byte{1}, payload); err == nil {
			t.Fatalf("malformed IDs accepted: %#v", ids)
		}
		if err = mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		conn.Close()
		db.Close()
	}
}
