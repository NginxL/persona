import unittest

import release_candidate
import release_pr


class ReleaseCandidateTests(unittest.TestCase):
    def pull(self, number=42, title="release: v1.2.0", channel="release", **changes):
        value = {
            "number": number,
            "title": title,
            "state": "OPEN",
            "draft": False,
            "base": {"ref": "main"},
            "head": {"ref": "release/" + title.removeprefix("release: v") + "-r3",
                     "repo": {"full_name": "nwverse/persona"}},
            "user": {"login": "chyroc"},
            "html_url": "https://example.invalid/pull/" + str(number),
        }
        value.update(changes)
        return value

    def test_one_existing_candidate_is_reused_including_a_retry_suffix(self):
        existing = self.pull()
        self.assertIs(release_candidate.active_candidate([existing], "nwverse/persona", "release"), existing)
        self.assertIsNone(release_candidate.active_candidate([], "nwverse/persona", "release"))

    def test_other_channel_and_non_release_prs_do_not_own_the_candidate(self):
        self.assertIsNone(release_candidate.active_candidate([
            self.pull(number=1, title="release: v1.2.0-beta.4"),
            self.pull(number=2, title="feat: an ordinary change"),
        ], "nwverse/persona", "release"))

    def test_multiple_candidates_need_an_explicit_superseding_decision(self):
        with self.assertRaisesRegex(release_pr.ReleaseError, "#42, #43"):
            release_candidate.active_candidate([self.pull(42), self.pull(43)], "nwverse/persona", "release")

    def test_draft_fork_and_other_authors_cannot_supply_a_release_candidate(self):
        for changes in ({"draft": True}, {"user": {"login": "other"}},
                        {"head": {"repo": {"full_name": "fork/persona"}}},
                        {"base": {"ref": "another-branch"}}):
            with self.subTest(changes=changes):
                self.assertIsNone(release_candidate.active_candidate([
                    self.pull(**changes)
                ], "nwverse/persona", "release"))


if __name__ == "__main__":
    unittest.main()
