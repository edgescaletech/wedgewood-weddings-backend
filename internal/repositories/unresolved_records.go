package repositories

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const CDCSource = "salesforce_pubsub"

// All issue transitions serialize per subscription. Ordinary checkpoints only
// preserve status and do not scan/count this table on every message.
func LockSyncIssues(ctx context.Context, tx *sql.Tx, key string) error {
	_, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "sf_unresolved:"+key)
	return err
}

func RecordUnresolvedTx(ctx context.Context, tx *sql.Tx, key, id, reason string) error {
	if err := LockSyncIssues(ctx, tx, key); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO unresolved_sync_records
 (source_system,subscription_key,source_record_id,reason) VALUES ($1,$2,$3,$4)
 ON CONFLICT (source_system,subscription_key,entity_name,source_record_id) DO UPDATE SET
 retry_count=CASE WHEN unresolved_sync_records.status='unresolved' THEN unresolved_sync_records.retry_count ELSE 0 END,
 next_retry_at=CASE WHEN unresolved_sync_records.status='unresolved' THEN unresolved_sync_records.next_retry_at ELSE now() END,
 last_retried_at=CASE WHEN unresolved_sync_records.status='unresolved' THEN unresolved_sync_records.last_retried_at ELSE NULL END,
 reason=EXCLUDED.reason,status='unresolved',last_detected_at=now(),resolved_at=NULL,resolved_by=NULL,resolution_notes=NULL`, CDCSource, key, id, reason)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO integration_sync_state(source_system,subscription_key,status,last_error,updated_at)
 VALUES($1,$2,'recovery_incomplete','Unresolved Salesforce records await automatic verification',now())
 ON CONFLICT(source_system,subscription_key) DO UPDATE SET
 status=CASE WHEN integration_sync_state.status='recovery_required' THEN 'recovery_required' ELSE 'recovery_incomplete' END,
 last_error=EXCLUDED.last_error,updated_at=now()`, CDCSource, key)
	return err
}

func RecordUnresolved(ctx context.Context, conn *sql.Conn, key, id, reason string) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = RecordUnresolvedTx(ctx, tx, key, id, reason); err != nil {
		return err
	}
	return tx.Commit()
}

func CompleteSyncRecovery(ctx context.Context, conn *sql.Conn, key string) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = LockSyncIssues(ctx, tx, key); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO integration_sync_state(source_system,subscription_key,status,last_error,updated_at)
 SELECT $1::varchar(50),$2::varchar(100),CASE WHEN EXISTS(SELECT 1 FROM unresolved_sync_records WHERE source_system=$1 AND subscription_key=$2 AND status='unresolved') THEN 'recovery_incomplete' ELSE 'healthy' END,NULL,now()
 ON CONFLICT(source_system,subscription_key) DO UPDATE SET status=EXCLUDED.status,
 last_error=CASE WHEN EXCLUDED.status='recovery_incomplete' THEN 'Unresolved Salesforce records await automatic verification' ELSE NULL END,updated_at=now()`, CDCSource, key)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ResolveSyncIssueTx is called only after the caller has verified and applied
// current source data (or a verified deletion) in the same transaction.
func ResolveSyncIssueTx(ctx context.Context, tx *sql.Tx, key, id, status, operator, reason, evidence string, version time.Time) error {
	if status != "restored" && status != "confirmed_deleted" {
		return fmt.Errorf("invalid resolution status")
	}
	if operator == "" || reason == "" || evidence == "" {
		return fmt.Errorf("operator, reason and evidence are required")
	}
	if err := LockSyncIssues(ctx, tx, key); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE unresolved_sync_records SET status=$4,resolved_at=now(),resolved_by=$5,resolution_notes=$6
 WHERE source_system=$1 AND subscription_key=$2 AND source_record_id=$3 AND entity_name='Opportunity' AND status='unresolved'`, CDCSource, key, id, status, operator, reason)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	metadata, err := json.Marshal(map[string]any{"subscriptionKey": key, "sourceRecordId": id, "operator": operator, "reason": reason, "evidence": evidence, "sourceVersion": version, "resolution": status})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(id,action,target_type,target_id,metadata) VALUES($1,'sync.resolve','Opportunity',$2,$3)`, uuid.New(), id, string(metadata))
	if err != nil {
		return err
	}
	// Never clear recovery_required: a full reconciliation is still needed.
	_, err = tx.ExecContext(ctx, `UPDATE integration_sync_state SET status='healthy',last_error=NULL,updated_at=now()
 WHERE source_system=$1 AND subscription_key=$2 AND status='recovery_incomplete'
 AND NOT EXISTS(SELECT 1 FROM unresolved_sync_records WHERE source_system=$1 AND subscription_key=$2 AND status='unresolved')`, CDCSource, key)
	return err
}
