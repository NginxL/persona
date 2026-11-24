"""Observe one CI app-hosted test launch without changing its readiness contract."""

import argparse
from datetime import datetime, timedelta, timezone
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
    process = None
    code = 127
    try:
        launch.publish(path.with_suffix(path.suffix + ".status.txt"), "incomplete\n")
        temporary = path.with_suffix(path.suffix + ".command")
        with temporary.open("wb") as output:
            # The shell owns the observer's group. A separate session would let
            # this child escape the bounded cleanup while its parent is killed.
            process = subprocess.Popen(command, stdin=subprocess.DEVNULL,
                                       stdout=output, stderr=subprocess.STDOUT)
            try:
                code = process.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                # The command shares this process group; do not signal the
                # observer itself when only this command reaches its deadline.
                process.kill()
                process.wait()
                code = 124
        if code == 124 and temporary.stat().st_size == 0:
            temporary.write_text("unavailable: command timed out\n")
        temporary.replace(path)
        launch.publish(path.with_suffix(path.suffix + ".status.txt"), str(code) + "\n")
        if path.stat().st_size > limit:
            path.unlink()
            launch.publish_status(path.with_suffix(path.suffix + ".status.txt"), "output-limit\n")
        return code
    except (OSError, subprocess.SubprocessError) as error:
        launch.publish_status(path.with_suffix(path.suffix + ".status.txt"),
                              "unavailable: " + type(error).__name__ + "\n")
        return 127
    finally:
        if process is not None and process.poll() is None:
            # Reap an interrupted child here; an outer SIGKILL remains bounded
            # by the shell's kill of the whole observer process group.
            process.kill()
            process.wait()


def host_pids(udid, runner=subprocess.run):
    """Match executable paths, never command arguments or another device's app."""
    try:
        result = runner(["ps", "-axo", "pid=,comm="], capture_output=True, text=True, timeout=5)
    except subprocess.TimeoutExpired:
        return {"query_status": "query-timeout", "host_pids": []}
    except (OSError, subprocess.SubprocessError):
        return {"query_status": "query-failed", "host_pids": []}
    if result.returncode:
        return {"query_status": "query-failed", "host_pids": []}
    pattern = re.compile(r"^\s*(\d+)\s+(.*/CoreSimulator/Devices/" + re.escape(udid) +
                         r"/[^\n]*/Persona\.app/Persona)\s*$")
    pids = sorted({int(match.group(1)) for line in result.stdout.splitlines()
                   if (match := pattern.match(line))})
    return {"query_status": "completed", "host_pids": pids}


def start_live_log(scope_root, destination, deadline=None):
    """A scope failure must not remove the existing final collection."""
    try:
        return launch.start_live_log(scope_root, destination, deadline=deadline,
                                     regular=destination.name == "live-regular-readiness")
    except (OSError, subprocess.SubprocessError, launch.ScopeValidationError) as error:
        destination.mkdir(parents=True, exist_ok=True)
        category = error.category if isinstance(error, launch.ScopeValidationError) else "startup-failure"
        launch.publish(destination / "live-launch.status.txt",
                       f"unavailable: live-log-{category}; no launch cause established\n")
        return None


def stop_live_log(live):
    if live is None:
        return
    try:
        live.stop()
    except (OSError, subprocess.SubprocessError, TimeoutError):
        try:
            live.fail("stop-failure")
        except (OSError, subprocess.SubprocessError, launch.StatusPublicationError):
            # Live status publication cannot replace or skip the final collector.
            pass


def correlate_live(live, observation):
    if live is not None and observation["host_pids"]:
        live.correlate(observation["host_pids"])


def sample_host(udid, destination, label, runner=subprocess.run):
    observation = host_pids(udid, runner)
    pids = observation["host_pids"]
    destination.mkdir(parents=True, exist_ok=True)
    launch.publish(destination / (label + ".json"), json.dumps({
        "captured_at": datetime.now(timezone.utc).isoformat(), **observation,
        "note": "Process presence is not test-bundle or readiness evidence."}, indent=2) + "\n")
    for pid in pids:
        bounded_file(["sample", str(pid), "2", "-file", "/dev/stdout"],
                     destination / (label + f"-pid-{pid}.sample.txt"), timeout=10)
    return observation


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
    # An interrupted collector must never expose a successful final status or
    # claim unavailable evidence without an explicit terminal reason.
    launch.publish(destination / "observer.status.txt",
                   "collection incomplete; bounded unavailable evidence retained; no launch cause established\n")
    launch.publish(destination / "host-final.json",
                   json.dumps({"unavailable": "observer did not complete; no host-process verdict"}) + "\n")
    for product in range(3):
        for kind in ("architecture", "libraries"):
            path = destination / f"product-{product}-{kind}.txt"
            launch.publish(path, "unavailable: product collection incomplete\n")
            launch.publish(path.with_suffix(path.suffix + ".status.txt"), "incomplete\n")
    for product in range(2):
        path = destination / f"product-{product}-bundle-signature.txt"
        launch.publish(path, "unavailable: bundle signature collection incomplete\n")
        launch.publish(path.with_suffix(path.suffix + ".status.txt"), "incomplete\n")
    sampled = False
    host_seen_at = None
    deadline = monotonic() + seconds
    live = None
    first = None
    stopped = (destination / "observer.stop").exists()
    try:
        try:
            if not stopped:
                live = start_live_log(scope_root, destination,
                                      deadline=datetime.now(timezone.utc) + timedelta(seconds=seconds))
                first = sample_host(udid, destination, "host-first", runner)
                correlate_live(live, first)
            product_evidence(app, test_bundle, destination)
            launch.publish(destination / "product-ready", "bounded product phase finished\n")
            if (destination / "observer.stop").exists():
                stopped = True
            while not stopped and monotonic() < deadline - FINISH_BEFORE:
                if (destination / "observer.stop").exists():
                    stopped = True
                    break
                now = monotonic()
                if host_seen_at is not None and not sampled and now - host_seen_at >= SAMPLE_AFTER:
                    observation = sample_host(udid, destination, "host-after-30s", runner)
                    correlate_live(live, observation)
                    sampled = True
                elif host_seen_at is None:
                    observation = first if first is not None else sample_host(
                        udid, destination, "host-first", runner)
                    first = None
                    correlate_live(live, observation)
                    if observation["host_pids"]:
                        host_seen_at = now
                sleep(min(POLL_SECONDS, max(0, deadline - FINISH_BEFORE - monotonic())))
        except KeyboardInterrupt:
            stopped = True
        final_observation = sample_host(udid, destination, "host-final", runner)
        correlate_live(live, final_observation)
        # Sample before Xcode timeout cleanup can shut down the device. If Xcode
        # returned, retain its completed probe separately from this observation.
        bounded_file(["xcrun", "simctl", "list", "runtimes", "-j"],
                     destination / "runtimes-final.json", timeout=10, limit=1024 * 1024)
        now = datetime.now(timezone.utc)
        launch.collect(destination.parent, destination, now=now, scope_root=scope_root)
        launch.publish(destination / "observer.status.txt",
                       ("xcodebuild-finished" if stopped else "readiness-deadline") +
                       "; observation only; no launch cause established\n")
    finally:
        stop_live_log(live)
        if not (destination / "observer.status.txt").exists():
            launch.publish(destination / "observer.status.txt",
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
