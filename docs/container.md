# Container image

The Dockerfile builds a static Go binary in a separate builder stage and runs it
on `gcr.io/distroless/static-debian12:nonroot`. The runtime includes CA
certificates and timezone data, but no shell, package manager or compiler.
It defaults to UID/GID **65532:65532** and supports other numeric IDs.

## Build

```sh
docker build -t argus:local \
  --build-arg VERSION="$(git rev-parse --short HEAD)" .
docker run --rm argus:local --version
```

Build arguments are `GO_VERSION`, `VERSION`, `BUILD_DATE` and `RUNTIME_IMAGE`;
see the Dockerfile for their defaults. They configure the build and version
metadata, not runtime credentials.

For a multi-platform image:

```sh
docker buildx build --platform linux/amd64,linux/arm64 \
  -t YOUR_REGISTRY/argus:YOUR_TAG --push .
```

The build context excludes local environment files, active configuration,
credentials, databases and Git history. Only the example container configuration
is included. Real credentials, GeoIP databases and synthetic test MMDBs are not
included in the runtime image.

## Configuration and writable paths

Start with `etc/container.yaml.example`. It configures passive collection
without BGP mitigation. Mount a configuration file at `/etc/argus/config.yaml`
and set the local prefixes, interface indexes and enabled integrations for the
network being monitored. Supported export formats and limitations are described
in [NetFlow and IPFIX decoding](collector-decoder.md).

| Path | Requirements | Purpose |
|---|---|---|
| `/data` | Writable persistent directory accessible to the selected UID/GID | Working directory for SQLite databases, WAL/SHM files and relative logs |
| `/etc/argus/config.yaml` | Readable configuration file; may be read-only | Runtime configuration |
| `/data/geoip` | ASN and City MMDBs; writable when updating is enabled | GeoIP databases and update metadata |
| `/tmp` | Writable temporary directory | Temporary files; the image sets `TMPDIR=/tmp` |

The default `/data` directory belongs to 65532:65532. Bind mounts and alternate
numeric users require corresponding filesystem permissions. The application does
not interpret `PUID` or `PGID`; select an alternate identity with the container
runtime's user option. No runtime `chown` or user creation is performed.

All enabled features must have writable paths. Container-compatible examples:

```yaml
auth:
  db_path: /data/auth.db
  session_db_path: /data/auth_sessions.db
flowlog:
  db_path: /data/flows.sqlite
detection:
  rules_config: /etc/argus/detection.yml
  memory:
    log_path: /data/risk.log
```

Mount detection rules separately when enabling detection. Relative logs resolve
under `/data`. SQLite requires its containing directory to be writable so it can
create WAL, SHM and journal files; making only the database file writable is not
sufficient. Back up persistent state with Argus stopped or SQLite-aware tooling.

## GeoIP databases

Argus needs `GeoLite2-ASN.mmdb` and `GeoLite2-City.mmdb`. Preload appropriately
licensed databases into `/data/geoip`, or enable startup updates. With both
`maxmind.enabled` and `maxmind.update_on_start` disabled, the directory may be
mounted read-only. Restart Argus after externally replacing the databases to
reopen its readers.

### Startup updates

Startup updates run before GeoIP readers are opened. Enable them through runtime
environment variables:

```text
MAXMIND_UPDATE_ON_START=true
MAXMIND_ACCOUNT_ID=<account-id>
MAXMIND_LICENSE_KEY=<license-key>
```

Or configure the feature in YAML and supply credentials through the environment:

```yaml
maxmind:
  enabled: false
  update_on_start: true
  db_path: /data/geoip
  editions: [GeoLite2-ASN, GeoLite2-City]
  check_every: 24h
  http_timeout: 30s
```

Environment credentials override YAML credentials; the environment startup flag
overrides `update_on_start`. Credentials alone do not enable downloads. Never
put a license key in an image build argument or commit it to configuration.

Keep the database directory writable and persistent when updates are enabled.
The root filesystem and configuration file can remain read-only.

On first startup, missing databases are downloaded and validated. Later startups
reuse persisted check times to avoid requests within `check_every`. When a check
is due, authenticated HEAD requests inspect release metadata before downloading
changed archives. Missing or corrupt databases bypass the check interval.
Downloads are staged, validated and renamed atomically. Credentials are not
stored in update metadata or written to logs.

If updating fails but both cached databases are valid, startup warns and uses
them. Without a usable pair, startup fails. Inspect the `[maxmind]` logs for
update and cache status.

This feature runs at startup, not on a periodic timer. The legacy background
downloader controlled by `maxmind.enabled` does not reload open readers.
See MaxMind's [database update guidance](https://dev.maxmind.com/geoip/updating-databases/)
for service details, licensing and download limits.

## Run

Prepare `config.yaml` and a `geoip/` directory containing licensed databases:

```sh
docker volume create argus-data
docker run -d --name argus \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --tmpfs /tmp:rw,nosuid,nodev,noexec,size=16m,mode=1777 \
  --mount type=volume,src=argus-data,dst=/data \
  --mount type=bind,src="$(pwd)/config.yaml",dst=/etc/argus/config.yaml,readonly \
  --mount type=bind,src="$(pwd)/geoip",dst=/data/geoip,readonly \
  -p 127.0.0.1:9600:9600/tcp \
  -p 2055:2055/udp \
  argus:local
```

For startup downloads, omit the read-only GeoIP bind mount and pass the runtime
environment variables above. For another numeric identity, add `--user UID:GID`
and prepare the state directory's permissions first.

Configure API authentication and `api.allow_ips` explicitly: an allowlisted peer
is trusted administrative access. Published ports do not change how the
application interprets the caller's address.

The runtime has no shell or curl. Invoke the executable directly:

```sh
docker exec argus /usr/local/bin/argus debug --no-pprof
docker exec -it argus /usr/local/bin/argus auth \
  --db /data/auth.db --config /etc/argus/config.yaml user add -u admin -r admin
```

Set a stable `GOAUTH_MFA_ENCRYPTION_KEY` at runtime before using encrypted MFA
state. The CLI's `--db` and `--config` options select container paths rather than
its systemd-oriented defaults.

## Container checks

```sh
docker build -t argus:test .
python3 scripts/test-container.py --image argus:test
```

The same script supports Podman via `--engine podman`. It creates disposable
configuration and synthetic MMDB fixtures, disables external integrations, and
removes its test containers and volume afterward. Checks cover default and
alternate numeric identities, read-only root/configuration/GeoIP mounts,
SQLite/auth/session/flowlog creation, loopback API access, dropped capabilities,
no-new-privileges and clean SIGTERM shutdown. The flow-log database is opened,
but this smoke test does not inject exporter traffic.
