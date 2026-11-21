"""Compiler resource observation must not alter the build or export its arguments."""
from pathlib import Path
import io
import json
import os
import shlex
import subprocess
import sys
import tempfile
import textwrap
import time
import unittest
from unittest import mock
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts/ci"))
import apple_build_watch as watch


class CompilerResourceTests(unittest.TestCase):
    def test_only_compiler_names_and_resource_counts_are_recorded(self):
        result = subprocess.CompletedProcess([], 0, "\n".join([
            "  10  2 80.5 123456 /Toolchain/usr/bin/swift-frontend",
            "  11  2  2.0   4000 /Toolchain/usr/bin/swiftc",
            "  12 10  0.0    900 /usr/bin/xcodebuild",
            "  13  2  0.0    800 /usr/bin/clang",
        ]), "")
        parsed = watch.processes(lambda *args, **kwargs: result)
        self.assertEqual(parsed["counts"], {"swift-frontend": 1, "swiftc": 1, "xcodebuild": 1, "clang": 1})
        self.assertEqual(parsed["total_rss_kib"], 129156)
        self.assertNotIn("command", json.dumps(parsed["largest"]))

    def test_process_summary_keeps_the_eight_largest_compiler_entries(self):
        result = subprocess.CompletedProcess([], 0, "\n".join(
            f"  {pid}  2 1.0 {pid * 1000} /Toolchain/usr/bin/swift-frontend" for pid in range(10, 22)), "")
        parsed = watch.processes(lambda *args, **kwargs: result)
        self.assertEqual(watch.MAX_PROCESSES, 8)
        self.assertEqual(parsed["counts"], {"swift-frontend": 12})
        self.assertEqual(len(parsed["largest"]), 8)
        self.assertEqual([entry["pid"] for entry in parsed["largest"]], list(range(21, 13, -1)))

    def test_bounded_command_rejects_rather_than_truncates_evidence(self):
        result = subprocess.CompletedProcess([], 0, "x" * (watch.MAX_OUTPUT_BYTES + 1), "")
        self.assertEqual(watch.output(["ps"], runner=lambda *args, **kwargs: result),
                         {"command": ["ps"], "unavailable": "output-limit"})

    def test_failures_are_explicit_and_do_not_invent_resource_verdicts(self):
        def unavailable(*args, **kwargs):
            raise OSError("unavailable")
        metadata = watch.metadata(unavailable)
        self.assertTrue(all("unavailable" in item for item in metadata.values()))
        self.assertEqual(watch.processes(unavailable), {"counts": {}, "total_rss_kib": 0, "largest": []})

    def test_observer_stops_and_retains_final_sample(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stop = root / "stop"
            stop.write_text("")
            with mock.patch.object(watch, "metadata", return_value={}), \
                    mock.patch.object(watch, "sample") as sample:
                watch.observe(root, 1800, stop, monotonic=lambda: 0, sleep=lambda _: None)
            sample.assert_called_once_with(root, "final", mock.ANY, final=True)
            self.assertIn("periodic samples", (root / "observer.status.txt").read_text())

    def test_periodic_samples_stop_at_120_and_retain_only_a_bounded_final(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with mock.patch.object(watch, "metadata", return_value={}), \
                    mock.patch.object(watch, "sample") as sample:
                watch.observe(root, 3600, root / "stop", monotonic=lambda: 0, sleep=lambda _: None)
            self.assertEqual(sample.call_count, 121)
            self.assertEqual(sample.call_args_list[-1], mock.call(root, "final", mock.ANY, final=True))
            self.assertTrue(all(call.args[1] == f"sample-{index:03d}"
                                for index, call in enumerate(sample.call_args_list[:-1])))
            self.assertTrue((root / "observer.status.txt").read_text().startswith("120 periodic samples;"))

    def test_samples_are_compact_and_count_bounded(self):
        def fake_output(command, **kwargs):
            if command == ["memory_pressure"]:
                return subprocess.CompletedProcess(command, 0, "Stats:\nSystem-wide memory free percentage: 4%\n", "")
            if command == ["vm_stat"]:
                return subprocess.CompletedProcess(command, 0, "Pages occupied by compressor: 123456.\n", "")
            if command == ["sysctl", "vm.swapusage"]:
                return subprocess.CompletedProcess(command, 0, "vm.swapusage: total = 1024M used = 900M free = 124M\n", "")
            return subprocess.CompletedProcess(command, 0, "", "")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            record = watch.sample(root, "sample", runner=fake_output)
            self.assertEqual(record["memory_free_percent"], "System-wide memory free percentage: 4%")
            self.assertEqual(record["pages_used_by_compressor"], "Pages occupied by compressor: 123456.")
            self.assertNotIn("memory", record)
            self.assertLess((root / "sample.json").stat().st_size, 1024)
            self.assertLessEqual(watch.MAX_PERIODIC_SAMPLES, 120)
            self.assertLessEqual(watch.MAX_OUTPUT_BYTES, 64 * 1024)

    def test_observer_deadline_accepts_both_lane_budgets_and_rejects_larger_values(self):
        self.assertEqual(watch.MAX_SECONDS, 3600)
        for seconds in (1800, 3600):
            with self.subTest(seconds=seconds), \
                    patch.object(watch, "observe") as observe, \
                    patch.object(sys, "argv", ["apple_build_watch.py", "--output", "/tmp/output",
                                               "--stop", "/tmp/stop", "--seconds", str(seconds)]), \
                    patch.object(sys, "stderr", new=io.StringIO()):
                watch.main()
                observe.assert_called_once_with(Path("/tmp/output"), seconds, Path("/tmp/stop"))
        for seconds in (0, 3601):
            with self.subTest(seconds=seconds), \
                    patch.object(sys, "argv", ["apple_build_watch.py", "--output", "/tmp/output",
                                               "--stop", "/tmp/stop", "--seconds", str(seconds)]), \
                    patch.object(sys, "stderr", new=io.StringIO()), \
                    self.assertRaisesRegex(SystemExit, "2"):
                watch.main()

    def test_timeout_reaps_a_detached_watcher_and_its_ignoring_collector(self):
        source = (ROOT / "scripts/ci/apple.sh").read_text()
        start = source.index("ci_run 'Build iOS offline unit tests'")
        block = source[start:source.index("ci_run 'No CallKit in the simulator build'", start)]
        block = block.replace('ci_run \'Build iOS offline unit tests\' "$BUILD_SECONDS" bash -c',
                              "ci_run 'Build iOS offline unit tests' 1 bash -c", 1)
        with tempfile.TemporaryDirectory(prefix="apple-watch-hard-timeout-") as directory:
            fixture = Path(directory)
            tools = fixture / "tools"
            tools.mkdir()
            watcher = fixture / "detached_watcher.py"
            watcher.write_text(textwrap.dedent(f'''\
                #!{sys.executable}
                from pathlib import Path
                import os
                import signal
                import sys
                if sys.argv[1].endswith("run.py"):
                    os.execv(sys.executable, [sys.executable, *sys.argv[1:]])
                os.setsid()
                signal.signal(signal.SIGTERM, signal.SIG_IGN)
                Path({str(fixture / "watcher.pid")!r}).write_text(str(os.getpid()))
                Path({str(fixture / "collector.pid")!r}).write_text(str(os.getpid() + 1))
                signal.pause()
            '''))
            watcher.chmod(0o755)
            (tools / "python3").symlink_to(watcher)
            xcodebuild = tools / "xcodebuild"
            xcodebuild.write_text("#!/bin/bash\nsleep 30\n")
            xcodebuild.chmod(0o755)
            command = '''set -euo pipefail
            ci_run() {
              local label="$1" budget="$2"; shift 2
              python3 "$CI_ROOT/scripts/ci/run.py" --label "$label" --timeout "$budget" -- "$@"
            }
            CI_ROOT=''' + shlex.quote(str(ROOT)) + '''
            OUTPUT=''' + shlex.quote(str(fixture / "output")) + '''
            IOS_COMMON=(xcodebuild)
            IOS_UNIT_SELECTION=(-only-testing:PersonaTests)
            BUILD_SECONDS=1800
            ''' + block
            outer = subprocess.Popen(["/bin/bash", "-c", command], cwd=ROOT,
                                     env={**os.environ, "PATH": str(tools) + ":/usr/bin:/bin"},
                                     text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                     start_new_session=True)
            started = time.monotonic()
            try:
                output, _ = outer.communicate(timeout=20)
                elapsed = time.monotonic() - started
                self.assertEqual(outer.returncode, 124, output)
                self.assertLess(elapsed, 15, output)
                self.assertIn("TIMEOUT Build iOS offline unit tests", output)
                self.assertIn("exit 124", output)
                self.assertNotIn("Traceback", output)
            except subprocess.TimeoutExpired as error:
                self.fail("run.py returned 124 but the wrapper still waited for the watcher after 20 seconds: "
                          + str(error.stdout or ""))
            finally:
                if outer.poll() is None:
                    os.killpg(outer.pid, 9)
                    outer.wait(timeout=5)
            resource_dir = fixture / "output/diagnostics/build-resources"
            self.assertTrue((resource_dir / "final.json").is_file())
            self.assertTrue((resource_dir / "observer.status.txt").is_file())
            final = json.loads((resource_dir / "final.json").read_text())
            self.assertLess((resource_dir / "final.json").stat().st_size, 2048)
            self.assertIn("unavailable", final)
            self.assertNotIn("command", json.dumps(final.get("processes", {})))
            for name in ("watcher.pid", "collector.pid"):
                pid_path = fixture / name
                self.assertTrue(pid_path.is_file(), f"fixture did not create {name}")
                pid = int(pid_path.read_text())
                with self.assertRaises(ProcessLookupError, msg=f"{name} survived cleanup"):
                    os.kill(pid, 0)

    def test_every_lane_scope_and_budget_reaches_the_same_build_and_observer(self):
        source = (ROOT / "scripts/ci/apple.sh").read_text()
        start = source.index('if [ "$LANE" != packages ] && [ "$LANE" != ios-build ]; then')
        end = source.index("ci_run 'No CallKit in the simulator build'", start)
        block = source[start:end]
        self.assertIn('ci_run \'Build iOS offline unit tests\' "$BUILD_SECONDS" bash -c', block)
        expected_build = {
            "all": 1800,
            "ios": 1800,
            "ios18": 3600,
            "packages": None,
            "ios-build": None,
        }
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory)
            (fixture / "python3").write_text(textwrap.dedent('''\
                #!/bin/bash
                if [[ "$*" == *apple_build_watch.py* ]]; then
                  printf '%s\\n' "$*" >> "$FIXTURE_WATCH_ARGS"
                  exit 0
                fi
                exec /usr/bin/python3 "$@"
            '''))
            (fixture / "python3").chmod(0o755)
            (fixture / "xcodebuild").write_text(textwrap.dedent('''\
                #!/bin/bash
                printf '%s\\n' "$@" > "$FIXTURE_XCODE_ARGS"
            '''))
            (fixture / "xcodebuild").chmod(0o755)
            for lane, budget in expected_build.items():
                with self.subTest(lane=lane):
                    output = fixture / ("output " + lane)
                    watch_args = fixture / ("watch-" + lane)
                    xcode_args = fixture / ("xcode-" + lane)
                    calls = fixture / ("calls-" + lane)
                    env = {**os.environ, "PATH": str(fixture) + ":/usr/bin:/bin",
                           "FIXTURE_WATCH_ARGS": str(watch_args), "FIXTURE_XCODE_ARGS": str(xcode_args),
                           "FIXTURE_CALLS": str(calls)}
                    prefix = '''set -euo pipefail
                    ci_run() {
                      local label="$1" budget="$2"; shift 2
                      printf '%s\\n' "$budget" >> "$FIXTURE_CALLS"
                      /usr/bin/python3 "$CI_ROOT/scripts/ci/run.py" --label "$label" --timeout "$budget" -- "$@"
                    }
                    CI_ROOT=''' + shlex.quote(str(ROOT)) + '''
                    OUTPUT=''' + shlex.quote(str(output)) + '''
                    XCODE=(xcodebuild -project /fixture/Persona.xcodeproj -configuration Debug
                      -clonedSourcePackagesDirPath /fixture/cache)
                    TEST_SIGNING=(CODE_SIGNING_ALLOWED=YES CODE_SIGN_IDENTITY=- CODE_SIGN_STYLE=Manual
                      DEVELOPMENT_TEAM= PROVISIONING_PROFILE_SPECIFIER=)
                    LANE=''' + lane + "\n"
                    result = subprocess.run(["/bin/bash", "-c", prefix + block + "\nfi\n"], cwd=ROOT, env=env,
                                            capture_output=True, text=True, timeout=15)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    if budget is None:
                        self.assertFalse(calls.exists())
                        self.assertFalse(watch_args.exists())
                        self.assertFalse(xcode_args.exists())
                        self.assertFalse(output.exists())
                        continue
                    self.assertEqual(calls.read_text(), str(budget) + "\n")
                    self.assertIn(f"--seconds {budget}", watch_args.read_text())
                    expected_args = [
                        "-project", "/fixture/Persona.xcodeproj",
                        "-configuration", "Debug",
                        "-clonedSourcePackagesDirPath", "/fixture/cache",
                        "-scheme", "PersonaOfflineUnitTests",
                        "-derivedDataPath", str(output / "ios-tests"),
                        "-parallel-testing-enabled", "NO",
                        "-disableAutomaticPackageResolution",
                        "CODE_SIGNING_ALLOWED=YES", "CODE_SIGN_IDENTITY=-", "CODE_SIGN_STYLE=Manual",
                        "DEVELOPMENT_TEAM=", "PROVISIONING_PROFILE_SPECIFIER=",
                        "-destination", "generic/platform=iOS Simulator",
                        "ARCHS=" + os.uname().machine, "ONLY_ACTIVE_ARCH=YES",
                        "-skip-testing:PersonaTests/OldHubCompatibilityIntegrationTests",
                        "-skip-testing:PersonaTests/NotificationServiceTests/opensWhatTheRelayDelivered()",
                        "-skip-testing:PersonaTests/VideoFileTests/nativeVideoPlaysSeeksAndCleansUp()",
                        "-skip-testing:PersonaTests/VideoFileResourceTests/realHTTPReadsMiddleTailAndRejectsChangedRevision()",
                        "-only-testing:PersonaTests",
                        "-skip-testing:PersonaTests/AboutVersionRegularVisualTests",
                        "-skip-testing:PersonaTests/AssistantHTMLCardTests",
                        "-skip-testing:PersonaTests/AssistantHTMLSecurityTests",
                        "build-for-testing",
                    ]
                    self.assertEqual(xcode_args.read_text().splitlines(), expected_args)
                    self.assertTrue((output / "diagnostics/build-resources/stop").is_file())
                    self.assertNotIn("unbound variable", result.stdout + result.stderr)
        self.assertIn('if [ "$LANE" != packages ] && [ "$LANE" != ios-build ]; then', block)
        self.assertIn('ci_root="$1"; output="$2"; seconds="$3"; shift 3', block)
        self.assertIn('\' -- "$CI_ROOT" "$OUTPUT" "$BUILD_SECONDS" "${IOS_COMMON[@]}"', block)
        self.assertIn('"$@"\n', block)
        self.assertIn("-destination 'generic/platform=iOS Simulator'", block)
        self.assertIn('ARCHS="$(uname -m)" ONLY_ACTIVE_ARCH=YES', block)
        self.assertIn('"${IOS_UNIT_SELECTION[@]}" build-for-testing', block)
        self.assertIn('wait "$watcher" || true', block)
        self.assertNotIn("xcodebuild -jobs", block)
        self.assertNotIn("SWIFT_", block)


if __name__ == "__main__":
    unittest.main()
