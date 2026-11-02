# Container deployment

The Dockerfile builds a static Go binary in a separate builder stage and runs it
on `gcr.io/distroless/static-debian12:nonroot`. The runtime has CA certificates
and timezone data, but no shell, package manager, compiler or root init script.
The image defaults to **UID/GID 65532:65532** and supports other numeric IDs.
No runtime `chown`, user creation, privileged mode or extra capabilities are
needed for the passive collector.

## Build

```sh
docker build -t argus:local \
  --build-arg VERSION="$(git rev-parse --short HEAD)" .
docker run --rm argus:local --version
```

Build arguments: `GO_VERSION` (default `1.27.3`), `VERSION` (default `dev`),
`BUILD_DATE` (default `unknown`), and `RUNTIME_IMAGE` (default shown above).
Pass a digest-qualified runtime image if your supply-chain policy requires it.
The compiler version is independent of the Go version minimum in `go.mod`.
Build arguments set version metadata, not runtime configuration or secrets.

BuildKit/buildx supports native cross-compilation with its automatic platform
arguments; for example (publishing is an explicit operator action):

```sh
docker buildx build --platform linux/amd64,linux/arm64 \
  -t YOUR_REGISTRY/argus:YOUR_TAG --push .
```

The `.dockerignore` allowlists source/build inputs; local `.env`, active YAML
configurations, credentials, databases and Git history are not sent to the
builder. Only the example container configuration is baked into the runtime.
GeoIP databases and real credentials are **not** included. No synthetic test
MMDBs are included in the image.

## Configuration and writable paths

Start with `etc/container.yaml.example`, not the BGP/mitigation-enabled systemd
example. The image's default command reads `/etc/argus/config.yaml`; mount your
customised file there **read-only**. Set your actual `my_prefixes`, upstream
interface indexes, API access policy and internal PTR resolver. Use unsampled
NetFlow v9 while the separately identified decoder limitations remain unfixed.

| Path | Mount / permissions | Purpose |
|---|---|---|
| `/data` | Writable persistent volume, owned by the selected UID/GID or accessible via `fsGroup` | Working directory: main SQLite/WAL/SHM files, optional raw-flow database, auth/session DBs and relative feature/debug log files |
| `/etc/argus/config.yaml` | Read-only file, readable by the selected UID | Configuration; no rewriting or templating on startup |
| `/data/geoip` | Both MMDBs required; may be a separate **read-only** mount with updater disabled | `GeoLite2-ASN.mmdb` and `GeoLite2-City.mmdb` |
| `/tmp` | Ephemeral writable tmpfs / memory-backed `emptyDir` | Go/SQLite temporary files; `TMPDIR=/tmp` is set in the image |

The default `/data` image directory belongs to 65532:65532; Docker/Podman named
volumes initialise from that ownership. A bind mount or another UID does not
inherit those permissions: provision ownership/group access on the host or use
Kubernetes `fsGroup` with a supporting storage driver. Do not solve this with a
root entrypoint or a world-writable production data directory. The application
does not interpret `PUID`/`PGID`; use `--user` / `runAsUser` and `runAsGroup`.

Keep **all enabled features' writable absolute paths under `/data`**. For
example `auth.db_path: /data/auth.db`, `auth.session_db_path:
/data/auth_sessions.db`, `flowlog.db_path: /data/flows.sqlite` and
`detection.memory.log_path: /data/risk.log`. If detection is enabled, mount its
rules at a readable path such as `/etc/argus/detection.yml` and configure
`detection.rules_config` accordingly. Relative detection/blackhole/anomaly,
collector-debug and BGP-dump logs already resolve under `/data`.

A read-only root still needs a writable `/data` directory: SQLite requires its
**directory**, not just an existing `.sqlite` file, for WAL/SHM/journal files.
Back up persistent state with the application stopped or SQLite-aware tooling.

To keep collector Internet egress blocked, preload both MaxMind databases and
leave `maxmind.enabled: false`. An external updater can replace them; restart
Argus afterwards to reopen readers. If you enable its built-in updater, that
GeoIP directory must instead be writable and network access/credentials must be
provided. Removing `ptr` from `enrich` is not a universal DNS-disable switch;
ASN profile pages also have the external lookup described in the code review.
The current dashboard still uses browser-loaded CDN scripts; a shell-free image
and pod policy do not make the UI fully offline or contain browser requests.

## Hardened Docker run

Prepare an external `config.yaml` from the example and a `geoip/` directory
containing **real**, appropriately licensed ASN and City MMDB files. The
repository fixtures are only for testing.

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

Limit inbound UDP2055 to the actual router using your host firewall/network
policy. No BGP TCP179 exposure or `NET_ADMIN`, `NET_RAW`, host networking or
privileged mode is needed. If you elect to enable BGP later, its low-port bind
and route-control implications need a separate review.

**API authorization remains deliberate.** The example trusts only loopback.
Docker port publishing/NAT does not make a host browser a loopback caller inside
the container, so the API can return 403 until you configure access. The app
currently treats an allowlisted ingress peer as trusted administrative access;
do not blindly allow an entire LAN/pod CIDR or an unauthenticated proxy. Keep
independent authentication at your ingress and restrict direct API access.
These container controls do not repair the separately identified auth bugs.

For UID/GID overrides add `--user 10001:10001` and provision the state volume's
permissions first. Existing volumes are not automatically chowned when changing
IDs. An optional `GOMEMLIMIT` should be set below your actual container memory
limit with suitable headroom; this packaging does not change application queues
or provide an overload budget.

The runtime has no shell or curl. Invoke the binary directly for administration:

```sh
docker exec argus /usr/local/bin/argus debug --no-pprof
# Once auth.db_path and a stable MFA encryption key are configured:
docker exec -it argus /usr/local/bin/argus auth \
  --db /data/auth.db --config /etc/argus/config.yaml user add -u admin -r admin
```

Supply `GOAUTH_MFA_ENCRYPTION_KEY` through a secret, not an image build argument.
The CLI needs explicit `--db` and `--config`: its old systemd defaults are not
container paths. Separate-session inventory and other authentication limitations
from the code review remain outside this packaging change.

## Kubernetes hardening

These are fields to merge into your Deployment/StatefulSet, **not a complete
manifest**. Choose your own namespace, labels, image, resources, Services, PVCs
and policy. Keep one replica writing this SQLite state directory.

```yaml
# spec.template.spec
securityContext:
  runAsNonRoot: true
  runAsUser: 10001
  runAsGroup: 10001
  fsGroup: 10001
  fsGroupChangePolicy: OnRootMismatch
  seccompProfile:
    type: RuntimeDefault
automountServiceAccountToken: false
containers:
  - name: argus
    image: YOUR_REGISTRY/argus:YOUR_TAG
    securityContext:
      readOnlyRootFilesystem: true
      allowPrivilegeEscalation: false
      capabilities:
        drop: [ALL]
    ports:
      - name: http
        containerPort: 9600
        protocol: TCP
      - name: netflow
        containerPort: 2055
        protocol: UDP
    volumeMounts:
      - name: data
        mountPath: /data
      - name: config
        mountPath: /etc/argus
        readOnly: true
      - name: tmp
        mountPath: /tmp
volumes:
  - name: data
    persistentVolumeClaim:
      claimName: argus-data
  - name: config
    configMap:
      name: argus-config # key: config.yaml; use Secrets for credentials
  - name: tmp
    emptyDir:
      medium: Memory
      sizeLimit: 16Mi
```

Place the two MMDBs in the state volume's `geoip/` directory ahead of startup, or
add a separate read-only mount there. `fsGroup` handling depends on the CSI
backend; verify actual ownership for your chosen PVC rather than assuming it.
No Kubernetes API access or ServiceAccount token is needed. A TCP probe on 9600
can check the API listener without shell tools, but is not proof of healthy flow
ingestion. Cilium policy should constrain actual flow ingress and only the
outbound integrations you choose to enable; no cluster changes are made by the
Dockerfile or smoke test.

## Reproduce the container smoke test

```sh
docker build -t argus:test .
python3 scripts/test-container.py --image argus:test
# Or with rootless Podman:
podman build -t localhost/argus:test .
python3 scripts/test-container.py --engine podman --image localhost/argus:test
```

The script uses temporary configuration and synthetic MMDB fixtures, disables
external integrations, runs with `--network none`, and removes its containers
and named volume afterwards. It verifies default UID/GID 65532 and overridden
10001; read-only root/config/MMDB mounts; actual SQLite/auth/session/flowlog
creation; loopback API/debug access; zero effective/permitted/bounding
capabilities; no-new-privileges; and clean SIGTERM exit. The arbitrary-UID case
uses a disposable sticky tmpfs to avoid host `chown`/engine-specific UID mapping;
production volume ownership or CSI `fsGroup` remains an operator responsibility.
This is an OCI-runtime test, not a deployed Kubernetes/Cilium test. No live
router, real credentials, browser dependencies or external enrichment service is
used. The optional flow log is opened but no router export is injected by this
smoke test; application/parser tests cover their separate behaviors.
