"""Readiness full-process execution cases, split from test_apple_readiness.py (#14321).

The fixture and assertions are unchanged. `load_tests` runs only these two cases
in this module; the fixture's other tests remain in their original modules.
"""
import json
import os
import unittest

import test_apple_readiness as readiness

REGULAR_UDID = readiness.REGULAR_UDID

REGULAR_SUITE = readiness.REGULAR_SUITE
NEW = readiness.NEW
PHONE = readiness.PHONE
WATCH = readiness.WATCH
ROOT = readiness.ROOT


class AppleReadinessExecutionTests(readiness.AppleReadinessTests):
    def test_xcode_probe_replaces_the_hung_bootstatus_and_preserves_full_selection(self):
        result, calls = self.run_preparation()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((self.root / 'xcodegen-calls').is_file(), 'the real xcodegen dump invocation was not captured')
        self.assertEqual(len(calls), 5)
        self.assertEqual([call[-1] for call in calls], ["build-for-testing"] + ["test-without-building"] * 4)
        selectors = lambda args: [a for a in args if a.startswith(("-only-testing:", "-skip-testing:"))]
        self.assertEqual(selectors(calls[0]), selectors(calls[2]))
        self.assertEqual(selectors(calls[1]), ["-only-testing:PersonaTests/TypeScaleTests"])
        self.assertIn("-only-testing:PersonaTests", calls[2])
        self.assertIn("-skip-testing:PersonaTests/" + REGULAR_SUITE, calls[0])
        self.assertNotIn("-only-testing:PersonaTests/" + REGULAR_SUITE, calls[2])
        self.assertIn("ARCHS=" + os.uname().machine, calls[0])
        self.assertIn("ONLY_ACTIVE_ARCH=YES", calls[0])
        for call in calls[1:]:
            self.assertFalse(any(arg.startswith(("ARCHS=", "ONLY_ACTIVE_ARCH=")) for arg in call))
        for call in calls:
            self.assertEqual(call[call.index("-scheme") + 1], "PersonaOfflineUnitTests")
            self.assertEqual(call[call.index("-project") + 1], str(ROOT / "apple/PersonaOfflineUnitTests.xcodeproj"))
            self.assertEqual(call[call.index("-configuration") + 1], "Debug")
            self.assertEqual(call[call.index("-clonedSourcePackagesDirPath") + 1], "/fixture/spm/SourcePackages")
            self.assertIn("CODE_SIGNING_ALLOWED=YES", call)
            self.assertEqual(call[call.index("-derivedDataPath") + 1], str(self.output / "ios-tests"))
        context = json.loads((self.output / "simulator-readiness-context.json").read_text())
        self.assertEqual(context["udid"], NEW)
        self.assertEqual(context["bundle_id"], "org.example.persona.ci")
        self.assertEqual(set(context), {"udid", "bundle_id", "started_at"})
        regular_context = json.loads((self.output / "regular-readiness-context.json").read_text())
        self.assertEqual(regular_context["udid"], REGULAR_UDID)
        self.assertEqual(regular_context["bundle_id"], "org.example.persona.ci")
        self.assertNotEqual(regular_context["udid"], context["udid"])
        self.assertTrue((self.output / "diagnostics/live-readiness/observer.status.txt").is_file())
        self.assertTrue((self.output / "diagnostics/live-regular-readiness/observer.status.txt").is_file())
        for name in ("live-readiness", "live-regular-readiness"):
            observer = self.output / "diagnostics" / name
            self.assertIn("xcodebuild-finished", (observer / "observer.status.txt").read_text(),
                          result.stdout + result.stderr + "\n".join(
                              path.name + ": " + path.read_text()
                              for path in sorted(observer.glob("*.status.txt"))))
            self.assertTrue((observer / "simulator-launch.status.txt").is_file())
            self.assertTrue((observer / "product-ready").is_file(),
                            [str(path) for path in observer.iterdir()])
            self.assert_collected_products(observer)
        self.assertFalse((self.root / "collector-outlived-observer").exists(),
                         "the observer published a final status while its collector was still running")
        self.assert_recorded_processes_reaped()

    def test_stop_reaps_a_nested_child_after_its_collector_exits(self):
        result, _ = self.run_preparation(NESTED_CHILD="1")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((self.root / "nested-child-ready").exists())
        self.assert_recorded_processes_reaped()
        self.assert_collected_products(self.output / "diagnostics/live-readiness")
        sim_calls = (self.root / "calls").read_text()
        self.assertNotIn("simctl bootstatus", sim_calls)
        self.assertEqual([line for line in sim_calls.splitlines() if "simctl boot " in line],
                         ["xcrun simctl boot " + NEW])
        self.assertEqual(sim_calls.count("simctl create"), 2)
        self.assertIn('regular About fixture', (self.root / "calls").read_text())
        self.assertIn('com.apple.CoreSimulator.SimRuntime.iOS-27-0', sim_calls)
        self.assertTrue((self.root / "regular-probe-started").exists())
        self.assertTrue((self.root / "regular-suite-started").exists())
        self.assertIn("ios-about-regular.xcresult: 1 test cases, zero failed/skipped", result.stdout)
        self.assertEqual(self.fixture.device(REGULAR_UDID)["state"], "Shutdown")
        self.assertTrue((self.root / "privacy-granted").exists())
        self.assertTrue((self.root / "full-suite-started").exists())
        self.assertEqual(self.fixture.device(PHONE)["state"], "Shutdown")
        self.assertEqual(self.fixture.device(WATCH)["state"], "Shutdown")
        self.assertIn("1 test cases, zero failed/skipped", (self.output / "simulator-readiness.log").read_text())
        self.assertIn("1 test cases, zero failed/skipped", (self.output / "regular-readiness.log").read_text())
        self.assertEqual((self.root / "budgets").read_text().splitlines(), [
            "Generate iPhone-only offline unit-test project|120", "Generate offline unit-test Xcode project|120",
            "Build iOS offline unit tests|1800", "No CallKit in the simulator build|300",
            "Create stock iOS 26.0 test simulator|100",
            "Verify hosted iOS test readiness|900", "Grant Reminders access to the owned test app|300",
            "iOS offline unit and design/token tests|1800", "Inspect test results (ios-unit)|120",
            "Create stock regular-width About test simulator|100",
            "Verify hosted regular iPad test readiness|900",
            "iOS regular-width About visual test on real iPad|600",
            "Inspect test results (ios-about-regular)|120",
        ])
        self.assertIn("TEST_RUNNER_PERSONA_SHOTS",
                      (self.root / "xcode-env.jsonl").read_text())



def load_tests(loader, tests, pattern):
    suite = unittest.TestSuite()
    for name in sorted(n for n in vars(AppleReadinessExecutionTests) if n.startswith("test_")):
        suite.addTest(AppleReadinessExecutionTests(name))
    return suite


if __name__ == "__main__":
    unittest.main()
