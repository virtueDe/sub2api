#!/usr/bin/env python3
"""Bounded, read-only Linux/PG diagnostic capture; no SQL text or credentials.

Example (on the server):
  python3 db_pressure_sampler.py --postgres-container sub2api-postgres \
      --duration 120 --output /tmp/sub2api-pressure.jsonl

Without --postgres-container, psql uses existing libpq environment/pgpass.
This tool does not install a daemon or change server configuration.
"""

import argparse
import datetime
import json
import os
from pathlib import Path
import subprocess
import threading
import time


SQL = """
SET default_transaction_read_only = on;
SET statement_timeout = '1000ms';
SELECT json_build_object('visibility', CASE WHEN
 (SELECT rolsuper FROM pg_roles WHERE rolname=current_user)
 OR pg_has_role(current_user, 'pg_read_all_stats', 'USAGE')
 THEN 'all_sessions' ELSE 'own_role_only' END);
SELECT json_build_object(
 'pid',pid,'state',state,'wait_type',wait_event_type,'wait',wait_event,
 'query_age_ms',round(extract(epoch from(clock_timestamp()-query_start))*1000),
 'transaction_age_ms',round(extract(epoch from(clock_timestamp()-xact_start))*1000),
 'blockers',CASE WHEN wait_event_type='Lock' THEN pg_blocking_pids(pid) ELSE ARRAY[]::integer[] END,
 'query_kind',CASE
  WHEN query ILIKE '%usage_billing_dedup%' THEN 'billing_dedup'
  WHEN query ILIKE '%usage_group_rollup%' THEN 'group_rollup'
  WHEN query ILIKE '%usage_logs%' THEN 'usage_logs'
  WHEN query ILIKE '%ops_system_logs%' THEN 'system_logs'
  WHEN query ILIKE '%ops_error_logs%' THEN 'error_logs'
  WHEN query ILIKE '%settings%' THEN 'settings'
  ELSE 'other' END)
FROM pg_stat_activity
WHERE datname=current_database() AND pid<>pg_backend_pid() AND state<>'idle';
"""


def utc_now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


class BoundedWriter:
    def __init__(self, path, max_bytes):
        # Never truncate an existing capture or follow a pre-existing symlink.
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        self.file = os.fdopen(fd, "wb")
        self.limit = max_bytes
        self.written = 0
        self.lock = threading.Lock()
        self.full = threading.Event()

    def write(self, record, final=False):
        data = (json.dumps(record, ensure_ascii=True, separators=(",", ":")) + "\n").encode()
        with self.lock:
            # Reserve a footer even when a host sample fills the capture.
            if self.written + len(data) > self.limit - (0 if final else 256):
                self.full.set()
                return False
            self.file.write(data)
            self.file.flush()
            self.written += len(data)
            return True

    def close(self):
        self.file.close()


def process_snapshot(proc=Path("/proc")):
    result = {}
    for entry in proc.iterdir():
        if not entry.name.isdigit():
            continue
        try:
            raw = (entry / "stat").read_text()
            end = raw.rfind(")")
            fields = raw[end + 2:].split()
            ns_pid = int(entry.name)
            memory = {}
            for line in (entry / "status").read_text().splitlines():
                if line.startswith("NSpid:"):
                    ns_pid = int(line.split()[-1])
                if line.startswith(("RssAnon:", "RssFile:", "RssShmem:")):
                    key, value, _ = line.split()
                    memory[key.rstrip(":")] = int(value) * 1024
            try:
                namespace = os.readlink(entry / "ns/pid")
            except OSError:
                namespace = None
            io = {}
            try:
                for line in (entry / "io").read_text().splitlines():
                    key, value = line.split()
                    if key in ("read_bytes:", "write_bytes:"):
                        io[key.rstrip(":")] = int(value)
            except OSError:
                pass
            result[int(entry.name)] = {
                "name": raw[raw.find("(") + 1:end], "state": fields[0],
                "user": int(fields[11]), "system": int(fields[12]),
                "start": int(fields[19]), "rss_pages": int(fields[21]), "namespace_pid": ns_pid,
                "pid_namespace": namespace, "memory": memory, "io": io,
            }
        except (OSError, ValueError, IndexError):
            continue  # Processes can exit or become unreadable between reads.
    return result


def process_deltas(before, after, elapsed, ticks, page_size):
    rows = []
    for pid, current in after.items():
        previous = before.get(pid)
        if previous is None or previous["start"] != current["start"]:
            continue
        user = max(0, current["user"] - previous["user"]) / ticks / elapsed
        system = max(0, current["system"] - previous["system"]) / ticks / elapsed
        if user + system < 0.01 and current["state"] != "D":
            continue
        rows.append({"pid": pid, "namespace_pid": current["namespace_pid"], "name": current["name"],
                     "state": current["state"], "user_cores": round(user, 3), "system_cores": round(system, 3),
                     "rss_bytes": current["rss_pages"] * page_size,
                     "pid_namespace": current.get("pid_namespace"), "memory": current.get("memory", {}),
                     "io_delta": {key: max(0, value - previous.get("io", {}).get(key, value))
                                  for key, value in current.get("io", {}).items()}})
    ordered = sorted(rows, key=lambda row: row["user_cores"] + row["system_cores"], reverse=True)
    # Keep blocked processes even when they used no CPU during this interval.
    return [row for row in ordered if row["state"] == "D"][:20] + [row for row in ordered if row["state"] != "D"][:20]


def host_snapshot(proc=Path("/proc")):
    result = {}
    # Fixed files only: no process cmdline, environment or request payloads.
    for name in ("stat", "meminfo", "diskstats", "pressure/cpu", "pressure/io", "pressure/memory"):
        try:
            text = (proc / name).read_text()
            if name == "stat":
                text = "\n".join(line for line in text.splitlines() if line.startswith(("cpu ", "ctxt ", "procs_")))
            result[name] = text
        except OSError:
            result[name] = {"unavailable": True}
    return result


def database_snapshot(container=None):
    if container:
        command = ["docker", "exec", "-i", "-e", "PGCONNECT_TIMEOUT=2", "-e", "PGAPPNAME=sub2api-pressure-sampler", container, "sh", "-c",
                   'exec timeout -s KILL 2 psql -X -w -qAt -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"']
    else:
        command = ["psql", "-X", "-w", "-qAt", "-v", "ON_ERROR_STOP=1"]
    try:
        env = dict(os.environ, PGCONNECT_TIMEOUT="2", PGAPPNAME="sub2api-pressure-sampler")
        completed = subprocess.run(command, input=SQL, text=True, capture_output=True, timeout=3, env=env)
        if completed.returncode:
            # stderr may contain connection strings or credentials: never persist it.
            return {"error": "database_sample_failed", "exit_code": completed.returncode}
        allowed = {"pid", "state", "wait_type", "wait", "query_age_ms", "transaction_age_ms", "blockers", "query_kind"}
        rows = []
        visibility = "unknown"
        for line in completed.stdout.splitlines():
            if line.startswith("{"):
                row = json.loads(line)
                if row.get("visibility") in ("all_sessions", "own_role_only"):
                    visibility = row["visibility"]
                else:
                    rows.append({key: value for key, value in row.items() if key in allowed})
        return {"activity": rows, "visibility": visibility}
    except subprocess.TimeoutExpired:
        return {"error": "database_sample_timeout"}
    except (OSError, ValueError):
        return {"error": "database_sample_unavailable"}


def collect(args):
    if not Path("/proc/stat").is_file():
        raise RuntimeError("This sampler requires Linux /proc")
    writer = BoundedWriter(args.output, args.max_mib * 1024 * 1024)
    stop = threading.Event()
    deadline = time.monotonic() + args.duration

    def sample_database():
        while time.monotonic() < deadline and not stop.is_set() and not writer.full.is_set():
            writer.write({"kind": "database", "at": utc_now(), **database_snapshot(args.postgres_container)})
            stop.wait(3)

    worker = threading.Thread(target=sample_database)
    try:
        writer.write({"kind": "start", "at": utc_now(), "duration_seconds": args.duration,
                      "cpu_count": os.cpu_count(), "max_bytes": writer.limit,
                      "shutdown_grace_seconds": 3, "process_sample_limit": 40})
        previous = process_snapshot()
        last = time.monotonic()
        worker.start()
        while time.monotonic() < deadline and not writer.full.is_set():
            stop.wait(min(1, max(0, deadline - time.monotonic())))
            current = process_snapshot()
            now = time.monotonic()
            rows = process_deltas(previous, current, max(now-last, 0.001), os.sysconf("SC_CLK_TCK"), os.sysconf("SC_PAGE_SIZE"))
            writer.write({"kind": "host", "at": utc_now(), "sample_seconds": round(now-last, 3),
                          "processes": rows, **host_snapshot()})
            previous, last = current, now
    finally:
        stop.set()
        if worker.ident is not None:
            worker.join()
        writer.write({"kind": "end", "at": utc_now(), "capacity_reached": writer.full.is_set()}, final=True)
        writer.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--postgres-container")
    parser.add_argument("--duration", type=int, default=120)
    parser.add_argument("--max-mib", type=int, default=20)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if not 1 <= args.duration <= 900 or not 1 <= args.max_mib <= 100:
        parser.error("duration must be 1..900 seconds and max-mib must be 1..100")
    collect(args)


if __name__ == "__main__":
    main()
