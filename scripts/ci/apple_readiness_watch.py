"""Observe one CI app-hosted test launch without changing its readiness contract."""

import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import time

import apple_launch_diagnostics as launch

COMMAND_TIMEOUT = 20
POLL_SECONDS = 1
SAMPLE_AFTER = 30
FINISH_BEFORE = 35
MAX_SAMPLE_BYTES = 128 * 1024


def bounded_file(command, path, timeout=COMMAND_TIMEOUT, limit=MAX_SAMPLE_BYTES):
    """Run one bounded read-only command; retain only its bounded diagnostic output."""
    try:
        temporary = path.with_suffix(path.suffix + ".command")
        with temporary.open("wb") as output:
            process = subprocess.Popen(command, stdin=subprocess.DEVNULL, start_new_session=True,
                                       stdout=output, stderr=subprocess.STDOUT)
            try:
                code = process.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
                code = 124
        temporary.replace(path)
        path.with_suffix(path.suffix + ".status.txt").write_text(str(code) + "\n")
        if path.stat().st_size > limit:
            path.unlink()
            path.with_suffix(path.suffix + ".status.txt").write_text("output-limit\n")
        return code
    except (OSError, subprocess.SubprocessError) as error:
        path.with_suffix(path.suffix + ".status.txt").write_text("unavailable: " + type(error).__name__ + "\n")
        return 127


def host_pids(udid, runner=subprocess.run):
    """Match executable paths, never command arguments or another device's app."""
    try:
        result = runner(["ps", "-axo", "pid=,comm="], capture_output=True, text=True, timeout=5)
    except (OSError, subprocess.SubprocessError):
        return []
    if result.returncode:
        return []
    pattern = re.compile(r"^\s*(\d+)\s+(.*/CoreSimulator/Devices/" + re.escape(udid) +
                         r"/[^\n]*/Persona\.app/Persona)\s*$")
    return sorted({int(match.group(1)) for line in result.stdout.splitlines()
                   if (match := pattern.match(line))})


def sample_host(udid, destination, label, runner=subprocess.run):
    pids = host_pids(udid, runner)
    destination.mkdir(parents=True, exist_ok=True)
    (destination / (label + ".json")).write_text(json.dumps({
        "captured_at": datetime.now(timezone.utc).isoformat(), "host_pids": pids,
        "note": "Process presence is not test-bundle or readiness evidence."}, indent=2) + "\n")
    for pid in pids:
        bounded_file(["sample", str(pid), "2", "-file", "/dev/stdout"],
                     destination / (label + f"-pid-{pid}.sample.txt"), timeout=10)
    return pids


def product_evidence(app, test_bundle, destination):
    """Check the disposable app bundle and its executable, without entitlements/source."""
    for index, path in enumerate([app, test_bundle, app.parent / "Frameworks/libXCTestBundleInject.dylib"]):
        bounded_file(["file", str(path)], destination / f"product-{index}-architecture.txt", timeout=5)
    for index, bundle in enumerate([app.parent, test_bundle.parent]):
        bounded_file(["codesign", "--verify", "--verbose=2", str(bundle)],
                     destination / f"product-{index}-bundle-signature.txt", timeout=10)
    for index, path in enumerate([app, test_bundle, app.parent / "Frameworks/libXCTestBundleInject.dylib"]):
        bounded_file(["otool", "-L", str(path)], destination / f"product-{index}-libraries.txt", timeout=10)


def observe(udid, bundle, app, test_bundle, destination, seconds, runner=subprocess.run,
            monotonic=time.monotonic, sleep=time.sleep, scope_root=None):
    launch.identity(udid, bundle)
    if app.name != "Persona" or app.parent.name != "Persona.app" or test_bundle.name != "PersonaTests":
        raise ValueError("unexpected CI product identity")
    destination.mkdir(parents=True, exist_ok=True)
    sampled = False
    host_seen_at = None
    stopped = False
    deadline = monotonic() + seconds
    try:
        product_evidence(app, test_bundle, destination)
        while monotonic() < deadline - FINISH_BEFORE:
            if (destination / "observer.stop").exists():
                stopped = True
                break
            now = monotonic()
            if host_seen_at is None:
                pids = sample_host(udid, destination, "host-first", runner)
                if pids:
                    host_seen_at = now
            elif not sampled and now - host_seen_at >= SAMPLE_AFTER:
                sample_host(udid, destination, "host-after-30s", runner)
                sampled = True
            sleep(min(POLL_SECONDS, max(0, deadline - FINISH_BEFORE - monotonic())))
    except KeyboardInterrupt:
        stopped = True
    try:
        # Preserve a deterministic status and host record even when final
        # collection is interrupted by the outer readiness deadline.
        if not (destination / "host-final.json").exists():
            temporary = destination / "host-final.pending"
            temporary.write_text(json.dumps({"unavailable": "final host collection incomplete; no host-process verdict"}) + "\n")
            temporary.replace(destination / "host-final.json")
        # Sample before Xcode timeout cleanup can shut down the device. If Xcode
        # returned, retain its completed probe separately from this observation.
        sample_host(udid, destination, "host-final", runner)
        bounded_file(["xcrun", "simctl", "list", "runtimes", "-j"],
                     destination / "runtimes-final.json", timeout=10, limit=1024 * 1024)
        now = datetime.now(timezone.utc)
        launch.collect(destination.parent, destination, now=now, scope_root=scope_root)
        (destination / "observer.status.txt").write_text(
            ("xcodebuild-finished" if stopped else "readiness-deadline") +
            "; observation only; no launch cause established\n")
    finally:
        if not (destination / "observer.status.txt").exists():
            (destination / "observer.status.txt").write_text(
                "final collection incomplete; bounded unavailable evidence retained; no launch cause established\n")


def interrupted(signum, _frame):
    raise KeyboardInterrupt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--udid", required=True)
    parser.add_argument("--bundle-id", required=True)
    parser.add_argument("--app", type=Path, required=True)
    parser.add_argument("--test-bundle", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--scope-root", type=Path, required=True)
    parser.add_argument("--seconds", type=float, default=launch.READINESS_SECONDS)
    args = parser.parse_args()
    if not 0 < args.seconds <= launch.READINESS_SECONDS:
        parser.error("observation cannot exceed the readiness deadline")
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, interrupted)
    observe(args.udid, args.bundle_id, args.app, args.test_bundle, args.output, args.seconds,
            scope_root=args.scope_root)


if __name__ == "__main__":
    main()
