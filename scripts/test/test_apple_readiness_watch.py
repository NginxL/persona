"""Live readiness observation stays bounded, owned and separate from test verdicts."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
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
        self.assertEqual(watch.host_pids(UDID, lambda *args, **kwargs: result), [41])

    def test_process_list_failure_is_not_a_host_verdict(self):
        result = subprocess.CompletedProcess([], 1, "", "")
        self.assertEqual(watch.host_pids(UDID, lambda *args, **kwargs: result), [])


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
        (self.output / "simulator-readiness-context.json").write_text(json.dumps({
            "udid": UDID, "bundle_id": watch.launch.BUNDLE, "started_at": "2026-01-01T00:00:00+00:00"}))
        (self.output / "simulator.log").write_text(UDID + "\n")

    def test_observer_preserves_product_phase_and_final_host_state(self):
        ticks = iter([0, 1, 2])
        monotonic = lambda: next(ticks, 200)
        with mock.patch.object(watch, "product_evidence") as product, \
                mock.patch.object(watch, "sample_host", return_value=[]) as sample, \
                mock.patch.object(watch, "bounded_file", return_value=0) as bounded, \
                mock.patch.object(watch.launch, "collect") as collect:
            watch.observe(UDID, watch.launch.BUNDLE, self.app, self.test_bundle,
                          self.destination, 140, monotonic=monotonic, sleep=lambda _: None,
                          scope_root=self.output)
        product.assert_called_once()
        self.assertEqual([call.args[2] for call in sample.call_args_list], ["host-first", "host-final"])
        bounded.assert_called_once()
        collect.assert_called_once_with(self.output / "diagnostics", self.destination,
                                       now=mock.ANY, scope_root=self.output)
        self.assertIn("readiness-deadline", (self.destination / "observer.status.txt").read_text())

    def test_stop_before_xcode_cleanup_retains_host_sample(self):
        self.destination.mkdir(parents=True)
        (self.destination / "observer.stop").write_text("")
        with mock.patch.object(watch, "product_evidence"), \
                mock.patch.object(watch, "sample_host", return_value=[]) as sample, \
                mock.patch.object(watch, "bounded_file", return_value=0), \
                mock.patch.object(watch.launch, "collect"):
            watch.observe(UDID, watch.launch.BUNDLE, self.app, self.test_bundle, self.destination, 900)
        sample.assert_called_once_with(UDID, self.destination, "host-final", mock.ANY)
        self.assertIn("xcodebuild-finished", (self.destination / "observer.status.txt").read_text())

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
        ticks = iter([0, 1, 2, 3, 34])
        monotonic = lambda: next(ticks, 200)
        with mock.patch.object(watch, "product_evidence"), \
                mock.patch.object(watch, "sample_host", return_value=[41]) as sample, \
                mock.patch.object(watch, "bounded_file", return_value=0), \
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
            self.assertEqual(watch.bounded_file([sys.executable, "-c", "import signal; signal.pause()"],
                                               path, timeout=0.05), 124)
            self.assertEqual(path.with_suffix(".txt.status.txt").read_text(), "124\n")


if __name__ == "__main__":
    unittest.main()
