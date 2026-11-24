"""CI lanes, check-fast and the PR watcher agree on what a change affects, outside the listed differences (#11026).

The drift test plans a probe set of tracked paths with check-fast (--dry-run --each) and compares each path's checks
with CI's lanes (scripts/ci/paths.py) and the watcher's (scripts/operations/pr_watch_verify.py), and with the readers
affected_drift.Readers works out from the tests themselves (the Go packages, runtime packages and scripts suites
check-fast must run where CI runs a whole lane). The probes are derived, never listed: up to two tracked files for
every pattern of CI's lanes and of check-fast's table (scripts/ci/check_fast_map.py), every suite the table names, the
first file of every top-level directory and of each directory below it, every tracked path check-fast.sh names, and
every path that has a reader. A new mapping in one of the three that the others miss fails here, and so does a reader
check-fast stops finding; scripts/ci/affected_drift.py prints the same comparison for every tracked file.
"""
import fnmatch
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
import unittest.mock
import io

sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "scripts/operations"))
import affected_drift  # noqa: E402
import check_fast_map  # noqa: E402
import paths  # noqa: E402
import pr_watch_verify  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
_NAMED = re.compile(r"(?<![\w/.-])((?:\.github|android|apple|deploy|desktop|docs|harmony|integrations|native|schema|"
                    r"scripts|sdk|server|site|spec|web)/[\w./{},*-]+)")


def _braces(pattern):
    """A bash case label's {a,b} parts spelled out: scripts/{gen,test}/x -> scripts/gen/x, scripts/test/x."""
    match = re.search(r"\{([^{}]*)\}", pattern)
    if not match:
        return [pattern]
    return [expanded for part in match.group(1).split(",")
            for expanded in _braces(pattern[:match.start()] + part + pattern[match.end():])]


def probes(tracked, readers):
    """The paths the drift test compares (module docstring)."""
    found = set()
    patterns = [p for lane, lane_patterns in paths.FILTERS.items() if lane != "scripts" for p in lane_patterns]
    patterns += paths.SHARED
    patterns += check_fast_map.patterns() + check_fast_map.suites()
    names = _NAMED.findall((ROOT / "scripts/test/check-fast.sh").read_text())
    patterns += [expanded.rstrip(".,;)") for name in names for expanded in _braces(name)]
    for pattern in patterns:
        matches = [path for path in tracked if fnmatch.fnmatchcase(path, pattern)]
        found.update(matches[:1] + matches[-1:])
    found.update(readers.probes())
    seen = set()
    for path in tracked:
        parts = path.split("/")
        for depth in (1, 2):
            key = "/".join(parts[:depth]) if len(parts) > depth else path
            if key not in seen:
                seen.add(key)
                found.add(path)
    return sorted(found)


class LaneComparisonTests(unittest.TestCase):
    def test_check_fast_lines_name_the_lanes_they_run_part_of(self):
        lines = ["  queued: privacy scan", "  queued: web", "  queued: server go test ./internal/hub/...",
                 "  queued: integrations/runtime-sdk", "  queued: scripts/test/test_doc_paths.py"]
        self.assertEqual({"web", "server", "runtimes"}, affected_drift.fast_lanes(lines))
        self.assertEqual({"server"}, affected_drift.fast_lanes(["  queued: sdk/go go test"]))
        self.assertEqual({"runtimes"}, affected_drift.fast_lanes(["  queued: sdk/ts (bun)"]))
        # A SKIP or INFO note runs nothing, and the privacy scan and the scripts suites are the scripts lane's.
        self.assertEqual(set(), affected_drift.fast_lanes(["SKIP  apple/** builds and tests: need a Mac",
                                                           "INFO  CI lanes this change selects that check-fast does "
                                                           "not run: android", "  queued: privacy scan"]))

    def test_the_lanes_a_plan_names_as_not_run(self):
        lines = ["SKIP  apple/** builds and tests: iOS/macOS checks need a Mac",
                 "INFO  CI lanes this change selects that check-fast does not run: server, android, core "
                 "(bash scripts/ci/<lane>.sh)"]
        self.assertEqual({"apple", "server", "android", "core"}, affected_drift.named_lanes(lines))
        self.assertEqual(set(), affected_drift.named_lanes(["  queued: android i18n"]))

    def test_a_lane_only_one_side_runs_is_a_disagreement_unless_listed(self):
        found = affected_drift.compare("web/src/App.tsx", [])
        self.assertIn(("web", "ci", None, ""), found)
        found = affected_drift.compare("site/worker.js", ["  queued: server go test ./internal/hub/..."])
        self.assertIn(("server", "fast", None, ""), found)
        self.assertEqual([], [row for row in affected_drift.compare("web/src/App.tsx", ["  queued: web"])
                              if row.reason is None and row.lane == "web"])

    def test_a_ci_lane_check_fast_does_not_run_must_be_named(self):
        info = "INFO  CI lanes this change selects that check-fast does not run: android, apple, core (bash ...)"
        self.assertEqual([], [row for row in affected_drift.compare("native/app-core/Cargo.toml", [info])
                              if row.reason is None])
        found = affected_drift.compare("native/app-core/Cargo.toml", [])
        self.assertEqual({"android", "apple", "core"}, {row.lane for row in found if row.side == "named"})
        # Named, but run (or not CI's) is a disagreement too.
        found = affected_drift.compare("web/src/App.tsx", ["  queued: web", info])
        self.assertIn(("android", "named", None, "named, but not CI's"), found)

    def test_a_reader_missing_from_the_plan_is_a_disagreement(self):
        expected = [("server", "server go test ./internal/proto/..."), ("runtimes", "integrations/runtime-sdk"),
                    ("scripts", "scripts/ci/test_ci.py")]
        found = affected_drift.compare("spec/fixtures/x.json", [], expected)
        self.assertEqual(expected, [(row.lane, row.detail) for row in found if row.side == "reader"])
        lines = ["  queued: server go test ./internal/...", "  queued: integrations/runtime-sdk",
                 "  queued: scripts/ci/test_ci.py"]
        self.assertEqual([], [row for row in affected_drift.compare("spec/fixtures/x.json", lines, expected)
                              if row.side == "reader"])
        # ./internal/pro/... is not ./internal/proto/...; the whole server go test covers every package.
        self.assertTrue(affected_drift._covered("server go test ./internal/proto/...", {"server go test"}))
        self.assertFalse(affected_drift._covered("server go test ./internal/proto/...",
                                                 {"server go test ./internal/pro/..."}))

    def test_listed_differences_say_why(self):
        reason = affected_drift.allowed("android", "ci", "android/app/build.gradle.kts")
        self.assertIn("Gradle", reason)
        self.assertIsNone(affected_drift.allowed("web", "ci", "web/src/App.tsx"))
        for lane, side, patterns, reason in affected_drift.ALLOWED:
            self.assertIn(lane, affected_drift.LANES)
            self.assertIn(side, ("ci", "fast", "watcher", "ci-reader"))
            self.assertTrue(patterns and reason, lane)


class ReadersTests(unittest.TestCase):
    """affected_drift.Readers on a throwaway tree: what counts as reading a path, without check-fast."""

    FILES = {
        "server/internal/proto/contract_test.go":
            'package proto\n\nvar models = read("apple/Shared/Models.swift")\n'
            'var joined = filepath.Join(root, "apple", "Shared", "Joined.swift")\n'
            'var cases = filepath.Join("..", "..", "..", "spec", "fixtures", "answer", name)\n'
            '// read("apple/Shared/Commented.swift")\n',
        "server/internal/hub/samples_test.go": 'package hub\n\nconst consumer = "apple/Shared/Models.swift: Fold"\n',
        "server/internal/configuration/configuration_test.go":
            'package configuration\n\nvar raw, _ = os.ReadFile("../../../schema/configuration/all-types.json")\n',
        "server/internal/views/views_test.go": 'package views\n\nvar view = read("apple/Persona/Views/Unwatched.swift")\n',
        "apple/Persona/Views/Unwatched.swift": "",
        "apple/Shared/Models.swift": "", "apple/Shared/Joined.swift": "", "apple/Shared/Commented.swift": "",
        "apple/Shared/Other.swift": "", "spec/fixtures/answer/a.json": "", "spec/fixtures/answer/b.json": "",
        "spec/fixtures/other/c.json": "", "schema/configuration/all-types.json": "",
        "schema/extension/examples/Manifest.x.json": "",
        "integrations/clip/test/clip.test.js":
            "// spec/fixtures/other/c.json in a comment\n"
            "const cases = new URL('../../../spec/fixtures/answer/a.json', import.meta.url)\n",
        "integrations/clip/README.md": "Reads `spec/fixtures/other/c.json`.\n",
        "sdk/ts/src/fixtures.ts": 'const dir = new URL("../../../schema/extension/examples/", import.meta.url);\n',
        "scripts/ci/paths.py": "", "scripts/ci/test_ci.py": "import unittest\nimport paths\n",
        "scripts/operations/pr_watch_verify.py": "",
        "scripts/ci/test_drift.py": 'import unittest\nsys.path.insert(0, str(ROOT / "scripts/operations"))\n'
                                    "import pr_watch_verify\n",
        "scripts/test/test_elsewhere.py": "import unittest\nimport pr_watch_verify\n",  # never names its directory
        "scripts/test/test_verify.py": "import pr_watch_verify\n",  # not a unittest suite
        "scripts/test/compatibility_identifiers.py": "",
        "scripts/test/test_external_service_refs.py": "import unittest\nfrom compatibility_identifiers import X\n",
    }

    @classmethod
    def setUpClass(cls):
        cls.root = Path(tempfile.mkdtemp(prefix="affected-drift-readers-"))
        for path, text in cls.FILES.items():
            (cls.root / path).parent.mkdir(parents=True, exist_ok=True)
            (cls.root / path).write_text(text)
        cls.readers = affected_drift.Readers(cls.root, list(cls.FILES))

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.root)

    def expected(self, path):
        return self.readers.expected(path)

    def test_a_go_test_reads_a_whole_path_a_joined_path_or_a_directory(self):
        proto = [("server", "server go test ./internal/proto/...")]
        self.assertEqual(proto, self.expected("apple/Shared/Models.swift"))  # the wire sample's longer string reads nothing
        self.assertEqual(proto, self.expected("apple/Shared/Joined.swift"))
        self.assertEqual(proto, self.expected("spec/fixtures/answer/b.json"))
        self.assertEqual([("server", "server go test ./internal/configuration/...")],
                         self.expected("schema/configuration/all-types.json"))
        for path in ("apple/Shared/Commented.swift", "apple/Shared/Other.swift"):
            self.assertEqual([], self.expected(path), path)

    def test_a_client_file_a_go_test_reads_must_be_in_cis_server_lane(self):
        # apple/Persona/Views/** is outside CI's client contract inputs; a Go test that reads a file there is a gap of
        # CI's even when check-fast runs the test.
        expected = self.expected("apple/Persona/Views/Unwatched.swift")
        self.assertEqual([("server", "server go test ./internal/views/...")], expected)
        found = affected_drift.compare("apple/Persona/Views/Unwatched.swift",
                                       ["  queued: server go test ./internal/views/..."], expected)
        self.assertIn(("server", "ci-reader", None, "server go test ./internal/views/..."), found)
        self.assertEqual([], [row for row in affected_drift.compare("apple/Shared/Models.swift",
                                                                    ["  queued: server go test ./internal/proto/..."],
                                                                    self.expected("apple/Shared/Models.swift"))
                              if row.side in ("reader", "ci-reader")])

    def test_a_runtime_package_reads_a_fixture_or_a_directory_of_them(self):
        self.assertIn(("runtimes", "integrations/clip"), self.expected("spec/fixtures/answer/a.json"))
        self.assertNotIn(("runtimes", "integrations/clip"), self.expected("spec/fixtures/answer/b.json"))
        # A comment line or a README names it without reading it.
        self.assertEqual([], self.expected("spec/fixtures/other/c.json"))
        self.assertEqual([("runtimes", "sdk/ts (bun)")], self.expected("schema/extension/examples/Manifest.x.json"))

    def test_a_suite_imports_from_its_own_directory_or_one_it_names(self):
        self.assertEqual([("scripts", "scripts/ci/test_ci.py")], self.expected("scripts/ci/paths.py"))
        self.assertEqual([("scripts", "scripts/ci/test_drift.py")], self.expected("scripts/operations/pr_watch_verify.py"))
        # A suite a dedicated check runs is that check.
        self.assertEqual([("scripts", "external service refs")],
                         self.expected("scripts/test/compatibility_identifiers.py"))

    def test_the_probes_are_the_paths_with_a_reader(self):
        probes = self.readers.probes()
        for path in ("apple/Shared/Models.swift", "spec/fixtures/answer/a.json", "spec/fixtures/answer/b.json",
                     "schema/configuration/all-types.json", "scripts/ci/paths.py"):
            self.assertIn(path, probes)
        self.assertNotIn("apple/Shared/Other.swift", probes)


class WatcherLaneTests(unittest.TestCase):
    def test_the_watcher_runs_cis_lanes_with_the_scripts_lane_narrowed(self):
        names = ["server/internal/hub/hub.go"]
        self.assertEqual(["server"], pr_watch_verify.watcher_lanes(paths.select(names), names))
        names = ["scripts/ci/gofmt.py"]
        self.assertEqual(["server", "scripts"], pr_watch_verify.watcher_lanes(paths.select(names), names))
        # CI's apple and image lanes are not among the watcher's.
        names = ["apple/Shared/Models.swift"]
        self.assertNotIn("apple", pr_watch_verify.watcher_lanes(paths.select(names), names))

    def test_a_test_only_pr_runs_its_tests_instead_of_the_web_scripts_and_server_lanes(self):
        names = ["server/internal/hub/hub_test.go", "web/src/App.test.tsx"]
        # The desktop lane loads the web client, so a web test still runs it.
        self.assertEqual(["desktop"], pr_watch_verify.watcher_lanes(paths.select(names), names, focused=True))
        names = ["android/app/src/test/FooTest.kt"]
        self.assertEqual(["android"], pr_watch_verify.watcher_lanes(paths.select(names), names, focused=True))


class FrozenReaderPlanTests(unittest.TestCase):
    """The --each reader index preserves check-fast.sh's path-local git-grep rules."""

    def test_go_literals_client_paths_and_runtime_directories(self):
        expected = {
            "spec/fixtures/answer/a.json": (["internal/assistant"], ["integrations/clip", "sdk/ts"]),
            "spec/fixtures/answer/b.json": (["internal/assistant"], []),
            "schema/configuration/all-types.json": (["internal/configuration"], []),
            "apple/Shared/Models.swift": (["internal/hub"], []),
            "apple/Shared/Commented.swift": ([], []),
            "docs/example.md": (["internal/hub"], []),
            "integrations/clip/test/clip.test.js": (["internal/hub"], []),
            "scripts/test/server_test_env.py": (["internal/hub"], []),
        }
        lines = [
            'server/internal/hub/client_test.go:1:const model = "apple/Shared/Models.swift"',
            'server/internal/hub/comment_test.go:3:// "apple/Shared/Commented.swift"',
            'server/internal/assistant/read_test.go:4:os.ReadFile("spec/fixtures/answer/a.json")',
            'server/internal/assistant/join_test.go:5:filepath.Join("..", "spec", "fixtures", "answer", "b.json")',
            'server/internal/configuration/read_test.go:6:filepath.Join("..", "schema", "configuration", "all-types.json")',
            'server/internal/hub/doc_test.go:7:const doc = "docs/example.md"',
            'server/internal/hub/integration_test.go:8:const integration = "integrations"',
            'server/internal/hub/env_test.go:9:const helper = "server_test_env.py"',
            'integrations/clip/test/clip.test.js:10:const fixture = "../../../spec/fixtures/answer/a.json"',
            'integrations/clip/test/comment.test.js:11:// spec/fixtures/answer/b.json',
            'sdk/ts/src/read.ts:12:const fixture = "../../../spec/fixtures/answer/a.json"',
        ]
        def git_output(arguments, **kwargs):
            stdout = "\n".join(lines[:8] if "server/*_test.go" in arguments else lines[8:]) + "\n"
            return subprocess.CompletedProcess([], 0, stdout=stdout, stderr="")
        with unittest.mock.patch.object(subprocess, "run", side_effect=git_output):
            found = affected_drift.reader_plan(list(expected))
        self.assertEqual(expected, found)

    def test_a_missing_reader_record_cannot_shift_the_next_path(self):
        paths = ["docs/none.md", "apple/Shared/None.swift"]
        with unittest.mock.patch.object(affected_drift, "_git_reader_lines", return_value=[]), \
             unittest.mock.patch.object(subprocess, "run", return_value=subprocess.CompletedProcess([], 1,
                                                                                               stdout="", stderr="")):
            output = io.TextIOWrapper(io.BytesIO(), encoding="utf-8")
            affected_drift.write_reader_plan(paths, output)
            output.flush()
            self.assertEqual(b"docs/none.md\0\0\0apple/Shared/None.swift\0\0\0", output.buffer.getvalue())


class ResolverCommandLineTests(unittest.TestCase):
    """The paths.py commands check-fast reads: the lanes of a change and the web plan of each path."""

    def run_paths(self, flag, text):
        return subprocess.run([sys.executable, str(ROOT / "scripts/ci/paths.py"), flag], input=text, cwd=ROOT,
                              capture_output=True, text=True, timeout=120, check=True).stdout

    def test_lanes_for_prints_the_lanes_ci_runs(self):
        self.assertEqual("server android apple core scripts\n",
                         self.run_paths("--lanes-for", "apple/Shared/Models.swift\nnative/app-core/Cargo.toml\n"))
        self.assertEqual("scripts\n", self.run_paths("--lanes-for", "docs/example.md\n"))

    def test_lanes_for_each_prints_the_lanes_of_each_path_on_its_own(self):
        self.assertEqual("server apple scripts\nandroid apple core scripts\nscripts\n",
                         self.run_paths("--lanes-for-each",
                                        "apple/Shared/Models.swift\nnative/app-core/Cargo.toml\ndocs/example.md\n"))

    def test_the_web_plan_of_each_path_matches_its_own_web_plan(self):
        listed = ["web/src/App.tsx", "server/go.mod", "web/package.json", "spec/fixtures/agent-names/names.json"]
        lines = self.run_paths("--web-plan-each", "".join(p + "\n" for p in listed)).splitlines()
        self.assertEqual(len(listed), len(lines))
        for path, line in zip(listed, lines):
            mode, reason, _ = paths.web_plan([path])
            self.assertEqual(f"{mode} {reason}", line, path)


@unittest.skipUnless(shutil.which("timeout") or shutil.which("gtimeout"), "check-fast.sh needs GNU timeout")
class DriftTests(unittest.TestCase):
    # One path of each gap class #11026 fixed, and the plan line the fix added: the Go tests that read a client
    # source, a schema family's Go reader, a runtime package that reads a fixture, a suite that imports a watcher
    # module, and the INFO line that names the CI lanes check-fast does not run.
    FIXED = (("apple/Shared/Models.swift", "  queued: server go test ./internal/proto/..."),
             ("schema/configuration/all-types.json", "  queued: server go test ./internal/configuration/..."),
             ("spec/fixtures/input-clip/cases.json", "  queued: integrations/runtime-sdk"),
             ("scripts/operations/pr_watch_common.py", "  queued: scripts/operations/test_pr_watch_recovery.py"),
             ("native/app-core/Cargo.toml", "INFO  CI lanes this change selects that check-fast does not run: "))

    @classmethod
    def setUpClass(cls):
        cls.tracked = affected_drift.tracked()
        cls.readers = affected_drift.Readers(tracked_paths=cls.tracked)
        cls.listed = sorted(set(probes(cls.tracked, cls.readers)) | {path for path, _ in cls.FIXED})
        cls.plans = affected_drift.check_fast_plans(cls.listed)
        cls.rows = affected_drift.measure(cls.listed, readers=cls.readers, plans=cls.plans)

    def test_ci_check_fast_and_the_watcher_agree_outside_the_listed_differences(self):
        self.assertGreater(len(self.listed), 100)
        gaps = ["%s: %s %s %s" % (path, row.side, row.lane, row.detail) for path, _, _, _, found in self.rows
                for row in found if row.reason is None]
        self.assertEqual([], gaps, "\n".join(affected_drift.summary(self.rows)))

    def test_each_gap_this_issue_fixed_fails_the_comparison_if_it_comes_back(self):
        for path, line in self.FIXED:
            with self.subTest(path=path):
                lines = self.plans[path]
                without = [kept for kept in lines if not kept.startswith(line)]
                self.assertNotEqual(lines, without, "check-fast's plan no longer has %r" % line)
                expected = self.readers.expected(path)
                self.assertEqual([], [row for row in affected_drift.compare(path, lines, expected)
                                      if row.reason is None])
                self.assertNotEqual([], [row for row in affected_drift.compare(path, without, expected)
                                         if row.reason is None])

    def test_the_probes_cover_every_lane_pattern_that_matches_a_file(self):
        listed = set(self.listed)
        for lane, patterns in paths.FILTERS.items():
            if lane == "scripts":
                continue
            for pattern in patterns:
                if any(fnmatch.fnmatchcase(path, pattern) for path in self.tracked):
                    self.assertTrue(any(fnmatch.fnmatchcase(path, pattern) for path in listed), (lane, pattern))


if __name__ == "__main__":
    unittest.main()
