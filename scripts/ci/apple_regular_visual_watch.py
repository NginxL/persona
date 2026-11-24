"""Observe only the regular visual app-host launch inside its existing 600-second step."""

import argparse
from datetime import datetime, timedelta, timezone
import json
import re
import selectors
import signal
import subprocess
from pathlib import Path

import apple_launch_diagnostics as launch
import apple_readiness_watch as watch

VISUAL_SECONDS = launch.VISUAL_SECONDS
SAMPLE_SECONDS = 15
SYSTEM_AFTER = 60
FINISH_BEFORE = 35
VISUAL_PROCESSES = ("Persona", "debugserver", "launchd_sim")


def scoped_processes(udid, runner=None, owned_debuggers=None):
    """Return owned host/debugger metadata, never command arguments or environment.

    A debugger can precede its Persona target under this simulator's launchd. Keep
    that validated PID for this invocation when the short-lived parent disappears.
    """
    runner = runner or watch.subprocess.run
    owned_debuggers = set() if owned_debuggers is None else owned_debuggers
    try:
        result = runner(["ps", "-axo", "pid=,ppid=,stat=,command="],
                        capture_output=True, text=True, timeout=5)
    except watch.subprocess.TimeoutExpired:
        return {"query_status": "query-timeout", "processes": []}
    except (OSError, watch.subprocess.SubprocessError):
        return {"query_status": "query-failed", "processes": []}
    if result.returncode:
        return {"query_status": "query-failed", "processes": []}
    process_pattern = re.compile(r"^\s*(\d+)\s+(\d+)\s+(\S+)\s+([^\n]*?)\s*$")
    host_pattern = re.compile(r".*/CoreSimulator/Devices/" + re.escape(udid) +
                              r"/[^\n]*/Persona\.app/Persona$")
    records = []
    for line in result.stdout.splitlines():
        match = process_pattern.match(line)
        if not match:
            continue
        pid, ppid, state, command = match.groups()
        executable = command.split(None, 1)[0] if command else ""
        name = executable.rsplit("/", 1)[-1]
        # launchd_sim records the simulator data directory as its first command
        # token. Match that directory separately from its executable token.
        launcher = re.search(r"(?:^|\s)(\S*launchd_sim)(?:\s|$)", command)
        if launcher:
            executable = launcher.group(1)
            name = "launchd_sim"
        if name in VISUAL_PROCESSES:
            records.append((int(pid), int(ppid), state, name, executable, command))
    hosts = {pid for pid, _ppid, _state, name, path, _command in records
             if name == "Persona" and host_pattern.fullmatch(path)}
    launchers = {pid for pid, _ppid, _state, name, _path, command in records
                 if name == "launchd_sim" and re.match(
                     r"^/Library/Developer/CoreSimulator/Devices/" + re.escape(udid) + r"/(?:\s|$)", command)}
    processes = []
    for pid, ppid, state, name, _path, _command in records:
        if name == "Persona" and pid in hosts:
            processes.append({"pid": pid, "ppid": ppid, "state": state, "name": name})
        elif name == "debugserver":
            if ppid in hosts or ppid in launchers:
                owned_debuggers.add(pid)
            if pid in owned_debuggers:
                processes.append({"pid": pid, "ppid": ppid, "state": state, "name": name})
    present_debuggers = {process["pid"] for process in processes if process["name"] == "debugserver"}
    processes.extend({"pid": pid, "state": "not-present", "name": "debugserver"}
                     for pid in owned_debuggers - present_debuggers)
    return {"query_status": "completed", "processes": sorted(processes, key=lambda item: (item["name"], item["pid"]))}


def sample_processes(udid, destination, index, runner=watch.subprocess.run, owned_debuggers=None):
    observation = scoped_processes(udid, runner, owned_debuggers)
    label = f"process-{index:03d}"
    launch.publish(destination / (label + ".json"), json.dumps({
        "captured_at": datetime.now(timezone.utc).isoformat(), **observation,
        "note": "Process presence is not a launch, test-bundle or readiness verdict."}, indent=2) + "\n")
    for process in observation["processes"]:
        if "pid" not in process:
            continue
        watch.bounded_file(["ps", "-M", str(process["pid"])],
                           destination / (label + f"-{process['name']}-{process['pid']}-threads.txt"), timeout=5)
    return observation


def launch_system_lines(text, bundle):
    kept = []
    selected = False
    for line in text.splitlines():
        if re.fullmatch(r"[A-Za-z0-9_.-]+", line):
            selected = line == bundle
            if selected:
                kept.append(bundle + "\n")
        elif selected and (match := re.fullmatch(
                r"\s*(state|pid|runs|last exit code)\s*=\s*([A-Za-z0-9_.:-]+)\s*", line, re.I)):
            kept.append(f"{match.group(1).lower()} = {match.group(2)}\n")
    return kept


def launch_system_projection(raw, bundle):
    """Project only fixed state fields from a matching service block.

    Never return arbitrary launchctl lines: program, path, arguments and environment
    values can carry credentials without using a secret-like keyword.
    """
    try:
        text = raw.decode()
    except UnicodeDecodeError:
        return []
    return launch_system_lines(text, bundle)


def launch_system(udid, destination, bundle=launch.BUNDLE):
    """Filter while reading; even a silent or noisy launchctl cannot outlive its bound."""
    path = destination / "launch-system-after-60s.txt"
    status = path.with_suffix(path.suffix + ".status.txt")
    launch.publish(status, "incomplete\n")
    process = None
    try:
        with path.with_suffix(path.suffix + ".command").open("wb") as output:
            process = subprocess.Popen(
                ["xcrun", "simctl", "spawn", udid, "launchctl", "print", "system"],
                stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
            deadline = watch.time.monotonic() + 10
            kept = 0
            partial = bytearray()

            def export(raw):
                nonlocal kept
                for line in launch_system_lines(raw, bundle):
                    piece = line.encode()
                    if kept + len(piece) > watch.MAX_SAMPLE_BYTES:
                        raise launch.OutputLimitError("launch system output exceeded byte limit")
                    output.write(piece)
                    kept += len(piece)

            with selectors.DefaultSelector() as selector:
                selector.register(process.stdout, selectors.EVENT_READ)
                while selector.get_map():
                    remaining = deadline - watch.time.monotonic()
                    if remaining <= 0:
                        raise subprocess.TimeoutExpired(process.args, 10)
                    for key, _ in selector.select(remaining):
                        chunk = launch.os.read(key.fd, 4096)
                        if not chunk:
                            selector.unregister(key.fileobj)
                            continue
                        partial.extend(chunk)
                        if len(partial) > watch.MAX_SAMPLE_BYTES:
                            raise launch.OutputLimitError("launch system output exceeded byte limit")
            if process.wait(timeout=max(0, deadline - watch.time.monotonic())):
                raise subprocess.CalledProcessError(process.returncode, process.args)
            export(partial.decode())
        if not path.with_suffix(path.suffix + ".command").stat().st_size:
            raise launch.OutputLimitError("no matching bounded launch service state")
        path.with_suffix(path.suffix + ".command").replace(path)
        launch.publish(status, "0\n")
        return 0
    except subprocess.TimeoutExpired:
        launch.publish(status, "124\n")
        launch.publish(path, "unavailable: command timed out\n")
        return 124
    except launch.OutputLimitError:
        path.unlink(missing_ok=True)
        launch.publish_status(status, "output-limit\n")
        return 125
    except (OSError, subprocess.SubprocessError):
        launch.publish_status(status, "unavailable: command-failure\n")
        return 127
    finally:
        if process is not None:
            if process.poll() is None:
                process.kill()
                process.wait()
            process.stdout.close()


def observe(udid, bundle, destination, seconds=VISUAL_SECONDS, scope_root=None,
            monotonic=watch.time.monotonic, sleep=watch.time.sleep):
    launch.identity(udid, bundle)
    if seconds > VISUAL_SECONDS:
        raise ValueError("visual observation cannot exceed the existing step deadline")
    destination.mkdir(parents=True, exist_ok=True)
    launch.publish(destination / "observer.status.txt",
                   "collection incomplete; observation only; no launch cause established\n")
    launch.publish(destination / "launch-system-after-60s.txt", "unavailable: observation incomplete\n")
    launch.publish(destination / "launch-system-after-60s.txt.status.txt", "incomplete\n")
    deadline = monotonic() + seconds
    started = monotonic()
    stopped = (destination / "observer.stop").exists()
    live = None
    index = 0
    system_captured = False
    owned_debuggers = set()
    try:
        try:
            if not stopped:
                live = watch.start_live_log(scope_root, destination,
                                            deadline=datetime.now(timezone.utc) + timedelta(seconds=seconds))
            while not stopped and monotonic() < deadline - FINISH_BEFORE:
                if (destination / "observer.stop").exists():
                    stopped = True
                    break
                now = monotonic()
                if now - started >= index * SAMPLE_SECONDS:
                    observation = sample_processes(udid, destination, index, owned_debuggers=owned_debuggers)
                    watch.correlate_live(live, {"host_pids": [p["pid"] for p in observation["processes"]
                                                               if p["name"] == "Persona"]})
                    index += 1
                if not system_captured and now - started >= SYSTEM_AFTER:
                    launch_system(udid, destination)
                    system_captured = True
                sleep(min(1, max(0, deadline - FINISH_BEFORE - monotonic())))
        except KeyboardInterrupt:
            stopped = True
        if not system_captured and not stopped:
            launch_system(udid, destination)
        launch.collect(destination.parent, destination, scope_root=scope_root)
        launch.publish(destination / "observer.status.txt",
                       ("xcodebuild-finished" if stopped else "visual-deadline") +
                       "; observation only; no launch cause established\n")
    finally:
        watch.stop_live_log(live)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--udid", required=True)
    parser.add_argument("--bundle-id", required=True)
    parser.add_argument("--scope-root", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--seconds", type=float, default=VISUAL_SECONDS)
    args = parser.parse_args()
    if not 0 < args.seconds <= VISUAL_SECONDS:
        parser.error("observation cannot exceed the visual step deadline")
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, watch.interrupted)
    observe(args.udid, args.bundle_id, args.output, args.seconds, scope_root=args.scope_root)


if __name__ == "__main__":
    main()
