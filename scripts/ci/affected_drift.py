#!/usr/bin/env python3
"""How the CI lanes, check-fast and the PR watcher map a changed path, compared path by path (#11026).

Three places decide what a change affects: scripts/ci/paths.py picks CI's lanes, scripts/test/check-fast.sh picks the
local fast checks, and the PR watcher (scripts/operations/pr_watch_verify.py) picks the lanes it verifies a PR with.
The watcher reads paths.py itself; check-fast reads its own table (scripts/ci/check_fast_map.py) and adds what it finds
at run time. This compares the three for one path at a time and names every disagreement that ALLOWED does not
explain. It checks two things:

- Lanes. A check-fast check counts for a CI lane when it runs a fast part of that lane's work (FAST_LANES): the web
  check for web, Go tests and builds for server, the integration and sdk/ts tests for runtimes. A CI lane check-fast
  runs no part of must be named in its plan (the INFO line it reads from paths.py --lanes-for, or the apple SKIP
  line). The scripts lane is CI's "**", so every path selects it.
- Readers. Where CI runs a whole lane and check-fast runs only what reads the changed file, the readers are worked out
  here from the tests themselves (Readers), without check-fast, and each must be in check-fast's plan: the Go packages
  whose tests name a client source, fixture, schema, runtime file or protocol doc; the integrations/<pkg> and sdk/ts
  packages that name a fixture or schema; the scripts/**/test_*.py suites that import a scripts module. CI must
  select the lane of each reader too (a client source a Go test reads is in the server lane's inputs).

    python3 scripts/ci/affected_drift.py                 # every tracked file: a summary by top-level area
    python3 scripts/ci/affected_drift.py --table out.tsv # also the per-path table
    python3 scripts/ci/affected_drift.py path ...        # only these paths

check-fast plans with stub go, node and bun on PATH, so the plan does not depend on what this machine has installed.
"""
import argparse
import bisect
from collections import Counter, defaultdict, namedtuple
import fnmatch
from functools import cached_property
import os
from pathlib import Path
import posixpath
import re
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts/ci"))
sys.path.insert(0, str(ROOT / "scripts/operations"))
import paths  # noqa: E402
import pr_watch_verify  # noqa: E402

# CI's platform lanes. scripts is "**" in CI, so it is left out of the lane comparison.
LANES = ("server", "web", "android", "apple", "core", "runtimes", "desktop", "image")

# The check-fast lines that cover a lane: a queued check that runs a fast part of the lane's work. A SKIP or INFO line
# runs nothing, so it covers nothing.
FAST_LANES = (
    ("web", re.compile(r"  queued: web$")),
    ("server", re.compile(r"  queued: (server go test |server go build |server repo-wide guards$|sdk/go go test$)")),
    ("runtimes", re.compile(r"  queued: (integrations/|sdk/ts \(bun\)$)")),
)
# The lines that name a CI lane check-fast does not run: its INFO line (from paths.py --lanes-for) and the apple SKIP.
_NOT_RUN = re.compile(r"INFO  CI lanes this change selects that check-fast does not run: ([a-z, ]+) \(")
_APPLE_SKIP = "SKIP  apple/** builds and tests"

# One disagreement of a path. side: "ci" CI selects the lane and check-fast runs no part of it, "fast" the other way
# round, "watcher" the watcher does not run a lane CI selects, "named" check-fast neither runs nor names a lane CI
# selects (or names one it runs, or one CI does not select), "reader" a reader worked out by Readers is not in
# check-fast's plan, "ci-reader" CI does not select the lane of such a reader (detail: the reader's check-fast check).
# named and reader differences cannot be listed in ALLOWED.
Disagreement = namedtuple("Disagreement", "lane side reason detail")

# The differences that are meant, each with the reason. A row is (lane, side, patterns, reason). A path is explained
# when it matches one of the patterns (fnmatch: * matches / too).
_NAMED = "check-fast names it instead (the apple SKIP line, or the INFO line of the CI lanes it does not run)"
_READERS = "; the reader check holds check-fast to every one"
_SAMPLE_CONSUMERS = ("apple/Persona/Views/Machines/MachinesView.swift",
                     "android/feature/machines/src/main/kotlin/persona/feature/machines/MachineConcern.kt")
_SAMPLE_REASON = ("internal/hub's wire samples name these client decoders as the samples' consumers without reading "
                  "them; check-fast runs internal/hub for them, CI's server lane does not watch them")
ALLOWED = (
    # Lanes check-fast never runs: they need a toolchain or a build that takes minutes.
    ("android", "ci", ("**",), "check-fast never runs Gradle; " + _NAMED),
    ("apple", "ci", ("**",), "check-fast never runs Xcode; " + _NAMED),
    ("core", "ci", ("**",), "check-fast never runs cargo; " + _NAMED),
    ("desktop", "ci", ("**",), "check-fast never installs Electron; " + _NAMED),
    ("image", "ci", ("**",), "check-fast never builds the hub image (CI builds it for releases)"),
    ("image", "watcher", ("**",), "the watcher never builds the hub image (CI builds it for releases)"),
    ("apple", "watcher", ("**",), "the watcher has no Mac; it records a policy line for the apple lane instead"),
    # CI watches whole directories that tests outside the lane's tree read; check-fast runs the tests that read the
    # changed file, and names the lane when none does.
    ("server", "ci", tuple(paths.SERVER_CLIENT_INPUTS),
     "CI runs the server lane for every client contract source; check-fast runs the Go tests that read the file"
     + _READERS),
    ("server", "ci", ("spec/**",),
     "CI runs the server lane for all of spec/; check-fast runs the Go tests that read the changed fixture" + _READERS),
    ("server", "ci", ("docs/protocol/**", "docs/assistant/prompts.md"),
     "CI runs the server lane for the protocol docs and the prompt catalogue; check-fast runs the Go tests that "
     "mention the changed doc" + _READERS),
    ("server", "fast", ("docs/**",),
     "check-fast runs the Go tests that mention a changed doc outside a comment line, in a message or an example "
     "too; CI's server lane watches the docs Go tests read"),
    ("server", "fast", _SAMPLE_CONSUMERS, _SAMPLE_REASON),
    ("server", "ci-reader", _SAMPLE_CONSUMERS, _SAMPLE_REASON),
    ("runtimes", "ci", ("schema/**", "spec/**"),
     "CI runs the runtimes lane for all fixtures and schemas; check-fast runs the runtime packages that name the path"
     + _READERS),
    # A lane's own runner and workflow: CI runs the lane, check-fast the runner's unittests.
    ("server", "ci", ("scripts/ci/*", "scripts/test/go_trimpath.sh", ".github/workflows/*"),
     "the lane's own runner and workflow: their unittests run instead"),
    ("web", "ci", ("scripts/ci/web.sh", ".github/workflows/*"), "the lane's own runner and workflow"),
    ("runtimes", "ci", ("scripts/ci/runtimes.sh", ".github/workflows/*"), "the lane's own runner and workflow"),
    # CI's selection itself changed: CI runs every lane.
    ("server", "ci", tuple(paths.SHARED), "CI's selection itself: every lane runs"),
    ("web", "ci", tuple(paths.SHARED), "CI's selection itself: every lane runs"),
    ("runtimes", "ci", tuple(paths.SHARED), "CI's selection itself: every lane runs"),
)

# The server lane's inputs outside server/ whose Go readers check-fast runs instead of the lane. sdk/go and sdk/ts/src
# are left out: check-fast runs sdk/go's own module and the trusted adapter's tests for them, and the Go tests that
# name SDK paths name them inside a generated extension (internal/extmgr) or guard the plugins' imports.
GO_READ_INPUTS = tuple(pattern for pattern in paths.FILTERS["server"]
                       if not pattern.startswith(("server/", "sdk/", "scripts/ci/", ".github/"))
                       and pattern != "scripts/test/go_trimpath.sh")
# Where a Go test's reads are held to check-fast's plan and to CI's server lane: those inputs, and the whole of each
# tree CI watches only in part (apple/, android/: the client contract sources), so a client file a Go test reads but
# CI's server lane does not watch is a gap too.
GO_READ_SCOPE = GO_READ_INPUTS + tuple(sorted({pattern.split("/")[0] + "/**" for pattern in paths.SERVER_CLIENT_INPUTS}))
# The runtimes lane's shared inputs (fixtures and schemas), whose runtime readers check-fast runs instead of the lane.
RUNTIME_READ_INPUTS = tuple(pattern for pattern in paths.FILTERS["runtimes"]
                            if not pattern.startswith(("integrations/", "sdk/ts/", "scripts/ci/", ".github/")))
# The scripts suites check-fast leaves to a dedicated check (script_test_add), and that check's plan line.
DEDICATED = {
    "scripts/test/test_external_service_refs.py": "external service refs",
    "scripts/test/test_sdk_bundle.py": "sdk bundle snapshot",
    "scripts/test/test_extension_schema.py": "extension schema conformance",
    "scripts/app/test_android_i18n_check.py": "android i18n",
    "scripts/app/test_android_token_baseline.py": "android i18n",
    "scripts/app/test_android_motion_baseline.py": "android i18n",
    "scripts/app/test_android_slot_order.py": "android i18n",
}

_GO_LITERAL = re.compile(r'"((?:[^"\\\n]|\\.)*)"|`([^`\n]*)`')
_JS_LITERAL = re.compile(r'"((?:[^"\\\n]|\\.)*)"|\'((?:[^\'\\\n]|\\.)*)\'|`([^`\n]*)`')
_GO_COMMENT = re.compile(r"\s*//")
_JS_COMMENT = re.compile(r"\s*(//|\*|#)")
_SEPARATOR = re.compile(r"\s*,\s*")
_IMPORT = re.compile(r"^[ \t]*(?:from[ \t]+(\w+)[ \t]+import|import[ \t]+((?:\w+[ \t]*,[ \t]*)*\w+))", re.M)


def tracked(root=ROOT):
    return subprocess.check_output(["git", "ls-files"], cwd=root, text=True, timeout=60).splitlines()


def _matches(path, patterns):
    return any(fnmatch.fnmatchcase(path, pattern) for pattern in patterns)


def _ancestors(path):
    """The path and each directory it is in that is two levels deep or deeper: spec/fixtures/a/b.json gives
    spec/fixtures, spec/fixtures/a and the path."""
    parts = path.split("/")
    return ["/".join(parts[:end]) for end in range(2, len(parts) + 1)]


def named_in_line(source, line, literal):
    """The repository paths a source line names: each string literal, and each run of literals separated by commas
    (filepath.Join("..", "spec", "fixtures"), joined with /). A literal starting with ./ or ../ is resolved against
    the source's directory (a Go test runs in its package's directory, new URL(..., import.meta.url) is the file's),
    and also taken as relative to the repository root, the way a test that joins it with the root reads it."""
    found = [match for match in literal.finditer(line)]
    texts = [next((group for group in match.groups() if group is not None), "") for match in found]
    names = list(texts)
    run = texts[:1]
    for before, match, text in zip(found, found[1:], texts[1:]):
        if _SEPARATOR.fullmatch(line[before.end():match.start()]):
            run.append(text)
            continue
        if len(run) > 1:
            names.append("/".join(run))
        run = [text]
    if len(run) > 1:
        names.append("/".join(run))
    for name in names:
        name = name.strip()
        if not name or name.startswith("/") or "\\" in name:
            continue
        if name.startswith(("./", "../")):
            yield posixpath.normpath(posixpath.join(posixpath.dirname(source), name))
            yield re.sub(r"^(?:\.\.?/)+", "", name).rstrip("/")
        else:
            yield name.rstrip("/")


class Readers:
    """What reads a path, worked out from the tests themselves, without check-fast (#11026).

    go: {tracked file or directory: server packages (relative to server/)} for each server/**/_test.go that names it
    outside a // comment line. runtime: the same for the files of each integrations/<pkg> and of sdk/ts, outside a
    comment line (Markdown left out). suites: {scripts module: scripts/**/test_*.py unittest suites} for each suite that
    imports the module ("import <name>" or "from <name> import" at the start of a line) from its own directory or
    from a directory it names (it puts scripts/<dir> on sys.path to import from it). A directory counts when it is two
    levels deep or deeper (spec/fixtures, not spec).
    """

    def __init__(self, root=ROOT, tracked_paths=None):
        self.root = Path(root)
        self.files = sorted(tracked_paths if tracked_paths is not None else tracked(self.root))
        self.file_set = set(self.files)
        self.dirs = {"/".join(path.split("/")[:end]) for path in self.files for end in range(2, path.count("/") + 1)}

    def _text(self, path):
        try:
            data = (self.root / path).read_bytes()
        except OSError:
            return ""
        return "" if b"\0" in data else data.decode("utf-8", "replace")

    def _index(self, sources, owner, literal, comment, words):
        index = defaultdict(set)
        for source in sources:
            for line in self._text(source).splitlines():
                if not words.search(line) or comment.match(line):
                    continue
                for name in named_in_line(source, line, literal):
                    if name in self.file_set or name in self.dirs:
                        index[name].add(owner(source))
        return index

    @staticmethod
    def _words(patterns):
        """A line can name an input only when it holds the input's top-level directory (or file) name."""
        return re.compile("|".join(sorted({re.escape(pattern.split("/")[0]) for pattern in patterns})))

    @cached_property
    def go(self):
        sources = [path for path in self.files if path.startswith("server/") and path.endswith("_test.go")]
        return self._index(sources, lambda source: posixpath.dirname(source)[len("server/"):], _GO_LITERAL,
                           _GO_COMMENT, self._words(GO_READ_SCOPE))

    @cached_property
    def runtime(self):
        def owner(source):
            return "sdk/ts" if source.startswith("sdk/ts/") else "/".join(source.split("/")[:2])
        sources = [path for path in self.files if (path.startswith("sdk/ts/") or
                                                   (path.startswith("integrations/") and path.count("/") >= 2))
                   and not path.endswith(".md")]
        return self._index(sources, owner, _JS_LITERAL, _JS_COMMENT, self._words(RUNTIME_READ_INPUTS))

    @cached_property
    def suites(self):
        modules = defaultdict(list)
        for path in self.files:
            if path.startswith("scripts/") and path.endswith(".py") and re.fullmatch(r"\w+", posixpath.basename(path)[:-3]):
                modules[posixpath.basename(path)[:-3]].append(path)
        index = defaultdict(set)
        for suite in self.files:
            if not (suite.startswith("scripts/") and fnmatch.fnmatchcase(posixpath.basename(suite), "test_*.py")):
                continue
            text = self._text(suite)
            if "unittest" not in text:
                continue  # not a suite: check-fast and CI skip it
            names = set()
            for single, listed in _IMPORT.findall(text):
                names.update([single] if single else [name.strip() for name in listed.split(",")])
            for name in names:
                for module in modules.get(name, ()):
                    directory = posixpath.dirname(module)
                    if module != suite and (directory == posixpath.dirname(suite) or directory in text or re.search(
                            r'/\s*"%s"' % re.escape(posixpath.basename(directory)), text)):
                        index[module].add(suite)
        return index

    def expected(self, path):
        """[(lane, check-fast check)] for each reader of the path, in a stable order."""
        found = set()
        if _matches(path, GO_READ_SCOPE):
            for name in _ancestors(path):
                found.update(("server", "server go test ./%s/..." % package) for package in self.go.get(name, ()))
        if _matches(path, RUNTIME_READ_INPUTS):
            for name in _ancestors(path):
                found.update(("runtimes", "sdk/ts (bun)" if owner == "sdk/ts" else owner)
                             for owner in self.runtime.get(name, ()))
        for suite in self.suites.get(path, ()):
            found.add(("scripts", DEDICATED.get(suite, suite)))
        return sorted(found)

    def probes(self):
        """The paths that have a reader: every scripts module a suite imports, and the first and last tracked file
        under each input a Go test or a runtime package names (the file itself when it names a file)."""
        found = set(self.suites)
        for index, scope in ((self.go, GO_READ_SCOPE), (self.runtime, RUNTIME_READ_INPUTS)):
            for name in index:
                under = [name] if name in self.file_set else []
                for path in self.files[bisect.bisect_left(self.files, name + "/"):]:
                    if not path.startswith(name + "/"):
                        break
                    under.append(path)
                under = [path for path in under if _matches(path, scope)]
                found.update(under[:1] + under[-1:])
        return sorted(found)


def ci_lanes(path):
    return {lane for lane, on in paths.select([path]).items() if on and lane in LANES}


def watcher_lanes(path):
    """The lanes the watcher runs for a PR that changes only this path (not a test-only test/fix PR)."""
    return set(pr_watch_verify.watcher_lanes(paths.select([path]), [path])) & set(LANES)


def _stub_tools(directory):
    """go, node (v22) and bun that answer the version checks and do nothing else."""
    for name, body in (("go", ""), ("bun", ""), ("node", 'echo 22\n')):
        tool = Path(directory, name)
        tool.write_text("#!/bin/sh\n" + body)
        tool.chmod(0o755)


def check_fast_plans(path_list, root=ROOT):
    """{path: [check-fast's plan lines for that path alone]} from one check-fast --dry-run --each."""
    with tempfile.TemporaryDirectory(prefix="affected-drift-") as temporary:
        _stub_tools(temporary)
        # A dry run takes no slot, but this runs in CI's Scripts tests job, and a lane that starts check-fast turns
        # the slots off (docs/operations/testing.md "Heavy build slots"; test_heavy_slot.py holds the lanes to it).
        env = dict(os.environ, PATH=temporary + os.pathsep + os.environ.get("PATH", ""), PERSONA_HEAVY_SLOTS="0")
        result = subprocess.run(["bash", str(root / "scripts/test/check-fast.sh"), "--dry-run", "--changed-from", "-",
                                 "--each"], input="".join(p + "\n" for p in path_list), cwd=root, env=env,
                                capture_output=True, text=True, timeout=3600)
    if result.returncode:
        raise RuntimeError("check-fast --each failed (%d): %s" % (result.returncode, result.stderr[-2000:]))
    plans, current = {}, None
    for line in result.stdout.splitlines():
        if line.startswith("== "):
            current = line[3:]
            plans[current] = []
        elif current is not None:
            plans[current].append(line)
    return plans


def fast_lanes(lines):
    return {lane for lane, pattern in FAST_LANES for line in lines if pattern.match(line)}


def named_lanes(lines):
    """The CI lanes a plan names as not run: its INFO line, and apple for the apple SKIP line."""
    named = set()
    for line in lines:
        match = _NOT_RUN.match(line)
        if match:
            named.update(lane.strip() for lane in match.group(1).split(","))
        elif line.startswith(_APPLE_SKIP):
            named.add("apple")
    return named


def _covered(check, queued):
    """Whether a plan's queued checks include a reader's check; go test ./a/... covers ./a/b/... too."""
    if check in queued or (check.startswith("server go test ./") and "server go test" in queued):
        return True
    if check.startswith("server go test ./"):
        package = check[len("server go test ./"):-len("...")]
        return any(line.startswith("server go test ./") and package.startswith(line[len("server go test ./"):-len("...")])
                   for line in queued)
    return False


def allowed(lane, side, path):
    for row_lane, row_side, patterns, reason in ALLOWED:
        if row_lane == lane and row_side == side and _matches(path, patterns):
            return reason
    return None


def compare(path, lines, expected=()):
    """The disagreements of one path, given its check-fast plan lines and its readers ([(lane, check)])."""
    ci, watcher, fast, named = ci_lanes(path), watcher_lanes(path), fast_lanes(lines), named_lanes(lines)
    queued = {line[len("  queued: "):] for line in lines if line.startswith("  queued: ")}
    found = []
    for lane in LANES:
        if lane in ci and lane not in fast:
            found.append(Disagreement(lane, "ci", allowed(lane, "ci", path), ""))
            if lane != "image" and lane not in named:
                found.append(Disagreement(lane, "named", None, "not named in the plan"))
        if lane in fast and lane not in ci:
            found.append(Disagreement(lane, "fast", allowed(lane, "fast", path), ""))
        if lane in ci and lane not in watcher:
            found.append(Disagreement(lane, "watcher", allowed(lane, "watcher", path), ""))
        if lane in named and (lane not in ci or lane in fast):
            found.append(Disagreement(lane, "named", None, "named, but " + ("run" if lane in fast else "not CI's")))
    for lane, check in expected:
        if not _covered(check, queued):
            found.append(Disagreement(lane, "reader", None, check))
        if lane in LANES and lane not in ci:
            found.append(Disagreement(lane, "ci-reader", allowed(lane, "ci-reader", path), check))
    return found


def area(path):
    parts = path.split("/")
    return parts[0] if len(parts) == 1 else parts[0] + "/"


def measure(path_list, root=ROOT, readers=None, plans=None):
    """[(path, CI lanes, check-fast lanes, watcher lanes, disagreements)] for each path (plans: check_fast_plans')."""
    readers = readers or Readers(root)
    plans = check_fast_plans(path_list, root) if plans is None else plans
    rows = []
    for path in path_list:
        lines = plans.get(path, [])
        rows.append((path, ci_lanes(path), fast_lanes(lines), watcher_lanes(path),
                     compare(path, lines, readers.expected(path))))
    return rows


def summary(rows):
    """Lines: the disagreements counted by side, lane and top-level area, then the unexplained ones by area."""
    explained, gaps = Counter(), defaultdict(list)
    for path, _, _, _, found in rows:
        for row in found:
            if row.reason:
                explained[(row.side, row.lane, area(path), row.reason)] += 1
            else:
                gaps[(row.side, row.lane, area(path))].append(path + (" [%s]" % row.detail if row.detail else ""))
    lines = ["%d path(s) compared" % len(rows), "", "Intended differences (side, lane, area: paths):"]
    for (side, lane, where, reason), count in sorted(explained.items()):
        lines.append("  %s %s %s: %d (%s)" % (side, lane, where, count, reason))
    lines += ["", "Unexplained (side, lane, area: paths, examples):"]
    for (side, lane, where), found in sorted(gaps.items()):
        lines.append("  %s %s %s: %d, e.g. %s" % (side, lane, where, len(found), ", ".join(found[:3])))
    if not gaps:
        lines.append("  none")
    return lines


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("paths", nargs="*", help="paths to compare (default: every tracked file)")
    parser.add_argument("--table", help="write the per-path table (tab-separated) here")
    args = parser.parse_args(argv)
    if not shutil.which("timeout") and not shutil.which("gtimeout"):
        parser.error("check-fast.sh needs GNU timeout")
    rows = measure(args.paths or tracked())
    if args.table:
        with open(args.table, "w") as out:
            out.write("path\tci\tcheck-fast\twatcher\tdisagreements\n")
            for path, ci, fast, watcher, found in rows:
                out.write("\t".join([path, ",".join(sorted(ci)), ",".join(sorted(fast)), ",".join(sorted(watcher)),
                                     "; ".join("%s %s%s%s" % (row.side, row.lane,
                                                              " " + row.detail if row.detail else "",
                                                              "" if row.reason else " (gap)")
                                               for row in found)]) + "\n")
    print("\n".join(summary(rows)))
    return 1 if any(row.reason is None for entry in rows for row in entry[4]) else 0


if __name__ == "__main__":
    sys.exit(main())
