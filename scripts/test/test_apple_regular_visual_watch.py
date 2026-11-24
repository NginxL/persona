"""The regular visual observer stays scoped, read-only and inside the existing deadline."""

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts/ci"))
import apple_regular_visual_watch as visual

UDID = "11111111-1111-1111-1111-111111111111"
OTHER = "22222222-2222-2222-1111-111111111111"


class ProcessScopeTests(unittest.TestCase):
    def test_only_owned_host_and_its_debugger_have_public_metadata(self):
        owned = f"/Library/Developer/CoreSimulator/Devices/{UDID}/Application/X/Persona.app/Persona"
        foreign = owned.replace(UDID, OTHER)
        result = subprocess.CompletedProcess([], 0, "\n".join([
            f" 41 1 Ss {owned}", " 42 41 S /usr/libexec/debugserver",
            f" 43 999 S /usr/libexec/debugserver", f" 44 1 Ss {foreign}",
            f" 45 1 S /private/path/Persona --private-argument",
            f" 46 1 Ss /Library/Developer/CoreSimulator/Devices/{UDID}/ --some-launchd-argument launchd_sim",
        ]), "")
        observation = visual.scoped_processes(UDID, lambda *args, **kwargs: result)
        self.assertEqual(observation, {"query_status": "completed", "processes": [
            {"pid": 41, "ppid": 1, "state": "Ss", "name": "Persona"},
            {"pid": 42, "ppid": 41, "state": "S", "name": "debugserver"},
        ]})
        self.assertNotIn("/private", str(observation))
        self.assertNotIn(OTHER, str(observation))

    def test_owned_launchd_debugger_before_host_is_retained_across_samples(self):
        owned = f"/Library/Developer/CoreSimulator/Devices/{UDID}/Application/X/Persona.app/Persona"
        launcher = f"/Library/Developer/CoreSimulator/Devices/{UDID}/ --launchd /usr/sbin/launchd_sim"
        foreign_launcher = launcher.replace(UDID, OTHER)
        snapshots = iter([
            "\n".join([f"46 1 Ss {launcher}", "42 46 S /usr/libexec/debugserver",
                       f"47 1 Ss {foreign_launcher}", "43 47 S /usr/libexec/debugserver"]),
            f"41 46 Ss {owned}",
            f"41 1 Ss {owned}\n42 1 S /usr/libexec/debugserver",
        ])
        def runner(*args, **kwargs):
            return subprocess.CompletedProcess([], 0, next(snapshots), "")
        owned_debuggers = set()
        first = visual.scoped_processes(UDID, runner, owned_debuggers)
        second = visual.scoped_processes(UDID, runner, owned_debuggers)
        third = visual.scoped_processes(UDID, runner, owned_debuggers)
        self.assertEqual(first["processes"], [{"pid": 42, "ppid": 46, "state": "S", "name": "debugserver"}])
        self.assertEqual(second["processes"], [{"pid": 41, "ppid": 46, "state": "Ss", "name": "Persona"},
                                               {"pid": 42, "state": "not-present", "name": "debugserver"}])
        self.assertIn({"pid": 42, "ppid": 1, "state": "S", "name": "debugserver"}, third["processes"])
        self.assertEqual(owned_debuggers, {42})
        self.assertNotIn("43", str(first) + str(second) + str(third))

    def test_process_query_failures_remain_distinct(self):
        for error, status in [(subprocess.TimeoutExpired("ps", 5), "query-timeout"),
                              (OSError("private"), "query-failed")]:
            with self.subTest(status=status):
                with mock.patch.object(visual.watch.subprocess, "run", side_effect=error):
                    self.assertEqual(visual.scoped_processes(UDID),
                                     {"query_status": status, "processes": []})


class VisualObserverTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.output = Path(self.tmp.name)
        self.destination = self.output / "diagnostics/live-regular-visual"
        self.live = mock.Mock()

    def test_stream_starts_before_first_sample_and_stop_publishes_no_cause(self):
        self.destination.mkdir(parents=True)
        (self.destination / "observer.stop").write_text("")
        with mock.patch.object(visual.watch, "start_live_log") as start, \
                mock.patch.object(visual, "sample_processes") as sample, \
                mock.patch.object(visual, "launch_system") as system, \
                mock.patch.object(visual.watch, "stop_live_log") as stop, \
                mock.patch.object(visual.launch, "collect") as collect:
            visual.observe(UDID, visual.launch.BUNDLE, self.destination, scope_root=self.output)
        start.assert_not_called()
        sample.assert_not_called()
        system.assert_not_called()
        collect.assert_called_once()
        stop.assert_called_once_with(None)
        self.assertIn("xcodebuild-finished", (self.destination / "observer.status.txt").read_text())
        self.assertIn("no launch cause established", (self.destination / "observer.status.txt").read_text())

    def test_samples_at_zero_and_fifteen_seconds_and_queries_system_once_at_sixty(self):
        events = []
        clock = mock.Mock(side_effect=[0, 0, 0, 14, 15, 15, 59, 60, 60, 1000, 1000, 1000])
        def record_sample(_udid, _destination, index, **_kwargs):
            events.append(("sample", index))
            return {"query_status": "completed", "processes": []}
        with mock.patch.object(visual.watch, "start_live_log", return_value=self.live), \
                mock.patch.object(visual, "sample_processes", side_effect=record_sample), \
                mock.patch.object(visual, "launch_system", side_effect=lambda *args: events.append(("system",))) as system, \
                mock.patch.object(visual.watch, "stop_live_log"), \
                mock.patch.object(visual.launch, "collect"):
            visual.observe(UDID, visual.launch.BUNDLE, self.destination, scope_root=self.output,
                           monotonic=clock, sleep=lambda _: None)
        self.assertEqual(events, [("sample", 0), ("sample", 1), ("sample", 2), ("system",)])
        system.assert_called_once_with(UDID, self.destination)
        self.assertIn("visual-deadline", (self.destination / "observer.status.txt").read_text())

    def test_observer_rejects_a_lifetime_beyond_the_visual_step(self):
        with self.assertRaises(ValueError):
            visual.observe(UDID, visual.launch.BUNDLE, self.destination, visual.VISUAL_SECONDS + 1)

    def test_visual_live_scope_has_its_own_invocation_start_and_six_hundred_second_horizon(self):
        from datetime import datetime, timedelta, timezone
        current = datetime(2026, 1, 1, 0, 10, tzinfo=timezone.utc)
        (self.output / "regular-visual-context.json").write_text(json.dumps({
            "udid": UDID, "bundle_id": visual.launch.BUNDLE,
            "started_at": "2026-01-01T00:10:00+00:00"}))
        (self.output / "regular-readiness-context.json").write_text(json.dumps({
            "udid": UDID, "bundle_id": visual.launch.BUNDLE,
            "started_at": "2026-01-01T00:00:00+00:00"}))
        (self.output / "regular-simulator.log").write_text(UDID + "\n")
        context, start, _ = visual.launch.observation_scope(
            self.output, current, visual=True, seconds=600, allow_current_start=True)
        self.assertEqual(context["udid"], UDID)
        self.assertEqual(start, current)
        self.assertEqual(start + timedelta(seconds=600), current + timedelta(seconds=600))
        with mock.patch.object(visual.launch, "LiveLog") as live_type, \
                mock.patch.object(visual.launch.os, "getpgid", side_effect=[999, 1234]), \
                mock.patch.object(visual.launch.subprocess, "Popen") as popen:
            live_type.return_value.command = ["log", "stream"]
            live_type.return_value.process.pid = 1234
            popen.return_value.pid = 5678
            visual.launch.start_live_log(self.output, self.destination, now=lambda: current)
        self.assertEqual(live_type.call_args.args[2], current + timedelta(seconds=600))

    def test_launchctl_exports_only_scoped_service_identity(self):
        self.destination.mkdir(parents=True)
        real_popen = subprocess.Popen
        def fake_popen(command, **kwargs):
            self.assertEqual(command[:4], ["xcrun", "simctl", "spawn", UDID])
            return real_popen([
                sys.executable, "-c",
                "print('system'); "
                "print('  program = /unrelated/tool --private-system-argument'); "
                "print('unrelated.example'); print('  state = running'); "
                "print('org.example.persona.ci'); print('  state = waiting'); print('  pid = 41'); "
                "print('  runs = 1'); print('  last exit code = 0'); "
                "print('  path = /private/container/Persona.app'); "
                "print('  program = /path/tool org.example.persona.ci --unsafe-private-argument'); "
                "print('  arguments = {'); print('    --credential-without-keyword'); print('  }'); "
                "print('  environment = {'); print('    ORG_EXAMPLE_PERSONA_CI=unsafe-private-value'); print('  }'); "
                "print('other.example'); print('  state = running'); "
                "print('  program = other.example --other-private-argument')"
            ], stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        with mock.patch.object(visual.subprocess, "Popen", side_effect=fake_popen):
            self.assertEqual(visual.launch_system(UDID, self.destination), 0)
        exported = (self.destination / "launch-system-after-60s.txt").read_text()
        self.assertEqual(exported, "org.example.persona.ci\nstate = waiting\npid = 41\nruns = 1\nlast exit code = 0\n")
        for private in ("unsafe-private", "credential-without-keyword", "/private/container", "other-private", "environment"):
            self.assertNotIn(private, exported)

    def test_launchctl_rejects_arbitrary_values_in_allowlisted_fields(self):
        for raw in (f"{visual.launch.BUNDLE}\n state = running --private-argument\n",
                    f"{visual.launch.BUNDLE}\n pid = 41;unsafe-private-value\n",
                    f"{visual.launch.BUNDLE}\n program = {visual.launch.BUNDLE} --unsafe-private-argument\n"):
            self.assertEqual(visual.launch_system_projection(raw.encode(), visual.launch.BUNDLE),
                             [visual.launch.BUNDLE + "\n"])
        raw = f"{visual.launch.BUNDLE}\n state = running\nother.example\n state = running\n"
        self.assertEqual(visual.launch_system_projection(raw.encode(), visual.launch.BUNDLE),
                         [visual.launch.BUNDLE + "\n", "state = running\n"])
        self.assertEqual(visual.launch_system_projection(b"\xff", visual.launch.BUNDLE), [])

    def test_cli_converts_the_output_to_a_path_before_observing(self):
        with mock.patch.object(sys, "argv", ["apple_regular_visual_watch.py", "--udid", UDID,
                                             "--bundle-id", visual.launch.BUNDLE, "--scope-root", str(self.output),
                                             "--output", str(self.destination), "--seconds", "600"]), \
                mock.patch.object(visual, "observe") as observe:
            visual.main()
        self.assertEqual(observe.call_args.args[2], self.destination)

    def test_silent_launchctl_is_bounded_without_changing_shared_readiness_reader(self):
        self.destination.mkdir(parents=True)
        real_popen = subprocess.Popen
        def fake_popen(command, **kwargs):
            return real_popen([sys.executable, "-c", "import time; time.sleep(30)"],
                                    stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                    stderr=subprocess.DEVNULL)
        with mock.patch.object(visual.subprocess, "Popen", side_effect=fake_popen), \
                mock.patch.object(visual.watch.time, "monotonic", side_effect=[0, 0, 11]):
            self.assertEqual(visual.launch_system(UDID, self.destination), 124)
        self.assertIn("timed out", (self.destination / "launch-system-after-60s.txt").read_text())


if __name__ == "__main__":
    unittest.main()
