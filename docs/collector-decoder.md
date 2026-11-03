# NetFlow/IPFIX collector decoder draft

This draft addresses the collector panic reported in `parseTemplate`, together
with the v9/v10 framing error that can make valid IPFIX look malformed. It does
not change the WebUI/authentication middleware, deployment values, or gateway
configuration. Validate it against your exporter's templates before relying on
complete flow coverage.

## What the fix must preserve

- NetFlow v9 uses its 20-byte header and template FlowSet ID 0; IPFIX/v10 uses
  its 16-byte header and Template Set ID 2.
- Decode only the bytes actually received, checking packet/set/template/record
  bounds before slicing. A bad field count must not panic; a zero-length set or
  zero-width record must not wedge the UDP loop.
- Valid data after an invalid datagram must still be collected. Unsupported
  protocol versions must not terminate the daemon.
- Accept UDP datagrams larger than the previous 1500-byte receive allocation.
- Keep valid template/data decoding and field alignment when skipping unknown
  fields. Do not interpret enterprise-specific IEs as ordinary IANA field IDs.
- Preserve previously valid templates when rejecting malformed replacements.

## Protocol coverage and limits

The draft handles multiple ordinary templates per set, fixed-width known
fields, reduced-size integers, and short/extended IPFIX variable-length fields
for strings/raw/unknown elements. Enterprise fields consume their PEN and data
without being mistaken for IANA fields. Known IPv4/IPv6/MAC widths are validated.
Decoded values own their bytes rather than retaining the reused UDP buffer.
Native IPFIX wire IE 256 (`ethernetType`) is not exposed as Argus's internal
custom timestamp alias; the legacy v9 extension remains compatible.

Options template descriptors are validated and marked as **non-flow**; options
values and their sampling metadata are not applied. Per-template withdrawals
are handled, but withdraw-all operations are explicitly skipped. Unknown/vendor
fields preserve record alignment but do not gain semantic decoding. This is
not complete IPFIX transport/session conformance or sampling-rate correction.

The existing template-cache layout remains: protocols are checked before using a
template, but identical exporter/domain/template IDs in v9 and v10 still share a
cache slot and may need a template refresh. Standard IPFIX timestamp IEs are not
newly mapped to the application's custom timestamps; flows without a recognized
timestamp use the existing arrival-time fallback. Complete records before a bad
trailing record/set can still be emitted; malformed definitions do not overwrite
last-good definitions. UDP datagram length must match the IPFIX message length;
concatenated messages and IPv6 jumbograms are outside this draft.

Malformed/unsupported-input warnings are visible with `debug: "false"`, sharing
one five-second budget per collector. The next emitted warning reports how many
were suppressed. Packet payloads are not included. This protects diagnostic
volume rather than adding per-reason metrics or a new logging service.

## Local verification

The decoder regression, integration and fuzz tests referenced below were used
locally and are not included in these source commits. The named integration
test command requires those local test files.

```sh
go test -count=1 ./internal/collectors
go test -count=1 -run '^TestCollectorSurvivesBadDatagramsAndIngestsIPFIX$' -v ./cmd/argus
go test -race -count=1 ./internal/collectors ./cmd/argus
go test ./...
go vet ./...
go build ./...
```

The entrypoint integration test starts the actual daemon with disposable SQLite
state, licensed synthetic MaxMind fixtures, loopback listeners, and external
integrations disabled. It sends a synthetic IPFIX packet that reproduces the
original `[:1482] with capacity 1480` panic on the unpatched code. It then verifies
continued ingestion of native IPFIX and NetFlow v9, plus a larger-than-1500-byte
IPFIX message, through the real UDP listener into SQLite and the flow-log API.
It checks addresses, ports, protocol, byte/packet counters and interface indexes,
and requires clean shutdown. These are synthetic protocol fixtures, **not a
captured UCG-Fiber session** or proof of your exporter's complete field set.

## Checks completed on this draft

- Full Go test suite, `go vet ./...`, and `go build ./...` passed.
- Collector and entrypoint race tests passed, including the real UDP-to-SQLite
  integration and exact-value API assertions after malformed input.
- A final single-worker fuzz run completed 130366 executions without a panic or
  wedge; this is a smoke test, not a proof for every possible datagram.
- Linux amd64 and arm64 container images built successfully. Actual hardened
  container smoke tests ran on amd64 at UID/GID 65532 and 10001 with read-only
  root/config/GeoIP, dropped capabilities, no network, SQLite/auth/flowlog and
  clean shutdown. ARM64 was build-checked, not runtime-tested.
- These local checks did not include a UCG-Fiber capture, live router integration,
  registry push or deployment.

## Build an image for testing

For the Podman backend behind the `docker` command on the development host, run
from the repository root:

```sh
podman build \
  --platform linux/amd64,linux/arm64 \
  --manifest localhost/argus:ipfix-test \
  --build-arg VERSION=ipfix-draft \
  .

podman manifest inspect localhost/argus:ipfix-test
podman manifest push --all localhost/argus:ipfix-test \
  docker://harbor.ryn.nz/ryan/argus:ipfix-test
```

These are instructions, not a claim that this image was published. Use a fresh
local manifest name/tag for another iteration rather than accumulating builds
in an existing manifest. Build from a revision containing these decoder changes,
not the older collector implementation. Change your own
image values to the pushed test tag/digest, and retain the previous image for
rollback. No registry push, rollout, PVC change or gateway configuration change
is performed by these tests.

## UCG-Fiber validation

1. Keep exporter destination/port and the existing Cilium restrictions unchanged.
2. Confirm startup logs show the expected draft image/version and IPFIX/v10.
3. Check the pod remains running beyond template refreshes and actual traffic.
4. Verify real addresses, ports, interface indexes and nonzero byte/packet totals
   in the dashboard/data APIs; simply staying Ready does not prove decoding.
5. Compare a known transfer with the observed flow totals, accounting separately
   for exporter sampling and export intervals. This draft does not claim to
   correct sampled traffic totals.
6. If traffic is rejected or still misdecoded, retain the decoder warning/panic
   and obtain a bounded template-plus-data export capture using your approved
   debug workflow. The distroless application image has no shell or tcpdump.
   Export records/captures contain network metadata and should be treated as
   sensitive; do not post unredacted captures publicly.

Definitions of the native formats: [IPFIX RFC 7011](https://www.rfc-editor.org/rfc/rfc7011.html)
and [NetFlow v9 RFC 3954](https://www.rfc-editor.org/rfc/rfc3954.html).
