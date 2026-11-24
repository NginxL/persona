"""scripts/ci/apple.sh's hosted iOS readiness: the deadline and result-inspection cases of test_apple_readiness.py.

Split from that suite (#13293), then split by fixture cost (#14321) so each file stays well under the scripts lane's
120-second limit per suite (scripts/ci/python-tests.py). The fixture is test_apple_readiness.py's
AppleReadinessTests; only the two process-deadline tests defined here run from this file (load_tests).
"""
import unittest

import test_apple_readiness as readiness

AppleReadinessTests = readiness.AppleReadinessTests  # the cases below build fresh fixtures of it


class AppleReadinessDeadlineTests(readiness.AppleReadinessTests):
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
