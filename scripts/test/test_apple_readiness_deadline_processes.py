"""Readiness probe/result deadline phases, split from the permission deadline (#14321).

Every original phase and assertion remains; this separate suite prevents their
fresh fixture costs from hiding the permission-deadline verdict in one 120-second CI slot.
"""
import unittest

import test_apple_readiness as readiness

AppleReadinessTests = readiness.AppleReadinessTests


class AppleReadinessProcessDeadlineTests(readiness.AppleReadinessTests):
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



def load_tests(loader, tests, pattern):
    suite = unittest.TestSuite()
    for name in sorted(n for n in vars(AppleReadinessProcessDeadlineTests) if n.startswith("test_")):
        suite.addTest(AppleReadinessProcessDeadlineTests(name))
    return suite


if __name__ == "__main__":
    unittest.main()
