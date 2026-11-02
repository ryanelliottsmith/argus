package flowstore

import (
	"context"
	"testing"
	"time"

	"argus/internal/flowdir"
)

// Startup-relative ticks can divide one wall-clock bucket into several pieces.
func TestClosedBucketPersistsCombinedTotalsAndTopN(t *testing.T) {
	s := closedBucketStore(t)
	s.limits.TopIPs = 1
	s.limits.TopPrefixes = 1
	s.limits.TopPorts = 1
	tk := tlKey{1800, 65001, "in"}
	hk := detailKey{1800, 65001, "in"}
	// First non-boundary tick must not publish any part of this bucket.
	if err := s.flush5mBefore(1800); err != nil {
		t.Fatal(err)
	}
	if err := s.flushDetailBefore(1800); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s.db, "flowstore_proto"); n != 0 {
		t.Fatalf("open detail rows = %d", n)
	}
	// Second piece changes the winners. Prefix/IP/port losers must never reach DB.
	s.tl[tk].add(250, 5)
	s.tl[tk].ifaces["uplink"] += 250
	a := s.details[hk]
	a.ips[ipKey{"198.51.100.1", "192.0.2.1", 6, 80}] = &ipCounter{counter: counter{250, 5, 1}}
	a.pfx["198.51.100.0/24"] = &counter{250, 5, 1}
	a.ports[80] = &counter{250, 5, 1}
	// Fixture counters are independent here, just like Accumulate's counters.
	a.proto[6] = &counter{350, 7, 2}
	a.country["US"] = &counter{350, 7, 2}
	a.tcpFlows = 2
	a.synCount = 2
	s.meta[65001].pendingIn += 250
	if err := s.flush5mBefore(2100); err != nil {
		t.Fatal(err)
	}
	if err := s.flushDetailBefore(3600); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"flowstore_timeline", "flowstore_proto", "flowstore_country"} {
		var b, p, f int
		if err := s.db.QueryRow(`SELECT bytes,packets,flows FROM `+tbl).Scan(&b, &p, &f); err != nil {
			t.Fatal(err)
		}
		if b != 350 || p != 7 || f != 2 {
			t.Errorf("%s = %d/%d/%d, want 350/7/2", tbl, b, p, f)
		}
	}
	for _, tbl := range []string{"flowstore_top_ips", "flowstore_top_prefixes", "flowstore_ports"} {
		if n := countRows(t, s.db, tbl); n != 1 {
			t.Errorf("%s rows = %d, want exactly top 1", tbl, n)
		}
		var b int
		if err := s.db.QueryRow(`SELECT bytes FROM ` + tbl).Scan(&b); err != nil {
			t.Fatal(err)
		}
		if b != 250 {
			t.Errorf("%s winner bytes = %d, want 250", tbl, b)
		}
	}
	var iface, flags, total int
	if err := s.db.QueryRow(`SELECT bytes FROM flowstore_timeline_iface`).Scan(&iface); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT syn_count FROM flowstore_tcp_flags`).Scan(&flags); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT total_in FROM flowstore_asn_meta`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if iface != 350 || flags != 2 || total != 350 {
		t.Fatalf("iface/flags/meta = %d/%d/%d", iface, flags, total)
	}
	if len(s.tl) != 0 || len(s.details) != 0 {
		t.Fatal("closed buckets retained after successful commit")
	}
	// Repeating a successful flush must neither overwrite nor add totals again.
	if err := s.flush5mBefore(2100); err != nil {
		t.Fatal(err)
	}
	if err := s.flushDetailBefore(3600); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT total_in FROM flowstore_asn_meta`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 350 {
		t.Fatalf("repeated flush meta = %d", total)
	}
}

func TestRestartWithinOpenBucketDoesNotOverwritePersistedHistory(t *testing.T) {
	s := closedBucketStore(t)
	// Simulate a restart while this bucket is still open: no partial history exists.
	if err := s.flush5mBefore(1800); err != nil {
		t.Fatal(err)
	}
	if err := s.flushDetailBefore(1800); err != nil {
		t.Fatal(err)
	}
	restarted := closedBucketStore(t)
	restarted.db = s.db
	if err := restarted.warmupMeta(); err != nil {
		t.Fatal(err)
	}
	restarted.meta[65001].pendingIn = 25
	restarted.tl[tlKey{1800, 65001, "in"}].bytes = 25
	restarted.details[detailKey{1800, 65001, "in"}].proto[6].bytes = 25
	if err := restarted.flush5mBefore(1800); err != nil {
		t.Fatal(err)
	}
	if err := restarted.flushDetailBefore(1800); err != nil {
		t.Fatal(err)
	}
	if countRows(t, s.db, "flowstore_timeline") != 0 || countRows(t, s.db, "flowstore_proto") != 0 {
		t.Fatal("restart published partial current bucket")
	}
	if err := restarted.flush5mBefore(2100); err != nil {
		t.Fatal(err)
	}
	if err := restarted.flushDetailBefore(3600); err != nil {
		t.Fatal(err)
	}
	var b, total int
	if err := s.db.QueryRow(`SELECT bytes FROM flowstore_timeline`).Scan(&b); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT total_in FROM flowstore_asn_meta`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if b != 25 || total != 125 {
		t.Fatalf("post-restart history/meta = %d/%d, want 25/125 (open RAM is not durable)", b, total)
	}
}

func TestFlushDoesNotHoldIngestionLockDuringDBWait(t *testing.T) {
	for _, detail := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeline", true: "detail"}[detail], func(t *testing.T) {
			s := closedBucketStore(t)
			s.dir = flowdir.New(nil, nil)
			s.db.SetMaxOpenConns(1)
			conn, err := s.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			finished := make(chan error, 1)
			go func() {
				if detail {
					finished <- s.flushDetailBefore(3600)
				} else {
					finished <- s.flush5mBefore(3600)
				}
			}()
			// Wait until the writer is actually blocked acquiring the only DB connection.
			deadline := time.After(2 * time.Second)
			for s.db.Stats().WaitCount == 0 {
				select {
				case <-deadline:
					t.Fatal("writer did not reach DB wait")
				default:
					time.Sleep(time.Millisecond)
				}
			}
			ingested := make(chan struct{})
			go func() {
				s.Accumulate(&FlowEvent{PeerSrcAS: 65002, PeerDstAS: 65002, SrcHost: "203.0.113.2", DstHost: "192.0.2.2", Bytes: 9, Proto: 6, DstPort: 443})
				close(ingested)
			}()
			select {
			case <-ingested:
			case <-time.After(time.Second):
				t.Error("SQLite wait blocked ingestion")
			}
			conn.Close()
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFlushRetainsOpenBuckets(t *testing.T) {
	db := openTestDB(t)
	const now int64 = 1801
	tk := tlKey{now / 300 * 300, 65001, "in"}
	hk := detailKey{now / 1800 * 1800, 65001, "in"}
	s := &Store{db: db, limits: DefaultSettings(),
		tl:      map[tlKey]*tlVal{tk: {counter: counter{100, 2, 1}, ifaces: map[string]uint64{"uplink": 100}}},
		details: map[detailKey]*detailAccum{hk: {proto: map[uint8]*counter{6: {100, 2, 1}}}},
		meta:    map[uint32]*metaAccum{65001: {firstSeen: now, lastSeen: now, pendingIn: 100}},
	}
	if err := s.flush5mBefore(now / 300 * 300); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, "flowstore_timeline"); got != 0 {
		t.Errorf("open timeline persisted: %d rows", got)
	}
	if got := countRows(t, db, "flowstore_timeline_iface"); got != 0 {
		t.Errorf("open iface persisted: %d rows", got)
	}
	if s.tl[tk] == nil {
		t.Error("open timeline drained")
	}
	if err := s.flushDetailBefore(now / 1800 * 1800); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, db, "flowstore_proto"); got != 0 {
		t.Errorf("open detail persisted: %d rows", got)
	}
	if s.details[hk] == nil {
		t.Error("open detail drained")
	}
	var total int
	if err := db.QueryRow(`SELECT total_in FROM flowstore_asn_meta WHERE asn=65001`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 100 {
		t.Fatalf("meta delta = %d, want 100", total)
	}
}
