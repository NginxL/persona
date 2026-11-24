#!/usr/bin/env python3
"""The checks a change needs, for a PR's gate: only the affected Go packages and the fast checks its paths select.

The full CI lanes (scripts/ci/server.sh, web.sh, scripts.sh) run everything and take most of an hour one after another
on one machine; a PR that edits one Go package needs that package, what imports it, and the repository guards. This
works the change out from the diff against the merge base:

- Go: the server packages (and sdk/go packages) with a changed file, then every server package that imports one of
  them directly or through others, then every package whose tests import any of those. A changed file that is not Go
  (testdata, an embedded file, a migration) counts for the package whose directory holds it. The packages whose
  tests read a changed file outside the module (a fixture, schema, doc or client source) come from check-fast's plan
  (scripts/test/check-fast.sh: its "server go test ./<group>/..." lines), as does whether the repo-wide guard tests
  run. These packages are tested against Postgres the way scripts/ci/server-shards.py tests a shard (the partitioned
  packages in their partitions), and vetted for Linux and Windows.
- Everything else (privacy scan, the scripts suites a path selects, web tests vitest related picks, site, sdk/ts,
  integrations, the ratchets) is check-fast's own plan for the same diff, run by check-fast with --no-server-go.
- A change to what decides or runs the checks themselves (FULL: the CI scripts, the workflows, go.mod, the test
  environment helpers) runs the full lanes instead, as does a change check-fast cannot plan.

    python3 scripts/ci/affected.py plan [--base REF] [--changed-from FILE|-] [--json]
    python3 scripts/ci/affected.py packages [--base REF] [--changed-from FILE|-]   # the Go packages, one per line
    python3 scripts/ci/affected.py gates [--base REF] [--changed-from FILE|-]      # the gate groups to submit

`gates` names the existing gate groups (server-build, server-managers, server-node, server-other, web, scripts,
runtimes) the change needs, for a gate that runs whole lanes: the server shards that hold an affected package
(scripts/ci/server-shards.py's split), web and runtimes when CI's lane selection picks them, scripts when check-fast
plans any check beyond the server's Go ones; `all` for a change that runs the full lanes. The gate's own `affected`
group (scripts/ci/affected.sh) narrows further, to the packages.

scripts/ci/affected.sh runs the plan (docs/operations/testing.md, "Affected checks").
"""
import argparse
import fnmatch
import json
import os
from pathlib import Path
import posixpath
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
SERVER_MODULE = "github.com/nwverse/persona/server"
SDK_MODULE = "github.com/nwverse/persona/sdk/go"

# Paths whose change runs the full lanes: what selects or runs the checks, the module definitions and build tags,
# and the helpers every server test runs through. A wrong selection here would hide a failure, so they run it all.
FULL = (
    "scripts/ci/*",
    ".github/workflows/*",
    "server/go.mod", "server/go.sum", "sdk/go/go.mod", "sdk/go/go.sum", "server/build-tags.txt",
    "scripts/test/check-fast.sh", "scripts/test/go_partitions.py", "scripts/test/go_test_partitioned.py",
    "scripts/test/server_test_env.py", "scripts/test/go_trimpath.sh", "scripts/heavy_slot.py",
)

# The Go test groups check-fast plans ("server go test ./internal/hub/...") and its other server lines.
_GROUP = re.compile(r"^\s*queued: server go test \./(\S+)/\.\.\.$")
_GUARDS = "queued: server repo-wide guards"
_BUILD = "queued: server go build ./..."


def git(*args, cwd=ROOT):
    return subprocess.check_output(["git", *args], cwd=cwd, text=True, timeout=120)


def merge_base(base):
    return git("merge-base", "HEAD", base).strip()


def changed_files(base):
    """Every path the change adds, edits or deletes since the merge base, a rename's old and new paths both."""
    out = git("diff", "--name-only", "--no-renames", merge_base(base), "HEAD")
    return sorted({line for line in out.splitlines() if line})


def full_reason(paths):
    for path in paths:
        for pattern in FULL:
            if fnmatch.fnmatchcase(path, pattern):
                return f"{path} decides or runs the checks ({pattern})"
    return ""


def go_graph(root=ROOT):
    """{import path: (directory relative to the repository, imports, test imports)} for the server module's packages."""
    fmt = "{{.ImportPath}}\t{{.Dir}}\t{{join .Imports \",\"}}\t{{join .TestImports \",\"}},{{join .XTestImports \",\"}}"
    out = subprocess.check_output(["go", "list", "-e", "-f", fmt, "./..."], cwd=root / "server", text=True, timeout=300)
    graph = {}
    for line in out.splitlines():
        path, directory, imports, tests = line.split("\t")
        rel = os.path.relpath(directory, root).replace(os.sep, "/")
        graph[path] = (rel, {i for i in imports.split(",") if i}, {i for i in tests.split(",") if i})
    return graph


def owning_packages(paths, graph):
    """The import paths the changed files belong to: the package whose directory is the file's nearest ancestor (a
    server package, or an sdk/go package by its directory). A file in no package's directory counts for none."""
    dirs = {rel: path for path, (rel, _, _) in graph.items()}
    found = set()
    for changed in paths:
        if changed.startswith("sdk/go/"):
            directory = posixpath.dirname(changed)
            found.add(SDK_MODULE + directory[len("sdk/go"):] if directory != "sdk/go" else SDK_MODULE)
            continue
        if not changed.startswith("server/"):
            continue
        directory = posixpath.dirname(changed)
        while directory and directory != ".":
            if directory in dirs:
                found.add(dirs[directory])
                break
            directory = posixpath.dirname(directory)
    return found


def affected_packages(changed_pkgs, graph):
    """The server packages whose build or tests reach a changed package: the reverse import closure of changed_pkgs,
    then each package whose tests import one of those (a test import of a test-only package changes nothing else)."""
    importers = {}
    for path, (_, imports, _) in graph.items():
        for imported in imports:
            importers.setdefault(imported, set()).add(path)
    reached, todo = set(changed_pkgs), list(changed_pkgs)
    while todo:
        for importer in importers.get(todo.pop(), ()):
            if importer not in reached:
                reached.add(importer)
                todo.append(importer)
    tested = {path for path in graph if path in reached}
    tested |= {path for path, (_, _, tests) in graph.items() if tests & reached}
    return tested


def expand_groups(groups, graph):
    """check-fast's ./<group>/... patterns as the server packages under them."""
    found = set()
    for group in groups:
        prefix = SERVER_MODULE + "/" + group.strip("/")
        found |= {path for path in graph if path == prefix or path.startswith(prefix + "/")}
    return found


def check_fast_plan(changed, root=ROOT):
    """check-fast's dry-run plan for the paths: (its lines, the Go groups, guards wanted, build wanted, error). A dry
    run runs no check, so it takes no heavy-build slot (affected.sh runs the plan with PERSONA_HEAVY_SLOTS=0)."""
    result = subprocess.run(["bash", "scripts/test/check-fast.sh", "--dry-run", "--changed-from", "-"],
                            cwd=root, input="\n".join(changed) + "\n", capture_output=True, text=True, timeout=300)
    lines = (result.stdout + result.stderr).splitlines()
    if result.returncode != 0:
        return lines, [], False, False, f"check-fast could not plan the change (exit {result.returncode})"
    groups = [m.group(1) for m in map(_GROUP.match, lines) if m]
    return lines, groups, any(line.strip() == _GUARDS for line in lines), any(line.strip() == _BUILD for line in lines), ""


def make_plan(changed, root=ROOT, graph=None, fast=None):
    plan = {"changed": changed, "full": False, "reason": "", "go_packages": [], "go_guards": False,
            "check_fast": [], "lanes": []}
    if not changed:
        plan["reason"] = "no change against the base"
        return plan
    reason = full_reason(changed)
    if reason:
        plan.update(full=True, reason=reason)
        return plan
    lines, groups, guards, build, error = fast if fast is not None else check_fast_plan(changed, root)
    plan["check_fast"] = [line.strip() for line in lines if line.strip().startswith(("queued:", "SKIP", "INFO"))]
    if error:
        plan.update(full=True, reason=error)
        return plan
    if build:
        plan.update(full=True, reason="a server change no package group covers (check-fast: server go build ./...)")
        return plan
    graph = graph if graph is not None else go_graph(root)
    owned = owning_packages(changed, graph)
    packages = affected_packages(owned, graph) | expand_groups(groups, graph)
    plan["go_packages"] = sorted(packages)
    plan["go_guards"] = guards
    sys.path.insert(0, str(root / "scripts/ci"))
    import paths as lanes  # noqa: E402  (CI's lane selection, for the report)
    selected = lanes.select(changed)
    plan["lanes"] = [lane for lane in lanes.LANES if selected[lane]]
    return plan


def gate_groups(plan, root=ROOT):
    """The gate groups that cover the plan, in the gate's order; ["all"] for a full run, [] for nothing."""
    if plan["full"]:
        return ["all"]
    if not plan["changed"]:
        return []
    import importlib.util
    spec = importlib.util.spec_from_file_location("server_shards", root / "scripts/ci/server-shards.py")
    shards = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(shards)
    wanted = set()
    if plan["go_packages"] or plan["go_guards"]:
        wanted.add("server-build")
        wanted |= {"server-" + shards.shard_for(package) for package in plan["go_packages"]}
        if plan["go_guards"]:
            # The guard packages and tests live in the managers (hub, assistant) and other shards.
            wanted |= {"server-managers", "server-other"}
    elif "server" in plan["lanes"]:
        wanted.add("server-build")
    if "web" in plan["lanes"]:
        wanted.add("web")
    if "runtimes" in plan["lanes"]:
        wanted.add("runtimes")
    if any(line.startswith("queued:") and "server go" not in line and "repo-wide guards" not in line
           and "gofmt" not in line for line in plan["check_fast"]):
        wanted.add("scripts")
    order = ["server-build", "server-managers", "server-node", "server-other", "web", "scripts", "runtimes"]
    return [group for group in order if group in wanted]


def read_changed(args):
    if args.changed_from:
        text = sys.stdin.read() if args.changed_from == "-" else Path(args.changed_from).read_text()
        return sorted({line.strip() for line in text.splitlines() if line.strip()})
    return changed_files(args.base)


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("command", choices=("plan", "packages", "gates"))
    parser.add_argument("--base", default=os.environ.get("CI_AFFECTED_BASE", "origin/main"))
    parser.add_argument("--changed-from", help="read the changed paths from this file (- for stdin), not the diff")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()
    plan = make_plan(read_changed(args))
    if args.command == "packages":
        print("\n".join(plan["go_packages"]))
        return 0
    if args.command == "gates":
        print(" ".join(gate_groups(plan)))
        return 0
    if args.json:
        print(json.dumps(plan, indent=1))
        return 0
    print(f"Affected plan: {len(plan['changed'])} changed path(s)")
    if plan["full"] or not plan["changed"]:
        print(f"  full lanes: {plan['reason']}" if plan["full"] else f"  nothing to check: {plan['reason']}")
        return 0
    print(f"  CI lanes the paths select: {' '.join(plan['lanes']) or 'none'}")
    print(f"  Go packages to test with Postgres: {len(plan['go_packages'])}"
          + (" (and the repo-wide guard tests)" if plan["go_guards"] else ""))
    for package in plan["go_packages"]:
        print(f"    {package}")
    print("  check-fast (with --no-server-go):")
    for line in plan["check_fast"]:
        if "server go" not in line and "repo-wide guards" not in line:
            print(f"    {line}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
