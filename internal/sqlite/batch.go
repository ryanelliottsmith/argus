package sqlite

import (
	"database/sql"
	"fmt"
	"time"
)

// DefaultWriteBatchRows is the default for storage.write_batch_rows: how many
// rows a bulk prune deletes per transaction before committing and letting
// other writers take the lock.
const DefaultWriteBatchRows = 5000

// BatchPause is the gap between two batches. Go's pool gives no fairness and
// SQLite's busy handler polls with sleeps, so a tight loop of back-to-back
// write transactions would starve every other writer just like one long one.
const BatchPause = 20 * time.Millisecond

// DeleteBatched runs `DELETE FROM table WHERE <where>` in chunks of batch rows,
// committing each chunk separately so the write lock is released between chunks.
// table must be a rowid table and where should be served by an index.
// batch < 0 runs a single unbatched DELETE (the pre-batching behaviour);
// 0 means DefaultWriteBatchRows. Returns the number of rows deleted, including
// already committed chunks if a later chunk fails.
//
// table and where are interpolated, so they must be constants, never user input.
// Values in where must use placeholders and args.
func DeleteBatched(db *sql.DB, table, where string, batch int, args ...any) (int64, error) {
	if batch == 0 {
		batch = DefaultWriteBatchRows
	}
	if batch < 0 {
		res, err := db.Exec(`DELETE FROM `+table+` WHERE `+where, args...)
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	}
	q := fmt.Sprintf(`DELETE FROM %s WHERE rowid IN (SELECT rowid FROM %s WHERE %s LIMIT %d)`,
		table, table, where, batch)
	var total int64
	for {
		res, err := db.Exec(q, args...)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < int64(batch) {
			return total, nil
		}
		time.Sleep(BatchPause)
	}
}
