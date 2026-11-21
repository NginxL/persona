"""Unit tests for exact-head human review classification. No real GitHub."""
import subprocess
import sys
import unittest
from pathlib import Path
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import pr_watch_common
import pr_watch_human

HEAD = "a" * 40
OLD_HEAD = "b" * 40
SELF = "watchbot-login"


def review(review_id, state, commit_id=HEAD, login="reviewer", user_type="User",
           submitted_at=None):
    return {"id": review_id, "node_id": "MDE_%s" % review_id, "state": state,
            "commit_id": commit_id, "submitted_at": submitted_at or "2026-11-20T10:00:%02dZ" % (review_id % 60),
            "user": {"login": login, "type": user_type}, "author_association": "MEMBER"}


class ClassificationTests(unittest.TestCase):
    def classify(self, reviews, head=HEAD, self_login=SELF):
        return pr_watch_human.classify_human_reviews(reviews, head, self_login)

    def test_human_approval_on_the_exact_head_is_recognized(self):
        decision = self.classify([review(1, "APPROVED")])
        self.assertEqual(decision["decision"], "approved")
        self.assertEqual(decision["review"], {"id": 1, "login": "reviewer", "state": "APPROVED",
                                              "commit_id": HEAD, "submitted_at": "2026-11-20T10:00:01Z"})

    def test_change_request_outranks_another_reviewers_approval(self):
        reviews = [review(1, "APPROVED", login="approver"),
                   review(2, "CHANGES_REQUESTED", login="maintainer", submitted_at="2026-11-20T11:00:00Z")]
        decision = self.classify(reviews)
        self.assertEqual(decision["decision"], "changes_requested")
        self.assertEqual(decision["review"]["id"], 2)

    def test_comment_only_is_neither_approval_nor_a_block(self):
        decision = self.classify([review(1, "COMMENTED")])
        self.assertEqual(decision["decision"], "commented")
        self.assertEqual(decision["review"]["state"], "COMMENTED")

    def test_reviews_on_an_older_head_are_ignored(self):
        decision = self.classify([review(1, "APPROVED", commit_id=OLD_HEAD)])
        self.assertEqual(decision["decision"], "none")

    def test_a_new_head_re_evaluates_from_scratch(self):
        # Every review belongs to the previous head; the new head has no human state.
        decision = self.classify([review(1, "APPROVED", commit_id=OLD_HEAD),
                                  review(2, "CHANGES_REQUESTED", commit_id=OLD_HEAD)], head=HEAD)
        self.assertEqual(decision["decision"], "none")

    def test_bot_and_app_reviews_never_count_as_human(self):
        for user_type in ("Bot", "Organization", "Mannequin"):
            with self.subTest(user_type=user_type):
                decision = self.classify([review(1, "APPROVED", user_type=user_type)])
                self.assertEqual(decision["decision"], "none")

    def test_a_missing_user_is_ignored_not_treated_as_human(self):
        raw = review(1, "APPROVED")
        raw["user"] = None
        self.assertEqual(self.classify([raw])["decision"], "none")

    def test_the_watchers_own_reviews_cannot_self_approve(self):
        decision = self.classify([review(1, "APPROVED", login=SELF)])
        self.assertEqual(decision["decision"], "none")
        decision = self.classify([review(1, "COMMENTED", login=SELF)])
        self.assertEqual(decision["decision"], "none")

    def test_dismissal_as_the_latest_review_neutralizes_the_reviewer(self):
        reviews = [review(1, "APPROVED", submitted_at="2026-11-20T09:00:00Z"),
                   review(2, "DISMISSED", submitted_at="2026-11-20T12:00:00Z")]
        self.assertEqual(self.classify(reviews)["decision"], "none")

    def test_a_dismissal_followed_by_a_new_approval_counts(self):
        reviews = [review(1, "APPROVED", submitted_at="2026-11-20T09:00:00Z"),
                   review(2, "DISMISSED", submitted_at="2026-11-20T10:00:00Z"),
                   review(3, "APPROVED", submitted_at="2026-11-20T11:00:00Z")]
        decision = self.classify(reviews)
        self.assertEqual(decision["decision"], "approved")
        self.assertEqual(decision["review"]["id"], 3)

    def test_pending_reviews_are_neutral(self):
        reviews = [review(1, "PENDING", submitted_at=""),
                   review(2, "COMMENTED", submitted_at="2026-11-20T11:00:00Z")]
        decision = self.classify(reviews)
        self.assertEqual(decision["decision"], "commented")

    def test_same_reviewer_latest_effective_state_wins(self):
        # An approval followed by a change request on the same head blocks.
        reviews = [review(1, "APPROVED", submitted_at="2026-11-20T09:00:00Z"),
                   review(2, "CHANGES_REQUESTED", submitted_at="2026-11-20T10:00:00Z")]
        self.assertEqual(self.classify(reviews)["decision"], "changes_requested")
        # A later approval by the same reviewer resolves it.
        reviews.append(review(3, "APPROVED", submitted_at="2026-11-20T11:00:00Z"))
        decision = self.classify(reviews)
        self.assertEqual(decision["decision"], "approved")
        self.assertEqual(decision["review"]["id"], 3)

    def test_same_timestamp_breaks_ties_by_review_id(self):
        reviews = [review(1, "APPROVED", submitted_at="2026-11-20T10:00:00Z"),
                   review(2, "CHANGES_REQUESTED", submitted_at="2026-11-20T10:00:00Z")]
        self.assertEqual(self.classify(reviews)["decision"], "changes_requested")

    def test_malformed_records_fail_closed(self):
        bad_cases = [
            [{"id": "1", "state": "APPROVED", "commit_id": HEAD, "user": {"login": "x", "type": "User"},
              "submitted_at": "2026-11-20T10:00:00Z"}],
            [review(1, "MAYBE")],
            [review(1, "APPROVED", commit_id="short")],
            [{"id": 1, "state": "APPROVED", "commit_id": HEAD, "user": {"login": "", "type": "User"},
              "submitted_at": "2026-11-20T10:00:00Z"}],
            [{"id": 1, "state": "APPROVED", "commit_id": HEAD, "user": {"login": "x", "type": "User"},
              "submitted_at": None}],
            ["not-an-object"],
        ]
        for index, bad in enumerate(bad_cases):
            with self.subTest(index=index):
                with self.assertRaises(pr_watch_common.WatchError):
                    self.classify(bad)

    def test_arguments_are_validated(self):
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_human.classify_human_reviews([], "short", SELF)
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_human.classify_human_reviews([], HEAD, " ")


class FetchTests(unittest.TestCase):
    def test_fetch_reviews_accepts_a_paginated_array(self):
        payload = [review(1, "APPROVED")]
        with patch.object(pr_watch_human, "gh_json", return_value=payload) as gh:
            self.assertEqual(pr_watch_human.fetch_reviews("example/one", 7)[0]["id"], 1)
        argv = gh.call_args.args[0]
        self.assertEqual(argv[:2], ["api", "repos/example/one/pulls/7/reviews?per_page=100"])
        self.assertIn("--paginate", argv)

    def test_fetch_reviews_rejects_a_non_array(self):
        with patch.object(pr_watch_human, "gh_json", return_value={}):
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_human.fetch_reviews("example/one", 7)

    def test_authenticated_login_reads_raw_jq_output(self):
        ok = subprocess.CompletedProcess([], 0, "cklxx\n", "")
        with patch.object(pr_watch_human, "run", return_value=ok):
            self.assertEqual(pr_watch_human.authenticated_login(), "cklxx")
        failures = [subprocess.CompletedProcess([], 1, "", "api error"),
                    subprocess.CompletedProcess([], 0, "  \n", "")]
        for failure in failures:
            with self.subTest(failure=failure.returncode):
                with patch.object(pr_watch_human, "run", return_value=failure):
                    with self.assertRaises(pr_watch_common.WatchError):
                        pr_watch_human.authenticated_login()


if __name__ == "__main__":
    unittest.main()
