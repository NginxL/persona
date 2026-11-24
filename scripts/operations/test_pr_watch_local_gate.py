"""Local gate status trust, freshness and automatic-merge regression tests."""
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import pr_watch
from pr_watch_common import WatchError
from pr_watch_local_gate import CONTEXT, LocalGateError, require_local_gate

HEAD, BASE = "a" * 40, "b" * 40


class LocalGateTests(unittest.TestCase):
    def status(self, **changes):
        status = {"id": 10, "context": CONTEXT, "state": "success", "description": "EXIT_CODE=0",
                  "creator": {"login": "maintainer"}}
        status.update(changes)
        return status

    def check(self, statuses, permission="write"):
        read = Mock(side_effect=[statuses, {"permission": permission}])
        result = require_local_gate("example/project", HEAD, BASE, read)
        self.assertIn(HEAD, read.call_args_list[0].args[0][1])
        return result

    def test_explicit_pass_and_exact_baseline_exception(self):
        self.check([self.status()])
        self.check([self.status(description="BASE=%s NEW_FAILURES=0" % BASE)])

    def test_absent_hosted_only_pending_failed_and_malformed_evidence_block(self):
        cases = [[], [self.status(context="hosted-ci")], [self.status(state="pending")],
                 [self.status(state="failure")], [self.status(description="tests look good")],
                 [self.status(description="EXIT_CODE=1")], [self.status(description="BASE=%s NEW_FAILURES=0" % HEAD)],
                 [self.status(description="BASE=%s NEW_FAILURES=1" % BASE)], {}, [None]]
        for case in cases:
            with self.subTest(case=case), self.assertRaises(LocalGateError):
                self.check(case)

    def test_newer_failure_invalidates_older_success_regardless_of_order(self):
        for statuses in ([self.status(), self.status(id=11, state="failure")],
                         [self.status(id=11, state="pending"), self.status()]):
            with self.assertRaises(LocalGateError):
                self.check(statuses)

    def test_author_requires_current_write_permission(self):
        for permission in ("read", "none", "triage"):
            with self.subTest(permission=permission), self.assertRaises(LocalGateError):
                self.check([self.status()], permission)

    def test_read_failure_never_becomes_approval(self):
        for responses in ([WatchError("offline")], [[self.status()], WatchError("permission unavailable")]):
            with self.assertRaisesRegex(LocalGateError, "unable to read"):
                require_local_gate("example/project", HEAD, BASE, Mock(side_effect=responses))

    def test_cached_semantic_or_human_pass_cannot_bypass_merge_guard(self):
        with tempfile.TemporaryDirectory() as tmp:
            config = {"name": "nwverse/persona", "checkout": tmp, "merge": True}
            watcher = pr_watch.Watcher({"repositories": [config], "state": str(Path(tmp, "state.json")),
                                        "reviewer": {}, "webhook": {}, "poll_seconds": 60, "workers": 1})
            pr = {"number": 7, "title": "fix: example", "headRefOid": HEAD, "baseRefOid": BASE}
            saved = {"semantic": "passed", "semantic_source": "human", "verification": "passed"}
            with patch.object(pr_watch, "require_local_gate", side_effect=LocalGateError("Local gate: missing; not merged")) as gate, \
                 patch.object(pr_watch, "run") as merge, patch.object(watcher, "comment") as comment:
                watcher.finish_merge(config, pr, saved)
                merge.assert_not_called()
                gate.assert_called_once_with(config["name"], HEAD, BASE)
                state = watcher.state.get(config["name"], 7)
                self.assertEqual(state["outcome"], "merge_failed")
                self.assertIn("Local gate", state["failure"])
                comment.assert_called_once()
                watcher.finish_merge(config, pr, state)
                comment.assert_called_once()  # no repeated missing-evidence comments

    def test_success_reaches_exact_head_wrapper(self):
        watcher = object.__new__(pr_watch.Watcher)
        watcher.dry_run = False
        watcher.log = Mock()
        pr = {"number": 7, "title": "fix: example", "headRefOid": HEAD, "baseRefOid": BASE}
        for repo in ("nwverse/persona", "NWVERSE/Persona"):
            config = {"name": repo, "checkout": ".", "merge": True}
            with self.subTest(repo=repo), patch.object(pr_watch, "require_local_gate") as gate, \
                 patch.object(pr_watch, "run", return_value=Mock(returncode=0)) as merge, \
                 patch.object(pr_watch, "pr_snapshot", return_value={"state": "MERGED"}):
                self.assertTrue(watcher.merge(config, pr))
                gate.assert_called_once_with(repo, HEAD, BASE)
                self.assertIn(HEAD, merge.call_args.args[0])
                self.assertIn(BASE, merge.call_args.args[0])

    def test_repository_alias_keeps_the_wrapper_as_file_lock_owner(self):
        import threading
        watcher = object.__new__(pr_watch.Watcher)
        watcher.merge_locks_guard = threading.Lock()
        watcher.merge_locks = {}
        with patch.object(pr_watch.pr_watch_merge, "merge_lock", side_effect=AssertionError("wrapper owns the lock")):
            with watcher.merging("NWVERSE/Persona"):
                pass


if __name__ == "__main__":
    unittest.main()
