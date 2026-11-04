package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log"
	"time"
)

// CheckpointWAL runs PASSIVE before TRUNCATE to copy available WAL frames before
// TRUNCATE takes the writer lock and tries to shrink the -wal file to zero bytes.
// It returns busy=true when SQLite could not finish (for example, a pinned reader);
// that is not an error, the next tick retries.
//
// A dedicated checked-out connection uses a short busy timeout so waiting for
// readers does not hold the writer lock for the pool's full busy_timeout. This
// limits busy-handler waiting, not the total checkpoint duration: copying frames
// and disk I/O can take longer. The prior timeout is restored before reuse.
func CheckpointWAL(ctx context.Context, db *sql.DB) (busy bool, walPages, moved int, err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, 0, 0, err
	}
	defer conn.Close()
	var prevTimeout int
	if err = conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&prevTimeout); err != nil {
		return false, 0, 0, err
	}
	defer func() {
		// Cleanup must survive caller cancellation. Never return a connection with
		// an altered timeout to the pool if restoration itself fails.
		if _, restoreErr := conn.ExecContext(context.Background(), fmt.Sprintf(`PRAGMA busy_timeout=%d`, prevTimeout)); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore checkpoint busy_timeout: %w", restoreErr))
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if _, err = conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA busy_timeout=%d`, checkpointBusyTimeout.Milliseconds())); err != nil {
		return false, 0, 0, err
	}

	var b int
	if err = conn.QueryRowContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&b, &walPages, &moved); err != nil {
		return false, walPages, moved, err
	}
	err = conn.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&b, &walPages, &moved)
	return b != 0, walPages, moved, err
}

// checkpointBusyTimeout limits busy-handler waiting during checkpoint lock
// contention; it does not bound checkpoint I/O or total execution time.
const checkpointBusyTimeout = 250 * time.Millisecond

// IncrementalVacuum returns up to pages free pages to the OS. It is a no-op
// unless the file has auto_vacuum=INCREMENTAL (2); it reports the mode so the
// caller can log once that a one-off conversion is needed.
func IncrementalVacuum(ctx context.Context, db *sql.DB, pages int) (autoVacuum int, err error) {
	if err = db.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&autoVacuum); err != nil {
		return 0, err
	}
	if autoVacuum != 2 || pages <= 0 {
		return autoVacuum, nil
	}
	// PRAGMA arguments cannot be bound; pages is an int, so formatting is safe.
	// incremental_vacuum returns one (empty) row per page freed; drain them.
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`PRAGMA incremental_vacuum(%d)`, pages))
	if err != nil {
		return autoVacuum, err
	}
	for rows.Next() {
	}
	rows.Close()
	return autoVacuum, rows.Err()
}

// StartMaintenance checkpoints the WAL and incrementally vacuums every interval
// until ctx is done. interval <= 0 disables it.
func StartMaintenance(ctx context.Context, db *sql.DB, interval time.Duration, vacuumPages int) {
	if db == nil || interval <= 0 {
		log.Print("[SQLite] maintenance disabled")
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		warned := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			start := time.Now()
			busy, walPages, _, err := CheckpointWAL(ctx, db)
			if d := time.Since(start); d > time.Second {
				log.Printf("[SQLite] wal_checkpoint took %s (wal=%d pages)", d.Round(time.Millisecond), walPages)
			}
			switch {
			case err != nil:
				log.Printf("[SQLite] wal_checkpoint failed: %v", err)
			case busy:
				log.Printf("[SQLite] wal_checkpoint busy (wal=%d pages), retrying next tick", walPages)
			}
			mode, err := IncrementalVacuum(ctx, db, vacuumPages)
			if err != nil {
				log.Printf("[SQLite] incremental_vacuum failed: %v", err)
			} else if mode != 2 && vacuumPages > 0 && !warned {
				log.Printf("[SQLite] auto_vacuum=%d: freed pages are reused but the file never shrinks; "+
					"convert once offline with: PRAGMA auto_vacuum=INCREMENTAL; VACUUM; (see README)", mode)
				warned = true
			}
		}
	}()
}
