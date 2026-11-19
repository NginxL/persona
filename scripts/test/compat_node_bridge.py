#!/usr/bin/env python3
"""Stdio bridge to a real node in a disposable PID-isolated container.

No host home, sockets, process namespace or network is mounted. The container
runs only the supplied test binary; its data directories are private tmpfs.
"""
import argparse
import json
import os
import secrets
import signal
import subprocess
import sys
import threading


class Lines:
    """The whole lines in what is fed, until done or 1 MiB without a newline (frames, not lines): then none."""

    def __init__(self):
        self.buf, self.done = b"", False

    def feed(self, chunk):
        if self.done:
            return []
        self.buf += chunk
        *lines, self.buf = self.buf.split(b"\n")
        if len(self.buf) > 1 << 20:
            self.buf, self.done = b"", True
        return lines


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--binary", required=True)
    p.add_argument("--handshake-log", required=True)
    p.add_argument("--image", default="debian:bookworm-slim")
    a = p.parse_args()
    nonce = secrets.token_hex(8)
    name = "persona-compat-node-" + nonce
    command = ["docker", "run", "--rm", "--pull=never", "-i", "--name", name,
               "--label", "persona.compat.run=" + nonce, "--hostname", "fixture-node",
               "--network", "none", "--read-only", "--cap-drop", "ALL",
               "--security-opt", "no-new-privileges", "--memory", "512m", "--pids-limit", "128",
               "--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777", "--tmpfs", "/home/fixture:rw,nosuid,nodev,mode=1777",
               "--user", "65534:65534", "--env", "HOME=/home/fixture",
               "--mount", "type=bind,source=" + os.path.abspath(a.binary) + ",target=/usr/local/bin/personad,readonly",
               a.image, "/usr/local/bin/personad", "node", "--stdio"]
    child = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=sys.stderr.buffer)
    request_ids = set()
    lock = threading.Lock()

    def incoming():
        # Bytes go on as they come: once both ends switch to lane frames (docs/protocol/protocol.md "Lanes") the
        # stream is binary, and waiting for a newline would hold a frame back. Lines are only looked into until the
        # handshake, which comes before any switch.
        pending = Lines()
        try:
            while chunk := os.read(sys.stdin.fileno(), 65536):
                for line in pending.feed(chunk):
                    try:
                        frame = json.loads(line)
                        if frame.get("method") == "node.info":
                            with lock:
                                request_ids.add(frame.get("id"))
                            pending.done = True
                    except (ValueError, TypeError, AttributeError):
                        pass
                child.stdin.write(chunk)
                child.stdin.flush()
        except (OSError, BrokenPipeError):
            pass
        finally:
            try:
                child.stdin.close()
            except OSError:
                pass

    def ended(signum, frame):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, ended)
    threading.Thread(target=incoming, daemon=True).start()
    result = 1
    try:
        pending = Lines()
        while chunk := os.read(child.stdout.fileno(), 65536):
            for line in pending.feed(chunk):
                try:
                    frame = json.loads(line)
                    with lock:
                        known = frame.get("id") in request_ids
                    if known and "result" in frame:
                        # Only the node handshake is retained, not requests, secrets
                        # or arbitrary transcript/phone data.
                        with open(a.handshake_log, "w", encoding="utf-8") as f:
                            json.dump(frame["result"], f)
                        os.chmod(a.handshake_log, 0o600)
                        pending.done = True
                except (ValueError, TypeError, AttributeError):
                    pass
            sys.stdout.buffer.write(chunk)
            sys.stdout.buffer.flush()
        result = child.wait(timeout=10)
    except (KeyboardInterrupt, OSError, subprocess.TimeoutExpired):
        pass
    finally:
        # Verify ownership before removing the exact container created here.
        inspect = subprocess.run(["docker", "inspect", "--format", '{{index .Config.Labels "persona.compat.run"}}', name],
                                 capture_output=True, text=True, timeout=10)
        if inspect.returncode == 0 and inspect.stdout.strip() == nonce:
            subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15)
        if child.poll() is None:
            child.terminate()
            try:
                child.wait(timeout=10)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=10)
    return result

if __name__ == "__main__":
    sys.exit(main())
