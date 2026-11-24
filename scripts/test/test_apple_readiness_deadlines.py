"""scripts/ci/apple.sh's hosted iOS readiness: the deadline and result-inspection cases of test_apple_readiness.py.

Split from that suite (#13293) so each file stays well under the scripts lane's 120-second limit per suite
(scripts/ci/python-tests.py): the whole took 124 s on the CI machine. The fixture is test_apple_readiness.py's
AppleReadinessTests; only the tests defined here run from this file (load_tests).
"""
import unittest

import test_apple_readiness as readiness

AppleReadinessTests = readiness.AppleReadinessTests  # the cases below build fresh fixtures of it


class AppleReadinessDeadlineTests(readiness.AppleReadinessTests):
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

    def test_probe_and_result_inspection_share_one_deadline(self):
        for key, pid_name, product_hang in (("PROBE_HANG", "probe.pid", False),
                                            ("RESULT_HANG", "result.pid", False),
                                            ("PROBE_HANG", "probe.pid", True)):
            with self.subTest(phase=key, product_hang=product_hang):
                case = AppleReadinessTests()
                case.setUp()
                try:
                    result, _ = case.run_preparation(**{key: "1", "TEST_STEP_TIMEOUT": "1",
                                                        "PRODUCT_HANG": "1" if product_hang else "0"})
                    self.assertEqual(result.returncode, 124, result.stdout + result.stderr)
                    self.assertFalse((case.root / "full-suite-started").exists())
                    self.assertTrue((case.root / pid_name).exists())
                    # The deadline terminates the probe/result process, not just its shell.
                    self.assertEqual((case.root / pid_name.replace(".pid", ".stopped")).read_text(), "terminated")
                    observer = case.output / "diagnostics/live-readiness"
                    if product_hang:
                        self.assertIn("xcodebuild-finished", (observer / "observer.status.txt").read_text())
                        product = observer / "product-0-architecture.txt"
                        self.assertEqual(product.read_text(), "unavailable: command timed out\n")
                        self.assertEqual(product.with_suffix(".txt.status.txt").read_text(), "124\n")
                    else:
                        self.assertIn("xcodebuild-finished", (observer / "observer.status.txt").read_text())
                        case.assert_collected_products(observer)
                    self.assertTrue((observer / "host-final.json").is_file())
                    case.assert_recorded_processes_reaped()
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

    def test_start_is_inside_the_existing_permission_deadline(self):
        result, calls = self.run_preparation(BOOT_HANG="1")
        self.assertEqual(result.returncode, 124, result.stdout + result.stderr)
        self.assertEqual(len(calls), 2)
        self.assertEqual((self.root / "boot.stopped").read_text(), "terminated")
        self.assertFalse((self.root / "privacy-attempted").exists())
        self.assertFalse((self.root / "full-suite-started").exists())
        self.assertEqual((self.root / "budgets").read_text().splitlines()[-1],
                         "Grant Reminders access to the owned test app|300")


def load_tests(loader, tests, pattern):
    """Only this file's tests: the fixture class it extends runs its own in test_apple_readiness.py."""
    suite = unittest.TestSuite()
    for name in sorted(n for n in vars(AppleReadinessDeadlineTests) if n.startswith("test_")):
        suite.addTest(AppleReadinessDeadlineTests(name))
    return suite


if __name__ == "__main__":
    unittest.main()
