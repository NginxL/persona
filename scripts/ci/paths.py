#!/usr/bin/env python3
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

# --- What web/src and its tests read (#10619) ---------------------------------------------------------------------
# The web lane's inputs outside web/ and check-fast's web test selection both come from one scan of the files vitest
# and tsc read: the code under web/src and web/public-share, and every vitest file in web/. Nothing is listed by hand.

_WEB_CODE = re.compile(r"\.[cm]?[jt]sx?$")
_WEB_TEST = re.compile(r"\.(?:test|spec)\.[cm]?[jt]sx?$")
_WEB_SKIP_DIRS = {"node_modules", "dist", "coverage"}
# A path relative to the file, right after its opening quote: "../../../spec/fixtures/x.json", "./frame.js?raw",
# `../examples/${name}.json`. The quote is checked by hand: a pattern that starts at every quote is several times slower.
_RELATIVE = re.compile(r"""\.\.?/[^"'`\s]*""")
# The same path passed element by element: join(__dirname, "..", "..", "spec", "fixtures", "x").
_JOINED = re.compile(r"""["']\.\.["'](?:\s*,\s*["'][^"'\s,()]*["'])+""")
_ELEMENT = re.compile(r"""["']([^"']*)["']""")
_FS_IMPORT = re.compile(r"""["'](?:node:)?fs(?:/promises)?["']""")
# How a quoted fs module name ends ("fs", "node:fs/promises"): looked for as substrings before _FS_IMPORT runs,
# which on every file would take about a third of the scan's time.
_FS_ENDINGS = ('fs"', "fs'", 'fs/promises"', "fs/promises'")
_SCHEMA_REF = re.compile(r'"\$ref"\s*:\s*"([^"#:]+)')
_WILDCARD = re.compile(r"[*?\[{$]")

# Web files that most tests import (lib/store.ts ~825 of ~1,350 test files, lib/api.ts ~1,040, ui/atoms.tsx ~930,
# i18n/ ~1,190, the vitest setup file included) run the whole suite in check-fast: following the imports would select
# nearly all of it and only add the time to walk the graph. So does any change outside web/src (the Vite and
# TypeScript configs, package.json, package-lock.json, the global setup in web/scripts, public/, e2e/), a change to
# a web/src module the Vite configs import, and a change to anything the vitest setup files reach (_setup_reach).
WEB_FULL_SUITE = ("web/src/lib/store.ts", "web/src/lib/api.ts", "web/src/ui/atoms.tsx", "web/src/i18n/*")
WEB_CONFIGS = ("web/vite.config.ts", "web/vite.public.config.ts")
_CONFIG_IMPORT = re.compile(r"""from\s+["'](\./src/[^"']+)["']""")
# A config's test.setupFiles and test.globalSetup: one path or a list, relative to web/.
_SETUP_OPTION = re.compile(r"""\b(?:setupFiles|globalSetup)\s*:\s*(\[[^\]]*\]|["'][^"']*["'])""")
# How an import without its file's extension resolves (TypeScript's bundler resolution): the path as written, with
# an extension, or the index of a directory; an import that names a .js file may mean the .ts or .tsx beside it.
_MODULE_SUFFIXES = ("", ".ts", ".tsx", ".mts", ".js", ".jsx", ".mjs", "/index.ts", "/index.tsx", "/index.js")
# The modules a file imports to build, serve or test through a Vite config (WebFile.build).
_VITE_API = ("vite", "vitest/node")


class WebFile:
    """What one scanned web file reads.

    fs: it reads through node's fs (fs, node:fs, fs/promises, node:fs/promises, readFileSync), so the path can be built
    at run time.
    vite: it reads through Vite, a glob or a query import (?raw, ?url, ?inline, ?worker). Those paths are literal, but
    vitest related follows a dependency only when its id is a file on disk, which a query import's never is.
    build: it imports Vite's or vitest's node API (vite, vitest/node), so it builds or serves through a Vite config
    (scripts/public-share-build.test.mjs). What that build reads, the config's own reads included (vite.public.config.ts
    reads src/styles.css with readFileSync), is in no import of the file.
    named: the repository paths it names relative to itself (a trailing / for a directory), wildcards kept.
    """

    def __init__(self, path, text):
        self.path = path
        self.test = bool(_WEB_TEST.search(path))
        refs = [match.group(0) for match in _RELATIVE.finditer(text)
                if match.start() and text[match.start() - 1] in "\"'`"]
        refs += ["/".join(_ELEMENT.findall(match.group(0))) for match in _JOINED.finditer(text)]
        self.fs = "readFileSync" in text or (any(end in text for end in _FS_ENDINGS) and bool(_FS_IMPORT.search(text)))
        self.vite = "import.meta.glob" in text or any("?" in ref for ref in refs)
        self.build = any(f"{quote}{name}{quote}" in text for name in _VITE_API for quote in "\"'")
        base = posixpath.dirname(path)
        named = set()
        for ref in refs:
            ref = ref.split("?")[0]  # a Vite query (?raw) is not part of the file
            target = posixpath.normpath(posixpath.join(base, ref))
            if target in (".", "..") or target.startswith("../"):
                continue  # the repository root, or outside the repository
            named.add(target + "/" if ref.endswith("/") else target)
        self.named = sorted(named)


def _web_files(root):
    """The web files whose text the scan reads, as repository paths."""
    for directory, subdirs, names in os.walk(root / "web"):
        subdirs[:] = sorted(d for d in subdirs if d not in _WEB_SKIP_DIRS and not d.startswith("."))
        prefix = Path(directory).relative_to(root).as_posix()
        for name in sorted(names):
            path = f"{prefix}/{name}"
            if _WEB_CODE.search(name) and (_WEB_TEST.search(name) or path.startswith(("web/src/", "web/public-share/"))):
                yield path


def _pattern(root, target):
    """A named path as an fnmatch pattern: up to its first wildcard or ${…} part; a directory covers what is in it."""
    wildcard = _WILDCARD.search(target)
    if wildcard:
        return target[:wildcard.start()] + "*"
    if target.endswith("/") or (root / target).is_dir():
        return target.rstrip("/") + "/**"
    return target


def _schema_refs(root, target, seen):
    """A JSON file and, for a JSON Schema, the files its $refs name, followed to the end."""
    if target in seen:
        return
    seen.add(target)
    file = root / target
    if target.endswith(".json") and file.is_file():
        for ref in _SCHEMA_REF.findall(file.read_text(errors="replace")):
            _schema_refs(root, posixpath.normpath(posixpath.join(posixpath.dirname(target), ref)), seen)


def _minimal(patterns):
    """Drop each pattern another one already covers (fnmatch's * matches / too, as in select)."""
    kept = []
    for pattern in sorted(set(patterns)):
        if any(fnmatch.fnmatchcase(pattern, other) for other in kept):
            continue
        kept = [other for other in kept if not fnmatch.fnmatchcase(other, pattern)] + [pattern]
    return sorted(kept)


_SCANS = {}


def web_scan(root=ROOT):
    """Every file vitest and tsc read in web/: web/src, web/public-share and each vitest file elsewhere in web/.

    The repository's own scan is made once per process (FILTERS needs it on import); any other root is scanned anew.
    """
    root = Path(root)
    if root == ROOT and ROOT in _SCANS:
        return _SCANS[ROOT]
    scan = [WebFile(path, (root / path).read_text(errors="replace")) for path in _web_files(root)]
    if root == ROOT:
        _SCANS[ROOT] = scan
    return scan


def _add_read(root, target, found):
    """Add the patterns of one named path to found: a file as itself, with the files a JSON Schema's $refs name; a
    directory, a glob or a path with a ${…} part as everything under its literal part."""
    pattern = _pattern(root, target)
    if pattern == target:
        _schema_refs(root, target, found)
    else:
        found.add(pattern)


def _read_patterns(root, file):
    """The patterns of what a web file reads: each path it names (_add_read)."""
    found = set()
    for target in file.named:
        _add_read(root, target, found)
    return found


def web_inputs(root=ROOT):
    """The files outside web/ that web/src and its tests read, as fnmatch patterns (_read_patterns)."""
    root = Path(root)
    found = set()
    for file in web_scan(root):
        found.update(p for p in _read_patterns(root, file) if not p.startswith("web/"))
    return _minimal(found)


def _config_sources(root):
    """The web/src modules the Vite configs import, without their extension: they shape how every test is built."""
    sources = set()
    for config in WEB_CONFIGS:
        file = root / config
        if file.is_file():
            for ref in _CONFIG_IMPORT.findall(file.read_text(errors="replace")):
                sources.add(posixpath.splitext(posixpath.normpath(posixpath.join("web", ref)))[0])
    return sources


def _module(root, target):
    """The file an import of a named path resolves to (_MODULE_SUFFIXES), or None: not a module, or not there."""
    if target.endswith("/") or _WILDCARD.search(target):
        return None
    candidates = [target + suffix for suffix in _MODULE_SUFFIXES]
    if target.endswith(".js"):
        candidates += [target[:-3] + ".ts", target[:-3] + ".tsx"]
    return next((path for path in candidates if (root / path).is_file()), None)


def _setup_reach(root):
    """What the vitest setup files reach, as fnmatch patterns: each setup file the Vite configs name, everything it
    reads (_add_read), and each module it imports, followed to the end. Every test runs the setup files first, and
    vitest related follows only the test's own imports, so a change to any of these must run the whole suite."""
    scanned = {file.path: file for file in web_scan(root)}
    todo = []
    for config in WEB_CONFIGS:
        file = root / config
        if file.is_file():
            for option in _SETUP_OPTION.findall(file.read_text(errors="replace")):
                todo += [path for name in _ELEMENT.findall(option)
                         if (path := _module(root, posixpath.normpath(posixpath.join("web", name))))]
    reach, seen = set(), set()
    while todo:
        path = todo.pop()
        if path in seen:
            continue
        seen.add(path)
        reach.add(path)
        file = scanned.get(path) or WebFile(path, (root / path).read_text(errors="replace"))
        for target in file.named:
            module = _module(root, target)
            if module and _WEB_CODE.search(module):
                todo.append(module)
            elif module:
                _schema_refs(root, module, reach)  # a JSON or other file it imports, as itself
            else:
                _add_read(root, target, reach)
    return reach


def web_plan(changed, root=ROOT):
    """check-fast's web check for a list of changed repository paths (#10619): (mode, reason, files).

    "none" when nothing changed under web/ or in a file web/src and its tests read; "full" when the whole suite runs
    (WEB_FULL_SUITE, anything in web/ outside web/src, a module the Vite configs import, anything the vitest setup files
    reach); "related" when vitest related runs from files, relative to web/: the changed web/src and outside files,
    every vitest file that reads through fs or Vite or builds through a Vite config (the import graph does not see what
    they read), every other web/src module that reads through fs or builds, and each one that reads through Vite a
    path a changed file matches.
    """
    root = Path(root)
    inputs = web_inputs(root)
    web = [path for path in changed if path.startswith("web/")]
    outside = [path for path in changed if not path.startswith("web/")
               and any(fnmatch.fnmatchcase(path, pattern) for pattern in inputs)]
    if not web and not outside:
        return "none", "no change under web/ or in a file web/src and its tests read", []
    config = _config_sources(root)
    for path in web:
        if not path.startswith("web/src/"):
            return "full", f"{path} is outside web/src", []
        if any(fnmatch.fnmatchcase(path, pattern) for pattern in WEB_FULL_SUITE):
            return "full", f"{path} is a shared entry point", []
        if posixpath.splitext(path)[0] in config:
            return "full", f"{path} is part of the Vite config", []
    setup = _setup_reach(root)
    for path in web + outside:
        if any(fnmatch.fnmatchcase(path, pattern) for pattern in setup):
            return "full", f"{path} is part of the vitest setup", []
    readers = []
    for file in web_scan(root):
        always = file.fs or file.build or (file.test and file.vite)
        if always or (file.vite and any(fnmatch.fnmatchcase(path, pattern)
                                        for pattern in _read_patterns(root, file) for path in changed)):
            readers.append(file.path)
    files = list(dict.fromkeys(web + outside + sorted(readers)))
    reason = f"{len(web) + len(outside)} changed file(s) and {len(readers)} that read files from disk"
    return "related", reason, [posixpath.relpath(path, "web") for path in files]


# Go compatibility tests read these client contracts and interaction entry points,
# not unrelated Mac/Android screens or their build toolchains.
SERVER_CLIENT_INPUTS = [
    "apple/Shared/**", "apple/Persona/Views/Interactions/**", "apple/Persona/App/**",
    "apple/Persona/Intents/**", "apple/PersonaWatch/**", "apple/PersonaWidgets/**",
    "apple/LiveActivity/**", "apple/PersonaTests/**", "android/core/contracts/**",
]

FILTERS = {
    "server": ["server/**", "sdk/go/**", "sdk/ts/src/**", "schema/**", "spec/**", "integrations/**", "docs/assistant/prompts.md", "docs/protocol/**", "scripts/test/server_test_env.py", "scripts/test/go_trimpath.sh", "scripts/ci/*server*", "scripts/ci/postgres.py", "scripts/ci/gofmt.py", ".github/workflows/server-test.yml"] + SERVER_CLIENT_INPUTS,
    # The files outside web/ that web/src and its tests read are derived, never listed (web_inputs).
    "web": ["web/**", *web_inputs(), "scripts/ci/web.sh", ".github/workflows/web-test.yml"],
    "android": ["android/**", "native/app-core/**", "schema/**", "spec/**", "scripts/app/*android*", "scripts/ci/*android*", ".github/workflows/android-test.yml"],
    "apple": ["apple/**", "native/app-core/**", "schema/**", "spec/**", "scripts/app/sim-boot.sh", "scripts/i18n/**", "scripts/ci/apple*", "scripts/test/isolation-lint*", "scripts/test/isolation-lint/**", "scripts/test/apple_isolation_baseline.json", "scripts/test/test_apple_no_callkit.py", "scripts/release/check_no_callkit.py", ".github/workflows/apple-test.yml"],
    "core": ["native/app-core/**", "spec/fixtures/continuity/**", "android/core/rustcore/src/main/kotlin/**", "apple/Packages/PersonaCore/**", "harmony/entry/src/main/ets/core/rustcore/**", "harmony/entry/src/main/ets/core/PersonaCore.ts",
                   "harmony/entry/src/main/cpp/types/libpersona_core/**", "harmony/tests/rustcore-*.test.mjs", "harmony/tests/core-host/**",
                   "scripts/ci/core.sh", ".github/workflows/core-test.yml"],
    "runtimes": ["integrations/**", "sdk/ts/**", "schema/**", "spec/**", "scripts/ci/runtimes.sh", ".github/workflows/runtimes-test.yml"],
    # The lane builds and loads the web client, drives the synthetic hub and compiles the shared bridge type.
    "desktop": ["desktop/**", "web/**", "scripts/mock/mock_hub.py", "scripts/mock/mock_domains.py", "scripts/mock/desktops_mock.py",
                  "scripts/mock/stream_trace.py", "scripts/mock/test_desktops_mock.py", "spec/desktop-bridge.d.ts",
                  # web/scripts/build-pake.mjs compiles these Go sources into the sign-in WebAssembly.
                  "server/cmd/password-wasm/**", "server/internal/passwordpake/**", "server/go.mod", "server/go.sum", "scripts/ci/desktop*.sh", ".github/workflows/desktop-test.yml"],
    "scripts": ["**"],
    "image": ["Dockerfile", ".dockerignore", "deploy/docker/**", "server/**", "sdk/go/**", "web/**", "integrations/**", "schema/extension/**", "spec/fixtures/**", "scripts/ci/hub-image.sh", ".github/workflows/release.yml", ".github/workflows/hub-image.yml"],
}
SHARED = [".github/workflows/ci.yml", "scripts/ci/common.sh", "scripts/ci/run.py", "scripts/ci/paths.py", "scripts/ci/test_ci.py"]


def select(paths, all_platforms=False):
    shared = all_platforms or any(fnmatch.fnmatchcase(path, pattern) for path in paths for pattern in SHARED)
    return {platform: shared or any(fnmatch.fnmatchcase(path, pattern) for path in paths for pattern in patterns)
            for platform, patterns in FILTERS.items()}


LANES = tuple(name for name in FILTERS if name != "image")


def head_parents():
    """HEAD's parents, first parent first."""
    return subprocess.check_output(["git", "rev-parse", "HEAD^@"], timeout=30).decode().split()


def resolve_base(base=None, event=None, parents=()):
    if base:
        return base
    event = event or {}
    if "pull_request" in event:
        pull = event["pull_request"]
        # actions/checkout checks out refs/pull/<n>/merge: first parent the base branch as it is now, second the PR
        # head. base.sha is where the PR was opened and does not move as the base branch advances; comparing with it
        # would count every commit that landed since as the PR's.
        head = pull.get("head", {}).get("sha")
        if head and len(parents) == 2 and parents[1] == head:
            return parents[0]
        return pull["base"]["sha"]
    if "merge_group" in event:
        return event["merge_group"]["base_sha"]
    return "origin/main"


def select_lanes(paths, lanes="auto", all_platforms=False):
    if all_platforms or lanes == "all":
        return select([], True)
    if lanes == "auto":
        return select(paths)
    requested = {name.strip() for name in lanes.split(",")}
    unknown = requested - set(LANES)
    if unknown:
        raise ValueError(f"Unknown lanes: {', '.join(sorted(unknown))}; use auto, all, or {','.join(LANES)}")
    return {name: name in requested for name in FILTERS}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base")
    parser.add_argument("--all", action="store_true")
    parser.add_argument("--lanes", default="auto", help="auto, all, or comma-separated " + ",".join(LANES))
    parser.add_argument("--print-base", action="store_true")
    parser.add_argument("--web-inputs", action="store_true", help="print the files outside web/ that web/src and its tests read")
    parser.add_argument("--web-plan", action="store_true",
                        help="read changed paths from stdin, one per line, and print check-fast's web check: "
                             "a first line '<none|full|related> <reason>', then vitest related's files relative to web/")
    args = parser.parse_args()
    if args.web_inputs:
        print("\n".join(web_inputs()))
        return
    if args.web_plan:
        mode, reason, files = web_plan([line for line in sys.stdin.read().splitlines() if line])
        print("\n".join([f"{mode} {reason}", *files]))
        return
    event_path = os.environ.get("GITHUB_EVENT_PATH")
    event = json.loads(Path(event_path).read_text()) if event_path else {}
    parents = head_parents() if not args.base and "pull_request" in event else ()
    base = resolve_base(args.base, event, parents)
    if args.print_base:
        # Resolve to a commit before exposing it to guards or path selection.
        sha = subprocess.check_output(["git", "rev-parse", "--verify", "--end-of-options", f"{base}^{{commit}}"], timeout=30).decode().strip()
        print(sha)
        if os.environ.get("GITHUB_OUTPUT"):
            with open(os.environ["GITHUB_OUTPUT"], "a") as output:
                output.write(f"base={sha}\n")
        return
    paths = []
    if not args.all and args.lanes == "auto":
        raw = subprocess.check_output(["git", "diff", "--name-only", "--no-renames", "-z", f"{base}...HEAD"], timeout=30)
        paths = raw.decode().split("\0")[:-1]
    try:
        selected = select_lanes(paths, args.lanes, args.all)
    except ValueError as error:
        parser.error(str(error))
    print(json.dumps(selected, sort_keys=True))
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            for platform, needed in selected.items():
                output.write(f"{platform}={str(needed).lower()}\n")


if __name__ == "__main__":
    main()
