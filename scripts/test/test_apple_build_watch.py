"""Compiler resource observation must not alter the build or export its arguments."""
from pathlib import Path
import io
import json
import os
import shlex
import signal
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
sys.path.insert(0, str(ROOT / "scripts/test"))
import test_apple_offline_project as offline_project


class CompilerResourceTests(unittest.TestCase):
    @staticmethod
    def offline_project_fixture():
        case = offline_project.AppleOfflineProjectTests()
        case.setUp()
        return case.project

    def test_only_compiler_names_and_resource_counts_are_recorded(self):
        result = subprocess.CompletedProcess([], 0, "\n".join([
            "  10  2 80.5 123456 00:10 00:02.50 /Toolchain/usr/bin/swift-frontend -module-name Example -primary-file /build/Example.swift -o /build/Example.o",
            "  11  2  2.0   4000 00:11 00:00.25 /Toolchain/usr/bin/swiftc",
            "  12 10  0.0    900 00:12 00:00.00 /usr/bin/xcodebuild",
            "  13  2  0.0    800 00:13 00:00.00 /usr/bin/clang",
        ]), "")
        def legacy_ps(command, **kwargs):
            if "lstart=" in command:
                return subprocess.CompletedProcess(command, 1, "", "lstart unavailable")
            return result
        parsed = watch.processes(legacy_ps)
        self.assertEqual(parsed["counts"], {"swift-frontend": 1, "swiftc": 1, "xcodebuild": 1, "clang": 1})
        self.assertEqual(parsed["total_rss_kib"], 129156)
        self.assertNotIn("command", json.dumps(parsed["largest"]))
        frontend = next(entry for entry in parsed["largest"] if entry["name"] == "swift-frontend")
        self.assertEqual(frontend["target_module"], "Example")
        self.assertEqual(frontend["cpu_seconds"], 2.5)
        self.assertEqual(frontend["elapsed_seconds"], 10.0)
        self.assertEqual(frontend["primary_source"], "Example.swift")
        self.assertEqual(frontend["process_start_status"], "uncertain")

    @staticmethod
    def pipe_fixture(source):
        def popen(_command, **kwargs):
            return subprocess.Popen([sys.executable, "-I", "-c", source], **kwargs)
        return popen

    def test_streaming_selection_survives_noise_and_long_frontend_argv(self):
        source = textwrap.dedent('''\
            for pid in range(1000, 1200):
                print(f"{pid} 2 1.0 100 00:10 00:00.10 Sun Nov 22 10:00:00 2026 noise "
                      "/usr/bin/noise swift-frontend " + "N" * 4096)
            print("999 2 1.0 100 00:10 00:00.10 Sun Nov 22 10:00:00 2026 noise "
                  "/usr/bin/noise " + "N" * 100000)
            print("42 2 90.0 6000 00:05 00:01.50 Sun Nov 22 10:00:00 2026 swift-frontend "
                  "/Toolchain/swift-frontend -module-name Example " + "X" * 131072
                  + " -primary-file /private/source/NeverExport.swift")
            print("43 2 80.0 7000 00:06 00:02.50 Sun Nov 22 10:00:00 2026 swift-frontend "
                  "/Toolchain/swift-frontend -module-name Example "
                  "-primary-file /private/source/Second.swift -o /tmp/Second.o")
        ''')
        calls = []
        def popen(command, **kwargs):
            calls.append(command)
            return self.pipe_fixture(source)(command, **kwargs)
        parsed = watch.processes(popen=popen)
        self.assertEqual(parsed["counts"], {"swift-frontend": 2})
        self.assertEqual(parsed["total_rss_kib"], 13000)
        self.assertNotIn("unavailable", parsed)
        self.assertIn("ucomm=", calls[0][2])
        frontend = next(entry for entry in parsed["largest"] if entry["pid"] == 42)
        self.assertEqual(frontend["process_start"], "Sun Nov 22 10:00:00 2026")
        self.assertEqual(frontend["process_start_status"], "known")
        self.assertEqual(frontend["cpu_seconds"], 1.5)
        self.assertEqual(frontend["elapsed_seconds"], 5.0)
        self.assertEqual(frontend["target_module"], "unavailable")
        self.assertEqual(frontend["primary_source"], "unavailable")
        self.assertEqual(frontend["output"], "unavailable")
        other = next(entry for entry in parsed["largest"] if entry["pid"] == 43)
        self.assertEqual(other["primary_source"], "Second.swift")
        serialized = json.dumps(parsed)
        for forbidden in ("NeverExport", "/private", "/tmp", "NNNN", "XXXX", "args"):
            self.assertNotIn(forbidden, serialized)

        history = {}
        watch.track_frontends(history, parsed["largest"], 100.0)
        later = watch.processes(popen=self.pipe_fixture(source.replace("00:01.50", "00:03.50")))
        watch.track_frontends(history, later["largest"], 115.0)
        records = watch.final_frontend_history(history)
        record = next(item for item in records["records"] if item["pid"] == 42)
        self.assertEqual(record["sample_count"], 2)
        self.assertEqual(record["first_cpu_seconds"], 1.5)
        self.assertEqual(record["last_cpu_seconds"], 3.5)
        self.assertEqual(record["continuity"], "known")
        self.assertEqual(records["status"], "complete")
        self.assertLessEqual(len(records["records"]), watch.MAX_FINAL_FRONTEND_RECORDS)

    def test_streaming_legacy_lstart_fallback_and_command_failure_keep_their_semantics(self):
        legacy = self.pipe_fixture(
            "print('42 2 90.0 10000 01:10 00:02.50 swift-frontend "
            "/Toolchain/swift-frontend -module-name Example -primary-file /tmp/Example.swift')")
        calls = []
        def legacy_popen(command, **kwargs):
            calls.append(command)
            return legacy(command, **kwargs)
        parsed = watch.processes(popen=legacy_popen)
        self.assertEqual(len(calls), 2)
        self.assertFalse(any("lstart=" in field for field in calls[1][2]))
        self.assertNotIn("unavailable", parsed)
        self.assertIsNone(parsed["largest"][0]["process_start"])
        self.assertEqual(parsed["largest"][0]["process_start_status"], "uncertain")

        failed = self.pipe_fixture("raise SystemExit(1)")
        calls = []
        def failed_popen(command, **kwargs):
            calls.append(command)
            if "lstart=" in command[2]:
                return self.pipe_fixture("raise SystemExit(1)")(command, **kwargs)
            return self.pipe_fixture(
                "print('42 2 90.0 10000 01:10 00:02.50 swift-frontend /Toolchain/swift-frontend')"
            )(command, **kwargs)
        parsed = watch.processes(popen=failed_popen)
        self.assertNotIn("unavailable", parsed)
        self.assertIsNone(parsed["largest"][0]["process_start"])
        self.assertEqual(parsed["largest"][0]["process_start_status"], "uncertain")
        self.assertEqual(len(calls), 2)

        def both_failed_popen(command, **kwargs):
            calls.append(command)
            return failed(command, **kwargs)
        parsed = watch.processes(popen=both_failed_popen)
        self.assertEqual(parsed["unavailable"], "process-command-failed")
        self.assertEqual(parsed["process_start_status"], "uncertain")
        self.assertEqual(parsed["counts"], {})

    def test_streaming_collector_timeout_is_explicit_and_reaps_ps(self):
        process = None
        def popen(_command, **kwargs):
            nonlocal process
            process = subprocess.Popen([sys.executable, "-I", "-c", "import time; time.sleep(30)"], **kwargs)
            return process
        with mock.patch.object(watch, "COMMAND_TIMEOUT", 0.05):
            parsed = watch.processes(popen=popen)
        self.assertEqual(parsed["unavailable"], "TimeoutExpired")
        self.assertIsNotNone(process.poll())

    def test_valid_darwin_duration_forms_keep_their_existing_values(self):
        for value, expected in (
            ("00:00", 0.0),
            ("00:00.25", 0.25),
            ("01:02", 62.0),
            ("01:02:03.50", 3723.5),
            ("1-00:00", 86400.0),
            ("1-02:03:04", 93784.0),
            ("0001-0002:0003", 86523.0),
        ):
            with self.subTest(value=value):
                self.assertEqual(watch.duration(value), expected)

    def test_invalid_frontend_durations_fail_closed_in_both_collectors(self):
        for value in (
            "invalid", "00:invalid", "invalid:00", "nan", "inf",
            "1--01:02", "00:1e2", "9" * 400 + "-00:00",
            "1-2-03:04", "00::01", "00:01:02:03",
            "00:1_2", "00:+1", "00:-1", "00:01.", "00:.01",
            "1.5:02", "01:2.5:03", "00:NaN", "00:Infinity",
            "00:０１", "０１:02", "٠١:02", "00:٠٢", "１-00:00",
        ):
            with self.subTest(value=value):
                self.assertIsNone(watch.duration(value))
                line = (f"42 2 90.0 6000 00:05 {value} Sun Nov 22 10:00:00 2026 "
                        "swift-frontend /Toolchain/swift-frontend -module-name Example "
                        "-primary-file /private/Example.swift")
                legacy = watch.processes(
                    lambda *args, **kwargs: subprocess.CompletedProcess([], 1, "", ""))
                self.assertEqual(legacy["unavailable"], "process-command-failed")
                legacy = watch.processes(
                    lambda *args, **kwargs: subprocess.CompletedProcess([], 0, line, ""))
                self.assertEqual(legacy["unavailable"], "invalid-process-duration")
                self.assertEqual(legacy["counts"], {})
                self.assertEqual(legacy["largest"], [])
                self.assertNotIn("private", json.dumps(legacy))
                parsed = watch.processes(popen=self.pipe_fixture(f"print({line!r})"))
                self.assertEqual(parsed["unavailable"], "invalid-process-duration")
                self.assertEqual(parsed["counts"], {})
                self.assertEqual(parsed["largest"], [])
                self.assertNotIn("private", json.dumps(parsed))

    def test_partial_successful_listing_retries_without_lstart(self):
        calls = []
        def partial_stream(command, **kwargs):
            calls.append(command)
            source = "print('incomplete row')"
            if "lstart=" not in command[2]:
                source = ("print('42 2 90.0 6000 00:05 00:01.50 swift-frontend "
                          "/Toolchain/swift-frontend -module-name Example -primary-file /private/Example.swift')")
            return self.pipe_fixture(source)(command, **kwargs)
        parsed = watch.processes(popen=partial_stream)
        self.assertEqual(len(calls), 2)
        frontend = parsed["largest"][0]
        self.assertIsNone(frontend["process_start"])
        self.assertEqual(frontend["process_start_status"], "uncertain")
        self.assertEqual(frontend["cpu_seconds"], 1.5)

        without_start = ("42 2 90.0 6000 00:05 00:01.50 /Toolchain/swift-frontend "
                         "-module-name Example -primary-file /private/Example.swift")
        legacy_calls = []
        def partial_legacy(command, **kwargs):
            legacy_calls.append(command)
            output = "incomplete row"
            if "lstart=" not in command[2]:
                output = without_start
            return subprocess.CompletedProcess(command, 0, output, "")
        parsed = watch.processes(partial_legacy)
        self.assertEqual(len(legacy_calls), 2)
        self.assertFalse(any("lstart=" in field for field in legacy_calls[1]))
        self.assertNotIn("unavailable", parsed)
        frontend = parsed["largest"][0]
        self.assertIsNone(frontend["process_start"])
        self.assertEqual(frontend["process_start_status"], "uncertain")
        self.assertEqual(frontend["primary_source"], "Example.swift")
        self.assertEqual(frontend["cpu_seconds"], 1.5)
        history = {(42, "Sun Nov 22 10:00:00 2026"): watch.new_lifetime(self.frontend(), 85.0)}
        watch.track_frontends(history, parsed["frontend_candidates"], 100.0)
        self.assertEqual(set(history), {(42, "Sun Nov 22 10:00:00 2026"), (42, None)})
        self.assertEqual(history[(42, None)]["continuity"], "uncertain")
        self.assertIsNone(history[(42, None)]["process_start"])
        watch.track_frontends(history, parsed["frontend_candidates"], 115.0)
        self.assertEqual(history[(42, None)]["sample_count"], 2)
        self.assertEqual(history[(42, None)]["continuity"], "uncertain")
        self.assertEqual(history[(42, "Sun Nov 22 10:00:00 2026")]["sample_count"], 1)

        failed_legacy_calls = []
        def failed_legacy(command, **kwargs):
            failed_legacy_calls.append(command)
            if "lstart=" not in command[2]:
                return subprocess.CompletedProcess(command, 1, "", "ps failed")
            return subprocess.CompletedProcess(command, 0, "incomplete row", "")
        parsed = watch.processes(failed_legacy)
        self.assertEqual(len(failed_legacy_calls), 2)
        self.assertEqual(parsed["unavailable"], "process-command-failed")
        self.assertEqual(parsed["process_start_status"], "uncertain")
        self.assertEqual(parsed["largest"], [])
        self.assertEqual(parsed["frontend_candidates"], [])

    def test_process_snapshot_keeps_all_frontend_candidates_outside_largest(self):
        rows = [(pid, 2, "90.0", 9000 - pid, 5.0, 1.5,
                 "Sun Nov 22 10:00:00 2026", "/Toolchain/swift-frontend -module-name Example")
                for pid in range(1, 11)]
        summary = watch.process_snapshot(rows)
        self.assertEqual(len(summary["largest"]), watch.MAX_PROCESSES)
        self.assertEqual(len(summary["frontend_candidates"]), 10)
        history = {}
        watch.track_frontends(history, summary["frontend_candidates"], 100.0)
        self.assertEqual(len(history), 10)
        self.assertEqual(watch.final_frontend_history(history)["status"], "complete")

    def test_frontend_candidate_and_history_capacity_is_explicitly_truncated(self):
        for collector in ("rows", "stream"):
            with self.subTest(collector=collector):
                rows = [(pid, 2, "90.0", 1000, 5.0, 1.5,
                         "Sun Nov 22 10:00:00 2026", "/Toolchain/swift-frontend -module-name Example")
                        for pid in range(1, watch.MAX_FRONTEND_LIFETIMES + 3)]
                if collector == "rows":
                    summary = watch.process_snapshot(rows)
                else:
                    text = "\n".join(
                        f"{pid} 2 90.0 1000 00:05 00:01.50 Sun Nov 22 10:00:00 2026 "
                        "swift-frontend /Toolchain/swift-frontend -module-name Example"
                        for pid in range(1, watch.MAX_FRONTEND_LIFETIMES + 3))
                    summary = watch.processes(popen=self.pipe_fixture(f"print({text!r})"))
                self.assertEqual(len(summary["frontend_candidates"]), watch.MAX_FRONTEND_LIFETIMES)
                self.assertTrue(summary["candidate_truncated"])
                with tempfile.TemporaryDirectory() as directory:
                    history = {}
                    def fake_output(command, **kwargs):
                        return subprocess.CompletedProcess(command, 0, "", "")
                    def fake_processes(*args, **kwargs):
                        return dict(summary)
                    with mock.patch.object(watch, "processes", side_effect=fake_processes):
                        record = watch.sample(Path(directory), "final", runner=fake_output, final=True,
                                              frontend_history=history, monotonic=lambda: 100.0)
                    final = record["swift_frontend_lifetimes"]
                    self.assertEqual(final["status"], "truncated")
                    self.assertLessEqual(len(final["records"]), watch.MAX_FINAL_FRONTEND_RECORDS)
                    self.assertGreaterEqual(final["omitted_records"], 1)
                    self.assertNotIn("frontend_candidates", record["processes"])
                    self.assertNotIn("/Toolchain", (Path(directory) / "final.json").read_text())

    def test_streaming_command_failure_after_partial_stdout_has_no_candidates(self):
        row = ("42 2 90.0 6000 00:05 00:01.50 Sun Nov 22 10:00:00 2026 "
               "swift-frontend /Toolchain/swift-frontend -module-name Example")
        source = f"print({row!r}); raise SystemExit(1)"
        parsed = watch.processes(popen=self.pipe_fixture(source))
        self.assertEqual(parsed["unavailable"], "process-command-failed")
        self.assertEqual(parsed["largest"], [])
        self.assertEqual(parsed["frontend_candidates"], [])

    def test_streaming_history_keeps_frontends_beyond_largest(self):
        rows = "\n".join(
            f"{pid} 2 90.0 {9000 - pid} 00:05 00:01.50 Sun Nov 22 10:00:00 2026 "
            "swift-frontend /Toolchain/swift-frontend -module-name Example" for pid in range(1, 11))
        summary = watch.processes(popen=self.pipe_fixture(f"print({rows!r})"))
        self.assertEqual(len(summary["largest"]), watch.MAX_PROCESSES)
        history = {}
        watch.track_frontends(history, summary["frontend_candidates"], 100.0)
        self.assertEqual(len(history), 10)

    def test_sample_tracks_all_candidates_without_serializing_them(self):
        rows = "\n".join(
            f"{pid} 2 90.0 {9000 - pid} 00:05 00:01.50 Sun Nov 22 10:00:00 2026 "
            "swift-frontend /Toolchain/swift-frontend -module-name Example" for pid in range(1, 11))
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def fake_output(command, **kwargs):
                if command == ["memory_pressure"]:
                    return subprocess.CompletedProcess(command, 0, "System-wide memory free percentage: 50%", "")
                if command == ["vm_stat"]:
                    return subprocess.CompletedProcess(command, 0, "Pages occupied by compressor: 0.", "")
                return subprocess.CompletedProcess(command, 0, "", "")
            history = {}
            record = watch.sample(root, "sample", runner=fake_output, frontend_history=history,
                                  monotonic=lambda: 100.0, popen=self.pipe_fixture(f"print({rows!r})"))
            self.assertEqual(len(record["processes"]["largest"]), watch.MAX_PROCESSES)
            self.assertEqual(len(history), 10)
            self.assertNotIn("frontend_candidates", record["processes"])
            self.assertNotIn("candidate_truncated", record["processes"])
            self.assertNotIn("/Toolchain", (root / "sample.json").read_text())

    def test_partial_lstart_identity_falls_back_for_both_collectors(self):
        mixed = ("42 2 90.0 6000 00:05 00:01.50 Sun Nov 22 10:00:00 2026 "
                 "swift-frontend /Toolchain/swift-frontend -module-name Example\n"
                 "1 0 0.0 100 10:00 00:00.01 launchd /sbin/launchd\n")
        stream_calls = []
        def mixed_stream(command, **kwargs):
            stream_calls.append(command)
            source = f"print({mixed!r}, end='')"
            if "lstart=" not in command[2]:
                source = "print('invalid no-start row')"
            return self.pipe_fixture(source)(command, **kwargs)
        parsed = watch.processes(popen=mixed_stream)
        self.assertEqual(len(stream_calls), 2)
        self.assertFalse(any("lstart=" in field for field in stream_calls[1][2]))
        self.assertEqual(parsed["unavailable"], "invalid-process-output")
        self.assertEqual(parsed["largest"], [])

        calls = []
        def partial_legacy(command, **kwargs):
            calls.append(command)
            output = mixed
            if "lstart=" not in command[2]:
                output = "invalid no-start row\n"
            return subprocess.CompletedProcess(command, 0, output, "")
        parsed = watch.processes(partial_legacy)
        self.assertEqual(len(calls), 2)
        self.assertNotIn("lstart=", calls[1])
        self.assertEqual(parsed["unavailable"], "invalid-process-output")
        self.assertEqual(parsed["largest"], [])

        valid_frontend = ("42 2 90.0 6000 00:05 00:01.50 swift-frontend "
                          "/Toolchain/swift-frontend -module-name Example")
        for collector in ("legacy", "stream"):
            with self.subTest(collector=collector):
                calls = []
                def partial_second(command, **kwargs):
                    calls.append(command)
                    text = mixed if "lstart=" in command[2] else valid_frontend + "\nmalformed row\n"
                    if collector == "legacy":
                        return subprocess.CompletedProcess(command, 0, text, "")
                    return self.pipe_fixture(f"print({text!r}, end='')")(command, **kwargs)
                if collector == "legacy":
                    parsed = watch.processes(partial_second)
                else:
                    parsed = watch.processes(popen=partial_second)
                self.assertEqual(len(calls), 2)
                self.assertEqual(parsed["unavailable"], "invalid-process-output")
                self.assertEqual(parsed["largest"], [])
                self.assertEqual(parsed["frontend_candidates"], [])

                with tempfile.TemporaryDirectory() as directory:
                    history = {}
                    def fake_output(command, **kwargs):
                        return subprocess.CompletedProcess(command, 0, "", "")
                    with mock.patch.object(watch, "processes", return_value=parsed):
                        record = watch.sample(Path(directory), "final", runner=fake_output,
                                              final=True, frontend_history=history, monotonic=lambda: 100.0)
                    self.assertEqual(history, {"truncated": True})
                    self.assertEqual(record["swift_frontend_lifetimes"]["status"], "truncated")
                    self.assertEqual(record["swift_frontend_lifetimes"]["records"], [])

    def test_empty_lstart_retries_once_and_preserves_second_errors(self):
        for collector in ("legacy", "stream"):
            with self.subTest(collector=collector):
                calls = []
                def empty_then_oserror(command, **kwargs):
                    calls.append(command)
                    if len(calls) == 1:
                        if collector == "legacy":
                            return subprocess.CompletedProcess(command, 0, "", "")
                        return self.pipe_fixture("")(command, **kwargs)
                    raise OSError("second collector failed")
                if collector == "legacy":
                    parsed = watch.processes(empty_then_oserror)
                else:
                    parsed = watch.processes(popen=empty_then_oserror)
                self.assertEqual(len(calls), 2)
                self.assertNotIn("lstart=", calls[1])
                self.assertEqual(parsed["unavailable"], "OSError")
                self.assertEqual(parsed["process_start_status"], "uncertain")
                self.assertEqual(parsed["largest"], [])
                self.assertEqual(parsed["frontend_candidates"], [])

    def test_incomplete_no_start_listing_is_explicitly_unavailable(self):
        incomplete = "incomplete row"
        parsed = watch.processes(popen=self.pipe_fixture(f"print({incomplete!r})"))
        self.assertEqual(parsed["unavailable"], "invalid-process-output")
        self.assertEqual(parsed["largest"], [])

        parsed = watch.processes(
            lambda *args, **kwargs: subprocess.CompletedProcess([], 0, incomplete, ""))
        self.assertEqual(parsed["unavailable"], "invalid-process-output")
        self.assertEqual(parsed["largest"], [])

    def test_process_summary_keeps_the_eight_largest_compiler_entries(self):
        result = subprocess.CompletedProcess([], 0, "\n".join(
            f"  {pid}  2 1.0 {pid * 1000} 00:01 00:00.01 /Toolchain/usr/bin/swift-frontend" for pid in range(10, 22)), "")
        def legacy_ps(command, **kwargs):
            if "lstart=" in command:
                return subprocess.CompletedProcess(command, 1, "", "lstart unavailable")
            return result
        parsed = watch.processes(legacy_ps)
        self.assertEqual(watch.MAX_PROCESSES, 8)
        self.assertEqual(parsed["counts"], {"swift-frontend": 12})
        self.assertEqual(len(parsed["largest"]), 8)
        self.assertEqual([entry["pid"] for entry in parsed["largest"]], list(range(21, 13, -1)))
        self.assertEqual(sum(entry["name"] == "swift-frontend" for entry in parsed["largest"]), 8)

    def test_process_start_identity_is_parsed_from_bounded_ps_output(self):
        line = ("42 2 90.0 10000 01:10 00:02.50 Sun Nov 22 10:00:00 2026 "
                "/Toolchain/swift-frontend -module-name Example -primary-file /tmp/Example.swift")
        parsed = watch.processes(lambda *args, **kwargs: subprocess.CompletedProcess([], 0, line, ""))
        frontend = parsed["largest"][0]
        self.assertEqual(frontend["process_start"], "Sun Nov 22 10:00:00 2026")
        self.assertEqual(frontend["process_start_status"], "known")
        self.assertEqual(frontend["primary_source"], "Example.swift")
        self.assertNotIn("/tmp", json.dumps(parsed))

    def test_lstart_success_with_legacy_output_does_not_reuse_the_same_listing(self):
        line = "42 2 90.0 10000 01:10 00:02.50 /Toolchain/swift-frontend -module-name Example"
        calls = []
        def runner(command, **kwargs):
            calls.append(command)
            return subprocess.CompletedProcess(command, 0, line, "")
        parsed = watch.processes(runner)
        self.assertEqual(len(calls), 2)
        self.assertFalse(any("lstart=" in field for field in calls[1]))
        self.assertNotIn("unavailable", parsed)
        frontend = parsed["largest"][0]
        self.assertIsNone(frontend["process_start"])
        self.assertEqual(frontend["process_start_status"], "uncertain")
        self.assertEqual(frontend["target_module"], "Example")

    def test_process_rows_reject_an_invalid_start_without_guessing_argv(self):
        line = "42 2 90.0 10000 01:10 00:02.50 not a process start time /Toolchain/swift-frontend"
        self.assertEqual(watch.process_rows(line, True), [])
        self.assertEqual(watch.process_rows("not a ps row", False), [])

    def test_lstart_timeout_and_oserror_do_not_retry_legacy(self):
        for error in (subprocess.TimeoutExpired(["ps"], 10), OSError("collector unavailable")):
            with self.subTest(error=type(error).__name__):
                calls = []
                def failing(command, **kwargs):
                    calls.append(command)
                    raise error
                self.assertEqual(watch.processes(failing)["unavailable"], type(error).__name__)
                self.assertEqual(len(calls), 1)
                self.assertIn("lstart=", calls[0][2])

    def test_process_failures_and_malformed_legacy_output_are_explicit(self):
        for result in (subprocess.CompletedProcess([], 1, "", "ps failed"),
                       subprocess.CompletedProcess([], 0, "x" * (watch.MAX_OUTPUT_BYTES + 1), "")):
            with self.subTest(result=result.returncode):
                self.assertIn("unavailable", watch.processes(lambda *args, **kwargs: result))
        calls = []
        def legacy_malformed(command, **kwargs):
            calls.append(command)
            return subprocess.CompletedProcess(command, 0, "not a ps row", "")
        parsed = watch.processes(legacy_malformed)
        self.assertEqual(parsed["unavailable"], "invalid-process-output")
        self.assertEqual(parsed["largest"], [])
        self.assertEqual(len(calls), 2)
        self.assertFalse(any("lstart=" in field for field in calls[1]))

    @staticmethod
    def frontend(pid=42, start="Sun Nov 22 10:00:00 2026", cpu=1.5, elapsed=5.0, source="First.swift"):
        return {"pid": pid, "ppid": 2, "cpu_percent": "10.0", "rss_kib": 1000,
                "name": "swift-frontend", "cpu_seconds": cpu, "elapsed_seconds": elapsed,
                "process_start": start, "process_start_status": "known" if start else "uncertain",
                "target_module": "Example", "primary_source": source, "output": "First.o"}

    def test_frontend_identity_is_selected_and_sanitized_never_raw_argv(self):
        identity = watch.swift_frontend_identity(
            "/tool chain/swift-frontend -frontend -module-name ExampleModule "
            "-primary-file '/private source/Primary File.swift' -o /tmp/build/Example.o "
            "-sdk /private/toolchain -DSECRET_TOKEN Example.swift Other.swift")
        self.assertEqual(identity, {"target_module": "ExampleModule", "primary_source": "Primary_File.swift",
                                    "output": "Example.o"})
        serialized = json.dumps(identity)
        for forbidden in ("private", "toolchain", "SECRET", "Other.swift", "/tmp"):
            self.assertNotIn(forbidden, serialized)
        self.assertEqual(watch.swift_frontend_identity("/bin/swift-frontend -module-name 'not a module' -primary-file Secret.swift"),
                         {"target_module": "redacted", "primary_source": "redacted", "output": "unknown"})
        self.assertEqual(watch.swift_frontend_identity(None)["target_module"], "unavailable")
        self.assertEqual(watch.swift_frontend_identity("-module-name 'unterminated")["primary_source"], "redacted")
        self.assertEqual(watch.swift_frontend_identity("/bin/swift-frontend -module-name bad-token -primary-file /tmp/Ok.swift")["target_module"],
                         "redacted")
        self.assertEqual(watch.swift_frontend_identity("/bin/swift-frontend -primary-file /tmp/" + "x" * 300 + ".swift")["primary_source"],
                         "redacted")
        self.assertEqual(watch.safe_identity("/tmp/.hidden"), "redacted")

    def test_unquoted_space_in_source_path_is_unavailable_not_a_fake_basename(self):
        identity = watch.swift_frontend_identity(
            "/bin/swift-frontend -module-name Example -primary-file /tmp/Project File.swift -o /tmp/Example.o")
        self.assertEqual(identity, {"target_module": "unavailable", "primary_source": "unavailable",
                                    "output": "unavailable"})
        self.assertNotIn("Project", json.dumps(identity))

    def test_pid_first_last_cpu_elapsed_and_reuse_are_bounded(self):
        history = {}
        first = self.frontend()
        watch.track_frontends(history, [first], 100.0)
        watch.track_frontends(history, [self.frontend(cpu=4.75, elapsed=20.0)], 115.0)
        record = history[(42, first["process_start"])]
        self.assertEqual(record["first_seen_monotonic"], 100.0)
        self.assertEqual(record["last_seen_monotonic"], 115.0)
        self.assertEqual(record["first_cpu_seconds"], 1.5)
        self.assertEqual(record["last_cpu_seconds"], 4.75)
        self.assertEqual(record["sample_count"], 2)
        self.assertEqual(record["continuity"], "known")
        self.assertEqual(record["first_identity"]["primary_source"], "First.swift")
        replacement = self.frontend(start="Sun Nov 22 10:01:00 2026", cpu=0.1, elapsed=1.0)
        watch.track_frontends(history, [replacement], 130.0)
        self.assertEqual(len(history), 2)
        self.assertEqual(history[(42, first["process_start"])]["last_seen_monotonic"], 115.0)
        self.assertEqual(history[(42, replacement["process_start"])]["first_seen_monotonic"], 130.0)
        watch.track_frontends(history, [self.frontend(start=None, cpu=0.0)], 140.0)
        self.assertEqual(history[(42, None)]["continuity"], "uncertain")

    def test_consecutive_primary_files_do_not_restart_a_process(self):
        history = {}
        watch.track_frontends(history, [self.frontend()], 100.0)
        watch.track_frontends(history, [self.frontend(cpu=3.0, elapsed=20.0, source="Second.swift")], 115.0)
        record = history[(42, "Sun Nov 22 10:00:00 2026")]
        self.assertEqual(record["first_seen_monotonic"], 100.0)
        self.assertEqual(record["last_seen_monotonic"], 115.0)
        self.assertEqual(record["first_identity"]["primary_source"], "First.swift")
        self.assertEqual(record["last_identity"]["primary_source"], "Second.swift")
        self.assertEqual(record["identity_changes"], 1)
        self.assertEqual(len(history), 1)

    def test_known_unknown_known_start_does_not_backfill_or_merge_lifetimes(self):
        history = {}
        start = "Sun Nov 22 10:00:00 2026"
        watch.track_frontends(history, [self.frontend(start=start, cpu=1.0)], 100.0)
        watch.track_frontends(history, [self.frontend(start=None, cpu=2.0)], 115.0)
        watch.track_frontends(history, [self.frontend(start=start, cpu=3.0)], 130.0)
        self.assertEqual(len(history), 2)
        self.assertEqual(history[(42, None)]["continuity"], "uncertain")
        self.assertEqual(history[(42, None)]["sample_count"], 1)
        self.assertIsNone(history[(42, None)]["process_start"])
        self.assertEqual(history[(42, start)]["sample_count"], 2)
        self.assertEqual(history[(42, start)]["last_cpu_seconds"], 3.0)

    def test_duplicate_pid_in_one_sample_preserves_the_first_observation(self):
        history = {}
        watch.track_frontends(history, [self.frontend(), self.frontend(cpu=99.0)], 100.0)
        record = history[(42, "Sun Nov 22 10:00:00 2026")]
        self.assertEqual(record["first_seen_monotonic"], 100.0)
        self.assertEqual(record["last_seen_monotonic"], 100.0)
        self.assertEqual(record["last_cpu_seconds"], 1.5)
        self.assertEqual(record["sample_count"], 1)
        self.assertTrue(record["duplicate_pid_in_sample"])
        self.assertEqual(len(history), 1)

    def test_duplicate_pid_with_different_start_preserves_only_first_and_marks_uncertain(self):
        history = {}
        other_start = "Sun Nov 22 10:01:00 2026"
        watch.track_frontends(history, [self.frontend(), self.frontend(start=other_start, cpu=2.5)], 100.0)
        self.assertEqual(set(history), {(42, "Sun Nov 22 10:00:00 2026")})
        record = history[(42, "Sun Nov 22 10:00:00 2026")]
        self.assertEqual(record["first_seen_monotonic"], 100.0)
        self.assertEqual(record["last_seen_monotonic"], 100.0)
        self.assertEqual(record["last_cpu_seconds"], 1.5)
        self.assertTrue(record["duplicate_pid_in_sample"])
        self.assertEqual(record["continuity"], "uncertain")

    def test_pid_reuse_chain_and_full_bound_are_explicitly_truncated(self):
        history = {}
        for index in range(watch.MAX_FRONTEND_LIFETIMES + 10):
            start = f"Sun Nov 22 10:{index // 60:02d}:{index % 60:02d} 2026"
            watch.track_frontends(history, [self.frontend(start=start)], 100.0 + index)
        self.assertEqual(len([key for key in history if key != "truncated"]), watch.MAX_FRONTEND_LIFETIMES)
        self.assertTrue(history["truncated"])
        summary = watch.final_frontend_history(history)
        self.assertEqual(len(summary["records"]), watch.MAX_FINAL_FRONTEND_RECORDS)
        self.assertEqual(summary["status"], "truncated")
        self.assertGreater(summary["omitted_records"], watch.MAX_FRONTEND_LIFETIMES - watch.MAX_FINAL_FRONTEND_RECORDS)
        self.assertEqual(watch.MAX_FINAL_JSON_BYTES, 16 * 1024)
        self.assertEqual(watch.MAX_OUTPUT_BYTES, 64 * 1024)
        self.assertEqual(watch.MAX_PROCESSES, 8)

    def test_activity_log_path_allowlist_does_not_copy_or_export_contents(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            derived = root / "DerivedData"
            logs = derived / "Logs" / "Build"
            logs.mkdir(parents=True)
            (logs / "Example.xcactivitylog").write_bytes(b"synthetic bounded evidence")
            (logs / "Other.xcactivitylog").write_bytes(b"other")
            (logs / "ignored.txt").write_text("not listed")
            (logs / "symlink.xcactivitylog").symlink_to(logs / "Example.xcactivitylog")
            nested = logs / "nested"
            nested.mkdir()
            (nested / "nested.xcactivitylog").write_bytes(b"not listed")
            (derived / "Logs" / "other.xcactivitylog").write_bytes(b"not listed")
            paths = watch.activity_log_paths(derived)
            self.assertEqual({entry["name"] for entry in paths}, {"Example.xcactivitylog", "Other.xcactivitylog"})
            self.assertNotIn(str(root), json.dumps(paths))
            for index in range(5):
                (logs / f"extra-{index}.xcactivitylog").write_bytes(b"bounded")
            self.assertEqual(len(watch.activity_log_paths(derived)), watch.MAX_ACTIVITY_LOGS)
            self.assertEqual(watch.activity_log_paths(root / "missing"), [])
            self.assertEqual(watch.activity_log_paths(root / "relative"), [])
            linked_logs = root / "linked"
            linked_logs.mkdir()
            (linked_logs / "outside.xcactivitylog").write_bytes(b"not listed")
            (derived / "Logs" / "Build").rename(derived / "Logs" / "real-build")
            (derived / "Logs" / "Build").symlink_to(derived / "Logs" / "real-build", target_is_directory=True)
            self.assertEqual(watch.activity_log_paths(derived), [])
            self.assertFalse(hasattr(watch, "retain_activity_logs"))

    def test_observe_retains_first_last_history_in_final_sample(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            calls = []
            def fake_sample(destination, label, runner, **kwargs):
                calls.append((label, kwargs))
            with mock.patch.object(watch, "metadata", return_value={}), \
                    mock.patch.object(watch, "sample", side_effect=fake_sample):
                times = iter([0, 0, 0, 1])
                watch.observe(root, 1, root / "stop", monotonic=lambda: next(times), sleep=lambda _: None,
                              derived_data=Path("/fixture/DerivedData"))
            self.assertEqual([label for label, _ in calls], ["sample-000", "final"])
            self.assertIs(calls[0][1]["frontend_history"], calls[1][1]["frontend_history"])
            self.assertEqual(calls[0][1]["derived_data"], Path("/fixture/DerivedData"))

    def test_bounded_command_rejects_rather_than_truncates_evidence(self):
        result = subprocess.CompletedProcess([], 0, "x" * (watch.MAX_OUTPUT_BYTES + 1), "")
        self.assertEqual(watch.output(["ps"], runner=lambda *args, **kwargs: result),
                         {"command": ["ps"], "unavailable": "output-limit"})

    def test_failures_are_explicit_and_do_not_invent_resource_verdicts(self):
        def unavailable(*args, **kwargs):
            raise OSError("unavailable")
        metadata = watch.metadata(unavailable)
        self.assertTrue(all("unavailable" in item for item in metadata.values()))
        self.assertEqual(watch.processes(unavailable)["unavailable"], "OSError")

    def test_observer_stops_and_retains_final_sample(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stop = root / "stop"
            stop.write_text("")
            with mock.patch.object(watch, "metadata", return_value={}), \
                    mock.patch.object(watch, "sample") as sample:
                watch.observe(root, 1800, stop, monotonic=lambda: 0, sleep=lambda _: None)
            sample.assert_called_once_with(root, "final", mock.ANY, final=True, derived_data=None,
                                          frontend_history=mock.ANY, monotonic=mock.ANY)
            self.assertIn("periodic samples", (root / "observer.status.txt").read_text())

    def test_periodic_samples_stop_at_120_and_retain_only_a_bounded_final(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with mock.patch.object(watch, "metadata", return_value={}), \
                    mock.patch.object(watch, "sample") as sample:
                watch.observe(root, 3600, root / "stop", monotonic=lambda: 0, sleep=lambda _: None)
            self.assertEqual(sample.call_count, 121)
            self.assertEqual(sample.call_args_list[-1], mock.call(root, "final", mock.ANY, final=True,
                                                                 derived_data=None, frontend_history=mock.ANY,
                                                                 monotonic=mock.ANY))
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
            self.assertNotIn("frontend_candidates", record)
            self.assertNotIn("candidate_truncated", record)
            self.assertLess((root / "sample.json").stat().st_size, 1024)
            self.assertLessEqual(watch.MAX_PERIODIC_SAMPLES, 120)
            self.assertLessEqual(watch.MAX_OUTPUT_BYTES, 64 * 1024)

    def test_final_sample_serializes_compiler_history_and_no_raw_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            result = subprocess.CompletedProcess([], 0,
                "42 2 90.0 10000 1-02:03:04 01:02:03.50 Sun Nov 22 10:00:00 2026 "
                "/Toolchain/swift-frontend -module-name Example -primary-file /private/source/Example.swift -o /tmp/Example.o", "")
            history = {}
            def runner(command, **kwargs):
                if command[0] == "ps":
                    return result
                return subprocess.CompletedProcess(command, 0, "", "")
            record = watch.sample(root, "final", runner=runner,
                                  final=True, frontend_history=history, monotonic=lambda: 100.0)
            self.assertEqual(record["swift_frontend_lifetimes"]["records"][0]["first_elapsed_seconds"], 93784.0)
            self.assertEqual(record["swift_frontend_lifetimes"]["records"][0]["last_cpu_seconds"], 3723.5)
            self.assertEqual(record["swift_frontend_lifetimes"]["records"][0]["first_identity"]["target_module"], "Example")
            serialized = (root / "final.json").read_text()
            self.assertNotIn("/private", serialized)
            self.assertNotIn("/tmp", serialized)
            self.assertNotIn("args", serialized)
            self.assertNotIn("unavailable", record)
            self.assertEqual(record["xcactivitylogs"], [])

    def test_atomic_final_size_budget_truncates_history_and_preserves_old_file(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = {}
            for index in range(watch.MAX_FRONTEND_LIFETIMES):
                entry = self.frontend(pid=index, start=f"Sun Nov 22 10:{index // 60:02d}:{index % 60:02d} 2026")
                entry["target_module"] = "M" * 80
                entry["primary_source"] = "S" * 128
                entry["output"] = "O" * 128
                watch.track_frontends(history, [entry], 100.0 + index)
            old = {"previous": "complete evidence"}
            root.joinpath("final.json").write_text(json.dumps(old))
            result = watch.write_json(root, "final", {"swift_frontend_lifetimes": watch.final_frontend_history(history)})
            data = root.joinpath("final.json").read_bytes()
            self.assertLessEqual(len(data), watch.MAX_FINAL_JSON_BYTES)
            self.assertLessEqual(len(result["swift_frontend_lifetimes"]["records"]), watch.MAX_FINAL_FRONTEND_RECORDS)
            self.assertFalse(root.joinpath("final.pending").exists())
            if result["swift_frontend_lifetimes"]["status"] == "truncated":
                self.assertGreater(result["swift_frontend_lifetimes"]["omitted_records"], 0)
            self.assertEqual(json.loads(data)["swift_frontend_lifetimes"]["status"],
                             result["swift_frontend_lifetimes"]["status"])

    def test_unserializable_final_input_fails_closed_before_atomic_replace(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            root.joinpath("final.json").write_text('{"previous":"complete evidence"}\n')
            result = watch.write_json(root, "final", {"unserializable": object()})
            self.assertEqual(result["status"], "unavailable")
            self.assertIn("byte budget", result["unavailable"])
            self.assertEqual(json.loads(root.joinpath("final.json").read_text())["status"], "unavailable")
            self.assertLessEqual(root.joinpath("final.json").stat().st_size, watch.MAX_FINAL_JSON_BYTES)
            self.assertFalse(root.joinpath("final.pending").exists())

    def test_final_publishes_history_before_a_slow_final_collector(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = {}
            watch.track_frontends(history, [self.frontend()], 100.0)
            def slow_collector(command, **kwargs):
                published = json.loads(root.joinpath("final.json").read_text())
                self.assertEqual(published["swift_frontend_lifetimes"]["records"][0]["pid"], 42)
                self.assertIn("unavailable", published)
                raise subprocess.TimeoutExpired(command, kwargs["timeout"])
            record = watch.sample(root, "final", runner=slow_collector, final=True,
                                  frontend_history=history, monotonic=lambda: 115.0)
            self.assertIn("unavailable", record)
            published = json.loads(root.joinpath("final.json").read_text())
            self.assertEqual(published["swift_frontend_lifetimes"]["records"][0]["pid"], 42)
            self.assertIn("unavailable", published)
            self.assertFalse(root.joinpath("final.pending").exists())

    def test_normal_stop_slow_collector_retains_history_and_reaps_process_group(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stop = root / "stop"
            stop.touch()
            history = {}
            watch.track_frontends(history, [self.frontend()], 100.0)
            process = subprocess.Popen([
                "/usr/bin/python3", "-I", "-c",
                "import os,signal,sys,time; "
                "sys.path.insert(0, sys.argv[1]); import apple_build_watch as watch; "
                "watch.sample(__import__('pathlib').Path(sys.argv[2]), 'final', runner=lambda *a, **k: time.sleep(30), "
                "final=True, frontend_history=__import__('json').loads(sys.argv[3]))",
                str(ROOT / "scripts/ci"), str(root), json.dumps({str(key): value for key, value in history.items()}),
            ], start_new_session=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                deadline = time.monotonic() + 5
                while time.monotonic() < deadline:
                    if root.joinpath("final.json").exists():
                        published = json.loads(root.joinpath("final.json").read_text())
                        if published.get("swift_frontend_lifetimes", {}).get("records"):
                            break
                    if process.poll() is not None:
                        self.fail("final collector exited before its history was published: "
                                  + str(process.communicate()))
                    time.sleep(0.01)
                else:
                    self.fail("history was not published before the slow final collector")
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=5)
                published = json.loads(root.joinpath("final.json").read_text())
                self.assertEqual(published["swift_frontend_lifetimes"]["records"][0]["pid"], 42)
                self.assertIn("unavailable", published)
            finally:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=5)
                process.stdout.close()
                process.stderr.close()

    def test_observer_deadline_accepts_both_lane_budgets_and_rejects_larger_values(self):
        self.assertEqual(watch.MAX_SECONDS, 3600)
        for seconds in (1800, 3600):
            with self.subTest(seconds=seconds), \
                    patch.object(watch, "observe") as observe, \
                    patch.object(sys, "argv", ["apple_build_watch.py", "--output", "/tmp/output",
                                               "--stop", "/tmp/stop", "--seconds", str(seconds)]), \
                    patch.object(sys, "stderr", new=io.StringIO()):
                watch.main()
                observe.assert_called_once_with(Path("/tmp/output"), seconds, Path("/tmp/stop"),
                                               derived_data=None)
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
            (fixture / "xcodegen").write_text(textwrap.dedent('''\
                #!/bin/bash
                printf '%s\\n' "$*" >> "$FIXTURE_XCODEGEN_ARGS"
                if [[ "$*" == *dump* ]]; then
                  /usr/bin/python3 -c 'import json,os;from pathlib import Path;p=json.loads(Path(os.environ["FIXTURE_WHOLE"]).read_text());print(json.dumps(p))'
                fi
            '''))
            (fixture / "xcodegen").chmod(0o755)
            (fixture / "whole.json").write_text(json.dumps(self.offline_project_fixture()))
            for lane, budget in expected_build.items():
                with self.subTest(lane=lane):
                    output = fixture / ("output " + lane)
                    if budget is not None:
                        output.mkdir()
                    watch_args = fixture / ("watch-" + lane)
                    xcode_args = fixture / ("xcode-" + lane)
                    calls = fixture / ("calls-" + lane)
                    env = {**os.environ, "PATH": str(fixture) + ":/usr/bin:/bin",
                           "FIXTURE_WATCH_ARGS": str(watch_args), "FIXTURE_XCODE_ARGS": str(xcode_args),
                           "FIXTURE_WHOLE": str(fixture / "whole.json"),
                           "FIXTURE_XCODEGEN_ARGS": str(fixture / ("xcodegen-" + lane)),
                           "FIXTURE_CALLS": str(calls)}
                    prefix = '''set -euo pipefail
                    ci_run() {
                      local label="$1" budget="$2"; shift 2
                      printf '%s\\n' "$budget" >> "$FIXTURE_CALLS"
                      /usr/bin/python3 "$CI_ROOT/scripts/ci/run.py" --label "$label" --timeout "$budget" -- "$@"
                    }
                    CI_ROOT=''' + shlex.quote(str(ROOT)) + '''
                    OUTPUT=''' + shlex.quote(str(output)) + '''
                    OFFLINE_PROJECT=/fixture/PersonaOfflineUnitTests.xcodeproj
                    SOURCE=/fixture
                    SPM=/fixture/cache
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
                    self.assertEqual(calls.read_text(), "120\n120\n" + str(budget) + "\n")
                    generated = json.loads((output / "PersonaOfflineUnitTests.json").read_text())
                    self.assertEqual(set(generated["targets"]), {
                        "Persona", "PersonaTests", "PersonaWidgets", "PersonaNotify",
                    })
                    self.assertEqual(generated["targets"]["Persona"]["dependencies"],
                                     self.offline_project_fixture()["targets"]["Persona"]["dependencies"][2:])
                    self.assertIn("dump", (fixture / ("xcodegen-" + lane)).read_text())
                    self.assertIn(f"--seconds {budget}", watch_args.read_text())
                    self.assertIn(f"--derived-data {output / 'ios-tests'}", watch_args.read_text())
                    expected_args = [
                        "-project", "/fixture/PersonaOfflineUnitTests.xcodeproj",
                        "-configuration", "Debug",
                        "-clonedSourcePackagesDirPath", "/fixture/cache/SourcePackages",
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
