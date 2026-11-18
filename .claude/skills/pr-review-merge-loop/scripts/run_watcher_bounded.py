#!/usr/bin/python3
"""Optionally limit the trusted watcher process using a private JSON config.

Usage: run_watcher_bounded.py CONFIG WATCHER [WATCHER_ARG ...]
"""
import json
import os
from pathlib import Path
import resource
import sys


def main():
    if len(sys.argv) < 3:
        raise SystemExit("usage: run_watcher_bounded.py CONFIG WATCHER [ARG ...]")
    config = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
    first = config.get("first_cpu", 0)
    count = config.get("cpu_count", 0)
    memory = config.get("address_space_bytes", 0)
    if any(not isinstance(value, int) or isinstance(value, bool) or value < 0
           for value in (first, count, memory)):
        raise SystemExit("watcher resource values must be non-negative integers")
    if count and hasattr(os, "sched_setaffinity"):
        os.sched_setaffinity(0, set(range(first, first + count)))
    if memory:
        resource.setrlimit(resource.RLIMIT_AS, (memory, memory))
    watcher = str(Path(sys.argv[2]).expanduser().resolve())
    os.execv("/usr/bin/python3", ["/usr/bin/python3", "-I", watcher, *sys.argv[3:]])


if __name__ == "__main__":
    main()
