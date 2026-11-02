package flowstore

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func closedBucketStore(t *testing.T) *Store {
	t.Helper()
	c := counter{100, 2, 1}
	return &Store{db: openTestDB(t), limits: DefaultSettings(),
		tl: map[tlKey]*tlVal{{1800, 65001, "in"}: {counter: c, ifaces: map[string]uint64{"uplink": 100}}},
		details: map[detailKey]*detailAccum{{1800, 65001, "in"}: {
			ips: map[ipKey]*ipCounter{{"203.0.113.1", "192.0.2.1", 6, 443}: {counter: c}},
			pfx: map[string]*counter{"203.0.113.0/24": &c}, proto: map[uint8]*counter{6: &c},
			country: map[string]*counter{"US": &c}, ports: map[uint16]*counter{443: &c}, tcpFlows: 1, synCount: 1,
		}}, meta: map[uint32]*metaAccum{65001: {firstSeen: 1801, lastSeen: 1801, pendingIn: 100}},
	}
}

func requireSQL(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func TestFlushRowFailureRetriesAtomically(t *testing.T) {
	for _, table := range []string{"flowstore_timeline", "flowstore_timeline_iface", "flowstore_asn_meta", "flowstore_top_ips", "flowstore_top_prefixes", "flowstore_proto", "flowstore_country", "flowstore_ports", "flowstore_tcp_flags"} {
		t.Run(table, func(t *testing.T) {
			s := closedBucketStore(t)
			detail := table != "flowstore_timeline" && table != "flowstore_timeline_iface" && table != "flowstore_asn_meta"
			flush := func() error { return s.flush5mBefore(3600) }
			tables := []string{"flowstore_timeline", "flowstore_timeline_iface", "flowstore_asn_meta"}
			if detail {
				flush = func() error { return s.flushDetailBefore(3600) }
				tables = []string{"flowstore_top_ips", "flowstore_top_prefixes", "flowstore_proto", "flowstore_country", "flowstore_ports", "flowstore_tcp_flags"}
			}
			requireSQL(t, s.db, `CREATE TRIGGER fail_row BEFORE INSERT ON `+table+` BEGIN SELECT RAISE(ABORT, 'injected row failure'); END`)
			if err := flush(); err == nil {
				t.Error("row failure was swallowed")
			}
			for _, tbl := range tables {
				if n := countRows(t, s.db, tbl); n != 0 {
					t.Errorf("failed transaction left %d rows in %s", n, tbl)
				}
			}
			// New meta deltas must not be lost or included twice when retrying the snapshot.
			s.meta[65001].pendingIn += 7
			requireSQL(t, s.db, `DROP TRIGGER fail_row`)
			if err := flush(); err != nil {
				t.Fatal(err)
			}
			for _, tbl := range tables {
				if n := countRows(t, s.db, tbl); n != 1 {
					t.Errorf("retry left %d rows in %s, want 1", n, tbl)
				}
			}
			if err := flush(); err != nil {
				t.Fatal(err)
			}
			if !detail {
				var total int
				if err := s.db.QueryRow(`SELECT total_in FROM flowstore_asn_meta WHERE asn=65001`).Scan(&total); err != nil {
					t.Fatal(err)
				}
				if total != 107 {
					t.Errorf("meta after retry and repeat = %d, want 107", total)
				}
			}
		})
	}
}

func TestFlushBeginFailureRetainsBatch(t *testing.T) {
	for _, detail := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeline", true: "detail"}[detail], func(t *testing.T) {
			s := closedBucketStore(t)
			realDB := s.db
			dead, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "closed.db"))
			if err != nil {
				t.Fatal(err)
			}
			dead.Close()
			s.db = dead
			flush := func() error { return s.flush5mBefore(3600) }
			table := "flowstore_timeline"
			if detail {
				flush = func() error { return s.flushDetailBefore(3600) }
				table = "flowstore_proto"
			}
			if err := flush(); err == nil {
				t.Fatal("expected closed DB failure")
			}
			s.db = realDB
			if err := flush(); err != nil {
				t.Fatal(err)
			}
			if n := countRows(t, s.db, table); n != 1 {
				t.Fatalf("retry wrote %d rows, want 1", n)
			}
		})
	}
}

func TestFlushCommitFailureRetainsBatch(t *testing.T) {
	for _, detail := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeline", true: "detail"}[detail], func(t *testing.T) {
			s := closedBucketStore(t)
			s.db.SetMaxOpenConns(1)
			requireSQL(t, s.db, `PRAGMA foreign_keys=ON`)
			requireSQL(t, s.db, `CREATE TABLE parent(id INTEGER PRIMARY KEY); CREATE TABLE child(id INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED)`)
			table := "flowstore_timeline"
			flush := func() error { return s.flush5mBefore(3600) }
			if detail {
				table = "flowstore_proto"
				flush = func() error { return s.flushDetailBefore(3600) }
			}
			requireSQL(t, s.db, `CREATE TRIGGER fail_commit AFTER INSERT ON `+table+` BEGIN INSERT INTO child VALUES(1); END`)
			if err := flush(); err == nil {
				t.Fatal("expected deferred constraint commit failure")
			}
			if n := countRows(t, s.db, table); n != 0 {
				t.Fatalf("failed commit persisted %d rows", n)
			}
			requireSQL(t, s.db, `DROP TRIGGER fail_commit`)
			if err := flush(); err != nil {
				t.Fatal(err)
			}
			if n := countRows(t, s.db, table); n != 1 {
				t.Fatalf("retry wrote %d rows, want 1", n)
			}
		})
	}
}
