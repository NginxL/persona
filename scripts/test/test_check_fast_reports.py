"""Focused check-fast tests, split by behavior (#14321).

The shared fixture and these test methods are unchanged from test_check_fast.py.
Separate files keep each CI suite inside its existing 120-second bound.
"""
from pathlib import Path
import os
import subprocess
import sys
import unittest

from test_check_fast import FAILING, FAKE_PYTHON, PASSING, ROOT, ScriptsRepoTestCase, ci_is_suite

class CheckFastReportTests(ScriptsRepoTestCase):
    """The --all run and its reporting."""

    def test_all_runs_every_scripts_suite_in_one_job(self):
        # Mirror CI's Scripts tests job: --all runs every scripts/**/test_*.py, changed or not, except the ones a
        # dedicated job already runs. That stub fails under unittest, so the suite job must not pick it up. The count
        # is 9: test_other, test_gone, test_deep and the six guard suites stubbed in setUp.
        self.write("scripts/test/test_external_service_refs.py", FAILING)
        self.write("scripts/deep/nested/test_deep.py", PASSING)
        code, out = self.check_fast("--all", path=self.toolchain_without_node_and_go())
        self.assertEqual(1, code, out)  # missing node and go are core failures in --all mode
        self.assertIn("queued: scripts unittest suites (9)", out)
        self.assertIn("FAIL     scripts unittest suites", out)
        self.assertIn("9 suite(s): 1 failed, 0 timed out", out)
        self.assertIn("failed: scripts/tools/test_other.py", out)
        self.assertNotIn("failed: scripts/test/test_external_service_refs.py", out)
        self.assertNotIn("queued: scripts/tools/test_gone.py", out)  # one job, not one per suite


    def test_a_test_file_that_never_mentions_unittest_is_skipped_as_in_ci(self):
        # python -m unittest runs no tests in such a verification script and exits 5 ("NO TESTS RAN") from Python 3.12
        # on, so CI's python-tests.py skips it. The --all count is the eight suites of setUp.
        self.write("scripts/tools/test_verify.py", "print('a verification script that takes arguments')\n")
        code, out = self.check_fast("--all", path=self.toolchain_without_node_and_go())
        self.assertEqual(1, code, out)  # test_other.py fails, and node and go are missing
        self.assertIn("SKIP  scripts/tools/test_verify.py (not a unittest suite)", out)
        self.assertIn("8 suite(s): 1 failed, 0 timed out", out)
        code, out = self.check_fast()
        self.assertEqual(0, code, out)
        self.assertIn("SKIP  scripts/tools/test_verify.py (not a unittest suite)", out)
        self.assertNotIn("queued: scripts/tools/test_verify.py", out)


    def test_all_runs_the_suites_ci_runs(self):
        # Drift guard: on a copy of every real test_*.py, --all runs python-tests.py's suites minus the ones the
        # dedicated jobs run (script_test_add), and reports each file CI skips. A python3 stub records the runs.
        for real in ROOT.joinpath("scripts").rglob("test_*.py"):
            if "node_modules" not in real.parts:
                self.write(real.relative_to(ROOT), real.read_text(encoding="utf-8", errors="replace"))
        is_suite = ci_is_suite()
        found = {str(p.relative_to(self.repo)): is_suite(p) for p in self.repo.joinpath("scripts").rglob("test_*.py")}
        dedicated = {"scripts/test/test_external_service_refs.py", "scripts/test/test_sdk_bundle.py",
                     "scripts/test/test_extension_schema.py"}
        dedicated.update("scripts/app/test_android_%s.py" % name  # job_i18n runs these four
                         for name in ("i18n_check", "token_baseline", "motion_baseline", "slot_order"))
        expected = sorted(p for p, suite in found.items() if suite and p not in dedicated)
        bin_dir = Path(self.toolchain_without_node_and_go())
        (bin_dir / "python3").unlink()
        (bin_dir / "python3").write_text(FAKE_PYTHON)
        (bin_dir / "python3").chmod(0o755)
        log = bin_dir / "python.log"
        code, out = self.check_fast("--all", path=str(bin_dir), env={"FAKE_PYTHON_LOG": str(log)})
        ran = sorted(f"{Path(cwd).resolve().relative_to(self.repo.resolve())}/{args.split()[-1]}.py"
                     for cwd, args in (line.split("|", 1) for line in log.read_text().splitlines())
                     if args.startswith("-B -m unittest -q "))
        self.assertIn("scripts/test/test_check_fast.py", expected)
        self.assertEqual(expected, ran, out)
        for path in (p for p, suite in found.items() if not suite):
            self.assertIn(f"SKIP  {path} (not a unittest suite)", out)


    def run_raw(self, *command, env=None):
        """check-fast's exit code and output (stdout and stderr), with bytes that are not UTF-8 replaced."""
        run = subprocess.run(command, cwd=self.repo, env=dict(os.environ, TMPDIR=str(self.repo), **(env or {})),
                             capture_output=True, timeout=300)
        return run.returncode, (run.stdout + run.stderr).decode("utf-8", "replace")

    def test_output_that_is_not_utf8_keeps_its_log_tail(self):
        # In a UTF-8 locale macOS sed stops at the first byte that is not UTF-8 ("illegal byte sequence"), so the
        # summary lost a failed check's log tail (#11250).
        self.write("scripts/tools/test_bytes.py",
                   "import sys, unittest\n\nclass T(unittest.TestCase):\n    def test_bytes(self):\n"
                   "        sys.stdout.buffer.write(b'\\xff\\xfe not UTF-8\\n')\n"
                   "        sys.stdout.flush()\n        self.fail('the line after the bytes')\n")
        utf8 = "en_US.UTF-8" if sys.platform == "darwin" else "C.UTF-8"
        code, out = self.run_raw("bash", "scripts/test/check-fast.sh", "--base", "base", env={"LC_ALL": utf8})
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/tools/test_bytes.py", out)
        self.assertIn("AssertionError: the line after the bytes", out)  # the log's tail, under the summary line
        self.assertNotIn("illegal byte sequence", out)


    def test_with_stderr_closed_the_checks_still_report(self):
        # The job subshells copy stderr for the live slot lines (4>&2); with it closed every job failed with "no
        # status" (#11250). check-fast opens it on /dev/null instead and runs without the live lines. A doc change:
        # the plan of a changed scripts module once opened a pipe that took the free fd 2 and hid the failure.
        self.write("docs/x.md", "changed\n")
        code, out = self.run_raw("bash", "-c", 'exec 2>&-; exec bash scripts/test/check-fast.sh --base base')
        self.assertEqual(0, code, out)
        self.assertIn("PASS     scripts/test/test_doc_paths.py", out)
        self.assertNotIn("no status", out)


    def test_all_reports_a_slow_suite_as_timeout_and_runs_the_rest(self):
        self.write("scripts/tools/test_other.py", PASSING)
        self.write("scripts/tools/test_slow.py", PASSING.replace("pass", "__import__('time').sleep(30)"))
        self.write("scripts/tools/test_z_after.py", FAILING)  # sorted after the slow one: it still runs
        code, out = self.check_fast("--all", path=self.toolchain_without_node_and_go(),
                                    env={"CHECK_FAST_TIMEOUT_OTHER": "2"})
        self.assertEqual(1, code, out)
        self.assertIn("10 suite(s): 1 failed, 1 timed out", out)
        self.assertIn("timed out: scripts/tools/test_slow.py", out)
        self.assertIn("failed: scripts/tools/test_z_after.py", out)
        self.write("scripts/tools/test_z_after.py", PASSING)
        code, out = self.check_fast("--all", path=self.toolchain_without_node_and_go(),
                                    env={"CHECK_FAST_TIMEOUT_OTHER": "2"})
        self.assertIn("TIMEOUT  scripts unittest suites (10)", out)
        self.assertIn("1 suite(s) timed out: scripts/tools/test_slow.py", out)


    def test_a_diff_without_the_table_stops_before_planning(self):
        # Without the table nothing says what a path selects; --all does not read it.
        (self.repo / "scripts/ci/check_fast_map.py").unlink()
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast()
        self.assertEqual(2, code, out)
        self.assertIn("check-fast: picking the checks for a change needs python3 and scripts/ci/check_fast_map.py; "
                      "no plan made", out)
        self.assertNotIn("queued:", out)
        code, out = self.check_fast("--all", "--dry-run")
        self.assertEqual(0, code, out)
        self.assertIn("queued: scripts unittest suites", out)


    def test_a_diff_without_python3_stops_before_planning(self):
        # The toolchain_without_node_and_go tools, python3 left out too.
        bin_dir = Path(self.toolchain_without_node_and_go())
        (bin_dir / "python3").unlink(missing_ok=True)
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast(path=str(bin_dir))
        self.assertEqual(2, code, out)
        self.assertIn("check-fast: picking the checks for a change needs python3 and scripts/ci/check_fast_map.py; "
                      "no plan made", out)


    def test_a_table_that_fails_or_answers_wrong_stops_before_planning(self):
        cases = {
            "raise SystemExit('broken on purpose')\n":
                "python3 scripts/ci/check_fast_map.py failed (broken on purpose)",
            "": "python3 scripts/ci/check_fast_map.py listed fewer lines than paths",
            "import sys\nfor line in sys.stdin:\n    print('web\\tbogus')\n":
                "python3 scripts/ci/check_fast_map.py named a check this script does not know: bogus",
        }
        for table, message in cases.items():
            with self.subTest(message=message):
                self.write("scripts/ci/check_fast_map.py", table)
                code, out = self.check_fast()
                self.assertEqual(2, code, out)
                self.assertIn(f"check-fast: {message}; no plan made", out)
                self.assertNotIn("queued:", out)
