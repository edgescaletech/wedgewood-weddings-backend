package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// WithEventCacheGate coordinates a complete fetch-and-write with recovery.
// Normal cache writers take a shared lock before fetching. Recovery takes an
// exclusive lock before scanning the cache. Work must write on this same conn.
// The caller owns conn; successful cleanup leaves it open (it may hold other locks).
func WithEventCacheGate(ctx context.Context, conn *sql.Conn, recovery bool, work func() error) (err error) {
	acquire := "SELECT pg_try_advisory_lock_shared(hashtext('sf_event_cache_recovery'))"
	release := "SELECT pg_advisory_unlock_shared(hashtext('sf_event_cache_recovery'))"
	if recovery {
		acquire = "SELECT pg_try_advisory_lock(hashtext('sf_event_cache_recovery'))"
		release = "SELECT pg_advisory_unlock(hashtext('sf_event_cache_recovery'))"
	}
	for {
		var acquired bool
		if err = conn.QueryRowContext(ctx, acquire).Scan(&acquired); err != nil {
			DiscardBadConn(conn)
			return err
		}
		if acquired {
			break
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released bool
		cleanupErr := conn.QueryRowContext(cleanup, release).Scan(&released)
		if cleanupErr != nil || !released {
			DiscardBadConn(conn)
			if err == nil {
				err = fmt.Errorf("event cache coordination unlock unconfirmed: %v", cleanupErr)
			}
		}
	}()
	return work()
}
