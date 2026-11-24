#!/usr/bin/env python3
"""List the Go files gofmt would change, and fail when there is one.

    python3 scripts/ci/gofmt.py server sdk/go                # every .go file under them (CI's Go formatting step)
    python3 scripts/ci/gofmt.py server/internal/foo/foo.go   # just these files (check-fast's gofmt job, #12894)

A directory stands for every .go file below it, testdata included; a file stands for itself. A path that is neither is
an error, so a list of changed files that names a deleted one fails instead of passing on nothing. gofmt -l runs on 100
files at a time; each file whose formatting differs gets a line `gofmt: needs formatting (run gofmt -w): <file>`.
"""
from pathlib import Path
import subprocess
import sys


def go_files(paths):
    """The .go files under each directory, and each file itself, sorted and once; the paths that are neither."""
    files, missing = set(), []
    for name in paths:
        path = Path(name)
        if path.is_dir():
            files.update(str(found) for found in path.rglob("*.go"))
        elif path.is_file():
            files.add(name)
        else:
            missing.append(name)
    return sorted(files), missing


def main(argv=None):
    files, missing = go_files(sys.argv[1:] if argv is None else argv)
    failed = False
    for name in missing:
        print("gofmt.py: no such file or directory: %s" % name, file=sys.stderr)
        failed = True
    for offset in range(0, len(files), 100):
        result = subprocess.run(["gofmt", "-l", *files[offset:offset + 100]], capture_output=True, text=True,
                                timeout=30)
        if result.stdout or result.returncode:
            # gofmt -l prints bare file names; say what they are, so a failure summary has text to show.
            for name in result.stdout.splitlines():
                print(f"gofmt: needs formatting (run gofmt -w): {name}")
            print(result.stderr, end="")
            failed = True
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
