import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name("transcript_stats.py")


class TranscriptStatsTests(unittest.TestCase):
    def test_overlapping_and_symlinked_roots_count_each_file_once(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            parent = root / "projects"
            nested = parent / "nested"
            nested.mkdir(parents=True)
            (nested / "one.jsonl").write_text('{"type":"user"}\n')
            (parent / "other.jsonl").write_text('{"type":"assistant"}\n' * 2)
            alias = root / "alias"
            alias.symlink_to(nested, target_is_directory=True)
            result = subprocess.run([sys.executable, "-B", str(SCRIPT), str(parent), str(nested), str(alias)],
                                    capture_output=True, text=True, timeout=10, check=True)
        self.assertEqual(result.stdout.splitlines()[0], "2 files, 3 lines")
        self.assertRegex(result.stdout, re.compile(r"^\s*1  claude user\s+.*:1$", re.MULTILINE))
        self.assertRegex(result.stdout, re.compile(r"^\s*2  claude assistant\s+.*:1$", re.MULTILINE))

    def test_export_and_compare_around_runtime_upgrade(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            projects = root / "private-sample-path"
            projects.mkdir()
            transcript = projects / "session.jsonl"
            transcript.write_text(
                '{"type":"user","version":"1.0","message":{"content":"synthetic-body"}}\n'
                '{"type":"assistant","version":"1.0"}\n'
                '{"type":"assistant","version":"1.0"}\n'
            )
            command = [sys.executable, "-B", str(SCRIPT)]
            snapshot = subprocess.check_output(command + ["--json", str(projects)], text=True, timeout=10)
            self.assertNotIn(projects.name, snapshot)
            self.assertNotIn("synthetic-body", snapshot)
            self.assertEqual(json.loads(snapshot), {
                "schema_version": 1, "files": 1, "lines": 3,
                "kinds": {"claude assistant": 2, "claude user": 1}, "versions": {"claude 1.0": 3},
            })
            baseline = root / "before.json"
            baseline.write_text(snapshot)
            compare = command + ["--compare", str(baseline), str(projects)]
            unchanged = subprocess.check_output(compare, text=True, timeout=10)
            self.assertEqual(unchanged.count("No changes."), 2)

            transcript.write_text(
                '{"type":"assistant","version":"2.0"}\n'
                '{"type":"session_meta","payload":{"cli_version":"0.50"}}\n'
                '{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"NewRecord"}}}\n'
            )
            result = subprocess.check_output(compare, text=True, timeout=10)
            current = root / "after.json"
            after = subprocess.check_output(command + ["--json", str(projects)], text=True, timeout=10)
            current.write_text(after)
            # A live scan must not reach these default paths in offline mode.
            (root / ".claude" / "projects" / "unreadable.jsonl").mkdir(parents=True)
            env = dict(os.environ, HOME=str(root), CLAUDE_CONFIG_DIR=str(root / "other-profile"))
            offline = subprocess.check_output(command + ["--compare", str(baseline), "--against", str(current)],
                                              text=True, timeout=10, env=env)
            self.assertEqual(offline, result)
            self.assertEqual(baseline.read_text(), snapshot)
            self.assertEqual(current.read_text(), after)
            self.assertNotIn(projects.name, result)
            self.assertIn("Files: 1 -> 1; lines: 3 -> 3.", result)
            self.assertIn("~ claude assistant: 2 -> 1", result)
            self.assertIn("- claude user: 1", result)
            self.assertIn("+ codex event_msg/item_completed/NewRecord: 1", result)
            self.assertIn("- claude 1.0: 3", result)
            self.assertIn("+ claude 2.0: 1", result)
            self.assertIn("+ codex 0.50: 1", result)

    def test_against_requires_baseline_and_excludes_directories(self):
        with tempfile.TemporaryDirectory() as directory:
            snapshot = Path(directory) / "snapshot.json"
            command = [sys.executable, "-B", str(SCRIPT)]
            compare = ["--compare", str(snapshot)]
            against = ["--against", str(snapshot)]
            for args in (against, ["--json", *against], [*compare, *against, directory]):
                with self.subTest(args=args):
                    result = subprocess.run(command + args, text=True, capture_output=True, timeout=10)
                    self.assertEqual(result.returncode, 2, result.stderr)
                    self.assertEqual(result.stdout, "")
                    self.assertIn("--against requires --compare and no transcript directories", result.stderr)

    def test_invalid_comparison_snapshot_is_an_argument_error(self):
        with tempfile.TemporaryDirectory() as directory:
            baseline = Path(directory) / "baseline.json"
            current = Path(directory) / "current.json"
            valid = {"schema_version": 1, "files": 1, "lines": 1, "kinds": {}, "versions": {}}
            baseline.write_text(json.dumps(valid))
            command = [sys.executable, "-B", str(SCRIPT), "--compare", str(baseline), "--against", str(current)]
            for content in (None, "{", json.dumps(dict(valid, schema_version=2)),
                            json.dumps(dict(valid, versions={"claude 1.0": -1}))):
                with self.subTest(content=content):
                    if content is not None:
                        current.write_text(content)
                    result = subprocess.run(command, text=True, capture_output=True, timeout=10)
                    self.assertEqual(result.returncode, 2, result.stderr)
                    self.assertEqual(result.stdout, "")
                    self.assertIn("cannot read comparison snapshot", result.stderr)
                    self.assertNotIn("Traceback", result.stderr)

    def test_invalid_baselines_are_reported_as_argument_errors(self):
        with tempfile.TemporaryDirectory() as directory:
            baseline = Path(directory) / "baseline.json"
            valid = {"schema_version": 1, "files": 1, "lines": 1, "kinds": {}, "versions": {}}
            command = [sys.executable, "-B", str(SCRIPT), "--compare", str(baseline), directory]
            for content in ("{", "[]", json.dumps(dict(valid, schema_version=2)),
                            json.dumps(dict(valid, files=True)), json.dumps(dict(valid, kinds={"row": -1})),
                            json.dumps(dict(valid, versions=[]))):
                with self.subTest(content=content):
                    baseline.write_text(content)
                    result = subprocess.run(command, text=True, capture_output=True, timeout=10)
                    self.assertEqual(result.returncode, 2, result.stderr)
                    self.assertEqual(result.stdout, "")
                    self.assertIn("baseline", result.stderr)
                    self.assertNotIn("Traceback", result.stderr)

    def test_incomplete_records_do_not_hide_valid_rows_in_either_output(self):
        with tempfile.TemporaryDirectory() as directory:
            transcript = Path(directory) / "session.jsonl"
            transcript.write_text(
                '{"type":"attachment","attachment":[],"version":3}\n'
                '{"type":"event_msg","payload":{"type":"item_completed","item":"missing","cli_version":[]}}\n'
                '{"type":"user","version":"2.0"}\n'
                '{"type":\n'
            )
            command = [sys.executable, "-B", str(SCRIPT), directory]
            text = subprocess.check_output(command, text=True, timeout=10)
            self.assertIn("(bad json)", text)
            self.assertIn(f"{transcript}:4", text)
            snapshot = json.loads(subprocess.check_output(command + ["--json"], text=True, timeout=10))
            self.assertEqual(snapshot["lines"], 4)
            self.assertEqual(snapshot["kinds"]["(bad json)"], 1)
            self.assertEqual(snapshot["kinds"]["claude user"], 1)
            self.assertEqual(snapshot["versions"], {"claude 2.0": 1})


if __name__ == "__main__":
    unittest.main()
