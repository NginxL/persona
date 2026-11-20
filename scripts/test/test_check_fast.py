"""check-fast.sh runs the scripts/ unittests a change needs (#3968), and the scripts/test suites that guard docs,
markdown, apple/ and deploy/docker/; Ctrl-C, SIGTERM and SIGHUP stop the checks it started; the per-path table it reads
its plan from (scripts/ci/check_fast_map.py, #11535) answers one line per path and names only checks it knows. The
server and web checks' inputs are tested in test_check_fast_inputs.py, split off so each file stays well under the
per-suite limit of scripts/ci/python-tests.py on a loaded machine (#11017).

Each test builds a throwaway repository with check-fast.sh, its table and stubs for the repository-wide checks it
always picks for a scripts/ change (the privacy scan, the extension schema test, the PR workflow test), commits a base,
makes a change on top and runs check-fast.sh --base against the base. The default-base tests run it without --base,
against refs/remotes/origin/* refs they write themselves.
"""
import importlib.util
import os
from pathlib import Path
import re
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
# check-fast.sh and the per-path table it reads its plan from (#11535): what a throwaway repository needs to plan.
CHECK_FAST_FILES = ("scripts/test/check-fast.sh", "scripts/ci/check_fast_map.py")


def copy_check_fast(repo):
    """Copy check-fast.sh and its table into the throwaway repository at repo."""
    for name in CHECK_FAST_FILES:
        (Path(repo) / name).parent.mkdir(parents=True, exist_ok=True)
        shutil.copy(ROOT / name, Path(repo) / name)


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
        copy_check_fast(self.repo)
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

    def test_a_changed_module_runs_the_suites_that_import_it(self):
        # From the suite's own directory, or from one it names (it puts scripts/<dir> on sys.path to import from it).
        # A suite that imports a module of that name without naming its directory imports another one (#11026).
        self.commit_on_top("scripts/tools/helper.py", "VALUE = 1\n", "base")
        self.commit_on_top("scripts/tools/test_uses_helper.py", "import helper\n" + FAILING, "base")
        self.commit_on_top("scripts/ci/test_named.py", 'import sys\nsys.path.insert(0, "scripts/tools")\n'
                           "from helper import VALUE\n" + FAILING, "base")
        self.commit_on_top("scripts/release/test_unrelated.py", "import os, helper\n" + FAILING, "base")
        self.write("scripts/tools/helper.py", "VALUE = 2\n")
        code, out = self.check_fast()
        self.assertEqual(1, code, out)
        self.assertIn("FAIL     scripts/tools/test_uses_helper.py", out)
        self.assertIn("FAIL     scripts/ci/test_named.py", out)
        self.assertNotIn("test_unrelated.py", out)
        self.assertNotIn("scripts/tools/test_other.py", out)

    def test_a_suite_a_dedicated_check_runs_schedules_that_check(self):
        # test_external_service_refs.py imports compatibility_identifiers.py and runs as its own check; four of the
        # scripts/app/test_android_* suites run in the android check, the others as suites.
        self.commit_on_top("scripts/test/compatibility_identifiers.py", "NAMES = []\n", "base")
        self.commit_on_top("scripts/test/test_external_service_refs.py",
                           "from compatibility_identifiers import NAMES\n" + PASSING, "base")
        self.commit_on_top("scripts/app/test_android_new_tool.py", PASSING, "base")
        self.write("scripts/test/compatibility_identifiers.py", "NAMES = ['x']\n")
        self.write("scripts/app/android_new_tool.py", "# changed\n")
        code, out = self.check_fast("--dry-run", "--base", "base")
        self.assertEqual(0, code, out)
        self.assertIn("queued: external service refs", out)
        self.assertNotIn("queued: scripts/test/test_external_service_refs.py", out)
        self.assertIn("queued: scripts/app/test_android_new_tool.py", out)
        self.assertIn("queued: android i18n", out)

    def test_a_ci_lane_or_watcher_file_runs_the_slot_callers_test(self):
        # test_heavy_slot.py reads these: a CI lane or a watcher step that names a script taking a slot must set
        # PERSONA_HEAVY_SLOTS=0. A new lane script once broke it with nothing local running it (#11026).
        self.commit_on_top("scripts/test/test_heavy_slot.py", PASSING, "base")
        readers = (".github/workflows/scripts.yml", "scripts/ci/lane.sh", "scripts/ci/lane.py",
                   "scripts/operations/watch.py", "scripts/operations/pr-watch.example.json",
                   "scripts/operations/persona-pr-watch.service", ".claude/skills/pr-review-merge-loop/scripts/run.py",
                   ".claude/skills/main-verify-fix-loop/SKILL.md")
        others = ("scripts/tools/other.py", ".github/CODEOWNERS", "scripts/operations/notes.txt", ".claude/settings.json",
                  "scripts/ci/test_lane.py")  # test_heavy_slot.py skips test_* files
        self.write("listed.txt", "".join(p + "\n" for p in readers + others))
        code, out = self.check_fast("--dry-run", "--changed-from", "listed.txt", "--each")
        self.assertEqual(0, code, out)
        plans, current = {}, None
        for line in out.splitlines():
            if line.startswith("== "):
                current = plans.setdefault(line[3:], [])
            elif current is not None:
                current.append(line.strip())
        self.assertEqual(sorted(readers + others), sorted(plans), out)
        for path, lines in plans.items():
            with self.subTest(path=path):
                self.assertEqual(path in readers, "queued: scripts/test/test_heavy_slot.py" in lines, out)

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
        # test_check_fast_inputs.py runs check-fast.sh with heavy_slot.py, so a change to any of them, or to the table
        # check-fast.sh plans from, runs them.
        # android.sh's own suite does not run for check-fast.sh.
        everything = ("test_check_fast.py", "test_check_fast_guards.py", "test_check_fast_inputs.py")
        for suite in (*everything, "test_heavy_slot.py", "test_android_sh.py"):
            self.commit_on_top(f"scripts/test/{suite}", FAILING, "base")
        self.commit_on_top("scripts/heavy_slot.py", "", "base")
        self.commit_on_top("scripts/check-fast.sh", "#!/bin/bash\n", "base")
        cases = {
            "scripts/test/check-fast.sh": everything,
            "scripts/check-fast.sh": everything,
            "scripts/test/test_check_fast.py": everything,
            "scripts/ci/check_fast_map.py": everything,  # the table check-fast plans from (#11535)
            # test_android_sh.py runs android.sh with it (#11250).
            "scripts/heavy_slot.py": ("test_heavy_slot.py", "test_check_fast_inputs.py", "test_android_sh.py"),
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
                if "test_android_sh.py" not in suites:
                    self.assertNotIn("test_android_sh.py", out)
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


class CheckFastTableTests(unittest.TestCase):
    """The per-path table check-fast plans from (scripts/ci/check_fast_map.py, #11535): its command line, the order its
    rules are tried in, and that check-fast.sh and the table name the same checks."""

    TABLE = ROOT / "scripts/ci/check_fast_map.py"

    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("check_fast_map", cls.TABLE)
        cls.table = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.table)

    def run_table(self, data, *args):
        return subprocess.run([sys.executable, str(self.TABLE), *args], input=data, capture_output=True, timeout=60)

    def test_it_prints_the_checks_of_each_path_on_its_own_line(self):
        paths = ["web/src/App.tsx", "notes/todo.txt", "server/cmd/personad/main.go", "integrations/pi/a b.js",
                 "web/src/App.tsx"]
        run = self.run_table("".join(path + "\n" for path in paths).encode())
        self.assertEqual(0, run.returncode, run.stderr)
        self.assertEqual(["web\tsuite:scripts/test/test_doc_sections.py", "unmapped",
                          "go-package:cmd/personad\tgo-guards\tsuite:scripts/test/test_doc_sections.py",
                          "integration:pi\tsuite:scripts/test/test_doc_sections.py",
                          "web\tsuite:scripts/test/test_doc_sections.py", ""], run.stdout.decode().split("\n"))
        self.assertEqual(b"", self.run_table(b"").stdout)

    def test_a_path_that_is_not_utf8_keeps_its_bytes_and_its_line(self):
        run = self.run_table(b"integrations/\xff/x.js\nnotes/\xfe\n")
        self.assertEqual(0, run.returncode, run.stderr)
        self.assertEqual(b"integration:\xff\tsuite:scripts/test/test_doc_sections.py\nunmapped\n", run.stdout)

    def test_help_prints_the_usage_and_anything_else_is_refused(self):
        run = self.run_table(b"", "--help")
        self.assertEqual(0, run.returncode)
        self.assertIn(b"check-fast's per-path table", run.stdout)
        self.assertEqual(2, self.run_table(b"", "--each").returncode)

    def test_the_first_rule_of_a_group_that_matches_wins(self):
        checks = self.table.checks
        # A dedicated script's own rule, not the scripts/* one after it.
        self.assertEqual(["external-refs", "suite:scripts/test/test_doc_sections.py"],
                         checks("scripts/test/test_external_service_refs.py"))
        # server/: a package group, a file directly in plugins/ (the guards run it), anything else a build.
        self.assertEqual(["go-package:internal/hub", "go-guards", "suite:scripts/test/test_doc_wire.py",
                          "suite:scripts/test/test_doc_sections.py"], checks("server/internal/hub/hub.go"))
        self.assertEqual(["go-guards", "suite:scripts/test/test_doc_sections.py"], checks("server/plugins/plugins.go"))
        self.assertEqual(["server-build", "go-guards"], checks("server/internal/README.md")[:2])
        # A markdown file in a wire-list package is a doc first: the four doc suites, not the wire list alone.
        self.assertIn("suite:scripts/test/test_doc_paths.py", checks("server/internal/proto/README.md"))
        # name:test_* matches the file name only: a test file is not a slot caller, a file in a test_ directory is.
        heavy = "suite:scripts/test/test_heavy_slot.py"
        self.assertNotIn(heavy, checks("scripts/operations/test_pr_watch.py"))
        self.assertIn(heavy, checks("scripts/operations/test_tools/watch.py"))
        self.assertIn(heavy, checks("scripts/operations/pr_watch.py"))

    def test_check_fast_and_the_table_name_the_same_checks(self):
        text = (ROOT / "scripts/test/check-fast.sh").read_text()
        block = text[text.index('case "$_c" in'):]
        block = block[:block.index("esac")]
        handled = set(re.findall(r"^\s+([a-z-]+)(?::\*)?\)", block, re.M))
        self.assertEqual(set(self.table.CHECKS) | {"suite"}, handled)
        named = {check for group in self.table.TABLE for _, listed in group for check in listed}
        self.assertEqual(set(self.table.CHECKS), {check for check in named if "/" not in check})

    def test_every_suite_the_table_names_is_a_unittest_suite_ci_runs(self):
        is_suite = ci_is_suite()
        suites = self.table.suites()
        self.assertGreater(len(suites), 20)
        for suite in suites:
            with self.subTest(suite=suite):
                self.assertTrue(suite.startswith("scripts/") and Path(suite).name.startswith("test_"), suite)
                self.assertTrue((ROOT / suite).is_file(), suite)
                self.assertTrue(is_suite(ROOT / suite), suite)


# A privacy-scan stub that never finishes on its own: it records its own pid, its parent's (the GNU timeout that runs
# it) and a sleeping child's, then waits.
HANGING_CHECK = """import os, subprocess, sys, time
child = subprocess.Popen(["sleep", "60"])
with open(sys.argv[0] + ".pids.tmp", "w") as f:
    f.write(f"{os.getpid()} {os.getppid()} {child.pid}")
os.replace(sys.argv[0] + ".pids.tmp", sys.argv[0] + ".pids")
time.sleep(60)
"""


# The signals the tests send. A child inherits an ignored signal, and a shell can never trap one that was ignored when
# it started, so check-fast.sh starts with them at their defaults whatever this runner got: check-fast runs each suite
# as a background job, which bash starts with SIGINT ignored, and nohup or a detached shell ignores SIGHUP
# (docs/lessons/testing.md, "Signal tests").
SIGNALS = (signal.SIGINT, signal.SIGHUP, signal.SIGTERM)


def default_signals():
    """preexec_fn: SIGINT, SIGHUP and SIGTERM at their defaults in the child, as a terminal starts check-fast.sh."""
    for sig in SIGNALS:
        signal.signal(sig, signal.SIG_DFL)


@unittest.skipUnless(shutil.which("ps"), "the signal tests list processes with ps")
@unittest.skipUnless(shutil.which("timeout") or shutil.which("gtimeout"), "check-fast.sh needs GNU timeout")
class CheckFastSignalTests(unittest.TestCase):
    """Ctrl-C, SIGTERM and SIGHUP to check-fast.sh stop the checks it started instead of leaving them orphaned."""

    def setUp(self):
        self.repo = Path(tempfile.mkdtemp(prefix="check-fast-signal-"))
        self.addCleanup(shutil.rmtree, self.repo, True)
        (self.repo / "scripts/test").mkdir(parents=True)
        copy_check_fast(self.repo)
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
        pid_file.unlink(missing_ok=True)  # a test that interrupts twice waits for the second run's check
        # An absolute script path puts the repo into the job subshells' command lines; TMPDIR puts it into tee's.
        # start_new_session keeps a stray signal off the test runner; default_signals undoes what the runner ignores.
        proc = subprocess.Popen(["bash", str(self.repo / "scripts/test/check-fast.sh"), "--base", "base"],
                                cwd=self.repo, env=dict(os.environ, TMPDIR=str(self.repo)), stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, text=True, start_new_session=True,
                                preexec_fn=default_signals)
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

    def test_a_runner_that_ignores_the_signals_does_not_pass_that_on(self):
        # As check-fast runs this suite: a background job with SIGINT ignored, here SIGHUP and SIGTERM too (#11250).
        saved = {sig: signal.signal(sig, signal.SIG_IGN) for sig in SIGNALS}
        try:
            for sig, code in ((signal.SIGINT, 130), (signal.SIGHUP, 129)):
                with self.subTest(signal=sig.name):
                    self.interrupt(sig, code)
        finally:
            for sig, handler in saved.items():
                signal.signal(sig, handler)


if __name__ == "__main__":
    unittest.main()
