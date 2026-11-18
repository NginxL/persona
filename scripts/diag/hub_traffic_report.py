#!/usr/bin/env python3
"""Read-only hourly client traffic report; no waiting, tokens, UA or message bodies."""
import argparse
import collections
import datetime as dt
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
from persona_identity import SERVICE_NAMES, DATABASE_NAMES


def timestamp(value):
    value = re.sub(r"\.(\d+)", lambda m: "." + (m.group(1) + "000000")[:6], value)
    parsed = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise ValueError("timestamps must include a timezone")
    return parsed.astimezone(dt.timezone.utc)


def iso(value):
    return value.isoformat().replace("+00:00", "Z")


def route_templates():
    values = {"unmatched", "overflow"}
    for line in (ROOT / "server/internal/hub/testdata/route_scopes.txt").read_text().splitlines():
        parts = line.split(None, 1)
        if len(parts) == 2 and not line.lstrip().startswith("#"):
            values.add(parts[1].strip())
    return values


def rows(stream, since, until):
    allowed = route_templates()
    for line in stream:
        try:
            message = json.loads(line).get("MESSAGE", "")
            if not isinstance(message, str) or "traffic minute=" not in message:
                continue
            fields = shlex.split(message[message.index("traffic minute=") + len("traffic "):])
            row = dict(field.split("=", 1) for field in fields if "=" in field)
            minute = timestamp(row["minute"])
            if not since <= minute < until:
                continue
            for key in ("count", "body_bytes", "max_body_bytes", "elapsed_ms"):
                row[key] = int(row[key])
                if row[key] < 0:
                    raise ValueError("negative counter")
            if not re.fullmatch(r"[a-z_]{1,48}", row["event"]) or not re.fullmatch(r"[a-z_-]{1,48}", row["action"]):
                continue
            row["tagged"] = all(key in row for key in ("source", "platform_basis", "source_basis"))
            if row.get("platform") not in ("ios", "android", "web", "watchos", "macos", "visionos", "unknown"):
                row["platform"] = "unknown"
            if row.get("source") not in ("app", "widget", "liveactivity", "diagnostic", "unknown"):
                row["source"] = "unknown"
            row["route"] = row["route"] if row["route"] in allowed else "unrecognized-template"
            row["hour"] = iso(minute.replace(minute=0, second=0, microsecond=0))
            yield row
        except (ValueError, KeyError, TypeError):
            continue  # never print a malformed/private journal record


def aggregate(records):
    totals = collections.defaultdict(lambda: [0, 0, 0, 0])
    untagged = unknown_platform = native_default = 0
    for row in records:
        key = (row["hour"], row["platform"], row["source"], row["event"], row["action"], row["route"])
        out = totals[key]
        out[0] += row["count"]
        out[1] += row["body_bytes"]
        out[2] = max(out[2], row["max_body_bytes"])
        out[3] += row["elapsed_ms"]
        untagged += not row["tagged"]
        if row["event"] == "http" and row["action"] == "started":
            unknown_platform += row["count"] if row["platform"] == "unknown" else 0
            native_default += row["count"] if row.get("source_basis") == "native-default" else 0
    return {"hourly": [dict(zip(("hour", "platform", "source", "event", "action", "route"), key), count=v[0], body_bytes=v[1], max_body_bytes=v[2], elapsed_ms=v[3]) for key, v in sorted(totals.items())],
            "untagged_bucket_rows": untagged, "unknown_platform_requests": unknown_platform,
            "native_default_surface_requests": native_default}


def connection_report(sessions, since, until, identity=None):
    selected = [s for s in sessions if identity is None or s["identity"] == identity]
    hours = collections.defaultdict(lambda: {"opens": 0, "closes": 0, "connected_seconds": 0, "reasons": collections.Counter()})
    durations, heartbeats = [], collections.Counter()
    for s in selected:
        start = timestamp(s["start"])
        end = timestamp(s["end"]) if s.get("end") else until
        if start >= until or end < since:
            continue
        heartbeat = s.get("heartbeat_interval", 0)
        if not isinstance(heartbeat, int) or not 0 <= heartbeat <= 900:
            heartbeat = 0
        if end > since:
            heartbeats[str(heartbeat)] += 1
        if since <= start < until:
            hours[iso(start.replace(minute=0, second=0, microsecond=0))]["opens"] += 1
            if s.get("end") and end < until:
                durations.append((end-start).total_seconds())
        if s.get("end") and since <= end < until:
            h = hours[iso(end.replace(minute=0, second=0, microsecond=0))]
            reason = s.get("reason", "unknown")
            if reason not in ("closed", "hub_stopped", "pong_timeout", "replaced", "error"):
                reason = "unknown"
            h["closes"] += 1
            h["reasons"][reason] += 1
        h = since.replace(minute=0, second=0, microsecond=0)
        while h < until:
            overlap = max(0, (min(end, until, h+dt.timedelta(hours=1))-max(start, since, h)).total_seconds())
            if overlap:
                hours[iso(h)]["connected_seconds"] += overlap
            h += dt.timedelta(hours=1)
    return {"hourly": [dict(hour=h, **value) for h, value in sorted(hours.items())],
            "mean_completed_in_window_seconds": sum(durations)/len(durations) if durations else None,
            "completed_in_window_count": len(durations), "heartbeat_intervals": dict(heartbeats)}


def read_connections(args, since, until):
    if args.connections_file:
        return json.loads(Path(args.connections_file).read_text())
    container = args.container
    if not container:
        running = subprocess.check_output(["docker", "ps", "--format", "{{.Names}}"], text=True, timeout=15).splitlines()
        matches = [name for name in DATABASE_NAMES if name in running]
        if len(matches) != 1:
            raise RuntimeError("select one installed Postgres container with --container")
        container = matches[0]
    sql = """BEGIN READ ONLY; SET LOCAL statement_timeout='15s';
SELECT coalesce(json_agg(json_build_object('identity',left(encode(sha256(convert_to('persona-traffic','UTF8')||decode('00','hex')||convert_to(device_id,'UTF8')),'hex'),16), 'start',connected_at,'end',disconnected_at,'reason',reason,'heartbeat_interval',heartbeat_interval)), '[]'::json)
FROM device_sessions WHERE connected_at < '%s' AND (disconnected_at IS NULL OR disconnected_at >= '%s'); COMMIT;""" % (iso(until), iso(since))
    cmd = ["docker", "exec", "-i", container, "sh", "-c", 'exec psql -X -U "${POSTGRES_USER:-persona}" -d "${POSTGRES_DB:-$POSTGRES_USER}" -At -v ON_ERROR_STOP=1']
    out = subprocess.run(cmd, input=sql, capture_output=True, text=True, timeout=30)
    if out.returncode:
        raise RuntimeError("read-only device-session query failed")
    return next(json.loads(line) for line in out.stdout.splitlines() if line.startswith("["))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--since", required=True)
    parser.add_argument("--until", required=True)
    parser.add_argument("--identity-file", help="private JSON containing only an opaque 16-hex traffic identity")
    parser.add_argument("--unit", help="installed hub journal unit; auto-detected when omitted")
    parser.add_argument("--container", help="installed Postgres container; auto-detected when unique")
    parser.add_argument("--journal-file", help="existing JSONL export instead of journalctl")
    parser.add_argument("--connections-file", help="existing sanitized connection JSON instead of Docker")
    parser.add_argument("--output", required=True, help="new private JSON file; refuses to overwrite")
    args = parser.parse_args()
    since, until = timestamp(args.since), timestamp(args.until)
    if until <= since or until > dt.datetime.now(dt.timezone.utc):
        raise ValueError("window must have ended; this command never waits")
    identity = None
    if args.identity_file:
        identity = json.loads(Path(args.identity_file).read_text())["identity"]
        if not isinstance(identity, str) or not re.fullmatch(r"[0-9a-f]{16}", identity):
            raise ValueError("identity file must contain a 16-hex anonymous identity, not a token")
    if args.journal_file:
        with Path(args.journal_file).open() as stream:
            records = list(rows(stream, since, until))
    else:
        unit = args.unit
        if not unit:
            for name in SERVICE_NAMES["hub"]:
                candidate = name + ".service"
                state = subprocess.run(["systemctl", "--user", "is-active", candidate], capture_output=True, text=True, timeout=10)
                if state.returncode == 0:
                    unit = candidate
                    break
        if not unit:
            raise RuntimeError("no active hub unit; provide --unit")
        # Allow the final minute to flush; the payload bucket timestamp clips the window.
        text = subprocess.check_output(["journalctl", "--user", "-u", unit, "--since", since.strftime("%Y-%m-%d %H:%M:%S UTC"), "--until", (until+dt.timedelta(minutes=2)).strftime("%Y-%m-%d %H:%M:%S UTC"), "--grep", "traffic minute=", "-o", "json", "--no-pager"], text=True, timeout=120)
        records = list(rows(text.splitlines(), since, until))
    sessions = read_connections(args, since, until)
    report = {"window_start": iso(since), "window_end": iso(until), "window_seconds": (until-since).total_seconds(),
              "full_day": until-since == dt.timedelta(days=1), "all_clients": aggregate(records),
              "all_connections": connection_report(sessions, since, until),
              "limits": "Inference labels are not hardware/process proof; native-default is ambiguous. Bodies are before outer gzip, not radio bytes. Sum received and completed separately; acceptance is not delivery. Final buffered/in-flight events may be lost on process termination."}
    if identity:
        report["target"] = aggregate([r for r in records if r.get("identity") == identity])
        report["target_connections"] = connection_report(sessions, since, until, identity)
    with os.fdopen(os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as output:
        json.dump(report, output, indent=2)
        output.write("\n")
    print("VERDICT: PASS read-only traffic report; full_day=%s; bucket_rows=%d" % (report["full_day"], len(records)))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, RuntimeError, OSError, subprocess.SubprocessError, StopIteration) as error:
        print("VERDICT: FAILED traffic report (%s); no raw records or credentials printed" % type(error).__name__, file=sys.stderr)
        sys.exit(1)
