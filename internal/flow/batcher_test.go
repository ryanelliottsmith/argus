package flow

import (
	"argus/internal/bgp"
	"argus/internal/enrich"
	"net"
	"path/filepath"
	"testing"

	"github.com/yl2chen/cidranger"
)

func TestEnrichmentWithoutBGPUsesLocalASNDatabase(t *testing.T) {
	geo, err := enrich.NewGeoIP(
		filepath.Join("..", "enrich", "testdata", "GeoLite2-ASN.mmdb"),
		filepath.Join("..", "enrich", "testdata", "GeoLite2-City.mmdb"), 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	ip := "1.0.0.1"
	want := geo.GetASNNumber(ip)
	if want == 0 {
		t.Fatal("test fixture must contain an ASN for " + ip)
	}
	batcher := &FlowEnricher{geo: geo}
	rec := &FlowRecord{SrcHost: ip, DstHost: ip}
	batcher.enrichAndFeed([]*FlowRecord{rec})
	if rec.PeerSrcAS != want || rec.PeerDstAS != want || rec.DstAS != want {
		t.Fatalf("local ASN fallback: src=%d dst=%d dstAS=%d, want %d", rec.PeerSrcAS, rec.PeerDstAS, rec.DstAS, want)
	}
}

func TestLocalASNFallbackPreservesExporterAndBGPASNs(t *testing.T) {
	geo, err := enrich.NewGeoIP(
		filepath.Join("..", "enrich", "testdata", "GeoLite2-ASN.mmdb"),
		filepath.Join("..", "enrich", "testdata", "GeoLite2-City.mmdb"), 10,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, useBGP := range []bool{false, true} {
		t.Run(map[bool]string{false: "exporter", true: "bgp"}[useBGP], func(t *testing.T) {
			batcher := &FlowEnricher{geo: geo}
			rec := &FlowRecord{SrcHost: "1.0.0.1", DstHost: "1.0.0.1"}
			if useBGP {
				_, subnet, err := net.ParseCIDR("1.0.0.0/24")
				if err != nil {
					t.Fatal(err)
				}
				batcher.ranger = cidranger.NewPCTrieRanger()
				if err := batcher.ranger.Insert(bgp.BGPEnrichedEntry{Net: *subnet, ASN: 64512}); err != nil {
					t.Fatal(err)
				}
			} else {
				rec.PeerSrcAS, rec.PeerDstAS, rec.DstAS = 64512, 64512, 64512
			}
			batcher.enrichAndFeed([]*FlowRecord{rec})
			if rec.PeerSrcAS != 64512 || rec.PeerDstAS != 64512 || rec.DstAS != 64512 {
				t.Fatalf("existing ASNs overwritten: src=%d dst=%d dstAS=%d", rec.PeerSrcAS, rec.PeerDstAS, rec.DstAS)
			}
		})
	}
}
