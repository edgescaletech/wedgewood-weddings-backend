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

// TryEventCacheGateShared never waits for recovery. The caller must close conn
// before doing an uncached fetch when acquired is false.
func TryEventCacheGateShared(ctx context.Context, conn *sql.Conn, work func() error) (acquired bool, err error) {
	lockCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	err = conn.QueryRowContext(lockCtx, "SELECT pg_try_advisory_lock_shared(hashtext('sf_event_cache_recovery'))").Scan(&acquired)
	cancel()
	if err != nil {
		DiscardBadConn(conn)
		return false, err
	}
	if !acquired {
		return false, nil
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		var released bool
		unlockErr := conn.QueryRowContext(cleanup, "SELECT pg_advisory_unlock_shared(hashtext('sf_event_cache_recovery'))").Scan(&released)
		if unlockErr != nil || !released {
			DiscardBadConn(conn)
			if err == nil {
				err = fmt.Errorf("cache unlock unconfirmed: %v", unlockErr)
			}
		}
	}()
	return true, work()
}
