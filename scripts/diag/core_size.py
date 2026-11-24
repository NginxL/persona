#!/usr/bin/env python3
"""How big Persona Core is at a git ref (#13978, docs/architecture/core-and-modules.md "How to build and run the core alone").

Prints, for each ref:
- core non-test Go lines: every *.go file under server/ that is not a _test.go file, server/internal/modules/ left out;
- each module's Go lines under server/internal/modules/<id>/ (non-test and test; `all` is the composition root);
- the stripped sizes of personad, persona-hub and persona-manager built by default and with -tags persona_core_only, with the same
  flags (go build -trimpath -ldflags '-s -w').

    python3 scripts/diag/core_size.py [--no-build] [--json] [ref …]   (default: HEAD)

Each ref is read with git archive into a temporary directory: the checkout and its worktrees are not touched.
"""
import argparse
import io
import json
import os
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PROGRAMS = ("personad", "persona-hub", "persona-manager")
MODULES = "server/internal/modules/"


def lines(data):
    return data.count(b"\n")


def count(ref):
    """Core and per-module line counts at ref, straight from git's objects."""
    archive = subprocess.run(["git", "-C", str(ROOT), "archive", "--format=tar", ref, "server"], check=True,
                             capture_output=True).stdout
    core, core_test, modules = 0, 0, {}
    with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
        for m in tar:
            if not m.isfile() or not m.name.endswith(".go"):
                continue
            n = lines(tar.extractfile(m).read())
            test = m.name.endswith("_test.go")
            if m.name.startswith(MODULES):
                mid = m.name[len(MODULES):].split("/", 1)[0]
                row = modules.setdefault(mid, {"lines": 0, "test_lines": 0})
                row["test_lines" if test else "lines"] += n
            elif test:
                core_test += n
            else:
                core += n
    return {"core_lines": core, "core_test_lines": core_test, "modules": dict(sorted(modules.items()))}


def sizes(ref):
    """Stripped binary sizes at ref, default and core-only."""
    out = {}
    with tempfile.TemporaryDirectory(prefix="persona-core-size.") as tmp:
        src = Path(tmp) / "src"
        src.mkdir()
        archive = subprocess.Popen(["git", "-C", str(ROOT), "archive", "--format=tar", ref, "server", "sdk/go"],
                                   stdout=subprocess.PIPE)
        subprocess.run(["tar", "-x", "-C", str(src)], stdin=archive.stdout, check=True)
        if archive.wait() != 0:
            raise SystemExit(f"git archive {ref} failed")
        env = dict(os.environ, GOFLAGS="")  # the flags below only, the same for both builds
        for label, tags in (("default", []), ("core_only", ["-tags", "persona_core_only"])):
            bindir = Path(tmp) / label
            args = ["go", "build", "-trimpath", "-ldflags", "-s -w", *tags, "-o", str(bindir) + "/",
                    *[f"./cmd/{p}" for p in PROGRAMS]]
            r = subprocess.run(args, cwd=src / "server", env=env, capture_output=True, text=True, timeout=1800)
            if r.returncode != 0:
                raise SystemExit(f"{ref}: {' '.join(args)}\n{r.stderr[-3000:]}")
            out[label] = {p: (bindir / p).stat().st_size for p in PROGRAMS}
    return out


def mib(n):
    return f"{n / 1048576:.1f} MiB"


def show(ref, commit, r):
    print(f"{ref} ({commit[:9]})")
    print(f"  {'core non-test Go lines':<24} {r['core_lines']:>9,}   (core test lines {r['core_test_lines']:,})")
    for mid, row in r["modules"].items():
        name = "modules/all (root)" if mid == "all" else "module " + mid
        print(f"  {name:<24} {row['lines']:>9,}   (test lines {row['test_lines']:,})")
    if not r["modules"]:
        print(f"  {'modules':<24} {'none':>9}")
    if "sizes" in r:
        d, c = r["sizes"]["default"], r["sizes"]["core_only"]
        for p in PROGRAMS:
            print(f"  {p:<16} default {mib(d[p]):>10}   core-only {mib(c[p]):>10}   "
                  f"(core-only {(d[p] - c[p]) / 1048576:.1f} MiB smaller; {d[p]:,} / {c[p]:,} bytes)")


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("refs", nargs="*", default=["HEAD"])
    ap.add_argument("--no-build", action="store_true", help="lines only, no binaries")
    ap.add_argument("--json", action="store_true")
    a = ap.parse_args()
    results = {}
    for ref in a.refs:
        commit = subprocess.run(["git", "-C", str(ROOT), "rev-parse", "--verify", ref + "^{commit}"], check=True,
                                capture_output=True, text=True).stdout.strip()
        r = count(commit)
        if not a.no_build:
            r["sizes"] = sizes(commit)
        r["commit"] = commit
        results[ref] = r
        if not a.json:
            show(ref, commit, r)
    if a.json:
        json.dump(results, sys.stdout, indent=2)
        print()
    return 0


if __name__ == "__main__":
    sys.exit(main())
