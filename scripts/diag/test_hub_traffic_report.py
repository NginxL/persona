#!/usr/bin/env python3
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("traffic_report", HERE / "hub_traffic_report.py")
report = importlib.util.module_from_spec(spec)
spec.loader.exec_module(report)


class TrafficReportTests(unittest.TestCase):
    def test_platform_surface_and_window(self):
        start, end = report.timestamp("2026-01-01T00:00:00Z"), report.timestamp("2026-01-02T00:00:00Z")
        events = []
        for platform, source, count in (("ios", "app", 80), ("ios", "widget", 30), ("ios", "liveactivity", 20), ("android", "app", 5), ("web", "app", 9)):
            message = 'traffic minute=2026-01-01T01:00:00Z identity=0123456789abcdef platform=%s source=%s platform_basis=user-agent source_basis=user-agent route="GET /api/v1/todos" event=http action=started count=%d body_bytes=0 max_body_bytes=0 elapsed_ms=0' % (platform, source, count)
            events.append(json.dumps({"MESSAGE": message}))
        data = report.aggregate(list(report.rows(events, start, end)))
        self.assertEqual(sum(row["count"] for row in data["hourly"]), 144)
        self.assertEqual(len(data["hourly"]), 5)
        self.assertEqual(data["untagged_bucket_rows"], 0)
        self.assertEqual(data["unknown_platform_requests"], 0)
        self.assertEqual(list(report.rows(events, end, report.timestamp("2026-01-03T00:00:00Z"))), [])

    def test_connection_overlap_and_redaction(self):
        start, end = report.timestamp("2026-01-01T00:00:00Z"), report.timestamp("2026-01-02T00:00:00Z")
        sessions = [{"identity": "0123456789abcdef", "start": "2025-12-31T23:30:00Z", "end": "2026-01-01T00:30:00Z", "reason": "private-note", "heartbeat_interval": "private-token"},
                    {"identity": "0123456789abcdef", "start": "2026-01-01T01:00:00Z", "end": "2026-01-01T01:01:00Z", "reason": "closed", "heartbeat_interval": 20}]
        data = report.connection_report(sessions, start, end)
        self.assertEqual(sum(h["connected_seconds"] for h in data["hourly"]), 1860)
        self.assertEqual(data["mean_completed_in_window_seconds"], 60)
        self.assertNotIn("private", json.dumps(data))

    def test_close_at_adjacent_window_boundary_is_counted_once(self):
        start = report.timestamp("2026-01-01T00:00:00Z")
        boundary = report.timestamp("2026-01-02T00:00:00Z")
        end = report.timestamp("2026-01-03T00:00:00Z")
        sessions = [{"identity": "0123456789abcdef", "start": "2026-01-01T23:59:00Z",
                     "end": "2026-01-02T00:00:00Z", "reason": "closed", "heartbeat_interval": 20}]
        previous = report.connection_report(sessions, start, boundary)
        following = report.connection_report(sessions, boundary, end)
        self.assertEqual(previous["hourly"], [{"hour": "2026-01-01T23:00:00Z", "opens": 1, "closes": 0,
                                              "connected_seconds": 60, "reasons": {}}])
        self.assertEqual(following["hourly"], [{"hour": "2026-01-02T00:00:00Z", "opens": 0, "closes": 1,
                                               "connected_seconds": 0, "reasons": {"closed": 1}}])
        self.assertEqual(previous["heartbeat_intervals"], {"20": 1})
        self.assertEqual(following["heartbeat_intervals"], {})
        self.assertEqual(following["completed_in_window_count"], 0)
        self.assertIsNone(following["mean_completed_in_window_seconds"])

    def test_boundary_close_preserves_other_window_and_identity_rules(self):
        start, end = report.timestamp("2026-01-02T00:00:00Z"), report.timestamp("2026-01-03T00:00:00Z")
        sessions = [{"identity": "0123456789abcdef", "start": "2026-01-01T23:00:00Z",
                     "end": "2026-01-01T23:59:59Z", "heartbeat_interval": 10},
                    {"identity": "0123456789abcdef", "start": "2026-01-01T23:59:00Z",
                     "end": "2026-01-02T00:00:00Z", "reason": "private-note", "heartbeat_interval": 20},
                    {"identity": "0123456789abcdef", "start": "2026-01-02T00:00:00Z",
                     "end": "2026-01-02T00:00:30Z", "reason": "closed", "heartbeat_interval": 30},
                    {"identity": "0123456789abcdef", "start": "2026-01-01T23:59:00Z",
                     "end": None, "heartbeat_interval": 40},
                    {"identity": "0123456789abcdef", "start": "2026-01-03T00:00:00Z",
                     "end": None, "heartbeat_interval": 50}]
        data = report.connection_report(sessions, start, end)
        self.assertEqual(sum(h["opens"] for h in data["hourly"]), 1)
        self.assertEqual(sum(h["closes"] for h in data["hourly"]), 2)
        self.assertEqual(sum(h["connected_seconds"] for h in data["hourly"]), 86430)
        self.assertEqual(data["hourly"][0]["reasons"], {"unknown": 1, "closed": 1})
        self.assertEqual(data["heartbeat_intervals"], {"30": 1, "40": 1})
        self.assertEqual((data["completed_in_window_count"], data["mean_completed_in_window_seconds"]), (1, 30))
        self.assertNotIn("private", json.dumps(data))
        self.assertEqual(report.connection_report(sessions, start, end, "fedcba9876543210")["hourly"], [])

    def test_read_only_query_includes_closes_at_window_start(self):
        start, end = report.timestamp("2026-01-02T00:00:00Z"), report.timestamp("2026-01-03T00:00:00Z")
        args = SimpleNamespace(connections_file=None, container="test-postgres")
        with patch.object(report.subprocess, "run", return_value=SimpleNamespace(returncode=0, stdout="BEGIN\n[]\nCOMMIT\n")) as run:
            self.assertEqual(report.read_connections(args, start, end), [])
        run.assert_called_once()
        self.assertEqual(run.call_args.args[0][:4], ["docker", "exec", "-i", "test-postgres"])
        sql = run.call_args.kwargs["input"]
        self.assertIn("BEGIN READ ONLY; SET LOCAL statement_timeout='15s';", sql)
        self.assertIn("connected_at < '2026-01-03T00:00:00Z'", sql)
        self.assertIn("disconnected_at IS NULL OR disconnected_at >= '2026-01-02T00:00:00Z'", sql)
        self.assertEqual(run.call_args.kwargs["timeout"], 30)

    def test_cli_and_refuses_overwrite(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "journal.jsonl").write_text("")
            (root / "connections.json").write_text("[]")
            command = [sys.executable, "-I", str(HERE / "hub_traffic_report.py"), "--since", "2026-01-01T00:00:00Z", "--until", "2026-01-02T00:00:00Z", "--journal-file", str(root / "journal.jsonl"), "--connections-file", str(root / "connections.json"), "--output", str(root / "out.json")]
            run = subprocess.run(command, text=True, capture_output=True, timeout=10)
            self.assertEqual(run.returncode, 0, run.stderr)
            self.assertTrue(json.loads((root / "out.json").read_text())["full_day"])
            again = subprocess.run(command, text=True, capture_output=True, timeout=10)
            self.assertNotEqual(again.returncode, 0)
            self.assertIn("VERDICT: FAILED", again.stderr)


if __name__ == "__main__":
    unittest.main()
