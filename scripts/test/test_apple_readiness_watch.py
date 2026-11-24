"""Live readiness observation stays bounded, owned and separate from test verdicts."""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts/ci"))
import apple_readiness_watch as watch

UDID = "11111111-1111-1111-1111-111111111111"
OTHER = "22222222-2222-2222-2222-222222222222"


class HostIdentityTests(unittest.TestCase):
    def test_exact_owned_host_path_only(self):
        output = "\n".join([
            f"  41 /Library/Developer/CoreSimulator/Devices/{UDID}/data/Containers/Bundle/Application/X/Persona.app/Persona",
            f"  42 /Library/Developer/CoreSimulator/Devices/{OTHER}/data/Containers/Bundle/Application/X/Persona.app/Persona",
            f"  43 /Library/Developer/CoreSimulator/Devices/{UDID}/data/Containers/Bundle/Application/X/Persona.app/PersonaTests",
            "  44 /tmp/Persona.app/Persona --private-argument",
            f"  45 /Library/Developer/CoreSimulator/Devices/{UDID}/data/Containers/Bundle/Application/X/Persona.app/Persona -argument",
        ])
        result = subprocess.CompletedProcess([], 0, output, "")
        observation = watch.host_pids(UDID, lambda *args, **kwargs: result)
        self.assertEqual(observation, {"query_status": "completed", "host_pids": [41]})

    def test_process_list_failure_timeout_and_empty_have_distinct_verdicts(self):
        cases = [
            (subprocess.CompletedProcess([], 0, "", ""), "completed"),
            (subprocess.CompletedProcess([], 1, "", ""), "query-failed"),
            (subprocess.TimeoutExpired("ps", 5), "query-timeout"),
            (OSError("synthetic private path"), "query-failed"),
        ]
        for result, status in cases:
            with self.subTest(status=status, result=type(result).__name__):
                def runner(*args, **kwargs):
                    if isinstance(result, Exception):
                        raise result
                    return result
                observation = watch.host_pids(UDID, runner)
                self.assertEqual(observation, {"query_status": status, "host_pids": []})
                self.assertNotIn("synthetic", str(observation))


class ObserverTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.output = self.root / "output"
        self.output.mkdir()
        self.destination = self.output / "diagnostics/live-readiness"
        self.app = self.root / "Persona.app/Persona"
        self.test_bundle = self.root / "Persona.app/PlugIns/PersonaTests.xctest/PersonaTests"
        self.empty = {"query_status": "completed", "host_pids": []}
        self.live = mock.Mock()
        (self.output / "simulator-readiness-context.json").write_text(json.dumps({
            "udid": UDID, "bundle_id": watch.launch.BUNDLE, "started_at": "2026-01-01T00:00:00+00:00"}))
        (self.output / "simulator.log").write_text(UDID + "\n")

    def test_observer_preserves_product_phase_and_final_host_state(self):
        ticks = iter([0, 1, 2])
        monotonic = lambda: next(ticks, 200)
        events = []
        with mock.patch.object(watch, "product_evidence",
                               side_effect=lambda *args, **kwargs: events.append("product")) as product, \
                mock.patch.object(watch, "sample_host",
                                  side_effect=lambda *args: events.append("host") or self.empty) as sample, \
                mock.patch.object(watch, "bounded_file", return_value=0) as bounded, \
                mock.patch.object(watch, "start_live_log", return_value=self.live), \
                mock.patch.object(watch, "stop_live_log"), \
                mock.patch.object(watch.launch, "collect") as collect:
            watch.observe(UDID, watch.launch.BUNDLE, self.app, self.test_bundle,
                          self.destination, 140, monotonic=monotonic, sleep=lambda _: None,
                          scope_root=self.output)
        product.assert_called_once()
        self.assertEqual([call.args[2] for call in sample.call_args_list], ["host-first", "host-final"])
        self.assertEqual(events[0], "host")
        bounded.assert_called_once()
        collect.assert_called_once_with(self.output / "diagnostics", self.destination,
                                       now=mock.ANY, scope_root=self.output)
        self.assertIn("readiness-deadline", (self.destination / "observer.status.txt").read_text())

    def test_stop_before_xcode_cleanup_retains_host_sample(self):
        self.destination.mkdir(parents=True)
        (self.destination / "observer.stop").write_text("")
        with mock.patch.object(watch, "product_evidence"), \
                mock.patch.object(watch, "sample_host", return_value=self.empty) as sample, \
                mock.patch.object(watch, "bounded_file", return_value=0), \
                mock.patch.object(watch, "start_live_log", return_value=self.live) as start, \
                mock.patch.object(watch, "stop_live_log") as stop, \
                mock.patch.object(watch.launch, "collect"):
            watch.observe(UDID, watch.launch.BUNDLE, self.app, self.test_bundle, self.destination, 900)
        sample.assert_called_once_with(UDID, self.destination, "host-final", mock.ANY)
        start.assert_not_called()
        self.assertTrue(all(call.args == (None,) for call in stop.call_args_list))
        self.assertIn("xcodebuild-finished", (self.destination / "observer.status.txt").read_text())

    def test_unavailable_live_stream_does_not_skip_final_collection(self):
        with mock.patch.object(watch, "product_evidence"), \
                mock.patch.object(watch, "sample_host", return_value=self.empty) as sample, \
                mock.patch.object(watch, "bounded_file", return_value=0), \
                mock.patch.object(watch.launch, "start_live_log", side_effect=OSError("synthetic startup failure")), \
                mock.patch.object(watch.launch, "collect") as collect:
            ticks = iter([0, 1, 2])
            watch.observe(UDID, watch.launch.BUNDLE, self.app, self.test_bundle,
                          self.destination, 140, monotonic=lambda: next(ticks, 200),
                          sleep=lambda _: None, scope_root=self.output)
        collect.assert_called_once()
        self.assertEqual([call.args[2] for call in sample.call_args_list], ["host-first", "host-final"])
        self.assertIn("live-log-startup-failure", (self.destination / "live-launch.status.txt").read_text())

    def test_stop_failure_keeps_final_host_and_original_final_collector(self):
        ticks = iter([0, 1, 2])
        live = mock.Mock()
        live.stop.side_effect = TimeoutError("synthetic stop timeout")
        with mock.patch.object(watch, "product_evidence"), \
                mock.patch.object(watch, "sample_host", return_value={"query_status": "completed", "host_pids": [41]}), \
                mock.patch.object(watch, "bounded_file", return_value=0), \
                mock.patch.object(watch, "start_live_log", return_value=live), \
                mock.patch.object(watch.launch, "collect") as collect:
            watch.observe(UDID, watch.launch.BUNDLE, self.app, self.test_bundle,
                          self.destination, 140, monotonic=lambda: next(ticks, 200),
                          sleep=lambda _: None, scope_root=self.output)
        live.correlate.assert_called_with([41])
        live.fail.assert_called_once_with("stop-failure")
        collect.assert_called_once()
        self.assertIn("readiness-deadline", (self.destination / "observer.status.txt").read_text())

    def test_final_host_pids_are_correlated_before_stream_stop(self):
        ticks = iter([0, 1, 2])
        events = []
        live = mock.Mock()
        live.correlate.side_effect = lambda pids: events.append(("correlate", pids))
        live.stop.side_effect = lambda: events.append(("stop",))
        with mock.patch.object(watch, "product_evidence"), \
                mock.patch.object(watch, "sample_host",
                                  side_effect=[self.empty, {"query_status": "completed", "host_pids": [99]}]), \
                mock.patch.object(watch, "bounded_file", return_value=0), \
                mock.patch.object(watch, "start_live_log", return_value=live), \
                mock.patch.object(watch.launch, "collect"):
            watch.observe(UDID, watch.launch.BUNDLE, self.app, self.test_bundle,
                          self.destination, 140, monotonic=lambda: next(ticks, 200),
                          sleep=lambda _: None, scope_root=self.output)
        self.assertEqual(events, [("correlate", [99]), ("stop",)])

    def test_live_scope_failure_returns_unavailable_without_a_process(self):
        self.destination.mkdir(parents=True)
        (self.output / watch.launch.CONTEXT).unlink()
        live = watch.start_live_log(self.output, self.destination)
        self.assertIsNone(live)
        self.assertIn("live-log-scope-context-fields",
                      (self.destination / "live-launch.status.txt").read_text())

    def test_live_programmer_error_is_not_relabelled_as_unavailable(self):
        with mock.patch.object(watch.launch, "start_live_log", side_effect=TypeError("programmer error")):
            with self.assertRaisesRegex(TypeError, "programmer error"):
                watch.start_live_log(self.output, self.destination)

    def test_sample_host_publishes_query_status_and_samples_only_returned_pids(self):
        result = subprocess.CompletedProcess([], 0, f"41 /Library/Developer/CoreSimulator/Devices/{UDID}/X/Persona.app/Persona\n", "")
        with mock.patch.object(watch, "bounded_file", return_value=0) as bounded:
            observation = watch.sample_host(UDID, self.destination, "host-first", lambda *args, **kwargs: result)
        self.assertEqual(observation["query_status"], "completed")
        self.assertEqual(observation["host_pids"], [41])
        bounded.assert_called_once()
        record = json.loads((self.destination / "host-first.json").read_text())
        self.assertEqual(record["query_status"], "completed")
        self.assertEqual(record["host_pids"], [41])

    def test_product_evidence_checks_app_bundle_and_executable_separately(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            app = root / "Persona.app/Persona"
            tests = root / "Persona.app/PlugIns/PersonaTests.xctest/PersonaTests"
            calls = []
            def record(command, path, **kwargs):
                calls.append(command)
                return 0
            with mock.patch.object(watch, "bounded_file", side_effect=record):
                watch.product_evidence(app, tests, root)
            self.assertEqual(calls.count(["codesign", "--verify", "--verbose=2", str(app.parent)]), 1)
            self.assertEqual(calls.count(["codesign", "--verify", "--verbose=2", str(tests.parent)]), 1)
            for path in (app, tests, app.parent / "Frameworks/libXCTestBundleInject.dylib"):
                self.assertIn(["file", str(path)], calls)
                self.assertIn(["otool", "-L", str(path)], calls)
            self.assertFalse(any(command[0] == "codesign" and command[-1] == str(app) for command in calls))

    def test_second_sample_distinguishes_persistent_host_wait(self):
        ticks = iter([0, 1, 2, 3, 35, 36])
        monotonic = lambda: next(ticks, 200)
        with mock.patch.object(watch, "product_evidence"), \
                mock.patch.object(watch, "sample_host", return_value={"query_status": "completed", "host_pids": [41]}) as sample, \
                mock.patch.object(watch, "bounded_file", return_value=0), \
                mock.patch.object(watch, "start_live_log", return_value=self.live), \
                mock.patch.object(watch, "stop_live_log"), \
                mock.patch.object(watch.launch, "collect"):
            watch.observe(UDID, watch.launch.BUNDLE, self.app, self.test_bundle,
                          self.destination, 140, monotonic=monotonic, sleep=lambda _: None,
                          scope_root=self.output)
        self.assertEqual([call.args[2] for call in sample.call_args_list],
                         ["host-first", "host-after-30s", "host-final"])


class BoundedCommandTests(unittest.TestCase):
    def test_bounded_file_keeps_exit_and_timeout_and_never_passes_output_as_json(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / "output.txt"
            self.assertEqual(watch.bounded_file([sys.executable, "-c", "print('bounded')"], path), 0)
            self.assertEqual(path.read_text(), "bounded\n")
            path.unlink()
            command = "import signal; signal.signal(signal.SIGTERM, signal.SIG_IGN); signal.pause()"
            self.assertEqual(watch.bounded_file([sys.executable, "-c", command], path, timeout=0.05), 124)
            self.assertEqual(path.read_text(), "unavailable: command timed out\n")
            self.assertEqual(path.with_suffix(".txt.status.txt").read_text(), "124\n")

    def test_bounded_command_shares_the_observer_group(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "output.txt"
            command = ("import os, signal; "
                       "open(os.environ['WATCH_TEST_PID'], 'w').write(str(os.getpid()) + ' ' + str(os.getpgid(0))); "
                       "signal.signal(signal.SIGTERM, signal.SIG_IGN); signal.pause()")
            pid_path = Path(directory) / "pid"
            original = watch.subprocess.Popen
            captured = {}
            def record(*args, **kwargs):
                process = original(*args, **kwargs)
                captured["process"] = process
                captured["kwargs"] = kwargs
                return process
            with mock.patch.dict(os.environ, {"WATCH_TEST_PID": str(pid_path)}), \
                    mock.patch.object(watch.subprocess, "Popen", side_effect=record):
                self.assertEqual(watch.bounded_file([sys.executable, "-c", command], path, timeout=0.1), 124)
            process = captured["process"]
            self.assertNotIn("start_new_session", captured["kwargs"])
            self.assertEqual(process.wait(timeout=1), -9)
            pid, pgid = map(int, pid_path.read_text().split())
            self.assertEqual(pid, process.pid)
            self.assertEqual(pgid, os.getpgid(0))
            self.assertEqual(path.read_text(), "unavailable: command timed out\n")
            self.assertEqual(path.with_suffix(".txt.status.txt").read_text(), "124\n")

    def test_interrupted_collection_has_explicit_atomic_incomplete_status(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            destination = root / "live-readiness"
            app = root / "Persona.app/Persona"
            tests = root / "Persona.app/PlugIns/PersonaTests.xctest/PersonaTests"
            with mock.patch.object(watch, "product_evidence", side_effect=KeyboardInterrupt), \
                    mock.patch.object(watch, "sample_host", side_effect=KeyboardInterrupt), \
                    mock.patch.object(watch, "start_live_log"):
                with self.assertRaises(KeyboardInterrupt):
                    watch.observe(UDID, watch.launch.BUNDLE, app, tests, destination, 900, scope_root=root)
            self.assertTrue((destination / "observer.status.txt").read_text().startswith("collection incomplete;"))
            path = destination / "product-0-architecture.txt"
            self.assertEqual(path.read_text(), "unavailable: product collection incomplete\n")
            self.assertEqual(path.with_suffix(".txt.status.txt").read_text(), "incomplete\n")


if __name__ == "__main__":
    unittest.main()
