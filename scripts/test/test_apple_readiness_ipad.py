"""scripts/ci/apple.sh's hosted iOS readiness: the regular-width iPad cases of test_apple_readiness.py.

Split from that suite so each file stays well under the scripts lane's 120-second limit per suite
(scripts/ci/python-tests.py): after the deadline cases moved out (#13293) the rest still took 112-120 s on a busy CI
machine and timed out there once. The fixture is test_apple_readiness.py's AppleReadinessTests; only the tests defined
here run from this file (load_tests).
"""
import json
import os
import subprocess
import unittest

import test_apple_readiness as readiness
from test_apple_readiness import FAKE_CLEANUP_SIMCTL, NEW, PHONE, REGULAR_SUITE, REGULAR_UDID, ROOT, SCRIPT

AppleReadinessTests = readiness.AppleReadinessTests  # the cases below build fresh fixtures of it


class AppleReadinessIpadTests(readiness.AppleReadinessTests):
    def test_cleanup_removes_both_owned_destinations_including_an_unparsed_ipad(self):
        source = SCRIPT.read_text()
        start = source.index("cleanup_device() {")
        block = source[start:source.index("cleanup() {", start)]
        tools = {"xcrun": FAKE_CLEANUP_SIMCTL}
        for name, body in tools.items():
            path = self.fixture.bin / name
            path.write_text(body)
            path.chmod(0o755)
        shell = '''set -euo pipefail
ci_run() { shift 2; "$@"; }
SIM="33333333-3333-3333-3333-333333333333"
SIM_STARTED=1
SIM_NAME="owned phone"
REGULAR_SIM=""
REGULAR_SIM_STARTED=1
REGULAR_SIM_NAME="owned regular"
''' + block + '''
cleanup_device "$SIM" "$SIM_NAME" "$SIM_STARTED"
cleanup_device "$REGULAR_SIM" "$REGULAR_SIM_NAME" "$REGULAR_SIM_STARTED"
'''
        result = subprocess.run(["bash", "-c", shell], cwd=ROOT, text=True, capture_output=True, timeout=10,
                                env=dict(os.environ, PATH=str(self.fixture.bin) + os.pathsep + os.environ["PATH"], FAKE=str(self.root)))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        calls = (self.root / "cleanup-calls").read_text().splitlines()
        self.assertEqual(calls, [f"simctl shutdown {NEW}", f"simctl delete {NEW}",
                                 "simctl list devices -j", f"simctl shutdown {REGULAR_UDID}",
                                 f"simctl delete {REGULAR_UDID}"])

    def test_regular_about_suite_uses_an_owned_real_ipad_and_strict_result(self):
        result, calls = self.run_preparation(regular_only=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(calls), 2)
        probe, call = calls
        for args in (probe, call):
            self.assertEqual(args[args.index("-scheme") + 1], "PersonaOfflineUnitTests")
        self.assertEqual([a for a in probe if a.startswith(("-only-testing:", "-skip-testing:"))],
                         ["-only-testing:PersonaTests/TypeScaleTests"])
        self.assertEqual(probe[probe.index("-destination") + 1], "platform=iOS Simulator,id=" + REGULAR_UDID)
        self.assertEqual(probe[probe.index("-resultBundlePath") + 1], str(self.output / "ios-regular-readiness.xcresult"))
        self.assertEqual(call[-1], "test-without-building")
        self.assertEqual(call[call.index("-destination") + 1],
                         "platform=iOS Simulator,id=" + REGULAR_UDID)
        self.assertEqual([a for a in call if a.startswith(("-only-testing:", "-skip-testing:"))],
                         ["-only-testing:PersonaTests/" + REGULAR_SUITE])
        self.assertEqual((self.output / "regular-simulator.log").read_text().strip(), REGULAR_UDID)
        self.assertIn('ipad-regular-fixture', (self.root / "calls").read_text())
        self.assertIn('com.apple.CoreSimulator.SimRuntime.iOS-27-0', (self.root / "calls").read_text())
        self.assertIn("1 test cases, zero failed/skipped", result.stdout)
        self.assertTrue((self.root / "regular-probe-started").exists())
        self.assertTrue((self.root / "regular-suite-started").exists())
        self.assertEqual(self.fixture.device(PHONE)["state"], "Shutdown")

    def test_dedicated_ipad_diagnostic_stops_after_strict_probe_and_retains_its_scope(self):
        result, calls = self.run_preparation(regular_only=True, LANE="ios-ipad-diagnostic")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0][calls[0].index("-destination") + 1], "platform=iOS Simulator,id=" + REGULAR_UDID)
        self.assertEqual([a for a in calls[0] if a.startswith(("-only-testing:", "-skip-testing:"))],
                         ["-only-testing:PersonaTests/TypeScaleTests"])
        self.assertTrue((self.root / "regular-probe-started").exists())
        self.assertFalse((self.root / "regular-suite-started").exists())
        artifacts = self.root / "diagnostic-artifacts"
        self.assertTrue((artifacts / "regular-readiness.log").is_file(), result.stdout + result.stderr)
        self.assertTrue((artifacts / "ios-regular-readiness.xcresult/tests.json").is_file())
        self.assertEqual(json.loads((artifacts / "regular-readiness-context.json").read_text())["udid"], REGULAR_UDID)
        self.assertTrue((artifacts / "live-regular-readiness/observer.status.txt").is_file())
        self.assertTrue((artifacts / "live-regular-readiness/simulator-launch.status.txt").is_file())
        self.assertFalse((artifacts / "live-readiness").exists())
        self.assertFalse((artifacts / "ios-unit.xcresult").exists())
        self.assertFalse((artifacts / "simulator-readiness.log").exists())
        self.assertFalse((artifacts / "simulator-launch.status.txt").exists())
        self.assertFalse((artifacts / "regular-readiness-context.json").is_symlink())
        self.assertIn("Verify hosted regular iPad test readiness|900", (self.root / "budgets").read_text())
        self.assertNotIn("iOS regular-width About visual test", (self.root / "budgets").read_text())
        for pid in self.watcher_pids.read_text().splitlines():
            with self.assertRaises(ProcessLookupError):
                os.kill(int(pid), 0)

    def test_dedicated_ipad_diagnostic_retains_failed_strict_results_without_visuals(self):
        for options in ({"REGULAR_PROBE_STATUS": "65"}, {"RESULT_STATUS": "Failed"},
                        {"RESULT_STATUS": "Skipped"}, {"RESULT_STATUS": "empty"},
                        {"RESULT_SUITE": "UnrelatedTests"}):
            with self.subTest(options=options):
                case = AppleReadinessTests()
                case.setUp()
                try:
                    result, calls = case.run_preparation(regular_only=True, LANE="ios-ipad-diagnostic", **options)
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(len(calls), 1)
                    artifacts = case.root / "diagnostic-artifacts"
                    self.assertTrue((artifacts / "regular-readiness.log").is_file())
                    self.assertTrue((artifacts / "live-regular-readiness/observer.status.txt").is_file())
                    self.assertFalse((case.root / "regular-suite-started").exists())
                    self.assertTrue((case.output / "diagnostics/live-regular-readiness/observer.stop").exists())
                    for pid in case.watcher_pids.read_text().splitlines():
                        with self.assertRaises(ProcessLookupError):
                            os.kill(int(pid), 0)
                finally:
                    case.doCleanups()

    def test_dedicated_ipad_outer_cleanup_keeps_regular_scope_before_context_publication(self):
        source = SCRIPT.read_text()
        helpers = source[source.index('cleanup_device() {'):source.index('trap cleanup EXIT')] + '\ntrap cleanup EXIT\n'
        shell = '''set -euo pipefail
CI_ROOT="$1"
OUTPUT="$2"
DIAGNOSTICS="$3"
LANE=ios-ipad-diagnostic
SIM=""
SIM_NAME="unused phone"
SIM_STARTED=0
REGULAR_SIM=""
REGULAR_SIM_NAME="unused regular"
REGULAR_SIM_STARTED=0
ci_run() { shift 2; "$@"; }
''' + helpers + '''
exit 65
'''
        (self.output / 'simulator-readiness.log').write_text('phone evidence must stay out')
        (self.output / 'simulator-readiness-context.json').write_text('phone context must stay out')
        (self.output / 'regular-readiness.log').write_text('regular evidence')
        for name in ('ios-readiness.xcresult', 'ios-regular-readiness.xcresult'):
            (self.output / name).mkdir()
            (self.output / name / 'tests.json').write_text(name)
        result = subprocess.run(['bash', '-c', shell, '--', str(ROOT), str(self.output), str(self.root / 'artifacts')],
                                cwd=ROOT, env=dict(os.environ, PATH=str(self.fixture.bin) + os.pathsep + os.environ['PATH']),
                                capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 65, result.stdout + result.stderr)
        artifacts = self.root / 'artifacts'
        self.assertTrue((artifacts / 'regular-readiness.log').is_file(), result.stdout + result.stderr)
        self.assertFalse((artifacts / 'simulator-readiness.log').exists())
        self.assertFalse((artifacts / 'simulator-readiness-context.json').exists())
        self.assertFalse((artifacts / 'ios-readiness.xcresult').exists())
        self.assertTrue((artifacts / 'ios-regular-readiness.xcresult/tests.json').is_file())
        self.assertEqual(json.loads((artifacts / 'regular-readiness-context.json').read_text()),
                         {'unavailable': 'regular iPad probe did not publish an owned regular-file context'})

    def test_regular_readiness_must_pass_before_the_regular_suite(self):
        for options in ({"REGULAR_PROBE_STATUS": "65"}, {"RESULT_STATUS": "Failed"},
                        {"RESULT_STATUS": "Skipped"}, {"RESULT_STATUS": "empty"},
                        {"RESULT_SUITE": "UnrelatedTests"}):
            with self.subTest(options=options):
                case = AppleReadinessTests()
                case.setUp()
                try:
                    result, calls = case.run_preparation(regular_only=True, **options)
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(len(calls), 1)
                    self.assertTrue((case.root / "regular-probe-started").exists())
                    self.assertTrue((case.output / "diagnostics/live-regular-readiness/observer.stop").exists())
                    self.assertFalse((case.root / "regular-suite-started").exists())
                finally:
                    case.doCleanups()

    def test_regular_readiness_and_result_inspection_share_one_deadline(self):
        for key, marker, lane in (("REGULAR_PROBE_HANG", "regular-probe.stopped", "ios"),
                                  ("RESULT_HANG", "result.stopped", "ios"),
                                  ("REGULAR_PROBE_HANG", "regular-probe.stopped", "ios-ipad-diagnostic")):
            with self.subTest(phase=key, lane=lane):
                case = AppleReadinessTests()
                case.setUp()
                # The global fixture models a hung legacy bootstatus. This case
                # exercises the separate iPad probe, so its ordinary boot returns.
                case.fixture.state["bootstatus_hang"] = False
                case.fixture.save()
                try:
                    result, calls = case.run_preparation(regular_only=True, LANE=lane, TEST_STEP_TIMEOUT="1", **{key: "1"})
                    self.assertEqual(result.returncode, 124, result.stdout + result.stderr)
                    self.assertEqual(len(calls), 1)
                    self.assertEqual((case.root / marker).read_text(), "terminated")
                    self.assertFalse((case.root / "regular-suite-started").exists())
                    observer = case.output / "diagnostics/live-regular-readiness"
                    self.assertIn("xcodebuild-finished", (observer / "observer.status.txt").read_text())
                    self.assertTrue((observer / "host-final.json").is_file())
                    for suffix in ("architecture", "libraries"):
                        self.assertEqual((observer / f"product-0-{suffix}.txt.status.txt").read_text(), "0\n")
                    self.assertEqual((observer / "product-0-bundle-signature.txt.status.txt").read_text(), "0\n")
                    case.assert_recorded_processes_reaped(iphone=False)
                finally:
                    case.doCleanups()

    def test_regular_about_failure_empty_or_skipped_results_cannot_pass(self):
        for options in ({"REGULAR_STATUS": "7"}, {"REGULAR_EMPTY": "1"}, {"REGULAR_RESULT": "Skipped"}):
            with self.subTest(options=options):
                case = AppleReadinessTests()
                case.setUp()
                try:
                    result, calls = case.run_preparation(regular_only=True, **options)
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual(len(calls), 2)
                finally:
                    case.doCleanups()


def load_tests(loader, tests, pattern):
    """Only this file's tests: the fixture class it extends runs its own in test_apple_readiness.py."""
    suite = unittest.TestSuite()
    for name in sorted(n for n in vars(AppleReadinessIpadTests) if n.startswith("test_")):
        suite.addTest(AppleReadinessIpadTests(name))
    return suite


if __name__ == "__main__":
    unittest.main()
