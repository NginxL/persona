"""A release PR can merge only after its exact proposed merge result passes every required platform check."""
import json
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import release_merge_gate as gate

COMMIT = "a" * 40
MERGE = "b" * 40


class ReleaseMergeGateTests(unittest.TestCase):
    def pr(self):
        return {"state": "open", "draft": False, "base": {"ref": "main"}, "head": {"sha": COMMIT, "repo": {"full_name": "nwverse/persona"}},
                "user": {"login": "chyroc"}, "title": "release: v0.9.2-beta.2", "merge_commit_sha": MERGE}

    def successful(self, command, **kwargs):
        if command[:2] == ["gh", "api"] and "/pulls/" in command[2]:
            return json.dumps(self.pr())
        if "check-runs" in command[2]:
            return json.dumps({"check_runs": [{"name": "All available release-PR checks passed", "head_sha": MERGE,
                                               "status": "completed", "conclusion": "success"}]})
        return json.dumps({"artifacts": [{"name": "release-pr-verified-7-" + MERGE, "expired": False}]})

    def test_non_release_pr_keeps_the_existing_merge_path(self):
        with patch.object(gate.subprocess, "check_output") as run:
            gate.require_verified("nwverse/persona", 7, COMMIT, "fix: a bug", run)
            run.assert_not_called()

    def test_exact_successful_merge_result_and_artifact_are_required(self):
        with patch.object(gate.subprocess, "check_output", side_effect=self.successful):
            gate.require_verified("nwverse/persona", 7, COMMIT, "release: v0.9.2-beta.2")

    def test_failed_missing_or_stale_verification_blocks_merge(self):
        for change in ("failed", "pending", "missing", "expired", "fork", "actor", "head"):
            def response(command, **kwargs):
                if command[:2] == ["gh", "api"] and "/pulls/" in command[2]:
                    data = self.pr()
                    if change == "fork":
                        data["head"]["repo"]["full_name"] = "fork/persona"
                    elif change == "actor":
                        data["user"]["login"] = "other"
                    elif change == "head":
                        data["head"]["sha"] = "c" * 40
                    return json.dumps(data)
                if "check-runs" in command[2]:
                    if change == "missing":
                        return json.dumps({"check_runs": []})
                    return json.dumps({"check_runs": [{"name": "All available release-PR checks passed", "head_sha": MERGE,
                                                       "status": "completed" if change == "failed" else "in_progress",
                                                       "conclusion": "failure" if change == "failed" else None}]})
                return json.dumps({"artifacts": [{"name": "release-pr-verified-7-" + MERGE, "expired": change == "expired"}]})
            with self.subTest(change=change), patch.object(gate.subprocess, "check_output", side_effect=response):
                with self.assertRaises(gate.ReleaseMergeError):
                    gate.require_verified("nwverse/persona", 7, COMMIT, "release: v0.9.2-beta.2")


if __name__ == "__main__":
    unittest.main()
