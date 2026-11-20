"""Bounded, idempotent release-PR creation."""
import json
import subprocess
import sys
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import create_release_pr as subject


class CreateReleasePRTests(unittest.TestCase):
    def test_empty_token_does_not_touch_github(self):
        with patch.object(subject, "gh") as call:
            with self.assertRaisesRegex(subject.ReleasePRError, "RELEASE_PR_TOKEN"):
                subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "")
            call.assert_not_called()

    def test_reuses_the_exact_existing_release_pr(self):
        response = subprocess.CompletedProcess([], 0, json.dumps([
            {"number": 41, "title": "unrelated", "head": {"ref": "fix/unrelated"}},
            {"number": 42, "title": "release: v1.0.0-beta.1", "head": {"ref": "release/1.0.0-beta.1"}, "html_url": "https://example/pr/42"},
        ]), "")
        with patch.object(subject, "gh", return_value=response) as call:
            subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")
            self.assertEqual(call.call_count, 1)
            self.assertEqual(call.call_args.args[0], ["api", "repos/nwverse/persona/pulls?state=open&base=main&per_page=100"])

    def test_unrelated_open_prs_do_not_own_the_release_branch(self):
        listing = subprocess.CompletedProcess([], 0, json.dumps([
            {"number": 1, "title": "other", "head": {"ref": "fix/other"}},
            {"number": 2, "title": "another", "head": {"ref": "fix/another"}},
        ]), "")
        created = subprocess.CompletedProcess([], 0, '{"html_url":"https://example/pr/44"}\n', "")
        with patch.object(subject, "gh", side_effect=[listing, created]):
            subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token")

    def test_throttled_creation_uses_bounded_backoff_then_succeeds(self):
        listing = subprocess.CompletedProcess([], 0, "[]", "")
        limited = subprocess.CompletedProcess([], 1, "", "GraphQL: was submitted too quickly")
        created = subprocess.CompletedProcess([], 0, '{"html_url":"https://example/pr/43"}\n', "")
        waits = []
        with patch.object(subject, "gh", side_effect=[listing, limited, limited, created]):
            subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token", attempts=3, wait=2, sleeper=waits.append)
            self.assertEqual(waits, [2, 4])

    def test_retry_budget_does_not_hide_a_failure(self):
        listing = subprocess.CompletedProcess([], 0, "[]", "")
        limited = subprocess.CompletedProcess([], 1, "", "was submitted too quickly")
        waits = []
        with patch.object(subject, "gh", side_effect=[listing] + [limited] * 3):
            with self.assertRaisesRegex(subject.ReleasePRError, "bounded retry budget"):
                subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token", attempts=3, wait=2, sleeper=waits.append)
            self.assertEqual(waits, [2, 4])

    def test_a_permanent_failure_is_not_retried(self):
        listing = subprocess.CompletedProcess([], 0, "[]", "")
        denied = subprocess.CompletedProcess([], 1, "", "Resource not accessible by integration")
        with patch.object(subject, "gh", side_effect=[listing, denied]):
            with self.assertRaises(subject.ReleasePRError):
                subject.create("nwverse/persona", "release: v1.0.0-beta.1", "release/1.0.0-beta.1", "body", "token", attempts=5, wait=2, sleeper=lambda _: self.fail("permanent failure was retried"))


if __name__ == "__main__":
    unittest.main()
