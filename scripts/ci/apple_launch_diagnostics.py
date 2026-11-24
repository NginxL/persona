"""Bounded launch/runtime evidence for one disposable CI simulator and readiness window."""

import argparse
from datetime import datetime, timedelta, timezone
import json
import os
from pathlib import Path
import re
import selectors
import signal
import stat
import subprocess
import sys
import tempfile
import threading
import time
import uuid

CONTEXT = "simulator-readiness-context.json"
BUNDLE = "org.example.persona.ci"
READINESS_SECONDS = 900
TIMEOUT = 20
MAX_BYTES = 128 * 1024
RAW_LOG_BYTES = 4 * 1024 * 1024
PROCESSES = ("CoreSimulatorService", "launchd_sim", "runningboardd", "SpringBoard", "testmanagerd", "lsd",
             "installd", "assertiond", "debugserver", "Persona")
RUNTIME_PROCESSES = ("simdiskimaged", "cryptexd")
SECRET = re.compile(r"environment|DYLD_|bearer\s|token=|password|secret|credential", re.I)
COLLECTION_ERRORS = (OSError, subprocess.SubprocessError, ValueError)


class ScopeValidationError(ValueError):
    """A fixed non-sensitive scope category, never the rejected value or raw error."""

    category = "scope-validation"

    def __init__(self):
        super().__init__(self.category)


class ScopeContextFieldsError(ScopeValidationError):
    category = "scope-context-fields"


class ScopeIdentityError(ScopeValidationError):
    category = "scope-identity"


class ScopeOwnedSimulatorError(ScopeValidationError):
    category = "scope-owned-simulator"


class ScopeContextTimeError(ScopeValidationError):
    category = "scope-context-time"


class StreamParseError(ValueError):
    """Malformed NDJSON; its raw content must never become an exported status."""


class OutputLimitError(ValueError):
    """A bounded command, stream or export exceeded its existing byte limit."""


class StatusPublicationError(Exception):
    """The caller must keep an unwritten status distinct from collection categories."""


def invalidate(status, evidence, context):
    for path in (status, evidence, context):
        try:
            path.unlink(missing_ok=True)
        except OSError:
            # A failed invalidation must not replace the original failure category.
            pass


def publish(path, data):
    # Complete the bounded write before replacement exposes it as current evidence.
    if isinstance(data, str):
        data = data.encode()
    fd, temporary = tempfile.mkstemp(dir=path.parent, prefix=path.name + ".", suffix=".tmp")
    try:
        with os.fdopen(fd, "wb", buffering=0) as stream:
            stream.write(data)
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def publish_status(status, text):
    try:
        publish(status, text)
    except OSError:
        # Never recategorize an unwritten status as the collector's prior stage.
        raise StatusPublicationError(text) from None


def unavailable(status, category):
    publish_status(status, f"unavailable: {category}-failure; no launch cause established\n")


def identity(udid, bundle):
    try:
        owned = str(uuid.UUID(udid)).upper() == udid and bundle == BUNDLE
    except (ValueError, TypeError, AttributeError):
        owned = False
    if not owned:
        raise ScopeIdentityError() from None


def record(path, udid, bundle):
    identity(udid, bundle)
    path.write_text(json.dumps({"udid": udid, "bundle_id": bundle,
                               "started_at": datetime.now(timezone.utc).isoformat()}))


def read_scope_file(path, error_type=ScopeContextFieldsError):
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
    except OSError:
        raise error_type() from None
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_size > 1024:
            raise error_type()
        data = os.read(fd, 1025)
        if len(data) > 1024:
            raise error_type()
        return data.decode()
    except (OSError, UnicodeDecodeError):
        raise error_type() from None
    finally:
        os.close(fd)


def scope(output, now):
    return observation_scope(output, now)


def bounded_output(command, timeout=TIMEOUT, limit=MAX_BYTES):
    """A bounded command reader; stderr is never exported as diagnostic evidence."""
    process = subprocess.Popen(command, stdin=subprocess.DEVNULL,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    data = bytearray()
    errors = bytearray()
    deadline = time.monotonic() + timeout
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ, data)
            selector.register(process.stderr, selectors.EVENT_READ, errors)
            while selector.get_map():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise TimeoutError("command timed out")
                for key, _ in selector.select(remaining):
                    chunk = os.read(key.fd, 4096)
                    if not chunk:
                        selector.unregister(key.fileobj)
                        continue
                    key.data.extend(chunk[:limit + 1 - len(key.data)])
                    if len(key.data) > limit:
                        raise OutputLimitError("command exceeded byte limit")
        code = process.wait(timeout=max(0, deadline - time.monotonic()))
        if code:
            raise ValueError(f"command failed (exit {code})")
        return bytes(data)
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
        process.stdout.close()
        process.stderr.close()


def anchored(pattern, text):
    if re.fullmatch(r"[0-9A-Fa-f-]{36}", pattern):
        return re.search(r"(?<![0-9A-Fa-f])" + re.escape(pattern) + r"(?![0-9A-Fa-f])", text, re.I)
    return re.search(r"(?<![\w.-])" + re.escape(pattern) + r"(?![\w.-])", text, re.I)


def ndjson(raw):
    try:
        entry = json.loads(raw)
    except (json.JSONDecodeError, UnicodeDecodeError):
        raise StreamParseError("invalid NDJSON") from None
    if not isinstance(entry, dict):
        raise StreamParseError("invalid launch log entry")
    return entry


def relevant(entry, udid, bundle, start, end, pids=()):
    """Launchd may omit the UDID, so require a process allowlist and one owned anchor."""
    try:
        message = entry["eventMessage"]
        process = Path(entry["processImagePath"]).name
        stamp = datetime.fromisoformat(entry["timestamp"])
    except (KeyError, TypeError, ValueError):
        return None
    if stamp.utcoffset() is None or not start <= stamp <= end or SECRET.search(message):
        return None
    if process in RUNTIME_PROCESSES:
        if not re.search(r"mount|cryptex|duplicate|disappear|23A8464|iOS[ _]26\.0", message, re.I):
            return None
        if not anchored(udid, message):
            return None
    elif process in PROCESSES:
        anchored_pids = {entry["processIdentifier"]} if entry.get("processIdentifier") in pids else set()
        # These three launch services also report their client in a structured
        # field. Do not treat an arbitrary numeric message as ownership proof.
        if process in {"launchd_sim", "assertiond", "debugserver", "runningboardd", "testmanagerd"}:
            for key in ("pid", "targetPid", "clientPid", "processID", "processIdentifier"):
                if isinstance(entry.get(key), int) and entry[key] in pids:
                    anchored_pids.add(entry[key])
            for match in re.finditer(r"(?<![\w.-])(?:pid|process(?:Identifier|ID)?)\s*(?:[:=]|\s)\s*(\d+)(?!\d)", message, re.I):
                if int(match.group(1)) in pids:
                    anchored_pids.add(int(match.group(1)))
        if not (anchored(udid, message) or anchored(bundle, message) or
                anchored(bundle + ".tests", message) or anchored_pids):
            return None
    else:
        return None
    return {"timestamp": stamp.isoformat(), "process": process, "eventMessage": message}


REAPER = r'''
import os, signal, sys, time
parent, group = int(sys.argv[1]), int(sys.argv[2])
deadline = time.monotonic() + float(sys.argv[3])
signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
while time.monotonic() < deadline:
    try:
        os.kill(parent, 0)
    except ProcessLookupError:
        break
    time.sleep(min(0.1, max(0, deadline - time.monotonic())))
try:
    os.killpg(group, signal.SIGKILL)
except ProcessLookupError:
    pass
'''


class LiveLog:
    """One owned log child, a draining reader, and a separate fail-closed status."""

    def __init__(self, command, destination, deadline, udid, bundle):
        self.command = command
        self.destination = destination
        self.deadline = deadline
        self.udid, self.bundle = udid, bundle
        self.pgid = os.getpgid(0)
        self.log_pgid = None
        self.lock = threading.RLock()
        self.status = "collection incomplete"
        self.raw_bytes = 0
        self.pending = []
        self.kept = []
        self.partial = bytearray()
        self.pids = set()
        self.process = None
        self.reaper = None
        self.thread = None
        self.stderr = None
        self.stderr_thread = None
        self.stderr_bytes = 0
        self.exit_code = None
        self.pre_stop_code = None
        self.stopped = False
        self.reader_error = None
        self.terminated = False
        self.timed_out = False

    def publish(self):
        with self.lock:
            encoded = json.dumps(self.kept, ensure_ascii=False).encode()
            if len(encoded) > MAX_BYTES:
                raise OutputLimitError("live launch log export exceeded byte limit")
            count = len(self.kept)
            self.status = "captured"
        publish(self.destination / "live-launch.json", encoded)
        publish(self.destination / "live-launch.status.txt",
                f"captured {count} scoped live events; diagnosis still required\n"
                if count else "no scoped live launch events; no cause established\n")

    def fail(self, category):
        with self.lock:
            if self.status != "collection incomplete":
                return
            self.status = category
            (self.destination / "live-launch.json").unlink(missing_ok=True)
            publish(self.destination / "live-launch.status.txt",
                    f"unavailable: live-log-{category}; no launch cause established\n")

    def correlate(self, pids):
        with self.lock:
            if not pids:
                return
            self.pids.update(pids)
            pending, self.pending = self.pending, []
            for entry in pending:
                self.retain(entry)

    def retain(self, entry):
        item = relevant(entry, self.udid, self.bundle, self.start, self.deadline, self.pids)
        if item:
            self.kept.append(item)
        elif self.pids:
            return
        elif len(json.dumps(self.pending + [entry], ensure_ascii=False).encode()) > MAX_BYTES:
            raise OutputLimitError("uncorrelated live launch log exceeded byte limit")
        else:
            self.pending.append(entry)
        if len(json.dumps(self.kept, ensure_ascii=False).encode()) > MAX_BYTES:
            raise OutputLimitError("live launch log export exceeded byte limit")

    def consume(self, raw):
        with self.lock:
            self.raw_bytes += len(raw)
            if self.raw_bytes > RAW_LOG_BYTES:
                raise OutputLimitError("raw live launch log exceeded byte limit")
            self.partial.extend(raw)
            while True:
                try:
                    newline = self.partial.index(10)
                except ValueError:
                    break
                entry, self.partial = self.partial[:newline], self.partial[newline + 1:]
                self.retain(ndjson(entry))
            if len(self.partial) > MAX_BYTES:
                raise OutputLimitError("single live launch event exceeded byte limit")

    def read(self):
        try:
            for raw in iter(self.process.stdout.readline, b""):
                self.consume(raw)
            if self.partial:
                self.consume(b"\n")
        except COLLECTION_ERRORS + (json.JSONDecodeError,) as error:
            self.reader_error = error
            if isinstance(error, (TimeoutError, subprocess.TimeoutExpired)):
                category = "timeout"
            elif isinstance(error, OutputLimitError):
                category = "output-limit"
            elif isinstance(error, StreamParseError):
                category = "parse-failure"
            else:
                category = "read-failure"
            self.fail(category)
        finally:
            self.process.stdout.close()
            self.exit_code = self.process.poll()

    def drain_stderr(self):
        try:
            while self.stderr.read(4096):
                self.stderr_bytes += 4096
                if self.stderr_bytes > RAW_LOG_BYTES:
                    # A bounded failure still closes the pipe; raw stderr is never exported.
                    self.fail("output-limit")
                    break
        except OSError:
            self.fail("read-failure")
        finally:
            self.stderr.close()

    def close_owned_group(self):
        """Stop only the dedicated log group, never the watcher or xcodebuild."""
        if self.log_pgid is None:
            return
        try:
            os.killpg(self.log_pgid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        # Wait for the leader and the independent stdout/stderr EOF. Descendant
        # processes that ignore TERM cannot retain the stream past this point.
        try:
            self.process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=2)

    def stop(self, timeout=2):
        if self.process is None or self.thread is None or self.stderr_thread is None:
            return
        if self.pre_stop_code is None:
            self.pre_stop_code = self.process.poll()
        self.stopped = True
        if self.reaper is not None and self.reaper.poll() is None:
            try:
                os.killpg(self.reaper.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                self.reaper.wait(timeout=2)
            except subprocess.TimeoutExpired:
                os.killpg(self.reaper.pid, signal.SIGKILL)
                self.reaper.wait(timeout=2)
        deadline = time.monotonic() + timeout
        try:
            if self.process.poll() is None:
                os.killpg(self.log_pgid, signal.SIGTERM)
                self.terminated = True
            self.thread.join(timeout=timeout)
            self.stderr_thread.join(timeout=max(0, deadline - time.monotonic()))
        except ProcessLookupError:
            pass
        if self.thread.is_alive() or self.stderr_thread.is_alive():
            self.timed_out = True
            self.close_owned_group()
        if self.process.poll() is None:
            try:
                self.process.wait(timeout=2)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=2)
        self.thread.join(timeout=2)
        self.stderr_thread.join(timeout=2)
        if self.thread.is_alive() or self.stderr_thread.is_alive():
            self.fail("read-timeout")
            raise TimeoutError("live log reader did not finish")
        if self.status != "collection incomplete":
            return
        if self.reader_error is not None:
            return
        if self.timed_out:
            self.fail("stop-timeout")
        elif self.pre_stop_code:
            self.fail("command-failure")
        else:
            self.publish()


def live_command():
    predicate = "(" + " OR ".join(f'process == "{name}"' for name in PROCESSES + RUNTIME_PROCESSES) + ")"
    return ["/usr/bin/log", "stream", "--style", "ndjson", "--info", "--debug", "--predicate", predicate]


def observation_scope(output, now, regular=False):
    """Resolve the existing phone/regular fixture without borrowing the other's scope."""
    context_name = "regular-readiness-context.json" if regular else CONTEXT
    simulator_name = "regular-simulator.log" if regular else "simulator.log"
    try:
        context = json.loads(read_scope_file(output / context_name))
        if not isinstance(context, dict) or set(context) != {"udid", "bundle_id", "started_at"}:
            raise ScopeContextFieldsError()
    except (json.JSONDecodeError, TypeError, ValueError):
        raise ScopeContextFieldsError() from None
    identity(context["udid"], context["bundle_id"])
    if read_scope_file(output / simulator_name, ScopeOwnedSimulatorError).strip() != context["udid"]:
        raise ScopeOwnedSimulatorError()
    try:
        start = datetime.fromisoformat(context["started_at"])
    except (TypeError, ValueError):
        raise ScopeContextTimeError() from None
    if start.utcoffset() != timedelta(0) or start > now or now - start > timedelta(hours=2):
        raise ScopeContextTimeError()
    end = min(now, start + timedelta(seconds=READINESS_SECONDS))
    if end <= start:
        raise ScopeContextTimeError()
    return context, start, end


def start_live_log(scope_root, destination, deadline=None, popen=subprocess.Popen,
                   now=lambda: datetime.now(timezone.utc), regular=False):
    """Start before the test child; leave the owned shell process group intact."""
    current = now()
    # The regular fixture has its own context file in the same explicit output root.
    context, start, _ = observation_scope(scope_root, current, regular=regular)
    # Live observation runs forward from now, so it must not inherit the
    # `observation_scope` end, which is already clamped to `now` for the
    # historical collector. The live window closes at the scope's readiness
    # horizon, optionally narrowed by an explicit caller deadline. Falling
    # back to `current` here would freeze the live filter at startup and
    # shrink the watchdog lifetime to ~10 seconds.
    end = start + timedelta(seconds=READINESS_SECONDS)
    if deadline is not None:
        end = min(end, deadline)
    destination.mkdir(parents=True, exist_ok=True)
    live = LiveLog(live_command(), destination, end, context["udid"], context["bundle_id"])
    publish(destination / "live-launch.status.txt", "unavailable: live-log-collection-incomplete; no launch cause established\n")
    (destination / "live-launch.json").unlink(missing_ok=True)
    try:
        live.process = popen(live.command, stdin=subprocess.DEVNULL,
                             stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                             bufsize=0, start_new_session=True)
        live.stderr = live.process.stderr
        live.log_pgid = os.getpgid(live.process.pid)
        if live.log_pgid != live.process.pid or live.log_pgid == live.pgid:
            live.process.kill()
            live.process.wait(timeout=2)
            live.fail("group-validation-failure")
            return live
    except (OSError, subprocess.SubprocessError):
        live.fail("startup-failure")
        return live
    live.start = start
    # The watchdog owns a separate session and records only the validated log
    # PGID. Parent death or its hard deadline reaps that group without ever
    # signalling the watcher, xcodebuild, or the watchdog itself.
    lifetime = max(1, (end - current).total_seconds() + 10)
    try:
        live.reaper = popen([sys.executable, "-c", REAPER, str(os.getpid()), str(live.log_pgid), str(lifetime)],
                            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                            start_new_session=True)
    except (OSError, subprocess.SubprocessError):
        live.close_owned_group()
        live.fail("startup-failure")
        return live
    live.thread = threading.Thread(target=live.read, name="bounded-live-launch-log", daemon=True)
    live.thread.start()
    live.stderr_thread = threading.Thread(target=live.drain_stderr, name="bounded-live-launch-stderr", daemon=True)
    live.stderr_thread.start()
    return live


def stream_output(command, start, end, udid, bundle, timeout=TIMEOUT):
    """Filter NDJSON while reading, before either raw or retained output can exhaust a cap."""
    process = subprocess.Popen(command, stdin=subprocess.DEVNULL,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    deadline = time.monotonic() + timeout
    raw = 0
    line = bytearray()
    kept = []
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ, "events")
            selector.register(process.stderr, selectors.EVENT_READ, "errors")
            while selector.get_map():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise TimeoutError("launch log timed out")
                for key, _ in selector.select(remaining):
                    chunk = os.read(key.fd, 4096)
                    if not chunk:
                        selector.unregister(key.fileobj)
                        continue
                    raw += len(chunk)
                    if raw > RAW_LOG_BYTES:
                        raise OutputLimitError("raw launch log exceeded byte limit")
                    if key.data == "errors":
                        continue
                    line.extend(chunk)
                    while True:
                        try:
                            newline = line.index(10)
                        except ValueError:
                            break
                        entry = line[:newline]
                        del line[:newline + 1]
                        item = relevant(ndjson(entry), udid, bundle, start, end)
                        if item:
                            kept.append(item)
                    if len(line) > MAX_BYTES:
                        raise OutputLimitError("single launch event exceeded byte limit")
        code = process.wait(timeout=max(0, deadline - time.monotonic()))
        if code:
            raise ValueError(f"launch log command failed (exit {code})")
        if line:
            item = relevant(ndjson(line), udid, bundle, start, end)
            if item:
                kept.append(item)
        encoded = json.dumps(kept, ensure_ascii=False).encode()
        if len(encoded) > MAX_BYTES:
            raise OutputLimitError("launch log export exceeded byte limit")
        return kept
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
        process.stdout.close()
        process.stderr.close()


def collect(output, destination, reader=bounded_output, now=None, stream_reader=stream_output, scope_root=None):
    status = destination / "simulator-launch.status.txt"
    evidence = destination / "simulator-launch.json"
    context = destination / ("regular-readiness-context.json" if destination.name == "live-regular-readiness" else CONTEXT)
    category = "publication"
    try:
        invalidate(status, evidence, context)
        category = "scope-validation"
        context_data, start, end = observation_scope(
            scope_root or output, now or datetime.now(timezone.utc),
            regular=destination.name == "live-regular-readiness")
        udid, bundle = context_data["udid"], context_data["bundle_id"]
        predicate = ("(" + " OR ".join(f'process == "{name}"' for name in PROCESSES + RUNTIME_PROCESSES) +
                     f') AND (eventMessage CONTAINS[c] "{udid}" OR eventMessage CONTAINS "{bundle}"' +
                     ' OR process == "simdiskimaged" OR process == "cryptexd")')
        command = ["/usr/bin/log", "show", "--style", "ndjson", "--info", "--debug",
                   "--start", start.strftime("%Y-%m-%d %H:%M:%S") + "+00:00",
                   "--end", end.strftime("%Y-%m-%d %H:%M:%S") + "+00:00", "--predicate", predicate]
        category = "log-command"
        if reader is bounded_output and stream_reader is stream_output:
            kept = stream_reader(command, start, end, udid, bundle)
        else:
            raw = reader(command)
            if len(raw) > MAX_BYTES:
                raise OutputLimitError("launch log exceeded byte limit")
            category = "stream-parse"
            kept = []
            for line in raw.decode().splitlines():
                if not line.strip():
                    continue
                item = relevant(ndjson(line), udid, bundle, start, end)
                if item:
                    kept.append(item)
        if not kept:
            category = "publication"
            publish_status(status, "unavailable: no scoped launch/runtime events; no cause established\n")
            return
        category = "publication"
        encoded = json.dumps(kept, ensure_ascii=False).encode()
        if len(encoded) > MAX_BYTES:
            raise OutputLimitError("export exceeded byte limit")
        # Context alone cannot be mistaken for a bundle; evidence is its final commit point.
        publish(context, json.dumps(context_data))
        try:
            publish(evidence, encoded)
            publish_status(status, f"captured {len(kept)} scoped events; diagnosis still required\n")
        except COLLECTION_ERRORS + (StatusPublicationError,):
            evidence.unlink(missing_ok=True)
            context.unlink(missing_ok=True)
            raise
    except StatusPublicationError:
        invalidate(status, evidence, context)
        raise
    except (OSError, subprocess.SubprocessError, ValueError, StreamParseError,
            OutputLimitError, ScopeValidationError) as error:
        invalidate(status, evidence, context)
        if isinstance(error, ScopeValidationError):
            unavailable(status, error.category)
        elif isinstance(error, (TimeoutError, subprocess.TimeoutExpired)):
            # Preserve the established command-deadline category, including the default stream.
            publish_status(status, "unavailable: scoped log command timed out\n")
        elif isinstance(error, OutputLimitError):
            unavailable(status, "output-limit")
        elif isinstance(error, StreamParseError):
            unavailable(status, "stream-parse")
        else:
            # Only fixed stage categories are exported, never raw errors or a fallback query.
            unavailable(status, category)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--record", type=Path, required=True)
    parser.add_argument("--udid", required=True)
    parser.add_argument("--bundle-id", required=True)
    args = parser.parse_args()
    record(args.record, args.udid, args.bundle_id)


if __name__ == "__main__":
    main()
