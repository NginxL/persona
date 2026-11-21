"""Bound resource evidence for one Apple build, without changing its scheduling."""

import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import time

COMMAND_TIMEOUT = 10
SAMPLE_SECONDS = 15
MAX_PROCESSES = 8
MAX_OUTPUT_BYTES = 64 * 1024
MAX_PERIODIC_SAMPLES = 120
MAX_SECONDS = 3600


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


def processes(runner=subprocess.run):
    """Only compiler names and resource use, never command-line source paths or arguments."""
    result = output(["ps", "-axo", "pid=,ppid=,%cpu=,rss=,comm="], runner=runner)
    entries = []
    for line in result.get("stdout", "").splitlines():
        match = re.match(r"\s*(\d+)\s+(\d+)\s+(\S+)\s+(\d+)\s+(.+)$", line)
        if match and Path(match.group(5)).name in {"swift-frontend", "swiftc", "swift", "clang", "clang++", "xcodebuild"}:
            entries.append({"pid": int(match.group(1)), "ppid": int(match.group(2)),
                            "cpu_percent": match.group(3), "rss_kib": int(match.group(4)),
                            "name": Path(match.group(5)).name})
    entries.sort(key=lambda entry: entry["rss_kib"], reverse=True)
    counts = {}
    for entry in entries:
        counts[entry["name"]] = counts.get(entry["name"], 0) + 1
    return {"counts": counts, "total_rss_kib": sum(entry["rss_kib"] for entry in entries),
            "largest": entries[:MAX_PROCESSES]}


def sample(destination, label, runner=subprocess.run, *, final=False):
    if final:
        # A bounded marker exists even if a collector must be reaped.
        path = destination / (label + ".json")
        pending = {"captured_at": datetime.now(timezone.utc).isoformat(),
                   "unavailable": "final collection incomplete; no pressure verdict"}
        temporary = destination / (label + ".pending")
        temporary.write_text(json.dumps(pending, indent=2) + "\n")
        temporary.replace(path)
    memory = output(["memory_pressure"], runner=runner)
    pages = output(["vm_stat"], runner=runner)
    swap = output(["sysctl", "vm.swapusage"], runner=runner)
    process = processes(runner)
    # Keep only the evidence needed to compare resource pressure over time. Full
    # pressure/process logs make a 30-minute build produce tens of megabytes.
    record = {"captured_at": datetime.now(timezone.utc).isoformat(),
              "memory_free_percent": next((line.strip() for line in memory.get("stdout", "").splitlines()
                                           if "memory free percentage" in line), None),
              "pages_used_by_compressor": next((line.strip() for line in pages.get("stdout", "").splitlines()
                                               if "occupied by compressor" in line), None),
              "swap": swap.get("stdout", "").strip(),
              "processes": process}
    if memory.get("exit") != 0 or pages.get("exit") != 0 or swap.get("exit") != 0:
        record["unavailable"] = "resource command failed; no pressure verdict"
    path = destination / (label + ".json")
    temporary = destination / (label + ".pending")
    temporary.write_text(json.dumps(record, indent=2) + "\n")
    temporary.replace(path)
    return record


def observe(destination, seconds, stop_path, runner=subprocess.run, monotonic=time.monotonic, sleep=time.sleep):
    destination.mkdir(parents=True, exist_ok=True)
    deadline = monotonic() + seconds
    (destination / "host-metadata.json").write_text(json.dumps(metadata(runner), indent=2) + "\n")
    count = 0
    try:
        while not stop_path.exists() and monotonic() < deadline and count < MAX_PERIODIC_SAMPLES:
            sample(destination, f"sample-{count:03d}", runner)
            count += 1
            sleep(max(0, min(SAMPLE_SECONDS, deadline - monotonic())))
        sample(destination, "final", runner, final=True)
        (destination / "observer.status.txt").write_text(
            f"{count} periodic samples; resource evidence only, no scheduling change or root-cause verdict\n")
    except KeyboardInterrupt:
        sample(destination, "final", runner, final=True)
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
    args = parser.parse_args()
    if not 0 < args.seconds <= MAX_SECONDS:
        parser.error("resource observation must fit within the selected lane's build deadline")
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, interrupted)
    observe(args.output, args.seconds, args.stop)


if __name__ == "__main__":
    main()
