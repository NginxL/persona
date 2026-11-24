"""Launch diagnostics must fail closed without exposing another simulator or raw output."""
from datetime import datetime, timedelta, timezone
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts/ci"))
import apple_launch_diagnostics as launch
import apple_diagnostics

UDID = "11111111-1111-1111-1111-111111111111"
OTHER = "22222222-2222-2222-2222-222222222222"
START = datetime(2026, 1, 1, 12, tzinfo=timezone.utc)


class LaunchDiagnosticsTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.output = Path(self.tmp.name) / "output"
        self.destination = Path(self.tmp.name) / "artifacts"
        self.output.mkdir()
        self.destination.mkdir()
        self.context = {"udid": UDID, "bundle_id": launch.BUNDLE, "started_at": START.isoformat()}
        self.save()
        (self.output / "simulator.log").write_text(UDID + "\n")
        self.calls = []

    def save(self):
        (self.output / launch.CONTEXT).write_text(json.dumps(self.context))

    def entry(self, **changes):
        entry = {"timestamp": "2026-01-01 12:01:00.000000+0000",
                 "processImagePath": "/System/Library/CoreSimulatorService",
                 "eventMessage": f"Unable to launch {launch.BUNDLE} on {UDID}: service unavailable",
                 "privateConfiguration": "DO NOT EXPORT", "extraCredential": "DO NOT EXPORT"}
        return dict(entry, **changes)

    def collect(self, entries=None, reader=None):
        def fake(command):
            self.calls.append(command)
            return ("\n".join(json.dumps(self.entry() if entries is None else entry)
                              for entry in ([None] if entries is None else entries)) + "\n").encode()
        launch.collect(self.output, self.destination, reader=reader or fake,
                       now=START + timedelta(minutes=17))

    def status(self):
        return (self.destination / "simulator-launch.status.txt").read_text()

    def test_exact_owned_identity_and_original_readiness_window(self):
        self.collect()
        command, = self.calls
        self.assertEqual(command[:7], ["/usr/bin/log", "show", "--style", "ndjson", "--info", "--debug", "--start"])
        self.assertEqual(command[7], "2026-01-01 12:00:00+00:00")
        self.assertEqual(command[command.index("--end") + 1], "2026-01-01 12:15:00+00:00")
        predicate = command[command.index("--predicate") + 1]
        self.assertIn(f'(eventMessage CONTAINS[c] "{UDID}" OR ', predicate)
        self.assertIn(f'eventMessage CONTAINS "{launch.BUNDLE}" OR ', predicate)
        self.assertNotIn(OTHER, predicate)
        self.assertNotIn("--last", command)
        exported = json.loads((self.destination / "simulator-launch.json").read_text())
        self.assertEqual(len(exported), 1)
        self.assertEqual(set(exported[0]), {"timestamp", "process", "eventMessage"})
        self.assertNotIn("DO NOT EXPORT", json.dumps(exported))
        self.assertIn("captured 1 scoped events", self.status())

    def test_scope_rejections_expose_only_fixed_prerequisite_subtypes(self):
        cases = [("missing", None, "scope-context-fields"),
                 ("invalid UDID", {**self.context, "udid": '" OR TRUE'}, "scope-identity"),
                 ("non-string UDID", {**self.context, "udid": None}, "scope-identity"),
                 ("lowercase UDID", {**self.context,
                                    "udid": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}, "scope-identity"),
                 ("other bundle", {**self.context, "bundle_id": "org.example.someone.else"}, "scope-identity"),
                 ("invalid time", {**self.context, "started_at": "invalid"}, "scope-context-time"),
                 ("naive time", {**self.context, "started_at": "2026-01-01T12:00:00"}, "scope-context-time"),
                 ("non-UTC time", {**self.context, "started_at": "2026-01-01T13:00:00+01:00"}, "scope-context-time"),
                 ("future time", {**self.context, "started_at": "2026-01-01T13:00:00+00:00"}, "scope-context-time"),
                 ("expired time", {**self.context, "started_at": "2025-12-31T12:00:00+00:00"}, "scope-context-time"),
                 ("extra credential", {**self.context, "credentials": "DO NOT EXPORT"}, "scope-context-fields"),
                 ("missing fields", {}, "scope-context-fields"),
                 ("non-object context", [], "scope-context-fields")]
        for label, context, category in cases:
            with self.subTest(case=label):
                path = self.output / launch.CONTEXT
                if context is None:
                    path.unlink()
                else:
                    path.write_text(json.dumps(context))
                (self.output / "simulator.log").write_text(UDID + "\n")
                self.collect()
                self.assertEqual(self.calls, [], label)
                self.assertEqual(self.status(),
                                 f"unavailable: {category}-failure; no launch cause established\n")
                self.assertNotIn("DO NOT EXPORT", self.status())
                self.assertFalse((self.destination / "simulator-launch.json").exists())
                self.assertFalse((self.destination / launch.CONTEXT).exists())

    def test_scope_io_errors_expose_no_raw_error_or_private_path(self):
        real_open = launch.os.open
        for name, category in ((launch.CONTEXT, "scope-context-fields"),
                               ("simulator.log", "scope-owned-simulator")):
            with self.subTest(name=name):
                def fail_private_file(path, *args, **kwargs):
                    if Path(path).name == name:
                        raise OSError("credential=DO NOT EXPORT /private/context.json")
                    return real_open(path, *args, **kwargs)
                with mock.patch.object(launch.os, "open", side_effect=fail_private_file):
                    self.collect()
                self.assertFalse(self.calls)
                self.assertEqual(self.status(),
                                 f"unavailable: {category}-failure; no launch cause established\n")
                self.assertNotIn("DO NOT EXPORT", self.status())
                self.assertNotIn("/private/", self.status())
                self.assertEqual({path.name for path in self.destination.iterdir()},
                                 {"simulator-launch.status.txt"})

    def test_context_symlink_is_not_read_or_copied(self):
        path = self.output / launch.CONTEXT
        path.unlink()
        private = self.output / "private-config.json"
        private.write_text(json.dumps(self.context))
        path.symlink_to(private)
        self.collect()
        self.assertFalse(self.calls)
        self.assertEqual(self.status(),
                         "unavailable: scope-context-fields-failure; no launch cause established\n")
        self.assertEqual({p.name for p in self.destination.iterdir()}, {"simulator-launch.status.txt"})

    def test_both_scope_files_reject_links_large_files_and_fifos(self):
        for name, category in ((launch.CONTEXT, "scope-context-fields"),
                               ("simulator.log", "scope-owned-simulator")):
            for kind in ("symlink", "large", "fifo", "directory"):
                with self.subTest(name=name, kind=kind):
                    path = self.output / name
                    original = path.read_bytes()
                    path.unlink()
                    if kind == "symlink":
                        target = self.output / "private-input"
                        target.write_bytes(original)
                        path.symlink_to(target)
                    elif kind == "large":
                        path.write_bytes(b"x" * 1025)
                    elif kind == "fifo":
                        os.mkfifo(path)
                    else:
                        path.mkdir()
                    self.collect()
                    self.assertFalse(self.calls)
                    self.assertEqual(self.status(),
                                     f"unavailable: {category}-failure; no launch cause established\n")
                    self.assertFalse((self.destination / "simulator-launch.json").exists())
                    if kind == "directory":
                        path.rmdir()
                    else:
                        path.unlink()
                    path.write_bytes(original)

    def test_owned_simulator_mismatch_is_distinct_from_identity(self):
        (self.output / "simulator.log").write_text(OTHER + "\n")
        self.collect()
        self.assertFalse(self.calls)
        self.assertEqual(self.status(),
                         "unavailable: scope-owned-simulator-failure; no launch cause established\n")
        self.assertNotIn(OTHER, self.status())

    def test_output_is_rechecked_and_empty_is_not_evidence(self):
        self.collect(entries=[
            self.entry(eventMessage=f"launch unrelated.example on {OTHER}"),
            self.entry(eventMessage=f"launch {launch.BUNDLE}.other on {OTHER}"),
            self.entry(eventMessage=f"launch unrelated.example on {OTHER}"),
            self.entry(processImagePath="/usr/libexec/launchd_sim",
                       eventMessage=f"Launch {launch.BUNDLE}.other failed"),
            self.entry(processImagePath="/Applications/UnrelatedApp"),
            self.entry(timestamp="2026-01-01 11:59:59+0000"),
            self.entry(timestamp="2026-01-01 12:15:02+0000"),
            self.entry(eventMessage=f"launch {launch.BUNDLE} on {UDID} environment SECRET=DO NOT EXPORT"),
        ])
        self.assertIn("no scoped launch/runtime events; no cause established", self.status())
        self.assertFalse((self.destination / "simulator-launch.json").exists())
        self.assertEqual(len(self.calls), 1)  # No broader fallback query.

    def test_udid_only_launchd_line_is_retained_but_not_attributed(self):
        self.collect(entries=[self.entry(processImagePath="/usr/libexec/launchd_sim",
                                        eventMessage=f"Service for {UDID} exited")])
        self.assertEqual(len(json.loads((self.destination / "simulator-launch.json").read_text())), 1)
        self.assertIn("diagnosis still required", self.status())

    def test_launchd_error_without_udid_and_runtime_events_remain_scoped(self):
        self.collect(entries=[
            self.entry(processImagePath="/usr/libexec/launchd_sim",
                       eventMessage=f"Launch of {launch.BUNDLE} failed: ipc/mig server died"),
            self.entry(processImagePath="/usr/libexec/simdiskimaged",
                       eventMessage=f"Performing a cryptex force unmount for iOS 26.0 (23A8464) on {UDID}"),
            self.entry(processImagePath="/usr/libexec/launchd_sim",
                       eventMessage="Unrelated application failed"),
            self.entry(processImagePath="/usr/libexec/cryptexd",
                       eventMessage="Ordinary runtime inventory"),
            self.entry(eventMessage=f"Launch {launch.BUNDLE} on {UDID} environment DYLD_PRIVATE=DO NOT EXPORT"),
        ])
        exported = json.loads((self.destination / "simulator-launch.json").read_text())
        self.assertEqual(len(exported), 2)
        self.assertEqual({item["process"] for item in exported}, {"launchd_sim", "simdiskimaged"})
        self.assertNotIn("DO NOT EXPORT", json.dumps(exported))
        self.assertIn("diagnosis still required", self.status())

    def test_programmer_errors_are_not_relabelled_as_collection_unavailable(self):
        with mock.patch.object(launch, "observation_scope", side_effect=TypeError("programmer error")):
            with self.assertRaisesRegex(TypeError, "programmer error"):
                launch.collect(self.output, self.destination, now=START + timedelta(minutes=1))
        self.assertFalse((self.destination / "simulator-launch.json").exists())

    def test_runtime_events_require_an_owned_udid_anchor(self):
        self.collect(entries=[self.entry(processImagePath="/usr/libexec/cryptexd",
                                        eventMessage="Unscoped duplicate cryptex mount for iOS 26.0")])
        self.assertIn("no scoped launch/runtime events", self.status())
        self.assertFalse((self.destination / "simulator-launch.json").exists())

    def test_regular_scope_never_borrows_the_phone_context_or_publishes_its_name(self):
        (self.output / "regular-readiness-context.json").write_text(json.dumps(self.context))
        (self.output / "regular-simulator.log").write_text(UDID + "\n")
        self.context = {**self.context, "udid": OTHER}
        self.save()
        (self.output / "simulator.log").write_text(OTHER + "\n")
        regular = self.destination / "live-regular-readiness"
        regular.mkdir()
        calls = []
        def reader(command):
            calls.append(command)
            return (json.dumps(self.entry(eventMessage=f"launch {launch.BUNDLE} on {UDID}: service unavailable")).encode() + b"\n")
        launch.collect(self.output, regular, reader=reader, now=START + timedelta(minutes=1))
        self.assertIn(UDID, calls[0][calls[0].index("--predicate") + 1])
        self.assertNotIn(OTHER, calls[0][calls[0].index("--predicate") + 1])
        self.assertEqual(json.loads((regular / "regular-readiness-context.json").read_text())["udid"], UDID)
        self.assertFalse((regular / launch.CONTEXT).exists())
        self.assertIn("captured 1 scoped events", (regular / "simulator-launch.status.txt").read_text())

    def test_streamed_collection_filters_before_the_export_limit(self):
        path = self.output / "events.ndjson"
        path.write_text("\n".join([json.dumps(self.entry(eventMessage="unrelated",
                        privateConfiguration="X" * 5000))] * 750 + [json.dumps(self.entry())]) + "\n")
        self.assertGreater(path.stat().st_size, launch.MAX_BYTES)
        self.assertLess(path.stat().st_size, launch.RAW_LOG_BYTES)
        command = ["/usr/bin/log", "show", "--style", "ndjson", "--info", "--debug",
                   "--start", START.strftime("%Y-%m-%d %H:%M:%S") + "+00:00",
                   "--end", (START + timedelta(minutes=15)).strftime("%Y-%m-%d %H:%M:%S") + "+00:00",
                   "--predicate", UDID + " " + launch.BUNDLE]
        input_value = mock.Mock()
        input_value.stdout = os.popen("cat " + str(path), "r")
        input_value.stderr = os.popen("cat /dev/null", "r")
        input_value.poll.return_value = 0
        input_value.wait.return_value = 0
        with mock.patch.object(launch.subprocess, "Popen", return_value=input_value):
            kept = launch.stream_output(command, START, START + timedelta(minutes=15), UDID, launch.BUNDLE)
        self.assertEqual(len(kept), 1)
        self.assertEqual(launch.RAW_LOG_BYTES, 4 * 1024 * 1024)

    def test_streamed_large_export_fails_closed(self):
        command = [sys.executable, "-c", "import os; os.write(1,b'x'*" + str(launch.MAX_BYTES + 100) + ")"]
        with self.assertRaisesRegex(ValueError, "single launch event"):
            launch.stream_output(command, START, START + timedelta(minutes=15), UDID, launch.BUNDLE)

    def test_collection_failures_name_the_boundary_not_a_cause(self):
        for error in (TimeoutError(), subprocess.TimeoutExpired("/usr/bin/log", 20)):
            with self.subTest(error=type(error).__name__):
                self.collect(reader=lambda command, error=error: (_ for _ in ()).throw(error))
                self.assertEqual(self.status(), "unavailable: scoped log command timed out\n")
        self.collect(reader=lambda command: b"not JSON DO NOT EXPORT")
        self.assertEqual(self.status(),
                         "unavailable: stream-parse-failure; no launch cause established\n")
        self.assertNotIn("DO NOT EXPORT", self.status())

    def test_log_command_failures_are_distinct_from_stream_parsing(self):
        for error in (OSError("credential /private/command-path DO NOT EXPORT"),
                      ValueError("command failed (exit 1) DO NOT EXPORT"),
                      subprocess.SubprocessError("DO NOT EXPORT")):
            with self.subTest(error=type(error).__name__):
                def reader(command):
                    self.calls.append(command)
                    raise error
                self.collect(reader=reader)
                self.assertEqual(self.status(),
                                 "unavailable: log-command-failure; no launch cause established\n")
                self.assertNotIn("DO NOT EXPORT", self.status())
                self.assertNotIn("/private/", self.status())
                self.assertEqual(len(self.calls), 1)  # No raw-error or broader fallback query.
                self.calls.clear()

    def test_reader_output_limit_has_its_own_category(self):
        self.collect(reader=lambda command: b"x" * (launch.MAX_BYTES + 1))
        self.assertEqual(self.status(),
                         "unavailable: output-limit-failure; no launch cause established\n")

    def test_default_stream_malformed_ndjson_is_not_a_command_or_publication_failure(self):
        real_popen = launch.subprocess.Popen
        def fake_log(command, **kwargs):
            return real_popen([sys.executable, "-c", "print('not NDJSON DO NOT EXPORT')"], **kwargs)
        with mock.patch.object(launch.subprocess, "Popen", side_effect=fake_log):
            launch.collect(self.output, self.destination, now=START + timedelta(minutes=17))
        self.assertEqual(self.status(),
                         "unavailable: stream-parse-failure; no launch cause established\n")
        self.assertNotIn("DO NOT EXPORT", self.status())
        self.assertFalse((self.destination / "simulator-launch.json").exists())

    def test_default_stream_raw_limit_is_not_a_parse_failure(self):
        with mock.patch.object(launch, "RAW_LOG_BYTES", 8):
            with self.assertRaises(launch.OutputLimitError):
                launch.stream_output([sys.executable, "-c", "import os; os.write(1,b'123456789')"],
                                     START, START + timedelta(minutes=15), UDID, launch.BUNDLE)

    def test_default_stream_command_exit_is_a_log_command_failure(self):
        real_popen = launch.subprocess.Popen
        def fake_log(command, **kwargs):
            return real_popen([sys.executable, "-c", "raise SystemExit(7)"], **kwargs)
        with mock.patch.object(launch.subprocess, "Popen", side_effect=fake_log):
            launch.collect(self.output, self.destination, now=START + timedelta(minutes=17))
        self.assertEqual(self.status(),
                         "unavailable: log-command-failure; no launch cause established\n")
        self.assertNotIn("exit", self.status())

    def test_default_stream_raw_limit_is_reported_at_collect_boundary(self):
        real_popen = launch.subprocess.Popen
        def fake_log(command, **kwargs):
            return real_popen([sys.executable, "-c", "import os; os.write(1,b'123456789')"], **kwargs)
        with mock.patch.object(launch.subprocess, "Popen", side_effect=fake_log), \
                mock.patch.object(launch, "RAW_LOG_BYTES", 8):
            launch.collect(self.output, self.destination, now=START + timedelta(minutes=17))
        self.assertEqual(self.status(),
                         "unavailable: output-limit-failure; no launch cause established\n")

    def test_default_stream_timeout_keeps_the_existing_category(self):
        real_popen = launch.subprocess.Popen
        def fake_log(command, **kwargs):
            return real_popen([sys.executable, "-c", "import signal; signal.pause()"], **kwargs)
        with mock.patch.object(launch.subprocess, "Popen", side_effect=fake_log), \
                mock.patch.object(launch.time, "monotonic", side_effect=[0, 100]):
            launch.collect(self.output, self.destination, now=START + timedelta(minutes=17))
        self.assertEqual(self.status(), "unavailable: scoped log command timed out\n")

    def test_partial_evidence_and_context_writes_leave_no_bundle(self):
        evidence = self.destination / "simulator-launch.json"
        context = self.destination / launch.CONTEXT
        status = self.destination / "simulator-launch.status.txt"
        stale = b"stale evidence DO NOT EXPORT"
        for name in (evidence.name, launch.CONTEXT):
            with self.subTest(name=name, failure="partial-write"):
                evidence.write_bytes(stale)
                context.write_bytes(stale)
                status.write_text("stale status")
                real_fdopen = launch.os.fdopen
                descriptors = {}

                def fixture_temporary(dir, prefix, suffix):
                    path = Path(dir) / (prefix + "partial" + suffix)
                    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_EXCL, 0o600)
                    descriptors[fd] = path
                    return fd, str(path)

                partial_paths = []

                class PartialWriter:
                    def __init__(self, stream):
                        self.stream = stream

                    def __enter__(self):
                        return self

                    def __exit__(self, *args):
                        return self.stream.__exit__(*args)

                    def write(self, data):
                        self.stream.write(data[:1])
                        raise OSError("private partial write DO NOT EXPORT")

                def partial_fdopen(fd, mode, buffering=-1):
                    stream = real_fdopen(fd, mode, buffering=buffering)
                    if mode == "wb" and buffering == 0 and descriptors.pop(fd).name.startswith(name + "."):
                        partial_paths.append(name)
                        return PartialWriter(stream)
                    return stream

                with mock.patch.object(launch.tempfile, "mkstemp", fixture_temporary), \
                        mock.patch.object(launch.os, "fdopen", partial_fdopen):
                    self.collect()
                self.assertEqual(partial_paths, [name])
                self.assertEqual(self.status(),
                                 "unavailable: publication-failure; no launch cause established\n")
                self.assertFalse(evidence.exists())
                self.assertFalse(context.exists())
                self.assertEqual(descriptors, {})
                self.assertFalse(any(path.suffix == ".tmp" for path in self.destination.iterdir()))

            with self.subTest(name=name, failure="replacement"):
                evidence.write_bytes(stale)
                context.write_bytes(stale)
                status.write_text("stale status")
                real_replace = launch.os.replace

                def fail_replace(source, destination):
                    if Path(destination).name == name:
                        raise OSError("private replacement DO NOT EXPORT")
                    real_replace(source, destination)

                with mock.patch.object(launch.os, "replace", fail_replace):
                    self.collect()
                self.assertEqual(self.status(),
                                 "unavailable: publication-failure; no launch cause established\n")
                self.assertFalse(evidence.exists())
                self.assertFalse(context.exists())
                self.assertFalse(any(path.suffix == ".tmp" for path in self.destination.iterdir()))

    def test_status_failure_propagates_the_fixed_intended_text_and_invalidates_bundle(self):
        evidence = self.destination / "simulator-launch.json"
        context = self.destination / launch.CONTEXT
        status = self.destination / "simulator-launch.status.txt"
        cases = ((None, "captured 1 scoped events; diagnosis still required\n"),
                 ([], "unavailable: no scoped launch/runtime events; no cause established\n"))
        for entries, intended in cases:
            with self.subTest(intended=intended):
                evidence.write_text("stale evidence")
                context.write_text("stale context")
                status.write_text("stale status")
                real_replace = launch.os.replace

                def fail_status(source, destination):
                    real_replace(source, destination)
                    if Path(destination) == status:
                        raise OSError("private status path credential DO NOT EXPORT")

                with mock.patch.object(launch.os, "replace", fail_status), \
                        self.assertRaises(launch.StatusPublicationError) as raised:
                    self.collect(entries=entries)
                self.assertEqual(str(raised.exception), intended)
                self.assertIsNone(raised.exception.__cause__)
                self.assertNotIn("DO NOT EXPORT", str(raised.exception))
                self.assertFalse(status.exists())
                self.assertFalse(evidence.exists())
                self.assertFalse(context.exists())
                self.assertFalse(any(path.suffix == ".tmp" for path in self.destination.iterdir()))

    def test_publication_replaces_complete_files_in_the_artifact_directory(self):
        self.collect()
        real_replace = launch.os.replace
        replacements = []

        def same_directory_replace(source, destination):
            replacements.append((source, destination))
            self.assertEqual(Path(source).parent, Path(destination).parent)
            self.assertTrue(Path(source).name.endswith(".tmp"))
            real_replace(source, destination)

        self.collect()
        with mock.patch.object(launch.os, "replace", same_directory_replace):
            self.collect()
        self.assertEqual([Path(destination).name for _, destination in replacements],
                         [launch.CONTEXT, "simulator-launch.json", "simulator-launch.status.txt"])
        self.assertFalse(any(path.suffix == ".tmp" for path in self.destination.iterdir()))

    def test_publication_failure_does_not_export_paths_or_hide_behind_stream_parse(self):
        real_replace = launch.os.replace
        with mock.patch.object(launch.os, "replace",
                               side_effect=lambda source, destination: real_replace(source, destination)
                               if Path(destination).name != "simulator-launch.json"
                               else (_ for _ in ()).throw(OSError("private evidence DO NOT EXPORT"))):
            self.collect()
        self.assertEqual(self.status(),
                         "unavailable: publication-failure; no launch cause established\n")
        self.assertNotIn("DO NOT EXPORT", self.status())
        self.assertNotIn("/private/", self.status())
        self.assertFalse((self.destination / "simulator-launch.json").exists())
        self.assertFalse((self.destination / launch.CONTEXT).exists())

    def test_status_failure_does_not_mask_an_existing_collection_category(self):
        status = self.destination / "simulator-launch.status.txt"
        intended = "unavailable: log-command-failure; no launch cause established\n"

        def reader(command):
            raise OSError("private command failure DO NOT EXPORT")

        with mock.patch.object(launch, "publish_status",
                               side_effect=launch.StatusPublicationError(intended)) as publish_status, \
                self.assertRaises(launch.StatusPublicationError) as raised:
            self.collect(reader=reader)
        publish_status.assert_called_once_with(status, intended)
        self.assertEqual(str(raised.exception), intended)
        self.assertNotIn("publication-failure", str(raised.exception))
        self.assertNotIn("DO NOT EXPORT", str(raised.exception))
        self.assertFalse((self.destination / "simulator-launch.json").exists())
        self.assertFalse((self.destination / launch.CONTEXT).exists())

    def test_bad_output_timeout_and_command_errors_remain_unavailable(self):
        for raw, category in ((b"not JSON DO NOT EXPORT", "stream-parse"),
                              (b"[]\n", "stream-parse"),
                              (b"\xff", "stream-parse"),
                              (b"x" * (launch.MAX_BYTES + 1), "output-limit")):
            with self.subTest(category=category, raw=raw[:20]):
                self.collect(reader=lambda command, raw=raw: raw)
                self.assertEqual(self.status(),
                                 f"unavailable: {category}-failure; no launch cause established\n")
                self.assertNotIn("DO NOT EXPORT", self.status())
                self.assertFalse((self.destination / "simulator-launch.json").exists())

    def test_real_reader_enforces_timeout_bytes_and_exit_status(self):
        self.assertEqual(launch.bounded_output([sys.executable, "-c", "print('ok')"]), b"ok\n")
        with self.assertRaises(TimeoutError):
            launch.bounded_output([sys.executable, "-c", "import signal; signal.pause()"], timeout=0.05)
        with self.assertRaisesRegex(ValueError, "byte limit"):
            launch.bounded_output([sys.executable, "-c", "import os; os.write(1,b'x'*4096)"], limit=64)
        with self.assertRaisesRegex(ValueError, "exit 7"):
            launch.bounded_output([sys.executable, "-c", "raise SystemExit(7)"])
        self.assertEqual(launch.TIMEOUT, 20)
        self.assertEqual(launch.MAX_BYTES, 128 * 1024)

    def test_existing_collector_calls_the_scoped_capture(self):
        def runner(command, **kwargs):
            return subprocess.CompletedProcess(command, 0, "{}", "")
        with mock.patch.object(launch, "collect") as capture:
            apple_diagnostics.collect(self.output, self.destination, runner)
        capture.assert_called_once_with(self.output, self.destination)
        self.assertEqual((self.destination / "simctl-list.status.txt").read_text(), "0\n")

    def test_record_cli_only_writes_synthetic_scope(self):
        command = [sys.executable, str(ROOT / "scripts/ci/apple_launch_diagnostics.py"),
                   "--record", str(self.output / launch.CONTEXT), "--udid", UDID, "--bundle-id", launch.BUNDLE]
        result = subprocess.run(command, text=True, capture_output=True, timeout=5)
        self.assertEqual(result.returncode, 0, result.stderr)
        context = json.loads((self.output / launch.CONTEXT).read_text())
        self.assertEqual(set(context), {"udid", "bundle_id", "started_at"})
        self.assertEqual(context["udid"], UDID)
        self.assertEqual(datetime.fromisoformat(context["started_at"]).utcoffset(), timedelta(0))


class LiveLaunchTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.output = Path(self.tmp.name) / "output"
        self.output.mkdir()
        (self.output / launch.CONTEXT).write_text(json.dumps({
            "udid": UDID, "bundle_id": launch.BUNDLE, "started_at": START.isoformat()}))
        (self.output / "simulator.log").write_text(UDID + "\n")
        self.destination = self.output / "diagnostics/live-readiness"
        self.live = None

    def tearDown(self):
        if self.live is not None and self.live.process is not None:
            try:
                self.live.stop()
            except (OSError, subprocess.SubprocessError, TimeoutError):
                if self.live.process.poll() is None:
                    self.live.process.kill()
                    self.live.process.wait(timeout=2)
                self.live.thread.join(timeout=2)
            self.assertIsNotNone(self.live.process.poll(), "live log child survived cleanup")
            self.assertFalse(self.live.thread.is_alive(), "live log reader survived cleanup")

    def start(self, body, deadline=None):
        child = ("import json, os, signal, subprocess, sys, time\n"
                 "def send(entry):\n"
                 "    print(json.dumps(entry), flush=True)\n" + body)
        def spawn(command, **kwargs):
            actual = [sys.executable, "-c", child] if command[0] == "/usr/bin/log" else command
            return subprocess.Popen(actual, **kwargs)
        self.live = launch.start_live_log(self.output, self.destination,
                                          deadline=deadline or START + timedelta(minutes=15),
                                          now=lambda: START + timedelta(minutes=1),
                                          popen=spawn)
        self.assertEqual(self.live.log_pgid, self.live.process.pid)
        self.assertNotEqual(self.live.log_pgid, os.getpgid(0))
        self.assertIsNotNone(self.live.reaper,
                             (self.destination / "live-launch.status.txt").read_text())
        return self.live

    def entry(self, message, process="launchd_sim"):
        return {"timestamp": "2026-01-01 12:01:00.000000+0000",
                "processImagePath": "/usr/libexec/" + process, "eventMessage": message}

    def test_pid_association_retains_an_event_emitted_before_the_pid_query(self):
        ready = Path(self.tmp.name) / "ready"
        self.start(f"send({self.entry('launch request for pid=41')!r})\n"
                   f"send({dict(self.entry('structured launch failure'), targetPid=41)!r})\n"
                   f"send({self.entry('request for pid=41 environment credential=redacted')!r})\n"
                   f"send({self.entry('ordinary count 41')!r})\n"
                   f"send({self.entry('request for pid=42')!r})\n"
                   "signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))\n"
                   f"open({str(ready)!r}, 'w').write(str(os.getpid()))\n"
                   "signal.pause()\n")
        deadline = time.monotonic() + 5
        while (not self.live.pending or not ready.exists()) and time.monotonic() < deadline:
            time.sleep(0.005)
        self.assertTrue(ready.exists(), "event source did not install its SIGTERM handler")
        self.assertFalse((self.destination / "live-launch.json").exists())
        self.live.correlate([41])
        self.live.stop()
        self.assertEqual(self.live.status, "captured",
                         (self.destination / "live-launch.status.txt").read_text())
        self.assertEqual([item["eventMessage"] for item in
                          json.loads((self.destination / "live-launch.json").read_text())],
                         ["launch request for pid=41", "structured launch failure"])
        self.assertNotIn("credential", (self.destination / "live-launch.json").read_text())
        self.assertIn("captured 2 scoped live events",
                      (self.destination / "live-launch.status.txt").read_text())

    def test_unbounded_noise_fails_closed_without_blocking_or_leaking_children(self):
        ready = Path(self.tmp.name) / "ready"
        self.start(f"send({self.entry('unrelated early launch line')!r})\n"
                   f"open({str(ready)!r}, 'w').write(str(os.getpid()))\n"
                   "for _ in range(1100):\n"
                   "    send({'padding': 'x' * 4096})\n"
                   "signal.pause()\n")
        deadline = time.monotonic() + 5
        while self.live.status == "collection incomplete" and time.monotonic() < deadline:
            time.sleep(0.005)
        self.assertTrue(ready.exists())
        self.live.stop()
        self.assertEqual(self.live.status, "output-limit",
                         (self.destination / "live-launch.status.txt").read_text())
        self.assertFalse((self.destination / "live-launch.json").exists())
        self.assertIsNotNone(self.live.process.poll())
        self.assertFalse(self.live.thread.is_alive())
        self.assertFalse(self.live.stderr_thread.is_alive())

    def test_ignoring_sigterm_is_killed_and_does_not_publish_success(self):
        ready = Path(self.tmp.name) / "ready"
        self.start("signal.signal(signal.SIGTERM, signal.SIG_IGN)\n"
                   f"open({str(ready)!r}, 'w').write(str(os.getpid()))\n"
                   "signal.pause()\n")
        deadline = time.monotonic() + 5
        while not ready.exists() and time.monotonic() < deadline:
            time.sleep(0.005)
        self.assertTrue(ready.exists(), "fixture did not install its SIGTERM handler")
        self.live.stop()
        self.assertEqual(self.live.process.returncode, -9)
        self.assertIn("stop-timeout", (self.destination / "live-launch.status.txt").read_text())
        self.assertEqual(self.live.reader_error, None)
        self.assertFalse((self.destination / "live-launch.json").exists())

    def test_stop_kills_descendants_without_touching_the_watcher_group(self):
        ready = Path(self.tmp.name) / "ready"
        self.start("nested = subprocess.Popen([sys.executable, '-c', 'import signal; signal.pause()'])\n"
                   f"open({str(ready)!r}, 'w').write(str(nested.pid))\n"
                   "signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))\n"
                   "signal.pause()\n")
        deadline = time.monotonic() + 5
        while not ready.exists() and time.monotonic() < deadline:
            time.sleep(0.005)
        self.assertTrue(ready.exists(), "nested child did not start")
        nested = int(ready.read_text())
        self.assertEqual(os.getpgid(nested), self.live.process.pid)
        watcher_pgid = os.getpgid(0)
        self.live.stop()
        self.assertEqual(os.getpgid(0), watcher_pgid)
        with self.assertRaises(ProcessLookupError, msg="nested log descendant survived stop"):
            os.kill(nested, 0)

            self.assertFalse(self.live.thread.is_alive(), "live log reader survived cleanup")
            if self.live.reaper is not None:
                self.assertIsNotNone(self.live.reaper.poll(), "live log watchdog survived cleanup")

    def test_parent_death_reaps_the_registered_log_group(self):
        # The real watcher is killed before its finally can run; only the
        # independently bounded watchdog can close the registered log group.
        import textwrap
        fixture = self.output / "parent-fixture"
        fixture.mkdir()
        (fixture / launch.CONTEXT).write_text(json.dumps({
            "udid": UDID, "bundle_id": launch.BUNDLE,
            "started_at": datetime.now(timezone.utc).isoformat()}))
        (fixture / "simulator.log").write_text(UDID + "\n")
        child = ("import os, signal, subprocess, sys\n"
                 "nested = subprocess.Popen([sys.executable, '-c', 'import signal; signal.pause()'])\n"
                 f"open({str(fixture / 'nested.pid')!r}, 'w').write(str(nested.pid))\n"
                 "sys.exit(0)\n")
        source = textwrap.dedent(f'''
            import json, os, subprocess, sys
            from pathlib import Path
            sys.path.insert(0, {str(ROOT / "scripts/ci")!r})
            import apple_launch_diagnostics as launch
            root = Path({str(fixture)!r})
            destination = root / "diagnostics/live-readiness"
            live = launch.start_live_log(root, destination, popen=lambda command, **kwargs: subprocess.Popen(
                [sys.executable, "-c", {child!r}] if command[0] == "/usr/bin/log" else command, **kwargs))
            import signal, time
            deadline = time.monotonic() + 5
            while not root.joinpath("nested.pid").exists() and time.monotonic() < deadline:
                time.sleep(0.005)
            nested_pid = int(root.joinpath("nested.pid").read_text())
            root.joinpath("processes.json").write_text(json.dumps({{
                "stream": live.process.pid, "nested": nested_pid,
                "watchdog": live.reaper.pid, "parent": os.getpid()}}))
            live.reaper.poll()
            import signal
            signal.pause()
        ''')
        env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1")
        parent = subprocess.Popen([sys.executable, "-c", source], env=env,
                                  stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        try:
            deadline = time.monotonic() + 5
            record = fixture / "processes.json"
            while not record.exists() and time.monotonic() < deadline:
                if parent.poll() is not None:
                    self.fail(parent.stderr.read().decode())
                time.sleep(0.005)
            self.assertTrue(record.exists(), "watcher did not register its owned processes")
            pids = json.loads(record.read_text())
            parent.kill()
            parent.wait(timeout=2)
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline:
                alive = []
                for name in ("stream", "nested", "watchdog"):
                    try:
                        os.kill(pids[name], 0)
                        alive.append(name)
                    except ProcessLookupError:
                        pass
                if not alive:
                    break
                time.sleep(0.01)
            self.assertEqual(alive, [], f"processes survived parent death: {alive}")
        finally:
            parent.stderr.close()
            if parent.poll() is None:
                parent.kill()
                parent.wait(timeout=2)

    def test_empty_success_is_distinct_from_failures(self):
        self.start("print('ready', file=sys.stderr, flush=True)\n")
        self.live.thread.join(timeout=5)
        self.assertFalse(self.live.thread.is_alive(), "empty command did not close its stdout")
        self.live.stop()
        self.assertEqual(self.live.exit_code, 0,
                         (self.destination / "live-launch.status.txt").read_text())
        self.assertIn("no scoped live launch events",
                      (self.destination / "live-launch.status.txt").read_text())
        self.assertEqual(json.loads((self.destination / "live-launch.json").read_text()), [])

    def test_startup_failure_has_its_own_status_and_no_process(self):
        self.live = launch.start_live_log(self.output, self.destination,
                                          deadline=START + timedelta(minutes=15),
                                          now=lambda: START + timedelta(minutes=1),
                                          popen=lambda *args, **kwargs: (_ for _ in ()).throw(OSError("private path")))
        self.assertIn("startup-failure", (self.destination / "live-launch.status.txt").read_text())
        self.assertFalse((self.destination / "live-launch.json").exists())

    def test_command_failure_has_its_own_status(self):
        self.start("sys.exit(7)\n")
        self.live.thread.join(timeout=5)
        self.assertFalse(self.live.thread.is_alive(), "failed command did not close its stdout")
        self.live.stop()
        self.assertEqual(self.live.exit_code, 7)
        self.assertIn("command-failure", (self.destination / "live-launch.status.txt").read_text())
        self.assertFalse((self.destination / "live-launch.json").exists())

    def test_eof_waits_for_the_completed_command_status(self):
        live = launch.LiveLog(["/usr/bin/log"], self.destination,
                              START + timedelta(minutes=15), UDID, launch.BUNDLE)
        live.start = START
        process = mock.Mock()
        process.stdout = io.BytesIO(b"")
        process.poll.return_value = None  # EOF was visible before the child was reaped.
        process.wait.side_effect = lambda timeout: 7
        live.process = process
        live.thread = threading.Thread(target=live.read)
        live.thread.start()
        live.thread.join(timeout=3)
        self.assertFalse(live.thread.is_alive())
        self.assertEqual(live.exit_code, 7)
        process.wait.assert_called_once_with(timeout=2)

    def test_omitted_deadline_keeps_scope_window_for_filter_and_watchdog(self):
        """`deadline=None` must not clamp the live end to startup.

        Regression guard for the P1 finding on PR #13333: with the previous
        `end = min(end, deadline or current)` an omitted deadline collapsed the
        live-event filter to the startup instant and shrank the watchdog
        lifetime to ~10 seconds. The fix preserves the scope-derived end so
        later events still pass the filter and the reaper covers the whole
        observation window.
        """
        # Substitute the Linux `/usr/bin/log` invocation only; clock, Popen and
        # reader stay real. The child emits one event at startup, then a
        # second event later so the live filter has to advance past startup.
        # Each event anchors the simulator UDID through CoreSimulatorService
        # so `relevant` accepts it without needing `correlate`.
        def entry(message, stamp):
            return {"timestamp": stamp,
                    "processImagePath": "/System/Library/CoreSimulatorService",
                    "eventMessage": f"{message} for {UDID}"}
        first = entry("initial scoped launch line", "2026-01-01 12:01:00.000000+0000")
        later = entry("later scoped launch line", "2026-01-01 12:05:00.000000+0000")
        child = ("import json, os, signal, sys, time\n"
                 "def send(entry):\n"
                 "    print(json.dumps(entry), flush=True)\n"
                 f"send({first!r})\n"
                 "signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))\n"
                 f"send({later!r})\n"
                 "signal.pause()\n")

        def spawn(command, **kwargs):
            actual = [sys.executable, "-c", child] if command[0] == "/usr/bin/log" else command
            return subprocess.Popen(actual, **kwargs)

        # `now` is 1 minute after the scope started; the scope window still
        # extends to `started_at + READINESS_SECONDS` (well past 15 minutes).
        self.live = launch.start_live_log(self.output, self.destination,
                                          deadline=None,
                                          now=lambda: START + timedelta(minutes=1),
                                          popen=spawn)
        self.assertIsNotNone(self.live.reaper,
                             (self.destination / "live-launch.status.txt").read_text())
        # The live end must equal the scope end (START + READINESS_SECONDS),
        # not the startup instant `current = START + 1 minute`.
        expected_end = START + timedelta(seconds=launch.READINESS_SECONDS)
        self.assertEqual(self.live.deadline, expected_end,
                         "live end was clamped to startup instead of the scope window")
        # The watchdog lifetime must cover the scope window (plus the 10s
        # margin), not collapse to ~10 seconds as in the regression.
        reaper_cmdline = Path(f"/proc/{self.live.reaper.pid}/cmdline").read_bytes()
        lifetime_arg = float(reaper_cmdline.split(b"\x00")[-2])
        expected_lifetime = (expected_end - (START + timedelta(minutes=1))).total_seconds() + 10
        self.assertGreater(lifetime_arg, 60,
                           f"watchdog lifetime {lifetime_arg}s collapsed to the startup margin")
        self.assertAlmostEqual(lifetime_arg, expected_lifetime, delta=1.0)

        # Both the startup-time and the post-startup events must survive the
        # live filter, because `start <= stamp <= end` covers the whole window.
        deadline_mono = time.monotonic() + 5
        while len(self.live.kept) < 2 and time.monotonic() < deadline_mono:
            time.sleep(0.005)
        self.live.stop()
        self.assertEqual(self.live.status, "captured",
                         (self.destination / "live-launch.status.txt").read_text())
        messages = [item["eventMessage"] for item in
                    json.loads((self.destination / "live-launch.json").read_text())]
        self.assertEqual(messages, [f"initial scoped launch line for {UDID}",
                                    f"later scoped launch line for {UDID}"])


if __name__ == "__main__":
    unittest.main()
