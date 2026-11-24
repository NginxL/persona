"""Readiness successful regression/model-picker lane cases, split by fixture cost (#14321).

The fixture and assertions are unchanged from test_apple_readiness.py. Explicit
load_tests prevents the fixture's other tests running a second time here.
"""
import unittest

import test_apple_readiness as readiness

REGULAR_SUITE = readiness.REGULAR_SUITE
NEW = readiness.NEW


class AppleReadinessLaneTests(readiness.AppleReadinessTests):
    def test_regression_lane_executes_only_both_suites_after_the_shared_probe(self):
        result, calls = self.run_preparation(regression_only=True,
                                             FULL_RESULT_SUITE="MainPagesStackTests,PhoneSystemToolsTests")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual([call[-1] for call in calls], ["build-for-testing"] + ["test-without-building"] * 2)
        selectors = lambda args: [a for a in args if a.startswith(("-only-testing:", "-skip-testing:"))]
        expected = ["-only-testing:PersonaTests/MainPagesStackTests",
                    "-only-testing:PersonaTests/PhoneSystemToolsTests"]
        self.assertEqual(selectors(calls[0]), expected)
        self.assertEqual(selectors(calls[1]), ["-only-testing:PersonaTests/TypeScaleTests"])
        self.assertEqual(selectors(calls[2]), expected)
        for call in calls:
            self.assertEqual(call[call.index("-scheme") + 1], "PersonaOfflineUnitTests")
        self.assertIn("ios-unit.xcresult: 2 test cases, zero failed/skipped", result.stdout)
        self.assertEqual((self.root / "calls").read_text().count("simctl create"), 1)
        self.assertFalse((self.root / "regular-probe-started").exists())
        self.assertFalse((self.root / "regular-suite-started").exists())
        self.assertTrue((self.output / "diagnostics/live-readiness/observer.status.txt").is_file())
        self.assertFalse((self.output / "regular-readiness-context.json").exists())
        self.assertEqual((self.root / "budgets").read_text().splitlines(), [
            "Generate iPhone-only offline unit-test project|120", "Generate offline unit-test Xcode project|120",
            "Build iOS offline unit tests|1800", "No CallKit in the simulator build|300",
            "Create stock iOS 26.0 test simulator|100",
            "Verify hosted iOS test readiness|900", "Grant Reminders access to the owned test app|300",
            "iOS offline unit and design/token tests|1800", "Inspect test results (ios-unit)|120",
        ])

    def test_model_picker_lane_executes_only_both_suites_after_the_shared_probe(self):
        result, calls = self.run_preparation(
            regression_only=True, lane="ios-model-picker",
            FULL_RESULT_SUITE="AssistantModelChoiceTests,AssistantModelPanelModelTests")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual([call[-1] for call in calls], ["build-for-testing"] + ["test-without-building"] * 2)
        selectors = lambda args: [a for a in args if a.startswith(("-only-testing:", "-skip-testing:"))]
        expected = ["-only-testing:PersonaTests/AssistantModelChoiceTests",
                    "-only-testing:PersonaTests/AssistantModelPanelModelTests"]
        self.assertEqual(selectors(calls[0]), expected)
        self.assertEqual(selectors(calls[1]), ["-only-testing:PersonaTests/TypeScaleTests"])
        self.assertEqual(selectors(calls[2]), expected)
        for call in calls:
            self.assertEqual(call[call.index("-scheme") + 1], "PersonaOfflineUnitTests")
        self.assertIn("ios-unit.xcresult: 2 test cases, zero failed/skipped", result.stdout)
        self.assertEqual((self.root / "calls").read_text().count("simctl create"), 1)
        self.assertFalse((self.root / "regular-probe-started").exists())
        self.assertFalse((self.root / "regular-suite-started").exists())
        self.assertTrue((self.output / "diagnostics/live-readiness/observer.status.txt").is_file())
        self.assertFalse((self.output / "regular-readiness-context.json").exists())
        self.assertEqual((self.root / "budgets").read_text().splitlines(), [
            "Generate iPhone-only offline unit-test project|120", "Generate offline unit-test Xcode project|120",
            "Build iOS offline unit tests|1800", "No CallKit in the simulator build|300",
            "Create stock iOS 26.0 test simulator|100",
            "Verify hosted iOS test readiness|900", "Grant Reminders access to the owned test app|300",
            "iOS offline unit and design/token tests|1800", "Inspect test results (ios-unit)|120",
        ])


def load_tests(loader, tests, pattern):
    suite = unittest.TestSuite()
    for name in sorted(n for n in vars(AppleReadinessLaneTests) if n.startswith("test_")):
        suite.addTest(AppleReadinessLaneTests(name))
    return suite


if __name__ == "__main__":
    unittest.main()
