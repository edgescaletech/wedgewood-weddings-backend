package pubsub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	sf "wedding-project/internal/adapters/salesforce"
	"wedding-project/internal/database"
	"wedding-project/internal/repositories"
)

var errStaleObservation = errors.New("source observation does not match newer cache state")

// RetryUnresolved runs only while the caller owns the scheduled sweep lock on
// conn. Source reads and writes share a nonblocking recovery gate; no SQL
// transaction remains open during a Salesforce call. Losing conn fails closed.
func RetryUnresolved(ctx context.Context, conn *sql.Conn, client *sf.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, err := database.TryEventCacheGateShared(ctx, conn, func() error {
		sourceCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		org, err := client.OrganizationID(sourceCtx)
		stop()
		if err != nil {
			return err
		}
		key := cdcSubscriptionKey(org, pubsubTopic())
		rows, err := conn.QueryContext(ctx, `SELECT source_record_id FROM unresolved_sync_records
   WHERE source_system=$1 AND subscription_key=$2 AND entity_name='Opportunity'
   AND status='unresolved' AND next_retry_at<=now()
   ORDER BY next_retry_at,source_record_id LIMIT 20`, repositories.CDCSource, key)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil || len(ids) == 0 {
			return err
		}
		// Fetch history lazily, at most once per batch, only for absent records.
		var history map[string]time.Time
		historyLoaded := false
		for _, id := range ids {
			if err = ctx.Err(); err != nil {
				return err
			}
			// Persist the attempt before calling Salesforce so timeouts rotate fairly.
			_, err = conn.ExecContext(ctx, `UPDATE unresolved_sync_records SET
    retry_count=retry_count+1,last_retried_at=now(),
    next_retry_at=now()+make_interval(hours=>LEAST(24, (1 << LEAST(retry_count,5))))
    WHERE source_system=$1 AND subscription_key=$2 AND entity_name='Opportunity'
    AND source_record_id=$3 AND status='unresolved'`, repositories.CDCSource, key, id)
			if err != nil {
				return err
			}
			sourceCtx, stop = context.WithTimeout(ctx, 10*time.Second)
			deleted, version, sourceErr := confirmedDeleted(sourceCtx, client, id)
			stop()
			evidence := "Current Salesforce queryAll and record fetch"
			if deleted {
				evidence = "Salesforce queryAll IsDeleted=true with SystemModstamp"
			}
			if errors.Is(sourceErr, ErrUnconfirmedMissing) {
				if !historyLoaded {
					historyLoaded = true
					sourceCtx, stop = context.WithTimeout(ctx, 10*time.Second)
					end := time.Now().UTC().Truncate(time.Minute)
					history, err = client.DeletedOpportunities(sourceCtx, end.Add(-14*24*time.Hour), end)
					stop()
					if err != nil {
						log.Printf("[Scheduled verification] Deletion history unavailable: %v", err)
					}
				}
				if stamp, ok := history[id]; ok {
					deleted, version, sourceErr = true, stamp, nil
					evidence = "Salesforce getDeleted Opportunity entry inside the covered history window"
				}
			}
			if sourceErr != nil {
				log.Printf("[Scheduled verification] Record %s remains unverified: %v", id, sourceErr)
				continue
			}
			var event *sf.Event
			if !deleted {
				sourceCtx, stop = context.WithTimeout(ctx, 10*time.Second)
				event, sourceErr = sf.GetEventByIDContext(sourceCtx, client, id)
				stop()
				if sourceErr != nil {
					log.Printf("[Scheduled verification] Record %s fetch failed: %v", id, sourceErr)
					continue
				}
			}
			err = applyResolvedSource(ctx, conn, key, id, event, version, "system:scheduled_retry", "Automatic Salesforce verification", evidence)
			if errors.Is(err, errStaleObservation) {
				continue
			}
			if err != nil {
				return fmt.Errorf("apply automatic verification: %w", err)
			}
		}
		return nil
	})
	return err
}
