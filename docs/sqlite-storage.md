# SQLite storage and maintenance

Argus stores aggregate history and application state in `detections.sqlite`.
The optional individual-flow log uses a separate `flows.sqlite` database; the
maintenance settings below do not control its retention or query behaviour.

## Bucket persistence and daily rollups

Only complete, closed aggregation buckets are detached for persistence. Detail
writes are transactional: row or commit failures retain the pending snapshot
for retry. Additive lifetime ASN deltas are cleared after a successful commit.
Daily rollups are published in one transaction so readers do not see partially
written days.

Daily IP rollups group by the daily table's primary key. If country enrichment
changes within a day, `MAX(country)` selects a deterministic value without
creating duplicate keys. This value is not necessarily the most recent country.

Completed days are rolled up before their source detail is pruned. If rollup
fails, dependent detail pruning is skipped so the next attempt can retry.

## Telemetry startup

Before collection starts, persisted minute buckets are restored into the
telemetry rings. The initial persistence watermark skips historical minutes
already restored from SQLite, avoiding an unnecessary history rewrite on the
first flush. Current and previous-minute buckets remain eligible for updates.

Telemetry schema or restore errors, including malformed rows, stop startup with
an explicit error instead of continuing with an incomplete restore.

## Retention batches

```yaml
storage:
  write_batch_rows: 5000
```

- Unset or zero uses 5000 rows per retention-delete transaction.
- A positive value sets the maximum rows deleted per chunk.
- A negative value selects one unbatched DELETE.

Flowstore detail, timeline, daily-rollup and completion-marker pruning, plus
telemetry minute-table pruning, use this setting. Successful chunks are
committed independently, with a short pause between full chunks to let other
writers run. If a later chunk fails, the next scheduled prune resumes with the
remaining expired rows.

This batching applies to **retention deletes only**. Detail persistence and
daily-rollup publication remain atomic and are not split by this setting.
Deleting rows frees pages for reuse; it does not necessarily shrink the file.

## WAL checkpoints

```yaml
storage:
  wal_checkpoint_interval: 5m
  incremental_vacuum_pages: 2000
```

Each maintenance tick runs a PASSIVE checkpoint before attempting TRUNCATE on
the same checked-out connection. A temporary 250ms busy timeout limits waiting
on reader locks, and the connection's previous timeout is restored before it
returns to the pool. A busy checkpoint is retried on the next tick.

The timeout does not bound total checkpoint I/O time. A successful TRUNCATE
shrinks the WAL file to zero. Incremental vacuum can return freed main-database
pages to the filesystem when `auto_vacuum=INCREMENTAL` is configured; see
[Storage and logs](../README.md#storage-and-logs) for the conversion procedure.

The batch and watermark settings reduce unnecessary writes and long retention
transactions; they do not establish a database-size or throughput guarantee.
