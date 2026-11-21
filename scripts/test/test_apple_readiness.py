"""Execute the CI preparation block with owned fake devices and real result validation."""
import json
import os
from pathlib import Path
import signal
import subprocess
import textwrap
import unittest

import test_sim_boot
from test_sim_boot import FAKE_XCRUN, NEW, PHONE, ROOT, WATCH

REGULAR_UDID = "44444444-4444-4444-4444-444444444444"
REGULAR_SUITE = "AboutVersionRegularVisualTests"

SCRIPT = ROOT / "scripts/ci/apple.sh"

FAKE_XCODE = textwrap.dedent('''\
    #!/usr/bin/env python3
    import json, os, signal, sys
    from pathlib import Path
    args = sys.argv[1:]
    root = Path(os.environ["FAKE"])
    with (root / "xcode-calls.jsonl").open("a") as log:
        log.write(json.dumps(args) + "\\n")
    with (root / "xcode-env.jsonl").open("a") as log:
        log.write(json.dumps({key: os.environ[key] for key in ("TEST_RUNNER_PERSONA_SHOTS",) if key in os.environ}) + "\\n")
    if "-only-testing:PersonaTests/AboutVersionRegularVisualTests" in args:
        assert "-only-testing:PersonaTests/AboutVersionRegularVisualTests" in args, args
        assert os.environ["TEST_RUNNER_PERSONA_SHOTS"].endswith("/regular-shots")
        assert args[args.index("-destination") + 1] == "platform=iOS Simulator,id=44444444-4444-4444-4444-444444444444"
        assert args[-1] == "test-without-building"
        assert args.count("-only-testing:PersonaTests/AboutVersionRegularVisualTests") == 1
        if os.environ.get("REGULAR_STATUS", "0") != "0":
            sys.exit(int(os.environ["REGULAR_STATUS"]))
        (root / "regular-suite-started").write_text("yes")
        bundle = Path(args[args.index("-resultBundlePath") + 1])
        bundle.mkdir()
        tests = [] if os.environ.get("REGULAR_EMPTY") == "1" else [{"nodeType": "Test Case",
            "nodeIdentifier": "PersonaTests/AboutVersionRegularVisualTests/theSharedRowMatchesTheNativeControlAtRegularWidth()",
            "result": os.environ.get("REGULAR_RESULT", "Passed")}]
        (bundle / "tests.json").write_text(json.dumps({"testNodes": tests}))
        sys.exit(0)
    if args[-1] == "build-for-testing":
        assert args[args.index("-destination") + 1] == "generic/platform=iOS Simulator"
        status = int(os.environ.get("BUILD_STATUS", "0"))
        if status == 0:
            # The product the CallKit check reads (#9290): one Mach-O, known by its magic number.
            app = Path(args[args.index("-derivedDataPath") + 1]) / "Build/Products/Debug-iphonesimulator/Persona.app"
            app.mkdir(parents=True, exist_ok=True)
            (app / "Persona").write_bytes(b"\\xcf\\xfa\\xed\\xfe" + bytes(28))
        sys.exit(status)
    regular_probe = args[args.index("-resultBundlePath") + 1].endswith("/ios-regular-readiness.xcresult")
    expected_udid = "44444444-4444-4444-4444-444444444444" if regular_probe else os.environ["OWNED_UDID"]
    assert args[args.index("-destination") + 1] == "platform=iOS Simulator,id=" + expected_udid
    probe = "-only-testing:PersonaTests/TypeScaleTests" in args
    if probe:
        probe_device = expected_udid
        # Xcode manages boot, installation and test-host launch, without simctl bootstatus.
        state = json.loads((root / "state.json").read_text())
        device = next(d for devices in state["devices"].values() for d in devices
                      if d["udid"] == probe_device)
        device["state"] = "Booted"
        (root / "state.json").write_text(json.dumps(state))
        bundle = Path(args[args.index("-resultBundlePath") + 1])
        (root / "probe.pid").write_text(str(os.getpid()))
        if bundle.name == "ios-regular-readiness.xcresult":
            (root / "regular-probe-started").write_text("yes")
            if os.environ.get("REGULAR_PROBE_HANG") == "1":
                def stop(*_):
                    (root / "regular-probe.stopped").write_text("terminated")
                    sys.exit(143)
                signal.signal(signal.SIGTERM, stop)
                signal.pause()
            if os.environ.get("REGULAR_PROBE_STATUS", "0") != "0":
                sys.exit(int(os.environ["REGULAR_PROBE_STATUS"]))
        if os.environ.get("PROBE_HANG") == "1":
            def stop(*_):
                (root / "probe.stopped").write_text("terminated")
                sys.exit(143)
            signal.signal(signal.SIGTERM, stop)
            signal.pause()
        if os.environ.get("PROBE_STATUS", "0") != "0":
            sys.exit(int(os.environ["PROBE_STATUS"]))
        bundle = Path(args[args.index("-resultBundlePath") + 1])
        bundle.mkdir()
        status = os.environ.get("RESULT_STATUS", "Passed")
        suite = os.environ.get("RESULT_SUITE", "TypeScaleTests")
        tests = [] if status == "empty" else [{"nodeType": "Test Case",
            "nodeIdentifier": "PersonaTests/" + suite + "/usesTextTokens()", "result": status}]
        (bundle / "tests.json").write_text(json.dumps({"testNodes": tests}))
        # Xcode manages the test destination; success does not keep it booted for simctl privacy.
        state = json.loads((root / "state.json").read_text())
        device = next(d for devices in state["devices"].values() for d in devices
                      if d["udid"] == probe_device)
        device["state"] = os.environ.get("POST_PROBE_STATE", "Shutdown")
        device["isAvailable"] = os.environ.get("POST_PROBE_AVAILABLE", "1") == "1"
        (root / "state.json").write_text(json.dumps(state))
    else:
        tests = [] if os.environ.get("FULL_RESULT_EMPTY") == "1" else [{"nodeType": "Test Case",
            "nodeIdentifier": "PersonaTests/" + suite + "/usesTextTokens()",
            "result": os.environ.get("FULL_RESULT", "Passed")} for suite in os.environ.get("FULL_RESULT_SUITE", "TypeScaleTests").split(",")]
        (root / "full-tests.json").write_text(json.dumps({"testNodes": tests}))
        (root / "full-suite-started").write_text("yes")
        sys.exit(int(os.environ.get("FULL_STATUS", "0")))
    ''')

FAKE_XCRUN_WRAPPER = textwrap.dedent('''\
    #!/usr/bin/env python3
    import json, os, signal, sys
    from pathlib import Path
    args = sys.argv[1:]
    root = Path(os.environ["FAKE"])
    if args[:1] == ["xcresulttool"]:
        (root / "result.pid").write_text(str(os.getpid()))
        if os.environ.get("REGULAR_PROBE_HANG") == "1" or os.environ.get("RESULT_HANG") == "1":
            def stop(*_):
                (root / "result.stopped").write_text("terminated")
                sys.exit(143)
            signal.signal(signal.SIGTERM, stop)
            signal.pause()
        if args[1:3] == ["test-results", "tests"] or args[2:4] == ["test-results", "tests"]:
            bundle = Path(args[args.index("--path") + 1])
            if bundle.name == "ios-unit.xcresult":
                print((root / "full-tests.json").read_text())
            else:
                print((bundle / "tests.json").read_text())
        else:
            bundle = Path(args[args.index("--path") + 1])
            print((bundle / "tests.json").read_text())
    elif args[:2] == ["simctl", "privacy"]:
        assert args[2:] == [os.environ["OWNED_UDID"], "grant", "reminders", "org.example.persona.ci"], args
        (root / "privacy-attempted").write_text("yes")
        state = json.loads((root / "state.json").read_text())
        device = next(d for devices in state["devices"].values() for d in devices
                      if d["udid"] == args[2])
        if device["state"] != "Booted":
            print("SimError 405: Unable to lookup in current state: " + device["state"], file=sys.stderr)
            sys.exit(149)
        if os.environ.get("PRIVACY_STATUS", "0") != "0":
            sys.exit(int(os.environ["PRIVACY_STATUS"]))
        (root / "privacy-granted").write_text("yes")
    else:
        if args[:2] == ["simctl", "boot"] and os.environ.get("BOOT_HANG") == "1":
            assert args[2:] == [os.environ["OWNED_UDID"]], args
            def stop(*_):
                (root / "boot.stopped").write_text("terminated")
                sys.exit(143)
            signal.signal(signal.SIGTERM, stop)
            signal.pause()
        os.execv(str(root / "bin/fake-simctl"), ["xcrun", *args])
    ''')


# `otool -arch all -L <file>` for the CallKit check (#9290): libSystem, and CallKit as well with OTOOL_CALLKIT=1.
FAKE_OTOOL = textwrap.dedent('''\
    #!/usr/bin/env python3
    import os, sys
    from pathlib import Path
    with (Path(os.environ["FAKE"]) / "otool-calls").open("a") as log:
        log.write(" ".join(sys.argv[1:]) + "\\n")
    print(sys.argv[-1] + ":")
    print("\\t/usr/lib/libSystem.B.dylib (compatibility version 1.0.0, current version 1.0.0)")
    if os.environ.get("OTOOL_CALLKIT") == "1":
        print("\\t/System/Library/Frameworks/CallKit.framework/CallKit (compatibility version 1.0.0, current version 1.0.0)")
    ''')


FAKE_CLEANUP_SIMCTL = '''#!/usr/bin/env python3
import os
import sys
from pathlib import Path
with (Path(os.environ["FAKE"]) / "cleanup-calls").open("a") as log:
    log.write(" ".join(sys.argv[1:]) + "\\n")
if sys.argv[1:] == ["simctl", "list", "devices", "-j"]:
    print('{"devices":{"runtime":[{"udid":"44444444-4444-4444-4444-444444444444","name":"owned regular"}]}}')
'''


class AppleReadinessTests(unittest.TestCase):
    def setUp(self):
        self.fixture = test_sim_boot.SimBootTests()
        self.fixture.setUp()
        self.addCleanup(self.fixture.tearDown)
        self.root = self.fixture.dir
        self.fixture.state["bootstatus_hang"] = True
        self.fixture.save()
        self.watcher_pids = self.root / "watcher-pids"
        self.watcher_pids.write_text("")
        python = self.fixture.bin / "python3"
        python.write_text(textwrap.dedent('''\
            #!/bin/bash
            if [[ "$*" == *apple_readiness_watch.py* ]]; then
              printf '%s\\n' "$$" >> "$FAKE/watcher-pids"
            fi
            exec /usr/bin/python3 "$@"
        '''))
        python.chmod(0o755)
        for name, body in (("xcodebuild", FAKE_XCODE), ("xcrun", FAKE_XCRUN_WRAPPER),
                           ("fake-simctl", FAKE_XCRUN), ("otool", FAKE_OTOOL),
                           ("file", "#!/bin/bash\nexit 0\n"), ("codesign", "#!/bin/bash\nexit 0\n")):
            if name == "fake-simctl":
                body = body.replace(
                    'if a[0] == "create":\n',
                    'if a[0] == "create":\n'
                    '    st["next"] = "' + NEW + '" if "iPhone" in a[2] else "' + REGULAR_UDID + '"\n')
            path = self.fixture.bin / name
            path.write_text(body)
            path.chmod(0o755)
        self.state = self.fixture.state
        self.state["next"] = REGULAR_UDID
        self.state["diagnostic_udid"] = NEW
        self.state["devicetypes"].append({"name": "iPad Pro 11-inch (M5)", "identifier": "ipad-regular-fixture"})
        for runtime in self.state["runtimes"]:
            if runtime["identifier"] == "com.apple.CoreSimulator.SimRuntime.iOS-27-0":
                runtime["supportedDeviceTypes"].append({"identifier": "ipad-regular-fixture"})
        self.fixture.state = self.state
        self.fixture.save()
        self.output = self.root / "output"
        self.output.mkdir()
        (self.output / "config.json").write_text(json.dumps({"app": {"bundle_id": "org.example.persona.ci"}}))

    def run_preparation(self, regular_only=False, regression_only=False, **options):
        source = SCRIPT.read_text()
        helpers = source[source.index("simulator_udid() {"):source.index("cleanup() {")]
        helpers += source[source.index("result() {"):source.index('\n  fi', source.index("\ntest_batch() {")) + 8]
        if regression_only:
            helpers += source[source.index("test_batch() {"):source.index('\nif [ "$LANE" = all ]')]
        settings = source.index('  if [ "$LANE" = ios18 ]; then',
                                source.index('if [ "$LANE" != packages ] && [ "$LANE" != ios-build ]; then'))
        helpers += source[settings:source.index('  IOS_COMMON=(', settings)]
        helpers += source[source.index('  IOS_COMMON=(', settings):source.index('  # Explicit fixture-only exclusions', settings)]
        if regression_only:
            start = source.index('  IOS_COMMON=(')
            end = source.index('\n  fi', source.index('if [ "$LANE" = ios-regressions ]; then',
                                                        source.index('  IOS_FAILED=0', start))) + len('\n  fi')
            block = source[start:end]
            owned_udid = NEW
        elif regular_only:
            start = source.index("  # The same compiled fixture and compatible runtime as #11289")
            block = source[start:source.index('  ci_run \'Verify token baseline', start)]
            owned_udid = REGULAR_UDID
        else:
            start = source.index('  IOS_COMMON=(')
            block = source[start:source.index('  result ios-unit "$SIM" TypeScaleTests IconVocabularyTests', start)]
            ordinary_result = source.index('  result ios-unit "$SIM" TypeScaleTests IconVocabularyTests', start)
            result_end = source.index('\n  fi', ordinary_result) + 3
            block += source[ordinary_result:result_end] + '\n  fi\n'
            block += source[source.index("  # The same compiled fixture and compatible runtime as #11289"):
                            source.index('  ci_run \'Verify token baseline', start)]
            owned_udid = NEW
        shell = '''set -euo pipefail
ci_run() {
  local label="$1" budget="$2"; shift 2
  printf '%s|%s\\n' "$label" "$budget" >> "$FAKE/budgets"
  python3 "$CI_ROOT/scripts/ci/run.py" --label "$label" --timeout "$TEST_STEP_TIMEOUT" -- "$@"
}
XCODE=(xcodebuild)
TEST_SIGNING=(CODE_SIGNING_ALLOWED=YES CODE_SIGN_IDENTITY=- CODE_SIGN_STYLE=Manual DEVELOPMENT_TEAM= PROVISIONING_PROFILE_SPECIFIER=)
''' + helpers + block
        env = dict(os.environ, PATH=str(self.fixture.bin) + os.pathsep + os.environ["PATH"],
                   LANE="ios-regressions" if regression_only else "ios", CI_ROOT=str(ROOT), SOURCE=str(ROOT), OUTPUT=str(self.output),
                   PERSONA_CONFIG=str(self.output / "config.json"), SIM_NAME="owned readiness fixture",
                   DIAGNOSTICS=str(self.root / "diagnostic-artifacts"),
                   CI_BUNDLE_ID="org.example.persona.ci",
                   IOS_RUNTIME="com.apple.CoreSimulator.SimRuntime.iOS-27-0", OWNED_UDID=owned_udid,
                   REGULAR_SIM_NAME="owned regular About fixture",
                   FAKE=str(self.root), TEST_STEP_TIMEOUT="10")
        env.update(options)
        env.setdefault("FULL_RESULT_SUITE", "TypeScaleTests,IconVocabularyTests,TokenRatchetTests,StyleSpecTests")
        result = subprocess.run(["bash", "-c", shell], cwd=ROOT, env=env,
                                capture_output=True, text=True, timeout=25)
        calls = self.root / "xcode-calls.jsonl"
        return result, [json.loads(line) for line in calls.read_text().splitlines()] if calls.exists() else []

    def test_xcode_probe_replaces_the_hung_bootstatus_and_preserves_full_selection(self):
        result, calls = self.run_preparation()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
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
            self.assertIn("xcodebuild-finished", (observer / "observer.status.txt").read_text())
            self.assertTrue((observer / "simulator-launch.status.txt").is_file())
            for suffix in ("architecture.txt", "libraries.txt"):
                self.assertTrue((observer / f"product-0-{suffix}").is_file())
            self.assertTrue((observer / "product-0-bundle-signature.txt").is_file())
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
            "Build iOS offline unit tests|1800", "No CallKit in the simulator build|300",
            "Create stock iOS 26.0 test simulator|100",
            "Verify hosted iOS test readiness|900", "Grant Reminders access to the owned test app|300",
            "iOS offline unit and design/token tests|1800", "Inspect test results (ios-unit)|120",
        ])

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
    def test_readiness_shell_does_not_depend_on_unexported_ci_variables(self):
        source = SCRIPT.read_text()
        marker = "  ci_run 'Verify hosted iOS test readiness'"
        start = source.index(marker)
        block = source[start:source.index("\n\n", start)]
        # A background observer must not inherit the captured stdout pipe. Keep
        # its log in this writable fixture, not at a machine-level fixture path.
        observer = textwrap.dedent('''\
            #!/usr/bin/env python3
            import json, os, signal, sys
            from pathlib import Path
            root = Path(os.environ["READINESS_FIXTURE"])
            arguments = sys.argv[1:]
            log = (root / "observer.log").open("w")
            sys.stdout = sys.stderr = log
            root.joinpath("observer.pid").write_text(str(os.getpid()))
            root.joinpath("observer-arguments.json").write_text(json.dumps(arguments))
            def stop(*_):
                root.joinpath("observer-stopped").write_text("terminated")
                sys.exit(0)
            signal.signal(signal.SIGTERM, stop)
            signal.signal(signal.SIGINT, stop)
            root.joinpath("observer-ready").write_text("ready")
            print("observer-ready", flush=True)
            log.close()
            os.close(1)
            os.close(2)
            signal.pause()
        ''')
        ci_root = self.root / "ci-root/scripts/ci"
        ci_root.mkdir(parents=True)
        (ci_root / "apple_readiness_watch.py").write_text(observer)
        (ci_root / "run.py").symlink_to(ROOT / "scripts/ci/run.py")
        (ci_root / "apple_result.py").symlink_to(ROOT / "scripts/ci/apple_result.py")
        (self.root / "ci-root/scripts/app").symlink_to(ROOT / "scripts/app", target_is_directory=True)
        probe = textwrap.dedent('''\
            #!/usr/bin/env python3
            import json, os, sys, time
            from pathlib import Path
            args = sys.argv[1:]
            root = Path(os.environ["READINESS_FIXTURE"])
            assert os.environ.get("OUTPUT") is None
            assert os.environ.get("CI_BUNDLE_ID") is None
            assert args.count("-only-testing:PersonaTests/TypeScaleTests") == 1, args
            assert args[args.index("-destination") + 1] == (
                "platform=iOS Simulator,id=11111111-1111-1111-1111-111111111111"), args
            assert args[-1] == "test-without-building", args
            deadline = time.monotonic() + 10
            while not root.joinpath("observer-ready").exists():
                if time.monotonic() >= deadline:
                    raise RuntimeError("observer did not become ready")
                time.sleep(0.01)
            assert not root.joinpath("observer-stopped").exists(), "observer stopped before the probe"
            bundle = Path(args[args.index("-resultBundlePath") + 1])
            assert bundle.parent == Path(os.environ["READINESS_OUTPUT"]), args
            bundle.mkdir()
            bundle.joinpath("tests.json").write_text(json.dumps({"testNodes": [{
                "nodeType": "Test Case",
                "nodeIdentifier": "PersonaTests/TypeScaleTests/usesTextTokens()",
                "result": "Passed"}]}))
            root.joinpath("probe-arguments.json").write_text(json.dumps(args))
        ''')
        result_reader = textwrap.dedent('''\
            #!/usr/bin/env python3
            import json, os, sys
            from pathlib import Path
            args = sys.argv[1:]
            assert args[:4] == ["xcresulttool", "get", "test-results", "tests"], args
            assert args[4] == "--path" and args[6:] == ["--compact"], args
            root = Path(os.environ["READINESS_FIXTURE"])
            assert os.environ.get("OUTPUT") is None
            assert os.environ.get("CI_BUNDLE_ID") is None
            root.joinpath("result-arguments.json").write_text(json.dumps(args[3:]))
            with root.joinpath("result-calls.jsonl").open("a") as log:
                log.write(json.dumps(args) + "\\n")
            print(Path(args[5]).joinpath("tests.json").read_text())
        ''')
        for name, body in (("xcodebuild", probe), ("xcrun", result_reader),
                           ("bash", "#!/bin/sh\nexec /bin/bash \"$@\"\n"),
                           ("python3", "#!/bin/sh\nexec /usr/bin/python3 \"$@\"\n"),
                           ("tee", "#!/bin/sh\n/bin/cat >> \"$1\"\n")):
            path = self.fixture.bin / name
            path.write_text(body)
            path.chmod(0o755)
        shell = '''set -euo pipefail
unset OUTPUT CI_BUNDLE_ID
ci_run() {
  local label="$1" budget="$2"
  printf '%s|%s\\n' "$label" "$budget" >> "$READINESS_FIXTURE/budgets"
  shift 2
  /usr/bin/python3 "$CI_ROOT/scripts/ci/run.py" --label "$label" --timeout "$budget" -- "$@"
}
CI_ROOT="$READINESS_CI_ROOT"
OUTPUT="$READINESS_OUTPUT"
LANE=ios
SIM=11111111-1111-1111-1111-111111111111
CI_BUNDLE_ID=org.example.persona.ci
IOS_TESTS=(xcodebuild -destination "platform=iOS Simulator,id=$SIM" -destination-timeout 120)
''' + block
        output = self.root / "writable-output"
        output.mkdir()
        env = {"PATH": str(self.fixture.bin) + os.pathsep + os.defpath,
               "FAKE": str(self.root), "READINESS_FIXTURE": str(self.root),
               "READINESS_CI_ROOT": str(self.root / "ci-root"),
               "READINESS_OUTPUT": str(output),
               "PYTHONDONTWRITEBYTECODE": "1"}
        shell_source = self.root / "extracted-readiness.sh"
        shell_source.write_text(shell)
        syntax = subprocess.run(["/bin/bash", "-n", str(shell_source)], cwd=self.root,
                                env=env, capture_output=True, text=True, timeout=10)
        self.assertEqual(syntax.returncode, 0, syntax.stdout + syntax.stderr)
        # The outer deadline owns the extracted shell's whole process group,
        # including the observer and its children if an assertion regresses.
        result = subprocess.run(["/usr/bin/python3", str(ROOT / "scripts/ci/run.py"),
                                 "--label", "readiness shell input regression", "--timeout", "30",
                                 "--", "/bin/bash", str(shell_source)], cwd=self.root, env=env,
                                capture_output=True, text=True, timeout=45)
        stdout, stderr = result.stdout, result.stderr
        log_path = output / "simulator-readiness.log"
        details = stdout + stderr + (log_path.read_text() if log_path.exists() else "")
        self.assertEqual(result.returncode, 0, details)
        self.assertNotIn("unbound variable", stdout + stderr)
        self.assertEqual((self.root / "observer-stopped").read_text(), "terminated")
        self.assertEqual((self.root / "budgets").read_text(), "Verify hosted iOS test readiness|900\n")
        arguments = json.loads((self.root / "observer-arguments.json").read_text())
        self.assertEqual(arguments[arguments.index("--bundle-id") + 1], "org.example.persona.ci")
        self.assertEqual(arguments[arguments.index("--scope-root") + 1], str(output))
        self.assertEqual(arguments[arguments.index("--output") + 1],
                         str(output / "diagnostics/live-readiness"))
        self.assertEqual(arguments[arguments.index("--seconds") + 1], "900")
        self.assertEqual((output / "simulator-readiness.log").read_text(),
                         "apple-ci: PASS ios-readiness.xcresult: 1 test cases, zero failed/skipped\n")
        self.assertIn("PASS Verify hosted iOS test readiness", stdout)
        self.assertTrue((output / "diagnostics/live-readiness/observer.stop").is_file())
        observer_pid = int((self.root / "observer.pid").read_text())
        with self.assertRaises(ProcessLookupError, msg="observer survived shell cleanup"):
            os.kill(observer_pid, 0)
        arguments = json.loads((self.root / "observer-arguments.json").read_text())
        self.assertEqual(arguments[arguments.index("--udid") + 1],
                         "11111111-1111-1111-1111-111111111111")
        for option, expected in (("--app", "ios-tests/Build/Products/Debug-iphonesimulator/Persona.app/Persona"),
                                 ("--test-bundle", "ios-tests/Build/Products/Debug-iphonesimulator/Persona.app/PlugIns/PersonaTests.xctest/PersonaTests")):
            self.assertEqual(arguments[arguments.index(option) + 1], str(output / expected))
        result_arguments = json.loads((self.root / "result-arguments.json").read_text())
        self.assertEqual(result_arguments,
                         ["tests", "--path", str(output / "ios-readiness.xcresult"), "--compact"])
        probe_arguments = json.loads((self.root / "probe-arguments.json").read_text())
        self.assertEqual(probe_arguments[probe_arguments.index("-resultBundlePath") + 1],
                         str(output / "ios-readiness.xcresult"))

    def test_compact_visual_output_directory_is_run_specific_and_not_a_skip(self):
        result, calls = self.run_preparation()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        expected = str(self.output / "shots")
        environments = [json.loads(line) for line in (self.root / "xcode-env.jsonl").read_text().splitlines()]
        self.assertEqual(environments[:2], [{}, {}])
        self.assertEqual(environments[2:], [{"TEST_RUNNER_PERSONA_SHOTS": expected},
                                           {"TEST_RUNNER_PERSONA_SHOTS": expected},
                                           {"TEST_RUNNER_PERSONA_SHOTS": str(self.output / "regular-shots")}])
        for call in calls:
            self.assertFalse(any(argument.startswith("PERSONA_SHOTS=") for argument in call))
        support = (ROOT / "apple/PersonaTests/AboutVersionRefractTests.swift").read_text()
        self.assertIn('try #require(ProcessInfo.processInfo.environment["PERSONA_SHOTS"])', support)
        self.assertIn("native.pixels == shared.pixels", support)
        self.assertIn("try FileManager.default.createDirectory(at: root", support)
        self.assertNotIn('IOS_UNIT_SELECTION+=(-skip-testing:PersonaTests/AboutVersionCompactVisualTests)',
                         SCRIPT.read_text()[SCRIPT.read_text().index('if [ "$LANE" != packages ] && [ "$LANE" != ios-build ]; then'):SCRIPT.read_text().index('if [ "$LANE" = ios18 ]; then', SCRIPT.read_text().index('IOS_FAILED=0'))])

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
                    watcher_pids = [int(pid) for pid in case.watcher_pids.read_text().splitlines()]
                    self.assertTrue(watcher_pids)
                    for pid in watcher_pids:
                        with self.assertRaises(ProcessLookupError, msg=f"watcher {pid} survived {key} cleanup"):
                            os.kill(pid, 0)
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

    def test_callkit_in_the_built_app_stops_before_creating_any_device(self):
        result, calls = self.run_preparation(OTOOL_CALLKIT="1")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("Persona.app/Persona → /System/Library/Frameworks/CallKit.framework/CallKit",
                      result.stdout + result.stderr)
        self.assertEqual(len(calls), 1)
        app = self.output / "ios-tests/Build/Products/Debug-iphonesimulator/Persona.app"
        self.assertEqual((self.root / "otool-calls").read_text(), "-arch all -L %s\n" % (app / "Persona"))
        self.assertFalse((self.root / "calls").exists())

    def test_build_failure_stops_before_creating_any_device(self):
        result, calls = self.run_preparation(BUILD_STATUS="7")
        self.assertEqual(result.returncode, 7, result.stdout + result.stderr)
        self.assertEqual(len(calls), 1)
        self.assertFalse((self.root / "calls").exists())

    def test_probe_command_failure_stops_before_permissions_and_full_suite(self):
        result, calls = self.run_preparation(PROBE_STATUS="65")
        self.assertEqual(result.returncode, 65, result.stdout + result.stderr)
        self.assertEqual(len(calls), 2)
        self.assertFalse((self.root / "privacy-granted").exists())
        self.assertFalse((self.root / "full-suite-started").exists())

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
        for key, pid_name in (("PROBE_HANG", "probe.pid"), ("RESULT_HANG", "result.pid")):
            with self.subTest(phase=key):
                case = AppleReadinessTests()
                case.setUp()
                try:
                    result, _ = case.run_preparation(**{key: "1", "TEST_STEP_TIMEOUT": "1"})
                    self.assertEqual(result.returncode, 124, result.stdout + result.stderr)
                    self.assertFalse((case.root / "full-suite-started").exists())
                    self.assertTrue((case.root / pid_name).exists())
                    # The deadline terminates the probe/result process, not just its shell.
                    self.assertEqual((case.root / pid_name.replace(".pid", ".stopped")).read_text(), "terminated")
                finally:
                    case.doCleanups()

    def test_probe_leaving_device_booted_does_not_boot_it_again(self):
        result, calls = self.run_preparation(POST_PROBE_STATE="Booted")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(len(calls), 5)
        self.assertNotIn("simctl boot", (self.root / "calls").read_text())
        self.assertTrue((self.root / "privacy-granted").exists())

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

    def test_failed_start_prevents_permission_and_full_suite(self):
        self.fixture.state["boot_rc"] = 7
        self.fixture.save()
        result, calls = self.run_preparation()
        self.assertEqual(result.returncode, 7, result.stdout + result.stderr)
        self.assertEqual(len(calls), 2)
        self.assertFalse((self.root / "privacy-attempted").exists())
        self.assertFalse((self.root / "full-suite-started").exists())
        self.assertEqual((self.root / "calls").read_text().count("simctl boot "), 1)

    def test_permission_failure_is_not_retried_or_allowed_to_start_full_suite(self):
        result, calls = self.run_preparation(PRIVACY_STATUS="149")
        self.assertEqual(result.returncode, 149, result.stdout + result.stderr)
        self.assertEqual(len(calls), 2)
        self.assertTrue((self.root / "privacy-attempted").exists())
        self.assertFalse((self.root / "privacy-granted").exists())
        self.assertFalse((self.root / "full-suite-started").exists())
        self.assertEqual((self.root / "calls").read_text().count("simctl boot "), 1)

    def test_start_is_inside_the_existing_permission_deadline(self):
        result, calls = self.run_preparation(BOOT_HANG="1", TEST_STEP_TIMEOUT="1")
        self.assertEqual(result.returncode, 124, result.stdout + result.stderr)
        self.assertEqual(len(calls), 2)
        self.assertEqual((self.root / "boot.stopped").read_text(), "terminated")
        self.assertFalse((self.root / "privacy-attempted").exists())
        self.assertFalse((self.root / "full-suite-started").exists())
        self.assertEqual((self.root / "budgets").read_text().splitlines()[-1],
                         "Grant Reminders access to the owned test app|300")

    def test_full_suite_failure_is_not_masked_by_successful_readiness(self):
        result, calls = self.run_preparation(FULL_STATUS="7")
        self.assertEqual(result.returncode, 7, result.stdout + result.stderr)
        self.assertEqual(len(calls), 3)


if __name__ == "__main__":
    unittest.main()
