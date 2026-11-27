package pubsub

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"time"

	sf "wedding-project/internal/adapters/salesforce"
	"wedding-project/internal/database"
	"wedding-project/internal/repositories"
)

var sfIDPattern = regexp.MustCompile(`^[a-zA-Z0-9]{15}([a-zA-Z0-9]{3})?$`)

func validSFID(id string) bool { return sfIDPattern.MatchString(id) }

// QueryAll distinguishes a confirmed soft deletion from an inaccessible or
// permanently removed record. Absence alone is never permission to delete.
func confirmedDeleted(ctx context.Context, client *sf.Client, id string) (bool, time.Time, error) {
	if !validSFID(id) {
		return false, time.Time{}, fmt.Errorf("invalid Salesforce ID")
	}
	records, err := client.QueryAllContext(ctx, "SELECT Id, IsDeleted, SystemModstamp FROM Opportunity WHERE Id = '"+id+"' LIMIT 1")
	if err != nil {
		return false, time.Time{}, err
	}
	if len(records) != 1 {
		return false, time.Time{}, fmt.Errorf("Opportunity %s missing from QueryAll: verify hard deletion or access before recovery", id)
	}
	var record struct {
		ID      string `json:"Id"`
		Version string `json:"SystemModstamp"`
		Deleted *bool  `json:"IsDeleted"`
	}
	if err = json.Unmarshal(records[0], &record); err != nil {
		return false, time.Time{}, err
	}
	if record.ID != id || record.Deleted == nil {
		return false, time.Time{}, fmt.Errorf("invalid deletion confirmation for %s", id)
	}
	version, err := sf.ParseSourceTimestamp(record.Version)
	if err != nil {
		return false, time.Time{}, err
	}
	return *record.Deleted, version, nil
}

// Reconcile the cached scheduling scope: all cached event weeks and the current
// configured upcoming weeks. This is NOT an import of the organization's entire history.
// The caller must establish the stream anchor before calling this function.
func reconcileCDCCache(ctx context.Context, conn *sql.Conn, client *sf.Client) error {
	return database.WithEventCacheGate(ctx, conn, true, func() error {
		return reconcileCDCCacheSnapshot(ctx, conn, client)
	})
}

func reconcileCDCCacheSnapshot(ctx context.Context, conn *sql.Conn, client *sf.Client) error {
	rows, err := conn.QueryContext(ctx, "SELECT event_date FROM events WHERE is_active IS NOT FALSE")
	if err != nil {
		return err
	}
	weeks := map[string]time.Time{}
	monday := func(t time.Time) time.Time {
		day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		return day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
	}
	for rows.Next() {
		var date time.Time
		if err = rows.Scan(&date); err != nil {
			rows.Close()
			return err
		}
		week := monday(date)
		weeks[week.Format("2006-01-02")] = week
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	current := monday(time.Now().UTC())
	weeksAhead := 4
	configured := os.Getenv("SF_TO_DB_SYNC_WEEKS_AHEAD")
	if configured == "" {
		configured = os.Getenv("SYNC_WEEKS_AHEAD")
	}
	if configured != "" {
		parsed, err := strconv.Atoi(configured)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("invalid recovery weeks ahead %q", configured)
		}
		weeksAhead = parsed
	}
	for i := 0; i < weeksAhead; i++ {
		week := current.AddDate(0, 0, i*7)
		weeks[week.Format("2006-01-02")] = week
	}
	keys := make([]string, 0, len(weeks))
	for key := range weeks {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		week := weeks[key]
		if err = func() error {
			events, err := sf.GetWeeklyEventsContext(ctx, client, week, week.AddDate(0, 0, 6))
			if err != nil {
				return err
			}
			found := map[string]bool{}
			tx, err := conn.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			for i := range events {
				if err = repositories.UpsertEvent(ctx, tx, &events[i]); err != nil {
					tx.Rollback()
					return err
				}
				found[events[i].ID] = true
			}
			if err = tx.Commit(); err != nil {
				return err
			}
			// Refresh the candidate IDs under the recovery gate: another sync
			// may have populated the cache before recovery acquired the gate.
			cachedRows, err := conn.QueryContext(ctx, `SELECT id FROM events WHERE is_active IS NOT FALSE AND event_date >= $1 AND event_date <= $2`, week.Format("2006-01-02"), week.AddDate(0, 0, 6).Format("2006-01-02"))
			if err != nil {
				return err
			}
			var cached []string
			for cachedRows.Next() {
				var id string
				if err = cachedRows.Scan(&id); err != nil {
					cachedRows.Close()
					return err
				}
				cached = append(cached, id)
			}
			err = cachedRows.Err()
			cachedRows.Close()
			if err != nil {
				return err
			}
			// Check cached records absent from the complete weekly response. A moved
			// event is refreshed; only an explicit IsDeleted=true permits removal.
			for _, old := range cached {
				if found[old] {
					continue
				}
				deleted, version, err := confirmedDeleted(ctx, client, old)
				if err != nil {
					return err
				}
				if deleted {
					if err = repositories.MarkEventDeleted(ctx, conn, old, version); err != nil {
						return err
					}
					continue
				}
				event, err := sf.GetEventByIDContext(ctx, client, old)
				if err != nil {
					return err
				}
				if err = repositories.UpsertEvent(ctx, conn, event); err != nil {
					return err
				}
			}
			return nil
		}(); err != nil {
			return err
		}
	}
	return nil
}
