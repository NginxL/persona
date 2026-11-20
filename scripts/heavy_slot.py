#!/usr/bin/env python3
"""Machine-wide slots for heavy builds, so parallel worktrees queue instead of thrashing (#10714).

    python3 scripts/heavy_slot.py run --kind xcode|gradle|go|web -- <command> [<argument>...]
    python3 scripts/heavy_slot.py status

run takes one of the kind's slots (an exclusive fcntl.flock on <dir>/<kind>.<n>.lock), runs the command as its child
and gives the slot back when the child exits. When every slot is busy it waits, printing one line when it starts to
wait, one when it gets a slot and one when it gives up. status lists every kind's slots and who holds them.

  PERSONA_HEAVY_SLOTS=0           off: run execs the command, so it runs exactly as without the helper
  PERSONA_HEAVY_SLOTS_<KIND>=<n>  that kind's slot count (0: no limit for that kind); the default comes from the
                                  core count (CORES_PER_SLOT: 14 cores give xcode 2, gradle 2, go 3, web 3)
  PERSONA_HEAVY_SLOT_TIMEOUT=<s>  the longest wait in seconds (default 2700, 45 minutes)
  PERSONA_HEAVY_SLOT_DIR=<dir>    the lock files' directory (default ~/Library/Caches/persona/slots on macOS, else
                                  $XDG_CACHE_HOME/persona/slots or ~/.cache/persona/slots)

A call inside a held slot never waits (PERSONA_HEAVY_SLOT_HELD names the held kinds and their holders' pids): of the
same kind it runs at once, the outer slot covers it; of another kind it takes a free slot of that kind if there is one
and runs anyway if not. No process holds one slot while it waits for another, so slots cannot deadlock.

The child is in the helper's process group and gets the helper's signal dispositions. TERM, HUP, INT, QUIT, USR1 and
USR2 sent to the helper are passed on to the child, except a signal the caller had ignored (it stays ignored, as it
would be for the command run directly), and INT or QUIT while the helper is in its terminal's foreground: the terminal
sent those to the child too. The helper exits with the child's status, dying by the same signal when the child did.
The lock descriptor is not inherited, so a daemon the command leaves running never keeps a slot.

Exit codes besides the command's own: 124 no slot came free within the timeout (the command did not run), 125 a bad
setting or argument, 126 the command could not be run, 127 it was not found. Windows has no fcntl: run says so in one
line and runs the command without a slot. docs/operations/testing.md "Heavy build slots".
"""
from __future__ import annotations

import argparse
import json
import os
import random
import shlex
import signal
import subprocess
import sys
import time

try:
    import fcntl
except ImportError:  # Windows
    fcntl = None

# Cores per slot: an xcodebuild or a Gradle build already uses every core and several GB of memory; a go test or a
# vitest run uses fewer for most of its run.
CORES_PER_SLOT = {"xcode": 7, "gradle": 7, "go": 4, "web": 4}
ARTICLE = {"xcode": "an", "gradle": "a", "go": "a", "web": "a"}
DEFAULT_TIMEOUT = 45 * 60
POLL_SECONDS = 0.25
HELD = "PERSONA_HEAVY_SLOT_HELD"
FORWARDED = ("SIGTERM", "SIGHUP", "SIGINT", "SIGQUIT", "SIGUSR1", "SIGUSR2")
# Signals whose default action dumps core: the helper exits with 128 + n for them instead of raising one on itself.
CORE_SIGNALS = ("SIGQUIT", "SIGILL", "SIGTRAP", "SIGABRT", "SIGFPE", "SIGBUS", "SIGSEGV", "SIGSYS", "SIGXCPU",
                "SIGXFSZ")
EXIT_TIMEOUT, EXIT_SETTING, EXIT_CANNOT_RUN, EXIT_NOT_FOUND = 124, 125, 126, 127


class SettingError(Exception):
    pass


def note(message):
    try:
        sys.stderr.write(f"heavy_slot: {message}\n")
        sys.stderr.flush()
    except (OSError, ValueError):
        pass


def duration(seconds):
    seconds = max(0, int(round(seconds)))
    minutes, seconds = divmod(seconds, 60)
    if not minutes:
        return f"{seconds}s"
    return f"{minutes}m{seconds}s" if seconds else f"{minutes}m"


def cores():
    try:
        return len(os.sched_getaffinity(0))
    except (AttributeError, OSError):
        return os.cpu_count() or 1


def default_count(kind, core_count=None):
    return max(1, (core_count or cores()) // CORES_PER_SLOT[kind])


def enabled(env):
    raw = env.get("PERSONA_HEAVY_SLOTS", "").strip()
    if raw in ("", "1"):
        return True
    if raw == "0":
        return False
    raise SettingError(f"PERSONA_HEAVY_SLOTS must be 0 (off) or 1 (on), not {raw!r}")


def slot_count(kind, env):
    name = f"PERSONA_HEAVY_SLOTS_{kind.upper()}"
    raw = env.get(name, "").strip()
    if not raw:
        return default_count(kind)
    if not raw.isdigit():
        raise SettingError(f"{name} must be a whole number of slots (0: no limit), not {raw!r}")
    return int(raw)


def wait_timeout(env):
    raw = env.get("PERSONA_HEAVY_SLOT_TIMEOUT", "").strip()
    if not raw:
        return DEFAULT_TIMEOUT
    try:
        value = float(raw)
    except ValueError:
        value = -1.0
    if not 0 <= value < float("inf"):
        raise SettingError(f"PERSONA_HEAVY_SLOT_TIMEOUT must be a number of seconds, not {raw!r}")
    return value


def slot_dir(env):
    if env.get("PERSONA_HEAVY_SLOT_DIR"):
        return env["PERSONA_HEAVY_SLOT_DIR"]
    home = os.path.expanduser("~")
    if sys.platform == "darwin":
        return os.path.join(home, "Library", "Caches", "persona", "slots")
    cache = env.get("XDG_CACHE_HOME", "")
    if not os.path.isabs(cache):
        cache = os.path.join(home, ".cache")
    return os.path.join(cache, "persona", "slots")


def slot_path(directory, kind, index):
    return os.path.join(directory, f"{kind}.{index}.lock")


def alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    except PermissionError:
        return True
    except OSError:
        return False
    return True


def held_kinds(env):
    """The kinds a live ancestor holds, from PERSONA_HEAVY_SLOT_HELD ("go:123,web:456"); an exited holder is stale."""
    kinds = {}
    for item in env.get(HELD, "").split(","):
        kind, _, pid = item.strip().partition(":")
        if kind in CORES_PER_SLOT and pid.isdigit() and alive(int(pid)):
            kinds[kind] = int(pid)
    return kinds


def with_held(env, kind):
    items = [item for item in env.get(HELD, "").split(",") if item.strip() and not item.startswith(kind + ":")]
    return {**env, HELD: ",".join(items + [f"{kind}:{os.getpid()}"])}


def try_lock(fd):
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        return False
    return True


def acquire(directory, kind, count, timeout):
    """(descriptor, seconds waited) of a slot of the kind, or (None, seconds waited) when none came free in time."""
    fds = [os.open(slot_path(directory, kind, index), os.O_RDWR | os.O_CREAT, 0o600) for index in range(count)]
    start = time.monotonic()
    announced = False
    try:
        while True:
            for fd in fds:
                if try_lock(fd):
                    for other in fds:
                        if other != fd:
                            os.close(other)
                    fds = []
                    return fd, (time.monotonic() - start if announced else 0.0)
            left = start + timeout - time.monotonic()
            if left <= 0:
                return None, time.monotonic() - start
            if not announced:
                note(f"waiting for {ARTICLE[kind]} {kind} slot ({count} busy, up to {duration(timeout)})")
                announced = True
            # Jitter keeps waiters that started together from polling in step.
            time.sleep(min(left, POLL_SECONDS * random.uniform(0.8, 1.2)))
    finally:
        for fd in fds:
            os.close(fd)


def record_holder(fd, kind, command):
    """The holder's pid, start and command in the lock file, for status; a free slot's stale record is never read."""
    try:
        record = {"pid": os.getpid(), "kind": kind, "since": time.time(), "cwd": os.getcwd(),
                  "command": shlex.join(command)[:300]}
        os.ftruncate(fd, 0)
        os.pwrite(fd, (json.dumps(record) + "\n").encode(), 0)
    except OSError:
        pass


def restore_child_signals():
    # Python ignores SIGPIPE and SIGXFSZ for itself; a command run directly would start with their default action.
    for name in ("SIGPIPE", "SIGXFSZ"):
        number = getattr(signal, name, None)
        if number is not None:
            signal.signal(number, signal.SIG_DFL)


def exec_command(command):
    """Replace this process with the command: the same pid, arguments, environment, stdio and exit status."""
    if os.name == "nt":  # exec on Windows starts a new process and exits this one at once
        return spawn_unsupervised(command)
    restore_child_signals()
    try:
        sys.stdout.flush()
    except (OSError, ValueError):
        pass
    try:
        os.execvp(command[0], command)
    except FileNotFoundError:
        note(f"{command[0]}: command not found")
        return EXIT_NOT_FOUND
    except OSError as error:
        note(f"{command[0]}: {error.strerror}")
        return EXIT_CANNOT_RUN
    return EXIT_CANNOT_RUN  # not reached: execvp returns only by raising


def spawn_unsupervised(command):
    try:
        return subprocess.call(command)
    except FileNotFoundError:
        note(f"{command[0]}: command not found")
        return EXIT_NOT_FOUND
    except OSError as error:
        note(f"{command[0]}: {error.strerror}")
        return EXIT_CANNOT_RUN


def in_terminal_foreground():
    for fd in (0, 1, 2):
        try:
            if os.isatty(fd):
                return os.tcgetpgrp(fd) == os.getpgrp()
        except OSError:
            continue
    return False


def die_like(signum):
    """End this process the way the child ended, by its signal; 128 + n when that cannot or should not happen."""
    if signum not in {getattr(signal, name, None) for name in CORE_SIGNALS}:
        try:
            sys.stdout.flush()
            sys.stderr.flush()
        except (OSError, ValueError):
            pass
        try:
            signal.signal(signum, signal.SIG_DFL)  # SIGKILL and SIGSTOP refuse a handler; they need none
        except (OSError, ValueError):
            pass
        try:
            if hasattr(signal, "pthread_sigmask"):
                signal.pthread_sigmask(signal.SIG_UNBLOCK, {signum})
            os.kill(os.getpid(), signum)
        except (OSError, ValueError):
            pass
    return 128 + signum


def supervise(command, env, fd):
    """Run the command as a child while this process holds the slot descriptor; return the child's exit status."""
    state = {"child": None, "pending": []}

    def forward(signum, _frame):
        if signum in (signal.SIGINT, getattr(signal, "SIGQUIT", None)) and in_terminal_foreground():
            return  # the terminal sent it to the whole foreground process group, the child included
        child = state["child"]
        if child is None:
            state["pending"].append(signum)
        elif child.returncode is None:
            try:
                os.kill(child.pid, signum)
            except OSError:
                pass

    previous = {}
    for name in FORWARDED:
        number = getattr(signal, name, None)
        if number is None:
            continue
        current = signal.getsignal(number)
        if current == signal.SIG_IGN:
            continue  # ignored by the caller: the child inherits the ignore, as it would run directly
        previous[number] = current
        signal.signal(number, forward)
    try:
        try:
            # close_fds (the default) keeps the slot descriptor out of the child; restore_signals (the default) gives
            # it the SIGPIPE and SIGXFSZ actions Python changed for itself.
            child = subprocess.Popen(command, env=env)
        except FileNotFoundError:
            note(f"{command[0]}: command not found")
            return EXIT_NOT_FOUND
        except OSError as error:
            note(f"{command[0]}: {error.strerror}")
            return EXIT_CANNOT_RUN
        state["child"] = child
        for signum in state["pending"]:  # signals that came before the child existed
            try:
                os.kill(child.pid, signum)
            except OSError:
                pass
        returncode = child.wait()
    finally:
        os.close(fd)  # gives the slot back
        for number, handler in previous.items():
            signal.signal(number, handler)
    if returncode < 0:
        return die_like(-returncode)
    return returncode


def run(kind, command, env=None):
    env = dict(os.environ if env is None else env)
    if not enabled(env):
        return exec_command(command)
    count = slot_count(kind, env)
    timeout = wait_timeout(env)
    if count == 0:
        return exec_command(command)
    if fcntl is None:
        note("this platform has no fcntl (Windows): running without a slot")
        return spawn_unsupervised(command)
    held = held_kinds(env)
    if kind in held:
        return exec_command(command)  # the caller's slot of this kind covers it
    directory = slot_dir(env)
    try:
        os.makedirs(directory, mode=0o700, exist_ok=True)
        fd, waited = acquire(directory, kind, count, 0 if held else timeout)
    except OSError as error:
        note(f"cannot use the slot directory {directory} ({error.strerror}): running without a slot")
        return exec_command(command)
    if fd is None:
        if held:
            outer = ", ".join(f"{name} (pid {pid})" for name, pid in sorted(held.items()))
            note(f"no free {kind} slot: running anyway inside the {outer} slot it was called from "
                 "(a nested call never waits)")
            return exec_command(command)
        note(f"timed out after {duration(waited)} waiting for {ARTICLE[kind]} {kind} slot ({count} busy); "
             f"did not run {command[0]} (PERSONA_HEAVY_SLOT_TIMEOUT sets the wait)")
        return EXIT_TIMEOUT
    if waited:
        note(f"got {ARTICLE[kind]} {kind} slot after {duration(waited)}")
    record_holder(fd, kind, command)
    return supervise(command, with_held(env, kind), fd)


def status(env=None):
    env = dict(os.environ if env is None else env)
    if not enabled(env):
        print("heavy build slots are off (PERSONA_HEAVY_SLOTS=0)")
        return 0
    if fcntl is None:
        print("heavy build slots need fcntl, which this platform (Windows) does not have")
        return 0
    directory = slot_dir(env)
    print(f"slots in {directory} ({cores()} cores)")
    for kind in CORES_PER_SLOT:
        count = slot_count(kind, env)
        if count == 0:
            print(f"{kind}: no limit (PERSONA_HEAVY_SLOTS_{kind.upper()}=0)")
            continue
        holders = []
        for index in range(count):
            path = slot_path(directory, kind, index)
            try:
                fd = os.open(path, os.O_RDONLY)
            except FileNotFoundError:
                continue
            except OSError:
                continue
            try:
                if try_lock(fd):
                    continue  # free; closing the descriptor below releases the probe's lock
                try:
                    record = json.loads(os.pread(fd, 4096, 0).decode() or "{}")
                except (OSError, ValueError):
                    record = {}
                since = record.get("since")
                age = duration(time.time() - since) if isinstance(since, (int, float)) else "?"
                holders.append(f"  slot {index}: pid {record.get('pid', '?')}, {age}, {record.get('command', '?')}")
            finally:
                os.close(fd)
        print(f"{kind}: {len(holders)} of {count} busy")
        for line in holders:
            print(line)
    return 0


class Parser(argparse.ArgumentParser):
    def error(self, message):
        self.print_usage(sys.stderr)
        note(message)
        sys.exit(EXIT_SETTING)


def main(argv=None):
    argv = sys.argv[1:] if argv is None else list(argv)
    # The command is everything after the first --, split off by hand: how argparse treats a -- inside a remainder
    # differs between Python versions, and the command's own arguments may hold another --.
    command = None
    if "--" in argv:
        split = argv.index("--")
        argv, command = argv[:split], argv[split + 1:]
    parser = Parser(prog="heavy_slot.py",
                    description="Machine-wide slots for heavy builds (docs/operations/testing.md).")
    commands = parser.add_subparsers(dest="action", required=True)
    runner = commands.add_parser("run", help="run -- <command> [<argument>...] in a slot of its kind")
    runner.add_argument("--kind", required=True, choices=sorted(CORES_PER_SLOT))
    commands.add_parser("status", help="list the slots and their holders")
    args = parser.parse_args(argv)
    try:
        if args.action == "status":
            if command is not None:
                parser.error("status takes no command")
            return status()
        if not command:
            parser.error("run needs a command after --")
        return run(args.kind, command)
    except SettingError as error:
        note(str(error))
        return EXIT_SETTING
    except KeyboardInterrupt:  # Ctrl-C while waiting for a slot
        return die_like(signal.SIGINT)


if __name__ == "__main__":
    sys.exit(main())
