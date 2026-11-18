#!/usr/bin/python3
"""Optionally bound a trusted command to CPU groups and process limits.

Usage: pr_watch_resources.py CONFIG [SLOT --] COMMAND [ARG ...]

CONFIG is a maintainer-owned JSON file. This wrapper does not impose a cgroup
RSS limit: RLIMIT_AS is a per-process virtual-address-space ceiling.
"""
import fcntl
import json
import os
from pathlib import Path
import resource
import sys

_lease = None


def positive_int(config, name, default=0):
    value = config.get(name, default)
    if not isinstance(value, int) or isinstance(value, bool) or value < 0:
        raise SystemExit(name + " must be a non-negative integer")
    return value


def reserve_slot(config, requested=None):
    global _lease
    count = positive_int(config, "slots")
    cpus_per_slot = positive_int(config, "cpus_per_slot")
    first_cpu = positive_int(config, "first_cpu")
    if not count or not cpus_per_slot:
        raise SystemExit("slots and cpus_per_slot must be positive")
    if requested is not None and not 0 <= requested < count:
        raise SystemExit("requested slot is outside the configured range")
    root = Path(config["lock_directory"]).expanduser()
    if not root.is_absolute():
        raise SystemExit("lock_directory must be absolute")
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    for slot in range(count):
        if requested is not None and slot != requested:
            continue
        lease = (root / ("slot-%02d.lock" % slot)).open("a+")
        try:
            fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            lease.close()
            continue
        _lease = lease
        # Python opens files close-on-exec; the lock must outlive exec.
        os.set_inheritable(lease.fileno(), True)
        lease.seek(0)
        lease.truncate()
        lease.write(str(os.getpid()))
        lease.flush()
        return set(range(first_cpu + slot * cpus_per_slot,
                         first_cpu + (slot + 1) * cpus_per_slot))
    # Preserve the CPU bound instead of falling back to unlimited CPUs.
    return set(range(first_cpu + (count - 1) * cpus_per_slot,
                     first_cpu + count * cpus_per_slot))


def main():
    arguments = sys.argv[1:]
    requested = None
    if len(arguments) >= 3 and arguments[1].isdigit() and arguments[2] == "--":
        requested = int(arguments[1])
        arguments = [arguments[0], *arguments[3:]]
    if len(arguments) < 2:
        raise SystemExit("usage: pr_watch_resources.py CONFIG [SLOT --] COMMAND [ARG ...]")
    config = json.loads(Path(arguments[0]).read_text(encoding="utf-8"))
    if not isinstance(config, dict):
        raise SystemExit("resource configuration must be an object")
    cpus = reserve_slot(config, requested)
    if hasattr(os, "sched_setaffinity"):
        os.sched_setaffinity(0, cpus)
    memory = positive_int(config, "address_space_bytes")
    cpu_seconds = positive_int(config, "cpu_seconds")
    if memory:
        resource.setrlimit(resource.RLIMIT_AS, (memory, memory))
    if cpu_seconds:
        resource.setrlimit(resource.RLIMIT_CPU, (cpu_seconds, cpu_seconds))
    os.execvp(arguments[1], arguments[1:])


if __name__ == "__main__":
    main()
