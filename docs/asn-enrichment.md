# ASN profile enrichment: RIPEstat

Argus uses RIPE NCC's RIPEstat Data API at `https://stat.ripe.net` for the
optional external context on `/asn/{asn}/profile` and the ASN page. This
replaces the unavailable BGPView API. It is not a change to NetFlow/IPFIX
collection, the BGP listener, or the local MaxMind ASN/GeoIP databases.

## Data mapping and limits

| Argus field | RIPEstat source | Meaning |
|---|---|---|
| ASN name | `as-overview`: `data.holder` | Holder/organisation label |
| Announced prefix count/list | `announced-prefixes`: `data.prefixes` | Distinct prefixes in the selected RIS snapshot |
| `peers_count` / `peers` | `asn-neighbours`: `data.neighbours` | Distinct RIS-observed left/right AS neighbours; **not** a contractual peering inventory |
| Country | Not supplied by these endpoints | Unavailable (`present: false`), not inferred from a registry or organisation name |
| IX presence | Not supplied by these endpoints | Unavailable (`present: false`), not fabricated as an empty confirmed inventory |
| Observed paths | Existing local flowstore/Pathfinder source | Unchanged; not supplied by RIPEstat |

The public JSON field names stay compatible. Provider and available-field
attribution use `ripestat`. The UI retains the **Peers** label; its count means
RIS-observed AS neighbours. Records flagged `uncertain` by RIPEstat are excluded:
these can result solely from a RIS collector's own peering, rather than reliable
evidence of an operational neighbour relationship.

The prefix request uses a point-in-time query rather than the endpoint's default
two-week history, which could include already-withdrawn prefixes. RIPEstat's
snapshot can lag real-time routing; neighbour data can have its own latest
snapshot. The provider's default prefix visibility threshold also excludes
routes seen by fewer than ten RIS full-feed peers. These results are **not** a
complete, live global routing table and should not be compared as exact totals
with a different provider or this router's own observations.

Counts are taken from the full deduplicated result before the existing
30-entry display-list limit. A successful empty result is distinguishable from
an unavailable/failed lookup. The existing cache, deadline, retry and
circuit-breaker behaviour keeps external lookup failures out of the flow-ingest
path; local ASN profile data remains available when RIPEstat is unreachable.

## Network-policy change

Allow the Argus pod to resolve DNS and connect to **`stat.ripe.net`, TCP 443**.
The API needs no authentication credentials. Replace the obsolete
`api.bgpview.io` FQDN in the existing Cilium policy; preserve DNS, MaxMind and
other separately required rules. For example, the RIPEstat-specific rule is:

```yaml
# One entry under the existing policy's spec.egress, not a complete policy.
- toFQDNs:
    - matchName: stat.ripe.net
  toPorts:
    - ports:
        - port: "443"
          protocol: TCP
```

Ensure the policy's endpoint selector actually matches the current Argus pod.
Changing a policy alone does not change the application's provider: rebuild and
roll out the binary containing this adapter. No cluster policy or deployment is
changed by these source files. Browser-originated links/CDNs are separate from
pod-originated API calls.

## Verification

The fixture tests and live probes below were used for local verification and
are not included in these source commits. Probe commands require those local
test files; a fresh checkout will not contain the named live probe.

Ordinary tests use local HTTP fixtures and do not need internet access:

```sh
go test ./internal/enrich/asnintel ./internal/api
go test -race ./internal/enrich/asnintel ./internal/api
```

An explicit live probe exercises the actual Go enrichment client against
RIPEstat, rather than just fetching sample JSON with curl:

```sh
ARGUS_RIPESTAT_LIVE=1 go test ./internal/enrich/asnintel \
  -run '^TestRIPEstatLive$' -count=1 -v
```

This probe makes public, read-only ASN queries. Its success establishes access
from the test host, **not** from a network-policy-restricted Kubernetes pod.
After an operator rebuilds/deploys and updates policy, the ASN profile response
should attribute external fields to `ripestat`. An uncached lookup should report
successful `source_status` for `base`, `prefixes` and `peers`; a cache hit reports
`source_status.cache` instead. Country and IX fields remaining unavailable is
intentional.

## Primary documentation

- AS overview: <https://stat.ripe.net/docs/data-api/api-endpoints/as-overview/>
- Announced prefixes and visibility threshold:
  <https://stat.ripe.net/docs/data-api/api-endpoints/announced-prefixes/>
- ASN neighbour definitions and collector-only uncertainty:
  <https://stat.ripe.net/docs/data-api/api-endpoints/asn-neighbours/>
