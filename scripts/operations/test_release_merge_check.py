"""Exact merge-result release check lifecycle."""
from __future__ import annotations

import json
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import release_merge_check as subject


class ReleaseMergeCheckTests(unittest.TestCase):
    def test_pending_creates_the_exact_merge_check_with_repo_scoped_arguments(self):
        calls = []
        def run(command, **kwargs):
            calls.append((command, kwargs))
            if len(calls) == 1:
                return subprocess.CompletedProcess(command, 0, '{"check_runs":[]}', "")
            return subprocess.CompletedProcess(command, 0, '{"id":42}', "")
        with patch.dict(subject.os.environ, {}, clear=True):
            result = subject.pending("nwverse/persona", "a" * 40, 123, "token", runner=run)
        self.assertEqual(result, 42)
        command, kwargs = calls[1]
        self.assertEqual(command, ["gh", "api", "-X", "POST", "repos/nwverse/persona/check-runs", "--input", "-"])
        payload = json.loads(kwargs["input"])
        self.assertEqual(payload["head_sha"], "a" * 40)
        self.assertEqual(payload["external_id"], "release-pr/123/" + "a" * 40)
        self.assertEqual(payload["status"], "in_progress")
        self.assertNotIn("--repo", command)
        self.assertNotIn("token", command)

    def test_pending_updates_the_same_external_id_instead_of_duplicating_it(self):
        existing = {"check_runs": [{"id": 42, "head_sha": "a" * 40, "external_id": "release-pr/123/" + "a" * 40}]}
        calls = []
        def run(command, **kwargs):
            calls.append(command)
            if len(calls) == 1:
                return subprocess.CompletedProcess(command, 0, json.dumps(existing), "")
            return subprocess.CompletedProcess(command, 0, '{}', "")
        result = subject.pending("nwverse/persona", "a" * 40, 123, "token", runner=run)
        self.assertEqual(result, 42)
        self.assertEqual(calls[1], ["gh", "api", "-X", "PATCH", "repos/nwverse/persona/check-runs/42", "--input", "-"])

    def test_conclusion_is_explicit_and_never_inferred_from_skipped_jobs(self):
        calls = []
        def run(command, **kwargs):
            calls.append((command, kwargs))
            return subprocess.CompletedProcess(command, 0, '{}', "")
        subject.conclude("nwverse/persona", 42, "failure", "token", runner=run)
        payload = json.loads(calls[0][1]["input"])
        self.assertEqual(payload["conclusion"], "failure")
        self.assertEqual(payload["status"], "completed")
        with self.assertRaises(subject.CheckError):
            subject.conclude("nwverse/persona", 42, "skipped", "token", runner=run)

    def test_invalid_identity_or_api_failure_cannot_write_a_check(self):
        for repository, commit, pr in (("bad", "a" * 40, 1), ("nwverse/persona", "short", 1),
                                       ("nwverse/persona", "a" * 40, 0)):
            with self.subTest(repository=repository, commit=commit, pr=pr):
                with self.assertRaises(subject.CheckError):
                    subject.pending(repository, commit, pr, "token", runner=lambda *args, **kwargs: self.fail("API called"))
        failed = subprocess.CompletedProcess([], 1, "", "denied")
        with self.assertRaisesRegex(subject.CheckError, "denied"):
            subject.pending("nwverse/persona", "a" * 40, 1, "token",
                            runner=lambda *args, **kwargs: failed)
        with self.assertRaises(subject.CheckError):
            subject.pending("nwverse/persona", "a" * 40, 1, "", runner=lambda *args, **kwargs: self.fail("API called"))


if __name__ == "__main__":
    unittest.main()
