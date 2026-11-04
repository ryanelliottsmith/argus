# ASN profile enrichment: RIPEstat

Argus uses RIPE NCC's RIPEstat Data API for optional external context on
`/asn/{asn}/profile` and the ASN page. External lookups are separate from
NetFlow/IPFIX collection, the BGP listener and local MaxMind ASN/GeoIP data.
The API requires no authentication credentials.

## Data mapping

| Argus field | RIPEstat source | Meaning |
|---|---|---|
| ASN name | `as-overview`: `data.holder` | Holder or organisation label |
| Announced prefix count/list | `announced-prefixes`: `data.prefixes` | Distinct prefixes in the selected RIS snapshot |
| `peers_count` / `peers` | `asn-neighbours`: `data.neighbours` | Distinct RIS-observed left/right AS neighbours, not a contractual peering inventory |
| Country | Not supplied by these endpoints | Unavailable (`present: false`) |
| IX presence | Not supplied by these endpoints | Unavailable (`present: false`), not a confirmed empty inventory |
| Observed paths | Local flowstore/Pathfinder data | Not supplied by RIPEstat |

The UI displays the neighbour count as **Peers**. Records flagged `uncertain`
by RIPEstat are excluded because they can reflect a RIS collector's own
peering rather than reliable evidence of an operational neighbour relationship.

## Coverage and freshness

Prefix requests use a point-in-time query rather than the endpoint's default
two-week history, which can include already-withdrawn prefixes. RIPEstat's
snapshot may lag real-time routing; neighbour data can use a different latest
snapshot. The default prefix visibility threshold excludes routes observed by
fewer than ten RIS full-feed peers.

Results are not a complete live global routing table. Counts are calculated
from validated, deduplicated results before the 30-entry display-list limit.
Successful empty results remain distinguishable from unavailable lookups.

Cached results, request deadlines, retries and a circuit breaker keep external
lookup failures out of the flow-ingest path. Local ASN profile data remains
available when RIPEstat is unreachable.

## Diagnosing lookup results

Inspect `/asn/{asn}/profile`:

- External field attribution uses `ripestat`.
- An uncached lookup reports provider status under `external.source_status`
  for `base`, `prefixes` and `peers`.
- Cache hits may instead report `external.source_status.cache`.
- `external.intel_error` reports an external enrichment failure, when present.
- Country and IX presence remaining unavailable is expected.

`has_bgp_data` describes locally visible BGP prefixes, not the completeness of
the RIPEstat response. A profile can have successful external enrichment while
local BGP data is unavailable.

## Provider documentation

- [AS overview](https://stat.ripe.net/docs/data-api/api-endpoints/as-overview/)
- [Announced prefixes and visibility threshold](https://stat.ripe.net/docs/data-api/api-endpoints/announced-prefixes/)
- [ASN neighbours and collector-only uncertainty](https://stat.ripe.net/docs/data-api/api-endpoints/asn-neighbours/)
