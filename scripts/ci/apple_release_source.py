#!/usr/bin/env python3
"""Snapshot an Apple release checkout and derive its package-resolution input without editing the tracked lockfile."""
from __future__ import annotations

import argparse
import os
from pathlib import Path
import shutil
import subprocess
import sys

import apple_resolved


def snapshot(source: Path, destination: Path, meta: str) -> None:
    """Copy the exact tracked source into a fresh directory, then stage the lockfile for this Meta configuration.

    Untracked local configuration is deliberately excluded. A release can opt into this staging explicitly; without it
    the existing developer-checkout release behavior is unchanged.
    """
    if destination.exists():
        raise ValueError("release source snapshot must be a new empty directory")
    destination.parent.mkdir(parents=True, exist_ok=True)
    # Keep the exact source revision and its history for privacy/version checks. Local edits are applied only to this
    # disposable worktree, while untracked private configuration is never copied.
    subprocess.run(["git", "clone", "--quiet", "--shared", "--no-checkout", str(source), str(destination)], check=True)
    subprocess.run(["git", "-C", str(destination), "reset", "--quiet", "--hard", "HEAD"], check=True)
    subprocess.run(["git", "-C", str(destination), "checkout", "--quiet", "--detach"], check=True)
    diff = subprocess.check_output(["git", "-C", str(source), "diff", "--binary", "HEAD"])
    if diff:
        subprocess.run(["git", "-C", str(destination), "apply", "--whitespace=nowarn", "-"], input=diff, check=True)
    files = subprocess.check_output([
        "git", "-C", str(source), "ls-files", "--others", "--exclude-standard", "-z", "--", ".",
        ":(exclude).ci-cache/**", ":(exclude)config.json", ":(exclude)CLAUDE.local.md",
    ])
    for raw in files.split(b"\0"):
        if not raw:
            continue
        relative = Path(os.fsdecode(raw))
        origin = source / relative
        if not origin.exists() and not origin.is_symlink():
            continue
        target = destination / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        if origin.is_symlink():
            target.symlink_to(os.readlink(origin))
        else:
            shutil.copy2(origin, target)
    tracked = source / "apple/Packages/PersonaSSH/Package.resolved"
    staged = destination / "apple/Packages/PersonaSSH/Package.resolved"
    evidence = destination / "resolved-variant.txt"
    apple_resolved.stage(tracked, staged, meta, evidence)
    print(f"release-source: {destination}")
    print(f"release-resolved: {evidence}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--destination", type=Path, required=True)
    parser.add_argument("--meta", choices=("YES", "NO"), required=True)
    args = parser.parse_args()
    snapshot(args.source.resolve(), args.destination.resolve(), args.meta)


if __name__ == "__main__":
    main()
