"""check-fast.sh runs the scripts/ unittests a change needs (#3968), and the scripts/test suites that guard docs,
markdown, apple/ and deploy/docker/; Ctrl-C, SIGTERM and SIGHUP stop the checks it started. The server and web checks'
inputs are tested in test_check_fast_inputs.py, split off so each file stays well under the per-suite limit of
scripts/ci/python-tests.py on a loaded machine (#11017).

Each test builds a throwaway repository with check-fast.sh and stubs for the repository-wide checks it always picks
for a scripts/ change (the privacy scan, the extension schema test, the PR workflow test), commits a base, makes a
change on top and runs check-fast.sh --base against the base. The default-base tests run it without --base, against
refs/remotes/origin/* refs they write themselves.
"""
import importlib.util
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest

ROOT = Path(__file__).resolve().parents[2]
PASSING = "import unittest\n\nclass T(unittest.TestCase):\n    def test_ok(self):\n        pass\n"
FAILING = "import unittest\n\nclass T(unittest.TestCase):\n    def test_broken(self):\n        self.fail('broken on purpose')\n"
# The scripts/test suites that guard files outside scripts/; stubbed passing unless a test breaks one.
GUARD_SUITES = ("test_doc_paths.py", "test_doc_go_paths.py", "test_doc_sections.py", "test_doc_wire.py",
                "test_apple_l10n.py", "test_docker_entrypoint.py")
# A python3 that records each call ("<cwd>|<args>") instead of running anything.
FAKE_PYTHON = '#!/bin/sh\nprintf "%s|%s\\n" "$PWD" "$*" >> "$FAKE_PYTHON_LOG"\n'


def ci_is_suite():
    """is_suite of scripts/ci/python-tests.py: the rule CI's Scripts tests job picks its suites by."""
    saved = list(sys.path)  # the module puts scripts/ci and scripts/test on sys.path for its own imports
    try:
        spec = importlib.util.spec_from_file_location("ci_python_tests", ROOT / "scripts/ci/python-tests.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
    finally:
        sys.path[:] = saved
    return module.is_suite


@unittest.skipUnless(shutil.which("timeout") or shutil.which("gtimeout"), "check-fast.sh needs GNU timeout")
class ScriptsRepoTestCase(unittest.TestCase):
    """The throwaway repository the scripts/ tests run check-fast.sh in, and its helpers; no tests of its own.
    test_check_fast_guards.py's tests use it too."""

    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="check-fast-test-"))
        self.addCleanup(shutil.rmtree, self.repo)
        (self.repo / "scripts/test").mkdir(parents=True)
        shutil.copy(ROOT / "scripts/test/check-fast.sh", self.repo / "scripts/test/check-fast.sh")
        self.write("scripts/privacy_check.py", "print('clean')\n")
        self.write("scripts/test/test_extension_schema.py", "")
        self.write("scripts/test/pr_needs_issue_test.mjs", "")
        self.write("scripts/tools/other.py", "")
        self.write("scripts/tools/test_other.py", FAILING)  # not related to the changes below: must not run
        self.write("scripts/tools/gone.py", "")
        self.write("scripts/tools/test_gone.py", PASSING)
        for name in GUARD_SUITES:
            self.write(f"scripts/test/{name}", PASSING)
        self.git("init", "-q")
        self.git("add", "-A")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base")
        self.git("tag", "base")

    def write(self, path, text):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text)

    def git(self, *args):
        subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True)

    def break_suite(self, name):
        """Make scripts/test/<name> fail and commit it into the base, so only the change made afterwards selects it."""
        self.commit_on_top(f"scripts/test/{name}", FAILING, "base")

    def check_fast(self, *args, path=None, env=None, base="base"):
        # TMPDIR inside the throwaway repo: a failed run keeps its log directory, and the cleanup removes it.
        # base=None runs without --base, so the script picks its default base.
        env = dict(os.environ, TMPDIR=str(self.repo), **({"PATH": path} if path else {}), **(env or {}))
        args = args or (("--base", base) if base else ())
        run = subprocess.run(["bash", "scripts/test/check-fast.sh", *args], cwd=self.repo,
                             capture_output=True, text=True, timeout=300, env=env)
        return run.returncode, run.stdout + run.stderr

    def commit_on_top(self, path, text, tag):
        """Commit one more file on top of HEAD and point tag at the new commit."""
        self.write(path, text)
        self.git("add", path)
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", tag)
        self.git("tag", "-f", tag)

    def toolchain_without_node_and_go(self):
        # A PATH with only the tools check-fast.sh itself needs, so node and go are missing on any machine. It lives
        # outside the repository, so a diff-mode run does not count it as a change.
        if getattr(self, "bin_dir", None):
            return str(self.bin_dir)
        self.bin_dir = Path(tempfile.mkdtemp(prefix="check-fast-bin-"))
        self.addCleanup(shutil.rmtree, self.bin_dir)
        bin_dir = self.bin_dir
        for tool in ("bash", "python3", "timeout", "gtimeout", "git", "mktemp", "rm", "dirname", "basename", "find",
                     "sort", "tee", "tail", "grep", "sed", "awk", "tr", "wc", "cut", "cat", "env"):
            found = shutil.which(tool)
            if found:
                (bin_dir / tool).symlink_to(found)
        return str(bin_dir)


class CheckFastScriptsTests(ScriptsRepoTestCase):
    """The base a run diffs against, the scripts/ suites a change names, and --all."""

    def test_without_base_it_diffs_against_origin_head(self):
        # The default branch's tip carries a failing test; a stale origin/master left at the first commit would
        # select it too, and fail the run.
        self.commit_on_top("scripts/tools/test_mid.py", FAILING, "mid")
        self.git("update-ref", "refs/remotes/origin/main", "mid")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
        self.git("update-ref", "refs/remotes/origin/master", "base")
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast(base=None)
        self.assertEqual(0, code, out)
        self.assertIn("merge-base with refs/remotes/origin/main", out)
        self.assertIn("PASS     scripts/tools/test_new.py", out)
        self.assertNotIn("test_mid.py", out)

    def test_without_base_origin_head_wins_over_origin_main(self):
        # A default branch that is neither main nor master; origin/main left at the first commit would select the
        # failing test on top of it.
        self.commit_on_top("scripts/tools/test_mid.py", FAILING, "mid")
        self.git("update-ref", "refs/remotes/origin/trunk", "mid")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
        self.git("update-ref", "refs/remotes/origin/main", "base")
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast(base=None)
        self.assertEqual(0, code, out)
        self.assertIn("merge-base with refs/remotes/origin/trunk", out)
        self.assertNotIn("test_mid.py", out)

    def test_without_base_a_plain_origin_head_still_counts(self):
        # origin/HEAD written as a plain ref to a commit, not as a symbolic ref.
        self.commit_on_top("scripts/tools/test_mid.py", FAILING, "mid")
        self.git("update-ref", "refs/remotes/origin/HEAD", "mid")
        self.git("update-ref", "refs/remotes/origin/main", "base")
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast(base=None)
        self.assertEqual(0, code, out)
        self.assertIn("merge-base with refs/remotes/origin/HEAD", out)
        self.assertNotIn("test_mid.py", out)

    def test_without_base_a_dangling_origin_head_falls_back_to_origin_main(self):
        # origin/HEAD is set at clone time; it can name a branch that was deleted since (here: master).
        self.git("update-ref", "refs/remotes/origin/main", "base")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/master")
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast(base=None)
        self.assertEqual(0, code, out)
        self.assertIn("merge-base with refs/remotes/origin/main", out)
        self.assertIn("PASS     scripts/tools/test_new.py", out)

    def test_without_base_a_legacy_clone_diffs_against_origin_master(self):
        self.git("update-ref", "refs/remotes/origin/master", "base")
        self.write("scripts/tools/test_new.py", FAILING)
        code, out = self.check_fast(base=None)
        self.assertEqual(1, code, out)
        self.assertIn("merge-base with refs/remotes/origin/master", out)
        self.assertIn("FAIL     scripts/tools/test_new.py", out)

    def test_without_base_or_origin_refs_it_stops_and_says_how_to_fix_it(self):
        self.write("scripts/tools/test_new.py", PASSING)
        code, out = self.check_fast(base=None)
        self.assertEqual(2, code, out)
        self.assertIn("no default base", out)
        self.assertIn("--base <ref>", out)
        self.assertNotIn("test_new.py", out)

    def test_an_explicit_base_wins_over_origin_head(self):
        self.commit_on_top("scripts/tools/test_mid.py", PASSING, "mid")
        self.git("update-ref", "refs/remotes/origin/main", "mid")
        self.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
        code, out = self.check_fast("--base", "base")
        self.assertEqual(0, code, out)
        self.assertIn("merge-base with base", out)
        self.assertIn("PASS     scripts/tools/test_mid.py", out)

    def test_an_empty_base_is_refused_not_read_as_no_base(self):
        self.git("update-ref", "refs/remotes/origin/main", "base")
        self.write("scripts/tools/test_new.py", PASSING)
        for args in (("--base", ""), ("--base=",)):
            with self.subTest(args=args):
                code, out = self.check_fast(*args)
                self.assertEqual(2, code, out)
                self.assertIn("--base needs a git ref", out)
                self.assertNotIn("test_new.py", out)

    def test_a_changed_test_is_run_and_its_failure_fails(self):
        self.write("scripts/tools/test_new.py", FAILING)
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/tools/test_new.py", out)
        self.assertNotIn("scripts/tools/test_other.py", out)

    def test_a_changed_script_runs_its_test_from_its_directory(self):
        # sim-boot.sh is tested by test_sim_boot.py: a dash in the name is an underscore in the test's.
        # Run from scripts/tools, as CI does: a relative fixture path resolves only there.
        self.write("scripts/tools/fixture.txt", "x")
        self.write("scripts/tools/test_tool_name.py", PASSING.replace("pass", "open('fixture.txt').close()"))
        self.git("add", "scripts/tools/test_tool_name.py", "scripts/tools/fixture.txt")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "test")
        self.git("tag", "-f", "base")
        self.write("scripts/tools/tool-name.sh", "echo changed\n")
        code, out = self.check_fast()
        self.assertEqual(0, code, out)
        self.assertIn("PASS     scripts/tools/test_tool_name.py", out)
        self.assertNotIn("scripts/tools/test_other.py", out)

    def test_a_shell_script_named_with_its_extension_runs_its_test(self):
        self.write("scripts/tools/db.sh", "echo changed\n")
        self.write("scripts/tools/test_db_sh.py", FAILING)
        self.git("add", "scripts/tools/test_db_sh.py")
        self.git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "test")
        self.git("tag", "-f", "base")
        self.write("scripts/tools/db.sh", "echo changed again\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/tools/test_db_sh.py", out)

    def test_a_deleted_test_is_not_run(self):
        (self.repo / "scripts/tools/test_gone.py").unlink()
        (self.repo / "scripts/tools/gone.py").unlink()
        code, out = self.check_fast()
        self.assertEqual(0, code, out)
        self.assertNotIn("test_gone.py", out)

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
        expected = sorted(p for p, suite in found.items()
                          if suite and p not in dedicated and not p.startswith("scripts/app/test_android_"))
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

    def test_a_hook_change_runs_the_privacy_suite(self):
        # test_privacy_check.py copies scripts/hooks into its temp repositories and runs the hooks there; a hook has no
        # .py or .sh name for the name mapping to match.
        self.break_suite("test_privacy_check.py")
        self.write("scripts/hooks/pre-push", "#!/bin/sh\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/test/test_privacy_check.py", out)

    def test_a_hook_or_adapter_generator_change_runs_the_adapter_hook_suite(self):
        # test_check_deno_adapter_sh.py runs pre-push and scripts/gen/deno_adapter.sh in its temp repositories (#6001).
        self.break_suite("test_check_deno_adapter_sh.py")
        for changed in ("scripts/hooks/pre-push", "scripts/gen/deno_adapter.sh"):
            with self.subTest(changed=changed):
                self.git("checkout", "-q", "--", ".")
                self.write(changed, "#!/bin/sh\n# changed\n")
                code, out = self.check_fast()
                self.assertEqual(1, code, out)
                self.assertIn("FAIL     scripts/test/test_check_deno_adapter_sh.py", out)

    def test_a_privacy_check_change_also_runs_the_suites_that_load_it(self):
        # scripts/release/check_bundle_privacy.py imports privacy_check; test_merge_pr_sh.py copies it into its temp
        # repositories, where merge-pr.sh runs it.
        self.commit_on_top("scripts/test/test_privacy_check.py", PASSING, "base")
        self.commit_on_top("scripts/release/test_check_bundle_privacy.py", FAILING, "base")
        self.commit_on_top("scripts/test/test_merge_pr_sh.py", FAILING, "base")
        self.write("scripts/privacy_check.py", "print('clean')\n# changed\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("PASS     scripts/test/test_privacy_check.py", out)
        self.assertIn("FAIL     scripts/release/test_check_bundle_privacy.py", out)
        self.assertIn("FAIL     scripts/test/test_merge_pr_sh.py", out)

    def test_a_ratchet_change_also_runs_the_suites_that_load_it(self):
        # test_changed_ratchets.py imports the two ratchets; test_merge_pr_sh.py copies them and changed_ratchets.py
        # into its temp repositories, where merge-pr.sh runs them (#7977).
        scripts = ("changed_ratchets.py", "sleep_ratchet.py", "web_font_ratchet.py")
        for name in scripts:
            self.commit_on_top("scripts/ci/" + name, "print('clean')\n", "base")
        self.commit_on_top("scripts/ci/test_changed_ratchets.py", FAILING, "base")
        self.break_suite("test_merge_pr_sh.py")
        for name in scripts:
            with self.subTest(name):
                self.git("checkout", "-q", "--", ".")
                self.write("scripts/ci/" + name, "print('clean')\n# changed\n")
                code, out = self.check_fast()
                self.assertEqual(1, code, out)
                self.assertIn("FAIL     scripts/ci/test_changed_ratchets.py", out)
                self.assertIn("FAIL     scripts/test/test_merge_pr_sh.py", out)

    def test_a_go_trimpath_change_also_runs_the_suites_of_its_callers(self):
        # server.sh and check-fast.sh compose GOFLAGS with the helper (#10614); test_ci.py and test_check_fast_inputs.py
        # run them.
        helper = "scripts/test/go_trimpath.sh"
        self.commit_on_top(helper, "#!/bin/sh\nprintf '%s\\n' -trimpath\n", "base")
        for suite in ("scripts/test/test_go_trimpath.py", "scripts/ci/test_ci.py",
                      "scripts/test/test_check_fast_inputs.py"):
            self.commit_on_top(suite, FAILING, "base")
        self.write(helper, "#!/bin/sh\n# changed\nprintf '%s\\n' -trimpath\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/test/test_go_trimpath.py", out)
        self.assertIn("FAIL     scripts/ci/test_ci.py", out)
        self.assertIn("FAIL     scripts/test/test_check_fast_inputs.py", out)

    def test_check_fast_and_the_slot_helper_run_every_file_of_its_tests(self):
        # check-fast.sh's tests are three files (#11017); the other two import test_check_fast.py's fixtures, and
        # test_check_fast_inputs.py runs check-fast.sh with heavy_slot.py, so a change to any of them runs them.
        everything = ("test_check_fast.py", "test_check_fast_guards.py", "test_check_fast_inputs.py")
        for suite in (*everything, "test_heavy_slot.py"):
            self.commit_on_top(f"scripts/test/{suite}", FAILING, "base")
        self.commit_on_top("scripts/heavy_slot.py", "", "base")
        self.commit_on_top("scripts/check-fast.sh", "#!/bin/bash\n", "base")
        cases = {
            "scripts/test/check-fast.sh": everything,
            "scripts/check-fast.sh": everything,
            "scripts/test/test_check_fast.py": everything,
            "scripts/heavy_slot.py": ("test_heavy_slot.py", "test_check_fast_inputs.py"),
        }
        for path, suites in cases.items():
            with self.subTest(path=path):
                with (self.repo / path).open("a") as f:
                    f.write("# changed\n")
                code, out = self.check_fast()
                self.git("checkout", "-q", "--", path)
                self.assertEqual(1, code, out)
                for suite in suites:
                    self.assertIn(f"FAIL     scripts/test/{suite}", out)
                self.assertNotIn("scripts/tools/test_other.py", out)

    def test_the_debug_switch_list_and_its_checker_run_the_suites_that_read_them(self):
        # debug_switches.txt matches no script name, yet a union merge can leave it out of order (#10616) and a new
        # name can equal one that ships; the checker and release.sh, which runs it, are tested by the same suites.
        self.commit_on_top("scripts/release/test_debug_switches.py", FAILING, "base")
        self.commit_on_top("scripts/release/test_release_debug_guard.py", FAILING, "base")
        self.break_suite("test_apple_release_switch_names.py")
        for path in ("scripts/release/debug_switches.txt", "scripts/release/debug_switches.py",
                     "scripts/release/release.sh"):
            with self.subTest(path=path):
                self.write(path, "changed\n")
                code, out = self.check_fast()
                (self.repo / path).unlink()
                self.assertEqual(1, code, out)
                self.assertIn("FAIL     scripts/release/test_debug_switches.py", out)
                self.assertIn("FAIL     scripts/release/test_release_debug_guard.py", out)
                self.assertIn("FAIL     scripts/test/test_apple_release_switch_names.py", out)
                self.assertNotIn("scripts/tools/test_other.py", out)

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


# A privacy-scan stub that never finishes on its own: it records its own pid, its parent's (the GNU timeout that runs
# it) and a sleeping child's, then waits.
HANGING_CHECK = """import os, subprocess, sys, time
child = subprocess.Popen(["sleep", "60"])
with open(sys.argv[0] + ".pids.tmp", "w") as f:
    f.write(f"{os.getpid()} {os.getppid()} {child.pid}")
os.replace(sys.argv[0] + ".pids.tmp", sys.argv[0] + ".pids")
time.sleep(60)
"""


@unittest.skipUnless(shutil.which("ps"), "the signal tests list processes with ps")
@unittest.skipUnless(shutil.which("timeout") or shutil.which("gtimeout"), "check-fast.sh needs GNU timeout")
class CheckFastSignalTests(unittest.TestCase):
    """Ctrl-C, SIGTERM and SIGHUP to check-fast.sh stop the checks it started instead of leaving them orphaned."""

    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="check-fast-signal-"))
        self.addCleanup(shutil.rmtree, self.repo, True)
        (self.repo / "scripts/test").mkdir(parents=True)
        shutil.copy(ROOT / "scripts/test/check-fast.sh", self.repo / "scripts/test/check-fast.sh")
        (self.repo / "scripts/privacy_check.py").write_text(HANGING_CHECK)
        (self.repo / "scripts/test/test_extension_schema.py").write_text("")
        (self.repo / "scripts/test/pr_needs_issue_test.mjs").write_text("")
        for args in (["init", "-q"], ["add", "-A"],
                     ["-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base"],
                     ["tag", "base"]):
            subprocess.run(["git", "-C", str(self.repo), *args], check=True, capture_output=True)
        with (self.repo / "scripts/privacy_check.py").open("a") as f:
            f.write("# changed\n")

    def survivors(self, pids):
        """Live processes from this run: any whose command line names the throwaway repo, plus the recorded pids."""
        ps = subprocess.run(["ps", "-A", "-o", "pid=", "-o", "stat=", "-o", "args="], capture_output=True, text=True,
                            check=True).stdout
        left = []
        for line in ps.splitlines():
            pid, stat, args = (line.split(None, 2) + ["", ""])[:3]
            if stat.startswith("Z"):
                continue
            if str(self.repo) in args or int(pid) in pids:
                left.append(line.strip())
        return left

    def interrupt(self, sig, code, group=False):
        pid_file = self.repo / "scripts/privacy_check.py.pids"
        # An absolute script path puts the repo into the job subshells' command lines; TMPDIR puts it into tee's.
        # start_new_session keeps a stray signal off the test runner.
        proc = subprocess.Popen(["bash", str(self.repo / "scripts/test/check-fast.sh"), "--base", "base"],
                                cwd=self.repo, env=dict(os.environ, TMPDIR=str(self.repo)), stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, text=True, start_new_session=True)
        pids = set()
        try:
            deadline = time.monotonic() + 30
            while not pid_file.exists():
                self.assertIsNone(proc.poll(), "check-fast.sh exited before the stub check started")
                self.assertLess(time.monotonic(), deadline, "the stub check never started")
                time.sleep(0.1)
            pids = {int(p) for p in pid_file.read_text().split()}
            if group:
                # A terminal's Ctrl-C or hangup, kill -- -PGID and runners that time out signal the whole process
                # group: the job subshells and tee get the signal too, not just the script.
                os.killpg(proc.pid, sig)
            else:
                proc.send_signal(sig)
            out, _ = proc.communicate(timeout=15)
            self.assertEqual(code, proc.returncode, out)
            self.assertIn("check-fast: interrupted", out)
            self.assertNotIn("Killed", out)  # bash's report of each job subshell it KILLed stays off the output
            left = self.survivors(pids)
            deadline = time.monotonic() + 5
            while left and time.monotonic() < deadline:
                time.sleep(0.2)
                left = self.survivors(pids)
            self.assertEqual([], left, out)
            # The interrupted run keeps its log directory (made under TMPDIR, the throwaway repo).
            self.assertTrue([p for p in self.repo.glob("check-fast.*") if p.is_dir()], out)
        finally:
            if proc.poll() is None:
                os.killpg(proc.pid, signal.SIGKILL)
                proc.wait()
            for pid in pids:
                try:
                    os.kill(pid, signal.SIGKILL)
                except (ProcessLookupError, PermissionError):
                    pass

    def test_sigterm_stops_the_running_checks(self):
        self.interrupt(signal.SIGTERM, 143)

    def test_sigint_stops_the_running_checks(self):
        self.interrupt(signal.SIGINT, 130)

    def test_sighup_stops_the_running_checks(self):
        self.interrupt(signal.SIGHUP, 129)

    def test_sigint_to_the_process_group_stops_the_running_checks(self):
        self.interrupt(signal.SIGINT, 130, group=True)

    def test_sigterm_to_the_process_group_stops_the_running_checks(self):
        self.interrupt(signal.SIGTERM, 143, group=True)

    def test_sighup_to_the_process_group_stops_the_running_checks(self):
        # Closing a terminal or a tmux pane sends SIGHUP to the whole foreground process group.
        self.interrupt(signal.SIGHUP, 129, group=True)


if __name__ == "__main__":
    unittest.main()
