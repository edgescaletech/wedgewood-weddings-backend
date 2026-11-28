package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"log"
	"time"
)

// DiscardBadConn marks the underlying driver connection as permanently unusable
// so database/sql discards it instead of returning it to the connection pool.
func DiscardBadConn(conn *sql.Conn) {
	if conn == nil {
		return
	}
	_ = conn.Raw(func(driverConn any) error {
		return driver.ErrBadConn
	})
	_ = conn.Close()
}

// ReleaseAdvisoryLock releases a PostgreSQL session advisory lock.
// If the unlock cannot be confirmed (query fails or returns false),
// the connection is marked with driver.ErrBadConn and destroyed to guarantee
// that a potentially locked connection is never returned to the pool.
func ReleaseAdvisoryLock(conn *sql.Conn, lockQuery string, lockParam string) {
	if conn == nil {
		return
	}

	unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var unlocked bool
	var err error
	if lockParam != "" {
		err = conn.QueryRowContext(unlockCtx, lockQuery, lockParam).Scan(&unlocked)
	} else {
		err = conn.QueryRowContext(unlockCtx, lockQuery).Scan(&unlocked)
	}

	if err != nil || !unlocked {
		log.Printf("[AdvisoryLock WARNING] Failed to confirm advisory unlock (%s %s, err: %v, unlocked: %v). Discarding connection from pool.", lockQuery, lockParam, err, unlocked)
		DiscardBadConn(conn)
		return
	}

	_ = conn.Close()
}
