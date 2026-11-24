"""Focused check-fast tests, split by behavior (#14321).

The shared fixture and these test methods are unchanged from test_check_fast.py.
Separate files keep each CI suite inside its existing 120-second bound.
"""
import unittest

from test_check_fast import FAILING, FAKE_PYTHON, PASSING, ROOT, ScriptsRepoTestCase, ci_is_suite

class CheckFastCallerTests(ScriptsRepoTestCase):
    """Callers, importers and related suite selection."""

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
        # check-fast.sh's tests are six files (#11017, #14321); the other five import test_check_fast.py's fixtures, and
        # test_check_fast_inputs.py runs check-fast.sh with heavy_slot.py, so a change to any of them, or to the table
        # check-fast.sh plans from, runs them. So does a change to a helper they copy into their throwaway
        # repositories (CHECK_FAST_FILES), which no name or import leads to.
        # android.sh's own suite does not run for check-fast.sh.
        everything = ("test_check_fast.py", "test_check_fast_guards.py", "test_check_fast_inputs.py",
                    "test_check_fast_base.py", "test_check_fast_reports.py", "test_check_fast_callers.py")
        for suite in (*everything, "test_heavy_slot.py", "test_android_sh.py"):
            self.commit_on_top(f"scripts/test/{suite}", FAILING, "base")
        self.commit_on_top("scripts/heavy_slot.py", "", "base")
        self.commit_on_top("scripts/check-fast.sh", "#!/bin/bash\n", "base")
        cases = {
            "scripts/test/check-fast.sh": everything,
            "scripts/check-fast.sh": everything,
            "scripts/test/test_check_fast.py": everything,
            "scripts/ci/check_fast_map.py": everything,  # the table check-fast plans from (#11535)
            "scripts/ci/check_fast_compare.py": everything,  # a failure's rerun and the merge base's verdict (#12733)
            "scripts/test/go_test_partitioned.py": everything,  # every Go group runs through it (#12765)
            "scripts/test/go_partitions.py": everything,  # what it splits
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
