"""Bound resource evidence for one Apple build, without changing its scheduling."""

import argparse
from datetime import datetime, timezone
import json
import math
import os
from pathlib import Path
import re
import selectors
import shlex
import signal
import stat
import subprocess
import time

COMMAND_TIMEOUT = 10
SAMPLE_SECONDS = 15
MAX_PROCESSES = 8
MAX_OUTPUT_BYTES = 64 * 1024
MAX_PERIODIC_SAMPLES = 120
MAX_SECONDS = 3600
MAX_FRONTEND_LIFETIMES = 512
MAX_FINAL_FRONTEND_RECORDS = 16
MAX_FINAL_JSON_BYTES = 16 * 1024
MAX_SAMPLE_JSON_BYTES = 64 * 1024
MAX_ACTIVITY_LOGS = 4
MAX_ACTIVITY_LOG_CANDIDATES = 64
SAFE_IDENTITY = re.compile(r"[^A-Za-z0-9_.-]+")
COMPILER_NAMES = {"swift-frontend", "swiftc", "swift", "clang", "clang++", "xcodebuild"}


def output(command, timeout=COMMAND_TIMEOUT, limit=MAX_OUTPUT_BYTES, runner=subprocess.run):
    try:
        result = runner(command, capture_output=True, text=True, timeout=timeout)
        if len(result.stdout) > limit:
            return {"command": command, "unavailable": "output-limit"}
        return {"command": command, "exit": result.returncode,
                "stdout": result.stdout, "stderr": result.stderr[:4096]}
    except (OSError, subprocess.SubprocessError) as error:
        return {"command": command, "unavailable": type(error).__name__}


def metadata(runner=subprocess.run):
    return {
        "cpu": output(["sysctl", "-n", "hw.ncpu", "hw.memsize"], runner=runner),
        "limits": output(["/bin/sh", "-c", "ulimit -a"], runner=runner),
        "swap": output(["sysctl", "vm.swapusage"], runner=runner),
        "disk": output(["df", "-h", "."], runner=runner),
    }


def safe_identity(value, limit=128):
    """Only an argv token's bounded basename; no directories or arbitrary content."""
    if not isinstance(value, str) or "/" not in value and "\\" not in value:
        return "redacted"
    name = re.split(r"[/\\]", value)[-1]
    if len(name) > limit or name.startswith("."):
        return "redacted"
    name = SAFE_IDENTITY.sub("_", name).strip("._")
    if not name or not name[-1].isalnum():
        return "redacted"
    return name


def swift_frontend_identity(command):
    """Retain selected identities only when unquoted argv has unambiguous boundaries."""
    unavailable = {"target_module": "unavailable", "primary_source": "unavailable", "output": "unavailable"}
    if not isinstance(command, str) or len(command) > MAX_OUTPUT_BYTES:
        return unavailable
    # ps args is not shell quoted. Interpreting a space in a source path as an
    # argument separator would invent a basename or even a module identity.
    if re.search(r"(?:^|\s)(?:-primary-file|-o)\s+(?:[^'\"/-]\S*\s+|/[^\s'\"]*\s+(?=[^'\"/-]))", command):
        return unavailable
    try:
        argv = shlex.split(command)
    except ValueError:
        return {"target_module": "redacted", "primary_source": "redacted", "output": "redacted"}
    module = output_path = primary = None
    for index, argument in enumerate(argv[1:], 1):
        if argument not in {"-module-name", "-o", "-primary-file"} or index + 1 >= len(argv):
            continue
        value = argv[index + 1]
        if argument == "-module-name":
            module = value if re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]{0,79}", value) else "redacted"
        elif argument == "-o":
            output_path = safe_identity(value)
        else:
            primary = safe_identity(value)
    return {"target_module": module or "unknown",
            "primary_source": primary or "unknown", "output": output_path or "unknown"}


def duration(value):
    # Darwin ps emits [[dd-]hh:]mm:ss[.fraction]. Bound every field before
    # conversion so hostile counters cannot overflow the diagnostic collector.
    match = re.fullmatch(r"(?:([0-9]{1,18})-)?(?:([0-9]{1,18}):)?([0-9]{1,18}):([0-9]{1,18}(?:\.[0-9]{1,18})?)", value)
    if match is None:
        return None
    try:
        days, hours, minutes = (int(field or 0) for field in match.group(1, 2, 3))
        seconds = ((days * 24 + hours) * 60 + minutes) * 60 + float(match.group(4))
    except (ValueError, OverflowError):
        return None
    return seconds if math.isfinite(seconds) else None


def valid_duration(value):
    """Reject malformed/non-finite ps counters before frontend arithmetic."""
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)


def process_snapshot(rows):
    entries = []
    frontend_candidates = []
    candidate_truncated = False
    seen_candidates = set()
    for row in rows:
        pid, ppid, cpu, rss, elapsed, cpu_time, start, command = row
        # ps does not quote argv. Prefer the executable token, while retaining
        # support for an unquoted toolchain directory containing spaces.
        executable = Path(command.split()[0]).name if command.split() else ""
        suffix_name = Path(command).name.split(" ", 1)[0]
        name = executable if executable in COMPILER_NAMES else suffix_name
        if name not in COMPILER_NAMES:
            continue
        entry = {"pid": pid, "ppid": ppid, "cpu_percent": cpu,
                 "rss_kib": rss, "name": name}
        if name == "swift-frontend":
            entry.update({"cpu_seconds": cpu_time, "elapsed_seconds": elapsed,
                          "process_start": start, "process_start_status": "known" if start is not None else "uncertain",
                          **swift_frontend_identity(command)})
            key = (pid, start)
            if key not in seen_candidates:
                if len(frontend_candidates) < MAX_FRONTEND_LIFETIMES:
                    frontend_candidates.append(entry)
                    seen_candidates.add(key)
                else:
                    candidate_truncated = True
        entries.append(entry)
    entries.sort(key=lambda entry: entry["rss_kib"], reverse=True)
    counts = {}
    for entry in entries:
        counts[entry["name"]] = counts.get(entry["name"], 0) + 1
    largest = entries[:MAX_PROCESSES]
    return {"counts": counts, "total_rss_kib": sum(entry["rss_kib"] for entry in entries),
            "largest": largest, "frontend_candidates": frontend_candidates,
            "candidate_truncated": candidate_truncated}


def process_start(value):
    """A process start token, not a source/module identity or a wall-clock export."""
    if re.fullmatch(r"[A-Za-z]{3} [A-Za-z]{3} [ 0-9]\d \d\d:\d\d:\d\d \d{4}", value):
        return value
    return None


def _process_rows_with_status(text, with_start):
    """Internal row parser retains whether compiler counters were invalid."""
    rows = []
    invalid_compiler_durations = False
    valid_rows = 0
    fields = r"\s*(\d+)\s+(\d+)\s+(\S+)\s+(\d+)\s+(\S+)\s+(\S+)\s+"
    pattern = re.compile(fields + (r"(.{24})(?:\s+|$)(.*)$" if with_start else r"(.*)$"))
    for line in text.splitlines():
        match = pattern.match(line)
        if not match:
            continue
        if with_start:
            start, command = process_start(match.group(7)), match.group(8)
            if start is None:
                continue
        else:
            start, command = None, match.group(7)
        valid_rows += 1
        elapsed, cpu_time = duration(match.group(5)), duration(match.group(6))
        executable = Path(command.split()[0]).name if command.split() else ""
        suffix_name = Path(command).name.split(" ", 1)[0]
        name = executable if executable in COMPILER_NAMES else suffix_name
        if name == "swift-frontend" and not (valid_duration(elapsed) and valid_duration(cpu_time)):
            invalid_compiler_durations = True
            continue
        rows.append((int(match.group(1)), int(match.group(2)), match.group(3),
                     int(match.group(4)), elapsed, cpu_time, start, command))
    return rows, invalid_compiler_durations, valid_rows


def process_rows(text, with_start):
    """Accept the bounded ps formats with and without optional start identity."""
    return _process_rows_with_status(text, with_start)[0]


def process_output(command, with_start, popen=subprocess.Popen):
    """Stream ps, retaining only bounded compiler summaries and no raw diagnostics."""
    fields = "pid=,ppid=,%cpu=,rss=,etime=,time=,"
    selected = command[:2] + [fields + ("lstart=," if with_start else "") + "ucomm=,args="]
    try:
        process = popen(selected, stdin=subprocess.DEVNULL,
                        stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    except (OSError, subprocess.SubprocessError) as error:
        return {"command": selected, "unavailable": type(error).__name__}
    deadline = time.monotonic() + COMMAND_TIMEOUT
    pattern = re.compile(rb"\s*(\d+)\s+(\d+)\s+(\S+)\s+(\d+)\s+(\S+)\s+(\S+)"
                         + (rb"\s+(.{24})" if with_start else b"")
                         + rb"\s+(\S+)(?:\s+(.*))?$")
    summary = process_snapshot([])
    candidates = []
    candidate_keys = set()
    pending = bytearray()
    truncated = False
    saw_output = False
    valid_rows = 0
    output_lines = 0
    invalid_compiler_durations = False

    def consume(raw):
        nonlocal valid_rows, output_lines, invalid_compiler_durations
        output_lines += 1
        match = pattern.match(raw)
        if not match:
            return
        name_index = 8 if with_start else 7
        start = None
        if with_start:
            start = process_start(match.group(7).decode("ascii", "replace"))
            if start is None:
                return
        valid_rows += 1
        name = match.group(name_index).decode("ascii", "replace")
        if name not in COMPILER_NAMES:
            return
        entry = {"pid": int(match.group(1)), "ppid": int(match.group(2)),
                 "cpu_percent": match.group(3).decode("ascii", "replace"),
                 "rss_kib": int(match.group(4)), "name": name}
        if name == "swift-frontend":
            cpu_seconds = duration(match.group(6).decode("ascii", "replace"))
            elapsed_seconds = duration(match.group(5).decode("ascii", "replace"))
            if not (valid_duration(elapsed_seconds) and valid_duration(cpu_seconds)):
                invalid_compiler_durations = True
                return
            if truncated:
                identity = {"target_module": "unavailable", "primary_source": "unavailable",
                            "output": "unavailable"}
            else:
                argv = match.group(name_index + 1) or b""
                identity = swift_frontend_identity(argv.decode("utf-8", "replace"))
            entry.update({"cpu_seconds": cpu_seconds,
                          "elapsed_seconds": elapsed_seconds,
                          "process_start": start,
                          "process_start_status": "known" if start is not None else "uncertain",
                          **identity})
            key = (entry["pid"], start)
            if key not in candidate_keys:
                if len(candidates) < MAX_FRONTEND_LIFETIMES:
                    candidates.append(entry)
                    candidate_keys.add(key)
                else:
                    summary["candidate_truncated"] = True
        summary["counts"][name] = summary["counts"].get(name, 0) + 1
        summary["total_rss_kib"] += entry["rss_kib"]
        summary["largest"].append(entry)
        summary["largest"].sort(key=lambda item: item["rss_kib"], reverse=True)
        del summary["largest"][MAX_PROCESSES:]

    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ, "stdout")
            selector.register(process.stderr, selectors.EVENT_READ, "stderr")
            while selector.get_map():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise subprocess.TimeoutExpired(selected, COMMAND_TIMEOUT)
                for key, _ in selector.select(remaining):
                    chunk = os.read(key.fd, 8192)
                    if not chunk:
                        selector.unregister(key.fileobj)
                        continue
                    if key.data == "stderr":
                        continue  # Never retain command diagnostics or raw argv.
                    while chunk:
                        newline = chunk.find(b"\n")
                        piece = chunk if newline < 0 else chunk[:newline]
                        if piece.strip():
                            saw_output = True
                        room = max(0, MAX_OUTPUT_BYTES - len(pending))
                        pending.extend(piece[:room])
                        if len(piece) > room:
                            truncated = True
                        if newline < 0:
                            break
                        consume(pending)
                        pending.clear()
                        truncated = False
                        chunk = chunk[newline + 1:]
        if pending:
            consume(pending)
        code = process.wait(timeout=max(0, deadline - time.monotonic()))
        if code:
            return {"command": selected, "unavailable": "process-command-failed"}
        return {"command": selected, "exit": 0, "summary": summary,
                "frontend_candidates": candidates,
                "saw_output": saw_output, "valid_rows": valid_rows, "output_lines": output_lines,
                "invalid_compiler_durations": invalid_compiler_durations}
    except (OSError, subprocess.SubprocessError) as error:
        return {"command": selected, "unavailable": type(error).__name__}
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
        process.stdout.close()
        process.stderr.close()


def stream_processes(popen):
    """Retry a failed or legacy lstart listing once with the no-start format."""
    def usable(result):
        if result.get("exit") != 0 or result["invalid_compiler_durations"]:
            return False
        return result["valid_rows"] == result["output_lines"] and result["saw_output"]

    def unavailable(result):
        reason = result.get("unavailable", "invalid-process-output")
        return {"counts": {}, "total_rss_kib": 0, "largest": [], "frontend_candidates": [],
                "candidate_truncated": False, "unavailable": reason,
                "process_start_status": "uncertain"}

    result = process_output(["ps", "-axo"], True, popen)
    if usable(result):
        return result["summary"] | {"frontend_candidates": result["frontend_candidates"]}
    if result.get("unavailable") not in (None, "process-command-failed"):
        return unavailable(result)
    if result.get("invalid_compiler_durations"):
        return unavailable({"unavailable": "invalid-process-duration"})
    result = process_output(["ps", "-axo"], False, popen)
    if result.get("invalid_compiler_durations"):
        result["unavailable"] = "invalid-process-duration"
        result["frontend_candidates"] = []
    if usable(result):
        return result["summary"] | {"frontend_candidates": result["frontend_candidates"]}
    return unavailable(result)


def processes(runner=subprocess.run, *, popen=None):
    """Only bounded resource summaries; frontend identities are selected from argv in memory."""
    if popen is not None or runner is subprocess.run:
        return stream_processes(popen if popen is not None else subprocess.Popen)
    command = ["ps", "-axo", "pid=,ppid=,%cpu=,rss=,etime=,time=,lstart=,args="]
    result = output(command, runner=runner)
    with_start = result.get("exit") == 0
    output_lines = len(result.get("stdout", "").splitlines())
    rows, invalid_durations, valid_rows = (_process_rows_with_status(result.get("stdout", ""), True)
                                          if with_start else ([], False, 0))
    unavailable = result.get("unavailable")
    if with_start and (valid_rows != output_lines or invalid_durations or not result.get("stdout", "").strip()):
        rows = []
        if invalid_durations:
            unavailable = "invalid-process-duration"
        else:
            # A successful lstart response with any missing or invalid start
            # needs fresh no-start evidence, not a partial known identity set.
            command = ["ps", "-axo", "pid=,ppid=,%cpu=,rss=,etime=,time=,args="]
            result = output(command, runner=runner)
            with_start = False
            if result.get("exit") != 0:
                unavailable = result.get("unavailable", "process-command-failed")
                result["unavailable"] = unavailable
            else:
                rows, invalid_durations, valid_rows = _process_rows_with_status(result.get("stdout", ""), False)
                output_lines = len(result.get("stdout", "").splitlines())
                if invalid_durations:
                    unavailable = "invalid-process-duration"
                elif result.get("stdout", "").strip() and valid_rows != output_lines:
                    unavailable = "invalid-process-output"
                if unavailable:
                    rows = []
            if unavailable:
                result["unavailable"] = unavailable
    elif not with_start and "unavailable" not in result and not unavailable:
        if result.get("exit", 0) != 0:
            unavailable = "process-command-failed"
        else:
            command = ["ps", "-axo", "pid=,ppid=,%cpu=,rss=,etime=,time=,args="]
            result = output(command, runner=runner)
            if result.get("exit") != 0:
                unavailable = result.get("unavailable", "process-command-failed")
                rows = []
            else:
                rows, invalid_durations, valid_rows = _process_rows_with_status(result.get("stdout", ""), False)
                output_lines = len(result.get("stdout", "").splitlines())
                if invalid_durations:
                    unavailable = "invalid-process-duration"
                elif result.get("stdout", "").strip() and valid_rows != output_lines:
                    unavailable = "invalid-process-output"
                if unavailable:
                    rows = []
    summary = process_snapshot(rows)
    if unavailable:
        summary.update({"counts": {}, "total_rss_kib": 0, "largest": [],
                        "unavailable": unavailable, "process_start_status": "uncertain"})
    return summary


def new_lifetime(entry, now, *, reused=False):
    return {"pid": entry["pid"], "process_start": entry.get("process_start"),
            "process_start_status": entry.get("process_start_status", "uncertain"),
            "first_seen_monotonic": now, "last_seen_monotonic": now,
            "first_cpu_seconds": entry["cpu_seconds"], "last_cpu_seconds": entry["cpu_seconds"],
            "first_elapsed_seconds": entry["elapsed_seconds"], "last_elapsed_seconds": entry["elapsed_seconds"],
            "sample_count": 1, "first_identity": {key: entry[key] for key in ("target_module", "primary_source", "output")},
            "last_identity": {key: entry[key] for key in ("target_module", "primary_source", "output")},
            "identity_changes": 0, "duplicate_pid_in_sample": False, "possible_pid_reuse": reused,
            "continuity": "known" if entry.get("process_start") is not None else "uncertain"}


def track_frontends(history, entries, now):
    """Track distinct start identities; source changes alone do not imply a restart."""
    seen = set()
    for entry in entries:
        if entry["name"] != "swift-frontend":
            continue
        if not (valid_duration(entry.get("cpu_seconds")) and valid_duration(entry.get("elapsed_seconds"))):
            continue  # Invalid counters never enter PID-reuse arithmetic or lifetime evidence.
        start = entry.get("process_start")
        key = (entry["pid"], start)
        current = history.get(key)
        if entry["pid"] in seen:
            # A duplicate PID never supplies a new lifetime, even with another
            # reported start: the first observation is the only reliable one.
            for instance in history.values():
                if (isinstance(instance, dict) and instance["pid"] == entry["pid"]
                        and instance["last_seen_monotonic"] == now):
                    instance["duplicate_pid_in_sample"] = True
                    instance["continuity"] = "uncertain"
                    break
            continue
        seen.add(entry["pid"])
        reused = (current is not None
                  and (entry["cpu_seconds"] < current["last_cpu_seconds"]
                       or entry["elapsed_seconds"] < current["last_elapsed_seconds"]))
        if reused or (current is not None and start is None and entry["cpu_seconds"] < current["last_cpu_seconds"]):
            if len(history) >= MAX_FRONTEND_LIFETIMES:
                history["truncated"] = True
                continue
            # Only a resource reset can separate an unknown-start observation;
            # retain the prior record before adding the new uncertain instance.
            key = (entry["pid"], start, now, entry["cpu_seconds"])
            current = None
            reused = True
            history[key] = new_lifetime(entry, now, reused=reused)
            continue
        if current is None:
            if len(history) >= MAX_FRONTEND_LIFETIMES:
                history["truncated"] = True
                continue
            history[key] = new_lifetime(entry, now, reused=reused)
        else:
            current["last_seen_monotonic"] = now
            current["last_cpu_seconds"] = entry["cpu_seconds"]
            current["last_elapsed_seconds"] = entry["elapsed_seconds"]
            current["last_identity"] = {key: entry[key] for key in ("target_module", "primary_source", "output")}
            current["sample_count"] += 1
            if current["first_identity"] != current["last_identity"]:
                current["identity_changes"] += 1
            if start is None:
                current["continuity"] = "uncertain"
    return history


def final_frontend_history(history):
    """Keep a small deterministic final summary, never every in-memory PID."""
    instances = [entry for key, entry in history.items() if key != "truncated"]
    instances.sort(key=lambda entry: (entry["pid"], entry["process_start"] or "",
                                      entry["first_seen_monotonic"], entry["first_cpu_seconds"]))
    retained = instances[:MAX_FINAL_FRONTEND_RECORDS]
    omitted = len(instances) - len(retained) + int(bool(history.get("truncated")))
    return {"records": retained, "status": "truncated" if omitted else "complete",
            "omitted_records": omitted, "max_records": MAX_FINAL_FRONTEND_RECORDS,
            "observed_records": len(instances)}


def open_directory(path, *, parent_fd=None):
    """Open one explicit directory without following its symbolic link."""
    try:
        return os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent_fd)
    except OSError:
        return None


def open_build_logs(derived_data):
    if not isinstance(derived_data, Path) or not derived_data.is_absolute():
        return None
    root = open_directory(derived_data)
    if root is None:
        return None
    try:
        logs = open_directory("Logs", parent_fd=root)
        if logs is None:
            return None
        try:
            return open_directory("Build", parent_fd=logs)
        finally:
            os.close(logs)
    finally:
        os.close(root)


def list_activity_logs(directory):
    """Allowlist existing names from an already-open Build directory."""
    names = sorted(os.listdir(directory))[:MAX_ACTIVITY_LOG_CANDIDATES]
    paths = []
    for name in names:
        try:
            info = os.stat(name, dir_fd=directory, follow_symlinks=False)
        except OSError:
            continue
        if (Path(name).name == name and name.endswith(".xcactivitylog")
                and len(name) <= 128 and stat.S_ISREG(info.st_mode)):
            paths.append({"name": name, "size_bytes": info.st_size})
            if len(paths) == MAX_ACTIVITY_LOGS:
                break
    return paths


def activity_log_paths(derived_data):
    """Allowlist existing build-log names, never export host paths."""
    directory = open_build_logs(derived_data)
    if directory is None:
        return []
    try:
        return list_activity_logs(directory)
    finally:
        os.close(directory)


def encoded_json(record):
    return (json.dumps(record, indent=2) + "\n").encode("utf-8")


def bounded_record(record, limit):
    """Reduce only optional history before writing; resource samples stay intact."""
    def unavailable(reason):
        return {"unavailable": reason, "status": "unavailable", "byte_budget": limit}

    try:
        history = record.get("swift_frontend_lifetimes")
        if isinstance(history, dict):
            while history.get("records") and len(encoded_json(record)) > limit:
                history["records"].pop()
                history["omitted_records"] += 1
                history["status"] = "truncated"
        if len(encoded_json(record)) > limit:
            return unavailable("diagnostic JSON exceeded its byte budget; evidence not serialized")
        return record
    except (TypeError, ValueError):
        return unavailable("diagnostic JSON was not serializable within its byte budget; evidence not serialized")


def write_json(destination, label, record):
    """Check the complete bytes before the only atomic replacement."""
    limit = MAX_FINAL_JSON_BYTES if label == "final" else MAX_SAMPLE_JSON_BYTES
    bounded = bounded_record(record, limit)
    data = encoded_json(bounded)
    if len(data) > limit:
        raise ValueError("bounded diagnostic JSON exceeds its byte budget")
    path = destination / (label + ".json")
    temporary = destination / (label + ".pending")
    temporary.write_bytes(data)
    temporary.replace(path)
    return bounded


def sample(destination, label, runner=subprocess.run, *, final=False, derived_data=None,
           frontend_history=None, monotonic=time.monotonic, popen=None):
    if final:
        # Publish accumulated evidence before any slow collector. If the normal
        # stop drain reaps this observer, the bounded history already exists.
        pending = {"captured_at": datetime.now(timezone.utc).isoformat(),
                   "unavailable": "final collection incomplete; no pressure verdict"}
        if frontend_history is not None:
            pending["swift_frontend_lifetimes"] = final_frontend_history(frontend_history)
        write_json(destination, label, pending)
    memory = output(["memory_pressure"], runner=runner)
    pages = output(["vm_stat"], runner=runner)
    swap = output(["sysctl", "vm.swapusage"], runner=runner)
    process = processes(runner, popen=popen)
    candidates = process.pop("frontend_candidates")
    candidate_truncated = process.pop("candidate_truncated")
    if frontend_history is not None:
        track_frontends(frontend_history, candidates, monotonic())
        if candidate_truncated or process.get("unavailable"):
            frontend_history["truncated"] = True
    # Keep only the evidence needed to compare resource pressure over time. Full
    # pressure/process logs make a 30-minute build produce tens of megabytes.
    record = {"captured_at": datetime.now(timezone.utc).isoformat(),
              "memory_free_percent": next((line.strip() for line in memory.get("stdout", "").splitlines()
                                           if "memory free percentage" in line), None),
              "pages_used_by_compressor": next((line.strip() for line in pages.get("stdout", "").splitlines()
                                               if "occupied by compressor" in line), None),
              "swap": swap.get("stdout", "").strip(),
              "processes": process,
              "xcactivitylogs": activity_log_paths(derived_data)}
    if memory.get("exit") != 0 or pages.get("exit") != 0 or swap.get("exit") != 0:
        record["unavailable"] = "resource command failed; no pressure verdict"
    if process.get("unavailable"):
        record["unavailable"] = "process collection failed; no compiler or pressure verdict"
    if final and frontend_history is not None:
        record["swift_frontend_lifetimes"] = final_frontend_history(frontend_history)
    return write_json(destination, label, record)


def observe(destination, seconds, stop_path, runner=subprocess.run, monotonic=time.monotonic, sleep=time.sleep,
            derived_data=None):
    destination.mkdir(parents=True, exist_ok=True)
    deadline = monotonic() + seconds
    (destination / "host-metadata.json").write_text(json.dumps(metadata(runner), indent=2) + "\n")
    frontend_history = {}
    count = 0
    try:
        while not stop_path.exists() and monotonic() < deadline and count < MAX_PERIODIC_SAMPLES:
            sample(destination, f"sample-{count:03d}", runner, derived_data=derived_data,
                   frontend_history=frontend_history, monotonic=monotonic)
            count += 1
            sleep(max(0, min(SAMPLE_SECONDS, deadline - monotonic())))
        sample(destination, "final", runner, final=True, derived_data=derived_data,
               frontend_history=frontend_history, monotonic=monotonic)
        (destination / "observer.status.txt").write_text(
            f"{count} periodic samples; resource evidence only, no scheduling change or root-cause verdict\n")
    except KeyboardInterrupt:
        sample(destination, "final", runner, final=True, derived_data=derived_data,
               frontend_history=frontend_history, monotonic=monotonic)
        (destination / "observer.status.txt").write_text("stopped; final sample retained\n")
    finally:
        if not (destination / "observer.status.txt").exists():
            (destination / "observer.status.txt").write_text(
                "final collection incomplete; bounded evidence retained, no pressure verdict\n")


def interrupted(signum, _frame):
    raise KeyboardInterrupt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--stop", type=Path, required=True)
    parser.add_argument("--seconds", type=float, required=True)
    parser.add_argument("--derived-data", type=Path)
    args = parser.parse_args()
    if args.derived_data is not None and not args.derived_data.is_absolute():
        parser.error("DerivedData must be the absolute path already selected by the build")
    if not 0 < args.seconds <= MAX_SECONDS:
        parser.error("resource observation must fit within the selected lane's build deadline")
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, interrupted)
    observe(args.output, args.seconds, args.stop, derived_data=args.derived_data)


if __name__ == "__main__":
    main()
