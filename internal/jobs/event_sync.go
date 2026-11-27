package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	"wedding-project/internal/adapters/salesforce"
	"wedding-project/internal/database"
	"wedding-project/internal/repositories"
)

// EventSyncWorker coordinates asynchronous background fetching and updating of Salesforce events.
type EventSyncWorker struct {
	db        *sql.DB
	sf        *salesforce.Client
	runningMu sync.Mutex
	running   map[string]chan struct{} // channel closed when sync finishes (prevents duplicate calls)
	sem       chan struct{}            // limits max concurrent active Salesforce syncs across all weeks
}

// NewEventSyncWorker creates a new background sync worker instance.
func NewEventSyncWorker(db *sql.DB, sf *salesforce.Client) *EventSyncWorker {
	return &EventSyncWorker{
		db:      db,
		sf:      sf,
		running: make(map[string]chan struct{}),
		sem:     make(chan struct{}, 2), // max 2 concurrent Salesforce syncs
	}
}

// SyncWeekAsync kicks off an asynchronous background sync for a specific week window.
// Returns false if a sync for this week is already currently in flight (deduplication).
func (w *EventSyncWorker) SyncWeekAsync(weekStart, weekEnd time.Time) bool {
	key := weekStart.Format("2006-01-02")

	w.runningMu.Lock()
	if _, isRunning := w.running[key]; isRunning {
		w.runningMu.Unlock()
		log.Printf("[AsyncSync] Sync for week %s is already in progress, skipping duplicate trigger", key)
		return false
	}
	w.runningMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		if err := w.SyncWeek(ctx, weekStart, weekEnd); err != nil {
			log.Printf("[AsyncSync ERROR] Failed to sync week %s: %v", key, err)
		}
	}()

	return true
}

// SyncWeek fetches events for the specified week from Salesforce and upserts them to PostgreSQL.
// If another goroutine is already syncing this week, it waits for that sync to finish (deduplication).
func (w *EventSyncWorker) SyncWeek(ctx context.Context, weekStart, weekEnd time.Time) error {
	key := weekStart.Format("2006-01-02")
	subscriptionKey := fmt.Sprintf("sf_events_week_%s", key)

	w.runningMu.Lock()
	waitCh, isRunning := w.running[key]
	if isRunning {
		w.runningMu.Unlock()
		log.Printf("[AsyncSync] Sync for week %s already in-flight, waiting for completion...", key)
		select {
		case <-waitCh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	doneCh := make(chan struct{})
	w.running[key] = doneCh
	w.runningMu.Unlock()

	defer func() {
		w.runningMu.Lock()
		delete(w.running, key)
		close(doneCh)
		w.runningMu.Unlock()
	}()

	// Acquire semaphore token to avoid overwhelming Salesforce with concurrent requests
	select {
	case w.sem <- struct{}{}:
		defer func() { <-w.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	var syncConn *sql.Conn
	// Multi-instance distributed coordination: acquire PostgreSQL advisory lock for this week
	if w.db != nil {
		conn, err := w.db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("checkout db connection for sync lock: %w", err)
		}

		syncConn = conn
		lockKey := fmt.Sprintf("sf_sync_%s", key)
		var acquired bool
		var waited bool

		// Capture the DB's initial sync state *before* waiting so we can compare DB timestamps
		// without relying on or comparing application clock vs database server clock.
		var initialSuccessAt sql.NullTime
		initialStateKnown := true
		err = conn.QueryRowContext(ctx,
			`SELECT last_success_at FROM integration_sync_state WHERE source_system = 'salesforce' AND subscription_key = $1`,
			subscriptionKey,
		).Scan(&initialSuccessAt)
		if err != nil {
			if err == sql.ErrNoRows {
				initialSuccessAt = sql.NullTime{Valid: false}
			} else {
				// Failed to query initial state; disable result reuse so we don't falsely conclude it advanced
				initialStateKnown = false
				log.Printf("[AsyncSync WARNING] Could not query initial sync state for %s: %v. Result reuse disabled.", subscriptionKey, err)
			}
		}

		for {
			if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext($1))", lockKey).Scan(&acquired); err != nil {
				database.DiscardBadConn(conn)
				return fmt.Errorf("query advisory lock for week %s: %w", key, err)
			}
			if acquired {
				break
			}

			waited = true
			// Another instance is syncing this week; wait and retry until lock is free or context expires
			log.Printf("[AsyncSync] Week %s is currently being synced by another instance. Waiting...", key)
			select {
			case <-ctx.Done():
				database.DiscardBadConn(conn)
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}

		defer func() {
			if acquired {
				database.ReleaseAdvisoryLock(conn, "SELECT pg_advisory_unlock(hashtext($1))", lockKey)
			} else {
				_ = conn.Close()
			}
		}()

		// If we waited for another instance to complete its sync, verify if it completed successfully
		if waited && initialStateKnown {
			var status string
			var postSuccessAt sql.NullTime
			query := `SELECT status, last_success_at FROM integration_sync_state WHERE source_system = 'salesforce' AND subscription_key = $1`
			if err := conn.QueryRowContext(ctx, query, subscriptionKey).Scan(&status, &postSuccessAt); err == nil {
				// Success if status is healthy and last_success_at advanced compared to what was in DB before waiting
				hasAdvanced := postSuccessAt.Valid && (!initialSuccessAt.Valid || postSuccessAt.Time.After(initialSuccessAt.Time))
				if status == "healthy" && hasAdvanced {
					log.Printf("[AsyncSync] Week %s was successfully synchronized by another instance (last_success_at: %v). Reusing fresh result.", key, postSuccessAt.Time)
					return nil
				}
				log.Printf("[AsyncSync] Waited for week %s, but another instance did not produce a new healthy sync (status=%q, initialSuccess=%v, postSuccess=%v). Proceeding with fresh fetch.", key, status, initialSuccessAt, postSuccessAt)
			}
		}
	}

	if syncConn == nil {
		return fmt.Errorf("event sync database is not configured")
	}
	return database.WithEventCacheGate(ctx, syncConn, false, func() error {

		if err := repositories.RecordSyncAttempt(ctx, w.db, "salesforce", subscriptionKey); err != nil {
			log.Printf("[AsyncSync WARNING] Failed to record sync attempt: %v", err)
		}

		t0 := time.Now()
		log.Printf("[AsyncSync] Background job starting for week %s to %s...", key, weekEnd.Format("2006-01-02"))

		events, err := salesforce.GetWeeklyEventsContext(ctx, w.sf, weekStart, weekEnd)
		if err != nil {
			if recErr := repositories.RecordSyncResult(ctx, w.db, "salesforce", subscriptionKey, err); recErr != nil {
				log.Printf("[AsyncSync WARNING] Failed to record sync error result: %v", recErr)
			}
			return fmt.Errorf("salesforce fetch: %w", err)
		}

		fetchDuration := time.Since(t0).Round(time.Millisecond)
		log.Printf("[AsyncSync] Fetched %d events from Salesforce in %s. Upserting to PostgreSQL...", len(events), fetchDuration)

		tSave := time.Now()
		if err := repositories.UpsertEvents(ctx, syncConn, events); err != nil {
			if recErr := repositories.RecordSyncResult(ctx, w.db, "salesforce", subscriptionKey, err); recErr != nil {
				log.Printf("[AsyncSync WARNING] Failed to record sync error result: %v", recErr)
			}
			return fmt.Errorf("upsert events: %w", err)
		}

		saveDuration := time.Since(tSave).Round(time.Millisecond)
		if recErr := repositories.RecordSyncResult(ctx, w.db, "salesforce", subscriptionKey, nil); recErr != nil {
			log.Printf("[AsyncSync WARNING] Failed to record sync success result: %v", recErr)
		}

		log.Printf("[AsyncSync SUCCESS] Synchronized %d events for week %s in %s (total: %s)",
			len(events), key, saveDuration, time.Since(t0).Round(time.Millisecond))
		return nil
	})
}

// StartScheduledSync begins a periodic background sweep (e.g. hourly or nightly)
// that pre-fetches upcoming weeks so PostgreSQL is always pre-warmed.
func (w *EventSyncWorker) StartScheduledSync(ctx context.Context, interval time.Duration, weeksAhead int) {
	ticker := time.NewTicker(interval)
	log.Printf("[AsyncSync Scheduler] Started background periodic sync (every %s, checking %d weeks ahead)", interval, weeksAhead)

	go func() {
		// Initial sweep in background after 5s delay on server boot
		time.Sleep(5 * time.Second)
		w.sweepUpcomingWeeks(ctx, weeksAhead)

		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				w.sweepUpcomingWeeks(ctx, weeksAhead)
			}
		}
	}()
}

func (w *EventSyncWorker) sweepUpcomingWeeks(ctx context.Context, weeksAhead int) {
	// Multi-instance distributed coordination: ensure only ONE instance sweeps at a time
	if w.db != nil {
		conn, err := w.db.Conn(ctx)
		if err != nil {
			log.Printf("[AsyncSync Scheduler WARNING] Failed to checkout DB conn for sweep lock: %v", err)
			return
		}

		var acquired bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtext('sf_sweep_scheduled_sync'))").Scan(&acquired); err != nil {
			database.DiscardBadConn(conn)
			log.Printf("[AsyncSync Scheduler WARNING] Failed to query sweep lock: %v", err)
			return
		} else if !acquired {
			_ = conn.Close()
			log.Println("[AsyncSync Scheduler] Another instance is currently running the periodic sweep. Skipping this run.")
			return
		}
		defer func() {
			if acquired {
				database.ReleaseAdvisoryLock(conn, "SELECT pg_advisory_unlock(hashtext('sf_sweep_scheduled_sync'))", "")
			} else {
				_ = conn.Close()
			}
		}()
	}

	now := time.Now()
	// Find Monday of current week
	offset := int(time.Monday - now.Weekday())
	if offset > 0 {
		offset -= 7
	}
	currentMonday := time.Date(now.Year(), now.Month(), now.Day()+offset, 0, 0, 0, 0, now.Location())

	for i := 0; i < weeksAhead; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		wStart := currentMonday.AddDate(0, 0, i*7)
		wEnd := wStart.AddDate(0, 0, 6)

		sweepCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if err := w.SyncWeek(sweepCtx, wStart, wEnd); err != nil {
			log.Printf("[AsyncSync Scheduler] Sweep error for week %s: %v", wStart.Format("2006-01-02"), err)
		}
		cancel()
	}
}
