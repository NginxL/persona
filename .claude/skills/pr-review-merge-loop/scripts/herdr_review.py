#!/usr/bin/python3
"""Run one read-only structured reviewer in an isolated Herdr tab.

Usage: herdr_review.py CONFIG -- [CLI_ARG ...]

The watcher owns semantic validation. This helper only transports the fixed
prompt and CLI JSON result, allocates bounded slots, and reclaims its own tab.
It does not start, stop or supervise Herdr itself.
"""
import atexit
import fcntl
import json
import os
from pathlib import Path
import shlex
import signal
import subprocess
import sys
import tempfile
import time

_cleanup_tab = None
_terminated = False
_config = None


def command(*args, timeout=90):
    result = subprocess.run(
        [_config["herdr_command"], "--session", _config["session"], *args],
        stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        text=True, timeout=timeout, check=False,
    )
    try:
        value = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        if result.returncode == 0 and not result.stdout.strip() and args[:2] == ("pane", "run"):
            return {}
        raise RuntimeError("Herdr did not return JSON: " + (result.stderr or result.stdout)[-500:]) from error
    if result.returncode or "error" in value:
        detail = value.get("error", {})
        raise RuntimeError(str(detail.get("message") or detail.get("code") or result.stderr[-500:]))
    return value.get("result", value)


def cleanup_tab():
    tab = _cleanup_tab
    if tab:
        try:
            command("tab", "close", tab, timeout=10)
        except (RuntimeError, subprocess.TimeoutExpired):
            pass


def terminate_signal(signum, frame):
    global _terminated
    _terminated = True
    cleanup_tab()
    raise SystemExit(128 + signum)


def wait_agent(pane, timeout=180):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = command("agent", "list", timeout=20)
        found = False
        for agent in result.get("agents", []):
            if agent.get("pane_id") != pane:
                continue
            found = True
            if agent.get("agent") != "claude":
                raise RuntimeError("unexpected reviewer executable")
            if agent.get("agent_status") == "blocked":
                screen = command("pane", "read", pane, "--source", "visible", "--lines", "45", timeout=20)
                text = json.dumps(screen, ensure_ascii=False)
                warning = _config.get("startup_warning", "")
                acceptance = _config.get("startup_acceptance", "")
                if not warning or not acceptance or warning not in text or acceptance not in text:
                    raise RuntimeError("reviewer requested an unexpected interaction")
                command("pane", "send-keys", pane, "right", "enter", timeout=20)
                time.sleep(1)
                continue
            if agent.get("agent_status") in ("idle", "done", "working"):
                return
            raise RuntimeError("reviewer startup state is not usable")
        if not found:
            try:
                screen = command("pane", "read", pane, "--source", "visible", "--lines", "45", timeout=20)
                if screen:
                    raise RuntimeError("reviewer exited before agent detection: " + json.dumps(screen, ensure_ascii=False)[-1200:])
            except RuntimeError as error:
                if "exited before agent detection" in str(error):
                    raise
        time.sleep(1)
    raise RuntimeError("reviewer did not become ready")


def wait_exit(pane, deadline):
    while time.monotonic() < deadline and not _terminated:
        agents = command("agent", "list", timeout=20).get("agents", [])
        if not any(agent.get("pane_id") == pane for agent in agents):
            return
        time.sleep(min(2, max(0.1, deadline - time.monotonic())))
    raise TimeoutError("Herdr reviewer exceeded its lifetime")


def forwarded_flags(arguments, forbidden):
    output = []
    options_with_values = {"--output-format", "--json-schema", "--tools", "--setting-sources", "--permission-mode"}
    index = 0
    while index < len(arguments):
        flag = arguments[index]
        if flag in forbidden:
            raise RuntimeError("reviewer cannot override " + flag)
        if flag in options_with_values:
            if index + 1 >= len(arguments):
                raise RuntimeError("missing reviewer option argument")
            output.extend(arguments[index:index + 2])
            index += 2
            continue
        if flag == "--add-dir":
            if index + 1 >= len(arguments):
                raise RuntimeError("missing reviewer add-dir argument")
            output.extend(arguments[index:index + 2])
            index += 2
            continue
        if flag not in output:
            output.append(flag)
        index += 1
    return output


def run(slot, flags):
    global _cleanup_tab
    root = Path(_config["runtime_directory"]).expanduser()
    lease_path = root / ("slot-%02d.json" % slot)
    if lease_path.exists():
        try:
            old = json.loads(lease_path.read_text(encoding="utf-8"))
            pid = old.get("pid")
            if isinstance(pid, int) and pid > 0:
                try:
                    os.kill(pid, 0)
                except ProcessLookupError:
                    tab = old.get("tab")
                    if isinstance(tab, str) and tab.startswith("w") and ":t" in tab:
                        command("tab", "close", tab, timeout=20)
        except (OSError, ValueError, RuntimeError, subprocess.TimeoutExpired):
            pass
    name = "pr-r%02d-%d" % (slot, os.getpid())
    with tempfile.TemporaryDirectory(prefix="slot-%02d-" % slot, dir=root) as temporary:
        work = Path(temporary)
        prompt_path = work / "prompt.txt"
        result_path = work / "result.json"
        prompt_path.write_text(sys.stdin.read(), encoding="utf-8")
        os.chmod(prompt_path, 0o600)
        tab = None
        pane = None
        with lease_path.open("w", encoding="utf-8") as lease:
            json.dump({"pid": os.getpid(), "slot": slot}, lease)
            lease.flush()
            os.fsync(lease.fileno())
            try:
                created = command("tab", "create", "--workspace", _config.get("workspace", "w1"),
                                  "--cwd", os.getcwd(), "--label", name, "--no-focus", timeout=30)
                tab = created["tab"]["tab_id"]
                pane = created["root_pane"]["pane_id"]
                _cleanup_tab = tab
                lease.seek(0)
                lease.truncate()
                json.dump({"pid": os.getpid(), "slot": slot, "tab": tab, "pane": pane}, lease)
                lease.flush()
                os.fsync(lease.fileno())
                launch = (_config["reviewer_launcher"] + " " + " ".join(shlex.quote(flag) for flag in flags)
                          + " < " + shlex.quote(str(prompt_path)) + " > " + shlex.quote(str(result_path))
                          + " 2> " + shlex.quote(str(work / "stderr.txt")))
                bounded = []
                wrapper = _config.get("process_wrapper", "").strip()
                if wrapper:
                    bounded = shlex.split(wrapper)
                    if "--slot" in bounded:
                        bounded = [str(slot) if argument == "--slot" else argument for argument in bounded]
                    else:
                        bounded.append(str(slot))
                bounded += ["zsh", "-lic", launch]
                if wrapper:
                    bounded.insert(-3, "--")
                command("pane", "run", pane, " ".join(shlex.quote(arg) for arg in bounded), timeout=30)
                wait_agent(pane)
                wait_exit(pane, time.monotonic() + int(_config.get("lifetime_seconds", 7200)))
                if not result_path.is_file():
                    detail = (work / "stderr.txt").read_text(errors="replace")[-1000:]
                    raise RuntimeError("reviewer produced no structured result: " + detail)
                result = json.loads(result_path.read_text(encoding="utf-8"))
                if not isinstance(result, dict) or result.get("is_error"):
                    raise RuntimeError("reviewer failed")
                if set(result) == {"findings"}:
                    # Add only the transport envelope; the watcher still fully
                    # validates every model-provided finding and changed line.
                    result = {"is_error": False, "subtype": "success", "result": "",
                              "structured_output": result, "permission_denials": []}
                sys.stdout.write(json.dumps(result, separators=(",", ":")))
            except BaseException:
                if tab is not None:
                    try:
                        command("tab", "close", tab, timeout=20)
                    except (RuntimeError, subprocess.TimeoutExpired):
                        pass
                raise
            finally:
                if tab is not None:
                    try:
                        command("tab", "close", tab, timeout=30)
                    except (RuntimeError, subprocess.TimeoutExpired):
                        pass
                for path in (prompt_path, result_path):
                    path.unlink(missing_ok=True)


def main():
    global _config
    signal.signal(signal.SIGTERM, terminate_signal)
    signal.signal(signal.SIGINT, terminate_signal)
    atexit.register(cleanup_tab)
    if len(sys.argv) < 4 or sys.argv[2] != "--":
        raise SystemExit("usage: herdr_review.py CONFIG -- [CLI_ARG ...]")
    _config = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
    for key in ("herdr_command", "session", "runtime_directory", "reviewer_launcher"):
        if not isinstance(_config.get(key), str) or not _config[key]:
            raise RuntimeError("missing reviewer configuration: " + key)
    root = Path(_config["runtime_directory"]).expanduser()
    if not root.is_absolute():
        raise RuntimeError("runtime_directory must be absolute")
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    flags = forwarded_flags(sys.argv[3:], set(_config.get("forbidden_flags", ["--model"])))
    required_flags = {
        "-p": None, "--output-format": "json", "--tools": "Read",
        "--permission-mode": "plan", "--json-schema": None,
    }
    for flag, value in required_flags.items():
        if flag not in flags:
            raise RuntimeError("reviewer requires " + flag)
        if value is not None:
            index = flags.index(flag)
            if index + 1 >= len(flags) or flags[index + 1] != value:
                raise RuntimeError("reviewer requires %s %s" % (flag, value))
    slots = int(_config.get("slots", 1))
    if not 1 <= slots <= 64:
        raise RuntimeError("reviewer slots must be between 1 and 64")
    locks = []
    try:
        for slot in range(slots):
            lock = (root / ("slot-%02d.lock" % slot)).open("a+")
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                lock.close()
                continue
            locks.append(lock)
            return run(slot, flags)
        raise RuntimeError("all %d bounded reviewer slots are occupied" % slots)
    finally:
        cleanup_tab()
        for lock in locks:
            fcntl.flock(lock, fcntl.LOCK_UN)
            lock.close()


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
