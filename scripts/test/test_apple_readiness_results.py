"""Readiness result and permission-handoff cases, split by fixture cost (#14321).

The fixture and assertions are unchanged from test_apple_readiness_deadlines.py.
`load_tests` keeps the inherited AppleReadinessTests cases out of this module, as before.
"""
import unittest

import test_apple_readiness as readiness

AppleReadinessTests = readiness.AppleReadinessTests


class AppleReadinessResultTests(readiness.AppleReadinessTests):
    def test_regression_lane_rejects_a_missing_empty_failed_or_skipped_suite(self):
        cases = (
            {"FULL_RESULT_SUITE": "MainPagesStackTests"},
            {"FULL_RESULT_EMPTY": "1"},
            {"FULL_RESULT": "Failed"},
            {"FULL_RESULT": "Skipped"},
            {"FULL_RESULT_SUITE": "MainPagesStackTests,UnrelatedTests"},
            {"FULL_STATUS": "7"},
        )
        for options in cases:
            with self.subTest(options=options):
                case = AppleReadinessTests()
                case.setUp()
                options.setdefault("FULL_RESULT_SUITE", "MainPagesStackTests,PhoneSystemToolsTests")
                try:
                    result, calls = case.run_preparation(regression_only=True, **options)
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(len(calls), 3)
                    self.assertFalse((case.root / "regular-probe-started").exists())
                    self.assertFalse((case.root / "regular-suite-started").exists())
                finally:
                    case.doCleanups()


    def test_probe_results_must_be_nonempty_passed_and_from_the_selected_suite(self):
        for options in ({"RESULT_STATUS": "Failed"}, {"RESULT_STATUS": "Skipped"},
                        {"RESULT_STATUS": "empty"}, {"RESULT_SUITE": "UnrelatedTests"}):
            with self.subTest(options=options):
                # Each attempt owns a separate device fixture; no retry of a failed probe.
                case = AppleReadinessTests()
                case.setUp()
                try:
                    result, calls = case.run_preparation(**options)
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(len(calls), 2)
                    self.assertFalse((case.root / "full-suite-started").exists())
                    self.assertFalse((case.root / "privacy-granted").exists())
                finally:
                    case.doCleanups()


    def test_permission_handoff_rejects_unavailable_or_transitional_device(self):
        for options in ({"POST_PROBE_STATE": "Booting"}, {"POST_PROBE_STATE": "Shutting Down"},
                        {"POST_PROBE_AVAILABLE": "0"}):
            with self.subTest(options=options):
                case = AppleReadinessTests()
                case.setUp()
                try:
                    result, calls = case.run_preparation(**options)
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(len(calls), 2)
                    self.assertNotIn("simctl boot", (case.root / "calls").read_text())
                    self.assertFalse((case.root / "privacy-attempted").exists())
                    self.assertFalse((case.root / "full-suite-started").exists())
                finally:
                    case.doCleanups()



def load_tests(loader, tests, pattern):
    """Only this file's tests: the fixture class it extends runs its own in test_apple_readiness.py."""
    suite = unittest.TestSuite()
    for name in sorted(n for n in vars(AppleReadinessResultTests) if n.startswith("test_")):
        suite.addTest(AppleReadinessResultTests(name))
    return suite


if __name__ == "__main__":
    unittest.main()
