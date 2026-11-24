"""check_fast_compare.py: what a failed check-fast step's failures are, the command that reruns each, and how they
compare with the merge base (#12733). Fake runners stand in for git, go and python; check-fast.sh's own use of the
helper is tested end to end in scripts/test/test_check_fast.py."""
import ast
import io
from pathlib import Path
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import check_fast_compare as compare  # noqa: E402

MODULE = "example.com/persona/server"
GO_HEAD = (
    "=== RUN   TestIgnoredRunLine\n"
    "--- FAIL: TestRedOnMain (0.01s)\n"
    "    golden_test.go:12: golden differs\n"
    "--- FAIL: TestMine (0.00s)\n"
    "    --- FAIL: TestMine/case (0.00s)\n"
    "FAIL\n"
    "FAIL\t" + MODULE + "/internal/assistant\t0.991s\n"
    "ok  \t" + MODULE + "/internal/hub\t1.2s\n"
    "--- FAIL: TestOther (0.00s)\n"
    "FAIL\n"
    "FAIL\t" + MODULE + "/internal/other\t0.1s\n"
    "# " + MODULE + "/internal/broken\n"
    "internal/broken/x.go:3:1: syntax error\n"
    "FAIL\t" + MODULE + "/internal/broken [build failed]\n"
    "FAIL\n")
# go_test_partitioned.py's output for ./internal/assistant/... (#12765): the other packages as they come, then each
# partition whole, after its header line.
RUNNER = ("python3", "-B", "../scripts/test/go_test_partitioned.py")
GO_PARTITIONED = (
    "== go test: 2 package(s)\n"
    "== go test: ./internal/assistant partition (2 tests)\n"
    "== go test: ./internal/assistant partition (1 tests)\n"
    "ok  \t" + MODULE + "/internal/assistant/inbox\t0.2s\n"
    "--- FAIL: TestInbox (0.00s)\n"
    "FAIL\n"
    "FAIL\t" + MODULE + "/internal/assistant/kbsent\t0.1s\n"
    "FAIL\n"
    "== go test: ./internal/assistant partition (2 tests), exit 1\n"
    "--- FAIL: TestRedOnMain (0.01s)\n"
    "FAIL\n"
    "FAIL\t" + MODULE + "/internal/assistant\t0.9s\n"
    "== go test: ./internal/assistant partition (1 tests), exit 1\n"
    "--- FAIL: TestMine (0.00s)\n"
    "FAIL\n"
    "FAIL\t" + MODULE + "/internal/assistant\t0.5s\n")
PY_39 = ("FAIL: test_a (test_thing.T)\n"
         "ERROR: test_b (test_thing.T)\n"
         "FAIL: test_c (test_thing.T) (case=1)\n"
         "ERROR: setUpClass (test_thing.Slow)\n")
PY_311 = "FAIL: test_a (test_thing.T.test_a)\nERROR: test_b (test_thing.T.test_b)\n"


def record(work, job, step, status, code, directory, start, end, description, *argv):
    """A step record as check-fast.sh's record_step writes it."""
    fields = [status, str(code), directory, str(start), str(end), description, *argv]
    (Path(work) / ("job.%d.step.%d" % (job, step))).write_bytes(b"".join(f.encode() + b"\0" for f in fields))


class FakeRunner:
    """Records each command; git worktree add makes the checkout from files, remove deletes it; the rest answers from
    a function of (argv, cwd)."""

    def __init__(self, answer=None, files=()):
        self.calls, self.answer, self.files = [], answer or (lambda argv, cwd: (0, "")), files

    def __call__(self, argv, cwd, timeout, slot=None):
        self.calls.append((list(argv), Path(cwd), timeout, slot))
        if argv[:1] == ["git"] and "worktree" in argv:
            path = Path(argv[-2] if "add" in argv else argv[-1])
            if "add" in argv:
                for name in self.files:
                    (path / name).parent.mkdir(parents=True, exist_ok=True)
                    (path / name).write_text("")
            else:
                shutil.rmtree(path, ignore_errors=True)
            return 0, ""
        return self.answer(argv, Path(cwd))

    def commands(self):
        return [argv for argv, _, _, _ in self.calls if argv[0] != "git"]

    def worktree_calls(self):
        return [argv for argv, _, _, _ in self.calls if argv[0] == "git"]


class HelperTestCase(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp(prefix="check-fast-compare-"))
        self.addCleanup(shutil.rmtree, self.root)
        (self.root / "server").mkdir()
        (self.root / "server/go.mod").write_text("module " + MODULE + "\n\ngo 1.24\n")
        for package in ("assistant", "other"):  # where a failure that passes at the base is rerun on its own
            (self.root / "server/internal" / package).mkdir(parents=True)
        self.work = self.root / "work"
        self.work.mkdir()
        self.out = io.StringIO()

    def step(self, status, directory, output, *argv, description="a check"):
        return compare.Step(status, 1, directory, list(argv), output, description)

    def comparison(self, steps, runner, changed=(), tip=None):
        base = compare.Commit("b" * 40, "merge base with", "origin/main")
        tip = compare.Commit(tip, "tip of", "origin/main") if tip else None
        return compare.Comparison(steps, self.root, changed, self.work / "compare", base, tip, runner,
                                  compare.Timeouts(300, 120), self.out)

    def lines(self):
        return self.out.getvalue().splitlines()


class ParsingTests(HelperTestCase):
    def test_go_failures_are_named_by_the_package_line_after_them(self):
        failed = compare.go_failures(GO_HEAD, MODULE)
        self.assertEqual({"./internal/assistant": ["TestRedOnMain", "TestMine"], "./internal/other": ["TestOther"],
                          "./internal/broken": []}, failed)

    def test_go_packages_outside_the_module_are_not_named(self):
        self.assertEqual({}, compare.go_failures("--- FAIL: TestX (0.0s)\nFAIL\telsewhere.org/x\t0.1s\n", MODULE))
        self.assertEqual({}, compare.go_failures(GO_HEAD, ""))

    def test_unittest_ids_in_both_python_formats(self):
        self.assertEqual((["test_thing.T.test_a", "test_thing.T.test_b", "test_thing.T.test_c", "test_thing.Slow"], []),
                         compare.unittest_failures(PY_39))
        self.assertEqual((["test_thing.T.test_a", "test_thing.T.test_b"], []), compare.unittest_failures(PY_311))

    def test_a_test_the_loader_cannot_find_is_unloaded_not_failed(self):
        output = ("ERROR: test_gone (unittest.loader._FailedTest)\n"
                  "ERROR: test_gone2 (unittest.loader._FailedTest.test_gone2)\n")
        self.assertEqual(([], ["test_gone", "test_gone2"]), compare.unittest_failures(output))

    def test_a_record_reads_its_step_output_from_the_byte_range(self):
        first, second = b"first step\n", b"second \xe2\x9c\x93 step\n"
        (self.work / "job.2.steps.log").write_bytes(first + second + b"third\n")
        record(self.work, 2, 1, "FAIL", 1, "scripts/tools", len(first), len(first + second), "scripts/tools/test_x.py",
               "python3", "-B", "-m", "unittest", "-q", "test_x")
        (step,) = compare.job_steps(self.work, 2)
        self.assertEqual("second ✓ step\n", step.output)
        self.assertEqual(["python3", "-B", "-m", "unittest", "-q", "test_x"], step.argv)
        self.assertEqual(("FAIL", "scripts/tools", "scripts/tools/test_x.py"),
                         (step.status, step.directory, step.description))

    def test_steps_come_in_job_then_step_order_and_one_job_alone(self):
        for job, number in ((10, 1), (2, 2), (2, 10)):
            record(self.work, job, number, "FAIL", 1, ".", 0, 0, "job %d step %d" % (job, number), "false")
        (self.work / "job.2.steps.log").write_text("")
        (self.work / "job.2.nsteps").write_text("10\n")
        self.assertEqual(["job 2 step 2", "job 2 step 10", "job 10 step 1"],
                         [step.description for step in compare.job_steps(self.work)])
        self.assertEqual(["job 10 step 1"], [step.description for step in compare.job_steps(self.work, 10)])

    def test_a_broken_record_is_an_error(self):
        (self.work / "job.0.step.1").write_bytes(b"FAIL\0")
        with self.assertRaises(compare.CompareError):
            compare.job_steps(self.work)


class RerunLineTests(HelperTestCase):
    def test_go_reruns_the_named_tests_by_package_with_count_1(self):
        step = self.step("FAIL", "server", GO_HEAD, "go", "test", "-count=1", "-timeout", "290s", "./internal/...")
        self.assertEqual([
            "(cd server && go test -count=1 -run '^(TestRedOnMain|TestMine)$' ./internal/assistant)",
            "(cd server && go test -count=1 -run '^(TestOther)$' ./internal/other)",
            "(cd server && go test -count=1 ./internal/broken)",
        ], compare.rerun_lines([step], self.root))

    def test_go_through_the_partitioned_runner_reruns_the_named_tests_of_every_partition(self):
        step = self.step("FAIL", "server", GO_PARTITIONED, *RUNNER, "go", "test", "-count=1", "-timeout", "290s",
                         "./internal/assistant/...")
        self.assertEqual([
            "(cd server && go test -count=1 -run '^(TestRedOnMain|TestMine)$' ./internal/assistant)",
            "(cd server && go test -count=1 -run '^(TestInbox)$' ./internal/assistant/kbsent)",
        ], compare.rerun_lines([step], self.root))

    def test_a_timed_out_partitioned_run_reruns_through_the_runner(self):
        step = self.step("TIMEOUT", "server", "", *RUNNER, "go", "test", "-count=1", "-timeout", "290s",
                         "./internal/assistant/...")
        self.assertEqual(["(cd server && python3 -B ../scripts/test/go_test_partitioned.py go test -count=1 -timeout "
                          "290s ./internal/assistant/...)"], compare.rerun_lines([step], self.root))

    def test_the_go_command_a_step_runs(self):
        go = ["go", "test", "./..."]
        for argv, expected in ((go, go), ([*RUNNER, *go], go), (["python3", "-I", "x/go_test_partitioned.py", *go], go),
                               (["../scripts/test/go_test_partitioned.py", *go], go),
                               (["env", "GOOS=windows", "GOARCH=amd64", "go", "vet", "./..."], ["go", "vet", "./..."]),
                               (["env", "GOOS=windows", "python3", "x.py"], None), (["env"], None),
                               (["python3", "scripts/privacy_check.py"], None), (["python3", "-m", "unittest"], None),
                               (["python3", "go_test_partitioned.py"], None), (["npm", "test"], None), ([], None)):
            with self.subTest(argv=argv):
                self.assertEqual(expected, compare.go_command(argv))

    def test_go_without_a_package_line_reruns_its_command(self):
        step = self.step("FAIL", "server", "go: no go.mod\n", "go", "test", "-count=1", "./...")
        self.assertEqual(["(cd server && go test -count=1 ./...)"], compare.rerun_lines([step], self.root))

    def test_unittest_reruns_the_failing_ids(self):
        step = self.step("FAIL", "scripts/tools", PY_39, "python3", "-B", "-m", "unittest", "-q", "test_thing")
        self.assertEqual(["(cd scripts/tools && python3 -B -m unittest -q test_thing.T.test_a test_thing.T.test_b "
                          "test_thing.T.test_c test_thing.Slow)"], compare.rerun_lines([step], self.root))

    def test_a_guard_or_a_timeout_reruns_its_command(self):
        guard = self.step("FAIL", ".", "found a host name\n", "python3", "scripts/privacy_check.py")
        slow = self.step("TIMEOUT", "server", "", "go", "test", "-count=1", "-timeout", "290s", "./internal/node/...")
        self.assertEqual(["python3 scripts/privacy_check.py",
                          "(cd server && go test -count=1 -timeout 290s ./internal/node/...)"],
                         compare.rerun_lines([guard, slow], self.root))

    def test_main_prints_the_lines_of_one_job(self):
        (self.work / "job.0.steps.log").write_text("FAIL: test_a (test_thing.T)\n")
        record(self.work, 0, 1, "FAIL", 1, "scripts/tools", 0, 28, "scripts/tools/test_thing.py",
               "python3", "-B", "-m", "unittest", "-q", "test_thing")
        out = io.StringIO()
        saved, sys.stdout = sys.stdout, out
        try:
            code = compare.main(["rerun", "--work", str(self.work), "--job", "0", "--root", str(self.root)])
        finally:
            sys.stdout = saved
        self.assertEqual(0, code)
        self.assertEqual("(cd scripts/tools && python3 -B -m unittest -q test_thing.T.test_a)\n", out.getvalue())


class CompareTests(HelperTestCase):
    def go_step(self):
        return self.step("FAIL", "server", GO_HEAD, "go", "test", "-count=1", "-timeout", "290s", "./internal/...")

    def test_named_go_tests_rerun_at_the_base_and_get_a_verdict_each(self):
        def answer(argv, cwd):
            if "base" not in cwd.parts:  # rerun here on its own: TestMine fails again, TestOther passes
                return 1, "--- FAIL: TestMine (0.00s)\n--- PASS: TestOther (0.00s)\nFAIL\n"
            if argv[-1] == "./internal/assistant":
                return 1, "--- FAIL: TestRedOnMain (0.01s)\n--- PASS: TestMine (0.00s)\nFAIL\n"
            return 0, "=== RUN   TestOther\n--- PASS: TestOther (0.00s)\nok\n"
        runner = FakeRunner(answer, files=("server/internal/assistant/a.go", "server/internal/other/o.go"))
        counts = self.comparison([self.go_step()], runner).compare()
        lines = self.lines()
        self.assertIn("ALREADY  server/internal/assistant TestRedOnMain: also fails at bbbbbbbbbb (merge base with "
                      "origin/main)", lines)
        self.assertIn("NEW      server/internal/assistant TestMine: passes at bbbbbbbbbb (merge base with origin/main) "
                      "and fails again when rerun here on its own, so likely caused by this change", lines)
        self.assertIn("FLAKY    server/internal/other TestOther: passes at bbbbbbbbbb (merge base with origin/main) and "
                      "when rerun here on its own: it failed only in the full run (load, timing or test order), so "
                      "this change is not shown to cause it", lines)
        self.assertTrue(any(line.startswith("UNKNOWN  server/internal/broken (no named test failed): not compared: "
                                            "no named test failed") for line in lines), lines)
        self.assertEqual((1, 1, 1, 0, 1), (counts["ALREADY"], counts["NEW"], counts["FLAKY"], counts["EDITED"],
                                           counts["UNKNOWN"]))
        checkout = self.work / "compare/base"
        self.assertEqual([
            ["go", "test", "-count=1", "-v", "-timeout", "290s", "-run", "^(TestRedOnMain|TestMine)$",
             "./internal/assistant"],
            ["go", "test", "-count=1", "-v", "-timeout", "290s", "-run", "^(TestOther)$", "./internal/other"],
            # Here, only what passed at the base.
            ["go", "test", "-count=1", "-v", "-timeout", "290s", "-run", "^(TestMine)$", "./internal/assistant"],
            ["go", "test", "-count=1", "-v", "-timeout", "290s", "-run", "^(TestOther)$", "./internal/other"],
        ], runner.commands())
        self.assertEqual([(checkout / "server", "go")] * 2 + [(self.root / "server", "go")] * 2,
                         [(cwd, slot) for argv, cwd, _, slot in runner.calls if argv[0] == "go"])
        self.assertEqual([
            ["git", "-C", str(self.root), "worktree", "add", "--detach", "--quiet", str(checkout), "b" * 40],
            ["git", "-C", str(self.root), "worktree", "remove", "--force", str(checkout)],
        ], runner.worktree_calls())
        self.assertFalse(checkout.exists())
        self.assertTrue(lines[-1].startswith("check-fast --compare-main: 4 failure(s) against bbbbbbbbbb: 1 ALREADY"),
                        lines[-1])
        self.assertIn("1 NEW (passing there, failing here again: likely this change), 1 FLAKY", lines[-1])

    def test_a_test_whose_package_test_files_the_change_edits_is_not_attributed(self):
        runner = FakeRunner(lambda argv, cwd: (0, "--- PASS: TestOther (0.00s)\n") if "base" in cwd.parts else
                            (1, "--- FAIL: TestOther (0.00s)\n"), files=("server/internal/other/o.go",))
        changed = ["server/internal/assistant/golden_test.go", "server/internal/other/other.go",
                   "server/internal/hub/hub_test.go"]
        self.comparison([self.go_step()], runner, changed).compare()
        lines = self.lines()
        for name in ("TestRedOnMain", "TestMine"):
            self.assertIn("EDITED   server/internal/assistant %s: not attributable, because this change edits that "
                          "test's files (server/internal/assistant/golden_test.go)" % name, lines)
        # A test file of another package is not compiled into these tests, and a non-test file is the change itself.
        self.assertIn("NEW      server/internal/other TestOther: passes at bbbbbbbbbb (merge base with origin/main) "
                      "and fails again when rerun here on its own, so likely caused by this change", lines)
        self.assertEqual(["./internal/other"] * 2, [argv[-1] for argv in runner.commands()])

    def test_testdata_counts_as_the_packages_test_files(self):
        failure = compare.GoTests("server", "./internal/assistant", ["TestRedOnMain"])
        self.assertEqual(["server/internal/assistant/testdata/golden/x.txt"], failure.test_files(
            ["server/internal/assistant/testdata/golden/x.txt", "server/internal/assistant/sub/x_test.go",
             "server/internal/assistantx/x_test.go"]))

    def test_go_verdicts_for_a_missing_test_a_build_failure_and_a_timeout(self):
        step = self.step("FAIL", "server", "--- FAIL: TestGone (0.0s)\nFAIL\tex/server/internal/a\t0.1s\n"
                         "--- FAIL: TestBroken (0.0s)\nFAIL\tex/server/internal/b\t0.1s\n"
                         "--- FAIL: TestSlow (0.0s)\nFAIL\tex/server/internal/c\t0.1s\n"
                         "--- FAIL: TestNoDir (0.0s)\nFAIL\tex/server/internal/d\t0.1s\n",
                         "go", "test", "-count=1", "./internal/...")
        (self.root / "server/go.mod").write_text("module ex/server\n")

        def answer(argv, cwd):
            return {"./internal/a": (0, "testing: warning: no tests to run\nok\n"),
                    "./internal/b": (1, "FAIL\tex/server/internal/b [build failed]\n"),
                    "./internal/c": (124, "")}[argv[-1]]
        runner = FakeRunner(answer, files=("server/internal/a/a.go", "server/internal/b/b.go", "server/internal/c/c.go"))
        self.comparison([step], runner).compare()
        text = self.out.getvalue()
        self.assertIn("UNKNOWN  server/internal/a TestGone: not compared: bbbbbbbbbb (merge base with origin/main) has "
                      "no test TestGone", text)
        self.assertIn("UNKNOWN  server/internal/b TestBroken: not compared: server/internal/b does not build at", text)
        self.assertIn("UNKNOWN  server/internal/c TestSlow: not compared: the rerun at bbbbbbbbbb (merge base with "
                      "origin/main) timed out", text)
        self.assertIn("UNKNOWN  server/internal/d TestNoDir: not compared: bbbbbbbbbb (merge base with origin/main) "
                      "has no server/internal/d", text)

    def test_python_ids_rerun_at_the_base(self):
        (self.root / "scripts/tools").mkdir(parents=True)
        (self.root / "scripts/tools/test_thing.py").write_text("import unittest\n")
        step = self.step("FAIL", "scripts/tools", PY_39, "python3", "-B", "-m", "unittest", "-q", "test_thing")

        def answer(argv, cwd):
            if "base" not in cwd.parts:
                return 1, "ERROR: test_b (test_thing.T)\n"
            return 1, ("FAIL: test_a (test_thing.T)\nERROR: setUpClass (test_thing.Slow)\n"
                       "ERROR: test_c (unittest.loader._FailedTest)\n")
        runner = FakeRunner(answer, files=("scripts/tools/test_thing.py",))
        counts = self.comparison([step], runner).compare()
        text = self.out.getvalue()
        self.assertIn("ALREADY  scripts/tools test_thing.T.test_a: also fails at", text)
        self.assertIn("NEW      scripts/tools test_thing.T.test_b: passes at bbbbbbbbbb (merge base with origin/main) "
                      "and fails again", text)
        self.assertIn("UNKNOWN  scripts/tools test_thing.T.test_c: not compared: bbbbbbbbbb (merge base with "
                      "origin/main) has no test test_thing.T.test_c", text)
        self.assertIn("ALREADY  scripts/tools test_thing.Slow: also fails at", text)
        self.assertEqual((2, 1, 1), (counts["ALREADY"], counts["NEW"], counts["UNKNOWN"]))
        self.assertEqual([["python3", "-B", "-m", "unittest", "-q", "test_thing.T.test_a", "test_thing.T.test_b",
                           "test_thing.T.test_c", "test_thing.Slow"],
                          ["python3", "-B", "-m", "unittest", "-q", "test_thing.T.test_b"]], runner.commands())
        self.assertEqual(self.work / "compare/base/scripts/tools", runner.calls[1][1])

    def test_a_suite_that_does_not_load_at_the_base_says_so(self):
        (self.root / "scripts/tools").mkdir(parents=True)
        (self.root / "scripts/tools/test_thing.py").write_text("import unittest\n")
        step = self.step("FAIL", "scripts/tools", PY_311, "python3", "-B", "-m", "unittest", "-q", "test_thing")
        runner = FakeRunner(lambda argv, cwd: (1, "ERROR: test_thing (unittest.loader._FailedTest)\n"),
                            files=("scripts/tools/test_thing.py",))
        self.comparison([step], runner).compare()
        self.assertIn("UNKNOWN  scripts/tools test_thing.T.test_a: not compared: scripts/tools/test_thing.py does not "
                      "load at bbbbbbbbbb", self.out.getvalue())

    def test_a_partitioned_go_command_reruns_with_the_go_timeout_in_a_go_slot(self):
        (self.root / "server").mkdir(exist_ok=True)
        command = compare.Command("server", [*RUNNER, "go", "test", "-count=1", "./internal/assistant/..."],
                                  "go test ./internal/assistant/...", root=self.root)
        runner = FakeRunner(lambda argv, cwd: (1, "FAIL\n"))
        checkout = compare.WorkingTree(self.root)
        command.compare(checkout, runner, compare.Timeouts(300, 120), lambda subject, output: "a log")
        self.assertEqual([(300, "go")], [(timeout, slot) for _, _, timeout, slot in runner.calls])

    def test_a_go_command_env_runs_reruns_whole_with_its_environment(self):
        # check-fast's Windows vet (#12894): its rerun keeps GOOS, and it reruns at the base in a go slot under the go
        # timeout. A go test env runs stays whole too, since narrowing it to named tests would drop the environment.
        vet = ["env", "GOOS=windows", "GOARCH=amd64", "go", "vet", "-ldflags=", "./internal/foo/..."]
        windows = self.step("FAIL", "server", "vet: x.go:3:2: undefined: syscall.Kill\n", *vet)
        named = self.step("FAIL", "server", GO_HEAD, "env", "GOOS=windows", "go", "test", "-count=1", "./...")
        self.assertEqual(["(cd server && env GOOS=windows GOARCH=amd64 go vet -ldflags= ./internal/foo/...)",
                          "(cd server && env GOOS=windows go test -count=1 ./...)"],
                         compare.rerun_lines([windows, named], self.root))
        command = compare.Command("server", vet, "GOOS=windows go vet ./internal/foo/...", root=self.root)
        runner = FakeRunner(lambda argv, cwd: (1, "vet: x.go:3:2: undefined: syscall.Kill\n"))
        command.compare(compare.WorkingTree(self.root), runner, compare.Timeouts(300, 120), lambda subject, out: "a log")
        self.assertEqual([(vet, 300, "go")], [(argv, timeout, slot) for argv, _, timeout, slot in runner.calls])

    def fresh_package_vet(self, *packages):
        """check-fast's Windows vet of packages, one of which, internal/fresh, this change adds (#12894)."""
        (self.root / "server/internal/fresh").mkdir()
        argv = ("env", "GOOS=windows", "GOARCH=amd64", "go", "vet", "-ldflags=", *packages)
        output = "# " + MODULE + "/internal/fresh\nvet: internal/fresh/stop.go:7:17: undefined: syscall.Kill\n"
        return argv, self.step("FAIL", "server", output, *argv, description="GOOS=windows go vet")

    def test_a_go_command_of_only_packages_the_base_lacks_is_not_compared(self):
        # go fails on a pattern whose directory is missing (lstat ...: no such file or directory) before it vets
        # anything, so that exit code says nothing about main: the package is new, as a named test's can be.
        _, step = self.fresh_package_vet("./internal/fresh/...")
        runner = FakeRunner(lambda argv, cwd: (1, "pattern ./internal/fresh/...: lstat ./internal/fresh/: no such "
                                                  "file or directory\n"), files=("server/internal/assistant/a.go",))
        counts = self.comparison([step], runner).compare()
        self.assertIn("UNKNOWN  GOOS=windows go vet: not compared: bbbbbbbbbb (merge base with origin/main) has no "
                      "server/internal/fresh", self.lines())
        self.assertEqual((0, 0, 1), (counts["ALREADY"], counts["NEW"], counts["UNKNOWN"]))
        self.assertEqual([], runner.commands())

    def test_a_go_command_reruns_at_the_base_without_the_packages_it_lacks(self):
        # Beside a group the base has, the new package is left out there, and the whole command reruns here.
        argv, step = self.fresh_package_vet("./internal/assistant/...", "./internal/fresh/...")
        runner = FakeRunner(lambda argv, cwd: (0, "") if "base" in cwd.parts else (1, step.output),
                            files=("server/internal/assistant/a.go",))
        counts = self.comparison([step], runner).compare()
        self.assertIn("NEW      GOOS=windows go vet: passes at bbbbbbbbbb (merge base with origin/main) and fails again "
                      "when rerun here on its own, so likely caused by this change", self.lines())
        self.assertEqual(1, counts["NEW"])
        self.assertEqual([(list(argv[:-1]), "base"), (list(argv), "work")],
                         [(argv, "base" if "base" in cwd.parts else "work") for argv, cwd, _, _ in runner.calls
                          if argv[0] == "env"])

    def test_a_go_command_that_fails_at_the_base_without_the_packages_it_lacks_says_so(self):
        argv, step = self.fresh_package_vet("./internal/assistant/...", "./internal/fresh/...")
        runner = FakeRunner(lambda argv, cwd: (1, "vet: internal/assistant/a.go:3:2: undefined: syscall.Kill\n"),
                            files=("server/internal/assistant/a.go",))
        self.comparison([step], runner).compare()
        self.assertIn("ALREADY  GOOS=windows go vet: also fails at bbbbbbbbbb (merge base with origin/main) (rerun there "
                      "without server/internal/fresh, which it does not have); 2 line(s) of its output here are not in "
                      "the base's, so this change may add to it, the first: # " + MODULE + "/internal/fresh",
                      self.lines())

    def test_a_command_narrows_to_itself_or_nothing(self):
        command = compare.Command(".", ["python3", "x.py"], "python3 x.py", root=self.root)
        self.assertIs(command, command.only(["python3 x.py"]))
        self.assertIsNone(command.only([]))

    def test_a_suite_the_change_edits_or_one_it_imports_is_not_attributed(self):
        (self.root / "scripts/test").mkdir(parents=True)
        (self.root / "scripts/test/test_guards.py").write_text("from test_fixtures import Repo\nimport unittest\n")
        step = self.step("FAIL", "scripts/test", "FAIL: test_a (test_guards.T)\n",
                         "python3", "-B", "-m", "unittest", "-q", "test_guards")
        runner = FakeRunner()
        self.comparison([step], runner, ["scripts/test/test_fixtures.py"]).compare()
        self.assertIn("EDITED   scripts/test test_guards.T.test_a: not attributable, because this change edits that "
                      "test's files (scripts/test/test_fixtures.py)", self.lines())
        self.assertEqual([], runner.calls)  # nothing left to compare: no checkout

    def test_a_guard_command_compares_its_exit_code_and_names_new_lines(self):
        same = self.step("FAIL", ".", "scripts/a.py:3: a host name (12.5s)\n", "python3", "scripts/privacy_check.py",
                         description="python3 scripts/privacy_check.py")
        more = self.step("FAIL", ".", "x.go: 1 -> 2\ny.go: 0 -> 1\n", "python3", "scripts/ci/sleep_ratchet.py",
                         description="python3 scripts/ci/sleep_ratchet.py")
        fixed = self.step("FAIL", "scripts/app", "missing ja\n", "python3", "-B", "android_slot_order.py",
                          description="scripts/app: android_slot_order.py")
        (self.root / "scripts/app").mkdir(parents=True)

        def answer(argv, cwd):
            if "base" not in cwd.parts:
                return 1, "missing ja\n"
            return {"scripts/privacy_check.py": (1, "scripts/a.py:3: a host name (0.4s)\n"),
                    "scripts/ci/sleep_ratchet.py": (1, "x.go: 1 -> 2\n"),
                    "android_slot_order.py": (0, "")}[argv[-1]]
        runner = FakeRunner(answer, files=("scripts/app/android_slot_order.py",))
        self.comparison([same, more, fixed], runner).compare()
        lines = self.lines()
        self.assertIn("ALREADY  python3 scripts/privacy_check.py: also fails at bbbbbbbbbb (merge base with "
                      "origin/main)", lines)
        self.assertIn("ALREADY  python3 scripts/ci/sleep_ratchet.py: also fails at bbbbbbbbbb (merge base with "
                      "origin/main); 1 line(s) of its output here are not in the base's, so this change may add to it, "
                      "the first: y.go: 0 -> 1", lines)
        self.assertIn("NEW      scripts/app: android_slot_order.py: passes at bbbbbbbbbb (merge base with origin/main) "
                      "and fails again when rerun here on its own, so likely caused by this change", lines)
        self.assertEqual({None}, {slot for argv, _, _, slot in runner.calls if argv[0] != "git"})

    def test_a_guard_the_change_edits_is_not_attributed(self):
        step = self.step("FAIL", ".", "found\n", "python3", "scripts/privacy_check.py",
                         description="python3 scripts/privacy_check.py")
        self.comparison([step], FakeRunner(), ["scripts/privacy_check.py"]).compare()
        self.assertIn("EDITED   python3 scripts/privacy_check.py: not attributable, because this change edits that "
                      "test's files (scripts/privacy_check.py)", self.lines())

    def test_web_installs_and_timeouts_are_not_compared(self):
        web = self.step("FAIL", "web", "error TS2322\n", "npm", "run", "typecheck", description="web: tsc -b (typecheck)")
        slow = self.step("TIMEOUT", "server", "", "go", "test", "-count=1", "./internal/node/...")
        runner = FakeRunner()
        counts = self.comparison([web, slow], runner).compare()
        self.assertIn("UNKNOWN  web: tsc -b (typecheck): not compared: npm needs its own install at the base "
                      "(web/node_modules); rerun it there by hand", self.lines())
        self.assertEqual(1, counts["UNKNOWN"])  # the timeout is not a failure to compare
        self.assertEqual([], runner.calls)

    def test_the_tip_reruns_only_what_also_fails_at_the_base(self):
        tip = "c" * 40

        def answer(argv, cwd):
            at_tip = "/compare/tip/" in str(cwd) + "/"
            if "base" not in cwd.parts and not at_tip:
                return 1, "--- FAIL: TestMine (0.0s)\n"
            if argv[-1] == "./internal/assistant":
                return 1, ("--- PASS: TestRedOnMain (0.0s)\n" if at_tip else
                           "--- FAIL: TestRedOnMain (0.0s)\n--- PASS: TestMine (0.0s)\n")
            return 1, "--- FAIL: TestOther (0.0s)\n"
        runner = FakeRunner(answer, files=("server/internal/assistant/a.go", "server/internal/other/o.go"))
        counts = self.comparison([self.go_step()], runner, tip=tip).compare()
        lines = self.lines()
        self.assertIn("TIP-PASS server/internal/assistant TestRedOnMain: passes at cccccccccc (tip of origin/main): "
                      "main fixed it after the merge base, so merging origin/main brings the fix", lines)
        self.assertIn("TIP-FAIL server/internal/other TestOther: also fails at cccccccccc (tip of origin/main)", lines)
        at_tip = [argv for argv, cwd, _, _ in runner.calls if argv[0] == "go" and "tip" in cwd.parts]
        self.assertEqual([["go", "test", "-count=1", "-v", "-timeout", "290s", "-run", "^(TestRedOnMain)$",
                           "./internal/assistant"],
                          ["go", "test", "-count=1", "-v", "-timeout", "290s", "-run", "^(TestOther)$",
                           "./internal/other"]], at_tip)
        self.assertEqual((1, 1), (counts["TIP-PASS"], counts["TIP-FAIL"]))
        self.assertIn("at the tip cccccccccc: 1 TIP-PASS (fixed since), 1 TIP-FAIL (still failing), 0 TIP-?", lines[-1])
        self.assertFalse((self.work / "compare/tip").exists())

    def test_a_tip_that_gives_no_answer_is_not_counted_again(self):
        def answer(argv, cwd):
            if "tip" in cwd.parts:
                return 124, ""
            return 1, "--- FAIL: TestRedOnMain (0.0s)\n--- FAIL: TestMine (0.0s)\n--- FAIL: TestOther (0.0s)\n"
        runner = FakeRunner(answer, files=("server/internal/assistant/a.go", "server/internal/other/o.go"))
        counts = self.comparison([self.go_step()], runner, tip="c" * 40).compare()
        self.assertEqual((3, 1, 3), (counts["ALREADY"], counts["UNKNOWN"], counts["TIP-?"]))
        self.assertIn("TIP-?    server/internal/other TestOther: not compared: the rerun at cccccccccc (tip of "
                      "origin/main) timed out", self.out.getvalue())
        self.assertIn("4 failure(s) against bbbbbbbbbb", self.lines()[-1])
        self.assertIn("0 TIP-PASS (fixed since), 0 TIP-FAIL (still failing), 3 TIP-? (not compared)", self.lines()[-1])

    def test_the_checkout_is_removed_by_its_path_when_a_rerun_breaks(self):
        def answer(argv, cwd):
            raise KeyboardInterrupt
        runner = FakeRunner(answer, files=("server/internal/assistant/a.go",))
        with self.assertRaises(KeyboardInterrupt):
            self.comparison([self.go_step()], runner).compare()
        checkout = str(self.work / "compare/base")
        self.assertEqual(["git", "-C", str(self.root), "worktree", "remove", "--force", checkout],
                         runner.worktree_calls()[-1])
        self.assertFalse(Path(checkout).exists())

    def test_a_checkout_that_cannot_be_made_stops_the_comparison(self):
        calls = []

        def runner(argv, cwd, timeout, slot=None):
            calls.append(argv)
            return (128, "fatal: invalid reference\n") if "add" in argv else (0, "")
        with self.assertRaises(compare.CompareError) as raised:
            self.comparison([self.go_step()], runner).compare()
        self.assertIn("git worktree add bbbbbbbbbb failed: fatal: invalid reference", str(raised.exception))
        self.assertEqual(["worktree", "remove", "--force"], calls[-1][3:6])

    def test_a_path_in_the_way_is_not_reused(self):
        (self.work / "compare/base").mkdir(parents=True)
        with self.assertRaises(compare.CompareError):
            self.comparison([self.go_step()], FakeRunner()).compare()


class RunnerTests(unittest.TestCase):
    def test_a_go_command_takes_a_slot_outside_its_timeout(self):
        with tempfile.TemporaryDirectory() as directory:
            slot = Path(directory) / "heavy_slot.py"
            slot.write_text("import sys, subprocess\nsys.exit(subprocess.call(sys.argv[sys.argv.index('--') + 1:]))\n")
            runner = compare.Runner(timeout_bin=shutil.which("timeout") or shutil.which("gtimeout"), heavy_slot=slot)
            if not runner.timeout_bin:
                self.skipTest("GNU timeout not found")
            code, output = runner([sys.executable, "-c", "print('ran')"], cwd=directory, timeout=30, slot="go")
            self.assertEqual((0, "ran\n"), (code, output))
            code, _ = runner([sys.executable, "-c", "import time; time.sleep(5)"], cwd=directory, timeout=1)
            self.assertEqual(124, code)


class SourceTests(unittest.TestCase):
    def test_it_does_not_import_the_pr_watcher(self):
        # The comparison follows the watcher's rules but runs without its configuration (#12733).
        tree = ast.parse((Path(compare.__file__)).read_text())
        imported = {alias.name for node in ast.walk(tree) if isinstance(node, ast.Import) for alias in node.names}
        imported |= {node.module for node in ast.walk(tree) if isinstance(node, ast.ImportFrom)}
        self.assertEqual(set(), {name for name in imported if name and name.startswith("pr_watch")})

    def test_every_go_test_it_runs_has_count_1(self):
        for argv in (compare.GoTests("server", "./internal/x", ["TestA"]).argv(),):
            self.assertIn("-count=1", argv)


if __name__ == "__main__":
    unittest.main()
