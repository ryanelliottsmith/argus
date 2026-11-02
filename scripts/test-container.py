#!/usr/bin/env python3
"""Exercise a built Argus image without credentials, Internet or a real router.

Usage: python3 scripts/test-container.py --engine podman --image localhost/argus:test
Requires Python 3 and a working Docker-compatible container engine.
"""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--engine", default="docker")
    parser.add_argument("--image", default="argus:test")
    parser.add_argument("--startup-update-cached", action="store_true",
                        help="exercise opt-in startup sync using cached DBs, with no download credentials")
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[1]

    def run(*command, check=True, timeout=60):
        result = subprocess.run(
            [args.engine, *command], text=True, capture_output=True, timeout=timeout
        )
        if check and result.returncode:
            raise RuntimeError(result.stderr or result.stdout)
        return result

    is_podman = "podman" in run("version").stdout.lower()
    image = json.loads(run("image", "inspect", args.image).stdout)[0]
    assert image["Config"]["User"] == "65532:65532", "image must default to non-root"
    assert image["Config"]["WorkingDir"] == "/data"
    fixture_dir = repo / "internal/enrich/testdata"
    assert (fixture_dir / "GeoLite2-ASN.mmdb").is_file()

    # Named-volume initialization checks image /data ownership for the default
    # user. A disposable sticky tmpfs lets an arbitrary UID write without root
    # or host chown (older Podman rejects tmpfs uid/gid options). Real deployments
    # should give that UID/GID ownership or fsGroup access, not use mode 1777.
    for uid in (65532, 10001):
        name = "argus-smoke-" + secrets.token_hex(6)
        volume = name + "-data" if uid == 65532 else None
        with tempfile.TemporaryDirectory(prefix="argus-container-") as tmp:
            tmp = Path(tmp)
            config = (repo / "etc/container.yaml.example").read_text()
            config = config.replace('db_path: ""', "db_path: /data/auth.db")
            config = config.replace("flowlog:\n  enabled: false", "flowlog:\n  enabled: true")
            (tmp / "config.yaml").write_text(config)
            os.chmod(tmp / "config.yaml", 0o644)
            command = [
                "run", "--detach", "--name", name, "--network", "none",
                "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
                "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=16m,mode=1777",
                "--mount", f"type=bind,src={tmp / 'config.yaml'},dst=/etc/argus/config.yaml,ro",
                "--mount", f"type=bind,src={fixture_dir},dst=/data/geoip,ro",
                "--env", "GOAUTH_MFA_ENCRYPTION_KEY=" + secrets.token_hex(32),
            ]
            if args.startup_update_cached:
                command += ["--env", "MAXMIND_UPDATE_ON_START=true"]
            if is_podman:
                # Avoid Podman's automatic extra writable tmpfs mounts and
                # automatic passwd entries, so tests exercise the image itself.
                command += ["--read-only-tmpfs=false", "--passwd=false"]
            try:
                if volume:
                    run("volume", "create", volume)
                    command += ["--mount", f"type=volume,src={volume},dst=/data"]
                else:
                    command += ["--user", f"{uid}:{uid}", "--tmpfs",
                                "/data:rw,nosuid,nodev,noexec,size=64m,mode=1777"]
                run(*command, args.image)
                for _ in range(60):
                    state = json.loads(run("inspect", name).stdout)[0]
                    if not state["State"]["Running"]:
                        raise RuntimeError("daemon exited:\n" + run("logs", name).stdout)
                    logs = run("logs", name)
                    if "[API] Listening" in logs.stdout + logs.stderr:
                        break
                    time.sleep(0.1)
                else:
                    raise RuntimeError("daemon never reached API startup")

                assert state["HostConfig"]["ReadonlyRootfs"]
                if args.startup_update_cached:
                    assert "[maxmind] startup update check" in logs.stdout + logs.stderr
                mounts = {mount["Destination"]: mount for mount in state["Mounts"]}
                for destination in ("/etc/argus/config.yaml", "/data/geoip"):
                    assert not mounts[destination]["RW"], destination + " mount is writable"
                run("exec", name, "/usr/local/bin/argus", "debug", "--no-pprof")
                run("exec", name, "/usr/local/bin/argus", "auth", "--db", "/data/auth.db",
                    "--config", "/etc/argus/config.yaml", "user", "list")
                blocked = run("exec", name, "/usr/local/bin/argus", "debug",
                              "--output", "/etc/argus/forbidden", check=False)
                assert blocked.returncode != 0, "configuration/root filesystem unexpectedly writable"

                run("cp", name + ":/data/.", str(tmp / "state"))
                for db in ("detections.sqlite", "auth.db", "auth_sessions.db", "flows.sqlite"):
                    assert (tmp / "state" / db).is_file(), "missing writable state file: " + db
                statuses = list((tmp / "state/debug-bundles").glob("*/proc-status.txt"))
                assert statuses, "debug command did not capture the running daemon"
                status = dict(line.split(":", 1) for line in statuses[0].read_text().splitlines() if ":" in line)
                assert set(status["Uid"].split()) == {str(uid)}, status["Uid"]
                assert set(status["Gid"].split()) == {str(uid)}, status["Gid"]
                for field in ("CapEff", "CapPrm", "CapBnd"):
                    assert int(status[field].strip(), 16) == 0, field + " not dropped"
                assert status["NoNewPrivs"].strip() == "1"
                # Docker/Podman --stop handles PID 1 directly: no shell wrapper.
                run("stop", "--time", "10", name)
                final = json.loads(run("inspect", name).stdout)[0]
                assert final["State"]["ExitCode"] == 0, "SIGTERM shutdown failed"
                print(f"PASS UID/GID {uid}: read-only root/config/GeoIP, SQLite/auth/flowlog, "
                      "loopback API, zero capabilities, no-new-privileges, clean SIGTERM")
            except BaseException:
                logs = run("logs", name, check=False)
                print(logs.stdout + logs.stderr)
                raise
            finally:
                original_failure = sys.exc_info()[0] is not None
                cleanup_commands = [("rm", "--force", name)]
                if volume:
                    cleanup_commands.append(("volume", "rm", volume))
                cleanup_errors = []
                for cleanup in cleanup_commands:
                    try:
                        result = run(*cleanup, check=False)
                        if result.returncode:
                            cleanup_errors.append(result.stderr or result.stdout)
                    except Exception as error:
                        cleanup_errors.append(str(error))
                if cleanup_errors:
                    message = "Container smoke cleanup failed: " + "; ".join(cleanup_errors)
                    print("WARNING: " + message, file=sys.stderr)
                    if not original_failure:
                        raise RuntimeError(message)


if __name__ == "__main__":
    main()
