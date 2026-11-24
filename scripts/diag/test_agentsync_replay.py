import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


class AgentSyncReplayTests(unittest.TestCase):
    def replay(self, *updates):
        return self.replay_messages(*((time, {"type": "revs", "machine_id": "fixture", "revs": revisions})
                                      for time, revisions in updates))

    def replay_messages(self, *updates):
        script = Path(__file__).with_name("agentsync_replay.py")
        with tempfile.TemporaryDirectory() as directory:
            recording = Path(directory) / "push.jsonl"
            recording.write_text("".join(
                json.dumps({"t": time, "msg": message}) + "\n" for time, message in updates
            ))
            return subprocess.run([sys.executable, "-B", str(script), str(recording)], capture_output=True,
                                  text=True, timeout=10)

    def test_zero_span_keeps_counts_without_rates(self):
        for initial, changed in [({"a": 1}, {"a": 2}), ({"a": 1, "b": 1}, {"a": 2, "b": 2})]:
            with self.subTest(changes=len(changed)):
                result = self.replay((0, initial), (30000, changed))
                self.assertEqual(result.returncode, 0, result.stderr)
                count = len(changed)
                self.assertEqual(result.stdout, f"{count} transcript changes over 0.0 min in {count} sessions "
                                                f"(0 while working): pulls {count} before, {count} after "
                                                "(per-minute rates unavailable)\n")

    def test_positive_span_keeps_per_minute_rates(self):
        result = self.replay((0, {"a": 1}), (30000, {"a": 2}), (90000, {"a": 3}))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "2 transcript changes over 1.0 min in 1 sessions (0 while working): "
                                        "pulls 2.0/min before, 2.0/min after\n")

    def test_no_changes_keeps_existing_error(self):
        result = self.replay((0, {"a": 1}))
        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stderr, "no transcript changes in the recording\n")

    def test_working_pacing_uses_state_and_accepts_legacy_status(self):
        for fields, working, after in (({"status": "idle", "state": "working"}, 3, 60),
                                       ({"status": "working", "state": "idle"}, 0, 90),
                                       ({"status": "working"}, 3, 60)):
            with self.subTest(fields=fields):
                result = self.replay_messages(
                    (0, {"type": "sessions", "machine_id": "fixture",
                         "sessions": [{"session_id": "a", "rev": 1, **fields}]}),
                    *((time, {"type": "revs", "machine_id": "fixture", "revs": {"a": rev}})
                      for time, rev in ((1000, 2), (2000, 3), (3000, 4))),
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn(f"({working} while working): pulls 90.0/min before, {after:.1f}/min after", result.stdout)

    def test_completion_releases_a_waiting_pull_without_counting_a_change(self):
        def session(rev, state):
            return {"type": "sessions", "machine_id": "fixture", "sessions": [
                {"session_id": "a", "rev": rev, "state": state, "status": state}]}

        result = self.replay_messages((0, session(1, "working")), (1000, session(2, "working")),
                                      (2000, session(3, "working")), (2500, session(3, "idle")),
                                      (3000, session(4, "idle")))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "3 transcript changes over 0.0 min in 1 sessions (2 while working): "
                                       "pulls 90.0/min before, 90.0/min after\n")


if __name__ == "__main__":
    unittest.main()
