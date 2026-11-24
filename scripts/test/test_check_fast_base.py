"""Focused check-fast tests, split by behavior (#14321).

The shared fixture and these test methods are unchanged from test_check_fast.py.
Separate files keep each CI suite inside its existing 120-second bound.
"""
import unittest

from test_check_fast import FAILING, FAKE_PYTHON, PASSING, ROOT, ScriptsRepoTestCase, ci_is_suite

class CheckFastBaseTests(ScriptsRepoTestCase):
    """Default-base selection and the scripts a changed file names."""

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
