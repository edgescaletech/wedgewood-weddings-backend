package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	"wedding-project/internal/adapters/salesforce"
	"wedding-project/internal/repositories"
)

// EventSyncWorker coordinates asynchronous background fetching and updating of Salesforce events.
type EventSyncWorker struct {
	db        *sql.DB
	sf        *salesforce.Client
	runningMu sync.Mutex
	running   map[string]bool // key: weekStart (e.g. "2026-11-23") prevents duplicate concurrent syncs
}

// NewEventSyncWorker creates a new background sync worker instance.
func NewEventSyncWorker(db *sql.DB, sf *salesforce.Client) *EventSyncWorker {
	return &EventSyncWorker{
		db:      db,
		sf:      sf,
		running: make(map[string]bool),
	}
}

// SyncWeekAsync kicks off an asynchronous background sync for a specific week window.
// Returns false if a sync for this week is already currently in flight (deduplication).
func (w *EventSyncWorker) SyncWeekAsync(weekStart, weekEnd time.Time) bool {
	key := weekStart.Format("2006-01-02")

	w.runningMu.Lock()
	if w.running[key] {
		w.runningMu.Unlock()
		log.Printf("[AsyncSync] Sync for week %s is already in progress, skipping duplicate trigger", key)
		return false
	}
	w.running[key] = true
	w.runningMu.Unlock()

	go func() {
		defer func() {
			w.runningMu.Lock()
			delete(w.running, key)
			w.runningMu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		if err := w.SyncWeek(ctx, weekStart, weekEnd); err != nil {
			log.Printf("[AsyncSync ERROR] Failed to sync week %s: %v", key, err)
		}
	}()

	return true
}

// SyncWeek fetches events for the specified week from Salesforce and upserts them to PostgreSQL.
func (w *EventSyncWorker) SyncWeek(ctx context.Context, weekStart, weekEnd time.Time) error {
	key := weekStart.Format("2006-01-02")
	subscriptionKey := fmt.Sprintf("sf_events_week_%s", key)

	_ = repositories.RecordSyncAttempt(ctx, w.db, "salesforce", subscriptionKey)

	t0 := time.Now()
	log.Printf("[AsyncSync] Background job starting for week %s to %s...", key, weekEnd.Format("2006-01-02"))

	events, err := salesforce.GetWeeklyEvents(w.sf, weekStart, weekEnd)
	if err != nil {
		repositories.RecordSyncResult(ctx, w.db, "salesforce", subscriptionKey, err)
		return fmt.Errorf("salesforce fetch: %w", err)
	}

	fetchDuration := time.Since(t0).Round(time.Millisecond)
	log.Printf("[AsyncSync] Fetched %d events from Salesforce in %s. Upserting to PostgreSQL...", len(events), fetchDuration)

	tSave := time.Now()
	if err := repositories.UpsertEvents(ctx, w.db, events); err != nil {
		repositories.RecordSyncResult(ctx, w.db, "salesforce", subscriptionKey, err)
		return fmt.Errorf("upsert events: %w", err)
	}

	saveDuration := time.Since(tSave).Round(time.Millisecond)
	repositories.RecordSyncResult(ctx, w.db, "salesforce", subscriptionKey, nil)

	log.Printf("[AsyncSync SUCCESS] Synchronized %d events for week %s in %s (total: %s)",
		len(events), key, saveDuration, time.Since(t0).Round(time.Millisecond))
	return nil
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
	now := time.Now()
	// Find Monday of current week
	offset := int(time.Monday - now.Weekday())
	if offset > 0 {
		offset -= 7
	}
	currentMonday := time.Date(now.Year(), now.Month(), now.Day()+offset, 0, 0, 0, 0, now.Location())

	for i := 0; i < weeksAhead; i++ {
		wStart := currentMonday.AddDate(0, 0, i*7)
		wEnd := wStart.AddDate(0, 0, 6)
		w.SyncWeekAsync(wStart, wEnd)
	}
}
