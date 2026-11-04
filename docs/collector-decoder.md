# NetFlow and IPFIX decoding

Argus accepts NetFlow v9 and IPFIX/v10 UDP exports. Each version uses its native
message header and template identifiers:

| Format | Header length | Ordinary template set | Options template set |
|---|---|---|---|
| NetFlow v9 | 20 bytes | 0 | 1 |
| IPFIX/v10 | 16 bytes | 2 | 3 |

## Decoder behaviour

- Validate message, set, template and record boundaries before reading fields.
- Reject malformed lengths and counts without panicking or stalling collection.
- Continue receiving after invalid datagrams or unsupported protocol versions.
- Receive datagrams larger than 1500 bytes without the previous fixed-buffer
  truncation.
- Preserve valid cached templates when a malformed replacement is rejected.
- Give emitted field values their own storage rather than retaining the reused
  receive buffer.

Ordinary templates support multiple definitions per set, reduced-size integers,
and short/extended IPFIX variable-length fields for strings and raw or unknown
elements. Enterprise fields consume their enterprise number and data without
being interpreted as ordinary IANA fields. IPv4, IPv6 and MAC widths are
validated. Unknown fields preserve record alignment but are not semantically
decoded.

IPFIX information element 256 (`ethernetType`) is not treated as Argus's internal
custom timestamp alias. The legacy NetFlow v9 timestamp extension remains
supported.

## Coverage limits

Options templates are validated and identified as non-flow data; their values
and sampling metadata are not applied. Individual template withdrawals are
handled, but withdraw-all operations are skipped. The decoder does not implement
complete IPFIX transport/session conformance or sampling-rate correction.

Exporter/domain/template IDs in v9 and v10 share a cache slot. The protocol is
checked before use, but alternating versions with identical IDs can require a
template refresh. Standard IPFIX timestamp elements are not mapped to the
internal custom timestamp fields; records without a recognized timestamp use
the arrival-time fallback.

Complete records preceding a malformed trailing record or set may still be
emitted. IPFIX message length must match UDP datagram length; concatenated
messages and IPv6 jumbograms are not supported.

## Diagnostics

Malformed or unsupported input produces warnings even when collector debugging
is disabled. Warnings share a five-second rate limit per collector; the next
warning reports how many diagnostics were suppressed. This count is not a
count of dropped flows. Packet payloads are not logged.

## Validating an exporter

Confirm that received records contain the expected addresses, ports, interface
indexes and positive byte/packet counters. Compare a known transfer with the
observed totals, accounting for exporter sampling and export intervals. A
running process alone does not establish complete exporter compatibility.

When investigating rejected records, collect the diagnostic and a bounded
capture containing both template and data messages. Captures contain network
metadata and should be redacted before sharing.

Format specifications: [IPFIX RFC 7011](https://www.rfc-editor.org/rfc/rfc7011.html)
and [NetFlow v9 RFC 3954](https://www.rfc-editor.org/rfc/rfc3954.html).
