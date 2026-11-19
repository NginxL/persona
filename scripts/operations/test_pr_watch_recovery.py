"""Real-Git recovery tests using only temporary watcher-owned directories."""
import os
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import pr_watch_common
import pr_watch_recovery
import pr_watch_repair


class RecoveryIntegrationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="pr-watch-recovery-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.checkout = self.root / "checkout"
        self.checkout.mkdir()
        self.state = self.root / "state.json"
        self.worktrees = pr_watch_recovery.owned_root(self.state)
        self.worktrees.mkdir()
        pr_watch_recovery.atomic_leases(self.state, {"version": 1, "prs": {}, "worktree_leases": {}})
        env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull)
        subprocess.run(["git", "init", "-q"], cwd=self.checkout, env=env, check=True, capture_output=True)

    def test_owned_path_rejects_external_and_symlinked_directories(self):
        (self.root / "outside").mkdir()
        for path in (self.root, self.root / "outside", self.worktrees, self.root / "state.json"):
            with self.subTest(path=path):
                if not path.exists():
                    path.touch()
                try:
                    pr_watch_recovery.owned_path(self.state, path)
                except pr_watch_common.WatchError:
                    pass
                else:
                    self.fail("accepted unsafe cleanup path: %s" % path)
        link = self.root / "link"
        link.symlink_to(self.worktrees, target_is_directory=True)
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_recovery.owned_path(self.state, link / "entry")

    def test_disk_pressure_reclaims_only_inactive_watcher_worktrees(self):
        stale = self.worktrees / "persona-7-old"
        stale.mkdir()
        os.utime(stale, (1, 1))
        active = self.worktrees / "persona-8-active"
        active.mkdir()
        outside = self.root / "outside"
        outside.mkdir()
        pr_watch_recovery.atomic_leases(self.state, {"version": 1, "prs": {}, "worktree_leases": {
            str(stale): {"active": False, "heartbeat": 1},
            str(active): {"active": True, "heartbeat": 1},
        }})
        def fake_run(argv, **kwargs):
            code = 1 if argv[:3] == ["git", "worktree", "remove"] else 0
            return subprocess.CompletedProcess(argv, code, "", "stale registration" if code else "")
        with patch.object(pr_watch_recovery, "run", side_effect=fake_run):
            self.assertTrue(pr_watch_recovery.recover_worktree_failure(
                self.checkout, self.state, "fatal: Out of diskspace", protected=[active]))
        self.assertFalse(stale.exists())
        self.assertTrue(active.exists())
        self.assertTrue(outside.exists())

    def test_unregistered_and_active_worktrees_are_never_bulk_deleted(self):
        stale = self.worktrees / "persona-7-old"
        stale.mkdir()
        os.utime(stale, (1, 1))
        active = self.worktrees / "persona-8-active"
        active.mkdir()
        pr_watch_recovery.atomic_leases(self.state, {"worktree_leases": {
            str(active): {"active": True, "heartbeat": 1}}})
        self.assertFalse(pr_watch_recovery.recover_worktree_failure(
            self.checkout, self.state, "fatal: no space left", protected=[active]))
        self.assertTrue(stale.exists())
        self.assertTrue(active.exists())

    def test_registered_worktree_is_not_recursively_deleted_after_a_remove_failure(self):
        registered = self.worktrees / "persona-7-live"
        registered.mkdir()
        (registered / ".git").write_text("gitdir: " + str(self.checkout / ".git/worktrees/persona-7-live") + "\n")
        registration = self.checkout / ".git/worktrees/persona-7-live"
        registration.parent.mkdir(parents=True)
        registration.mkdir()
        with patch.object(pr_watch_recovery, "run", return_value=subprocess.CompletedProcess([], 1, "", "registered")):
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_recovery.remove_worktree(self.checkout, self.state, registered)
        self.assertTrue(registered.exists())
        self.assertTrue(registration.exists())

    def test_unregistered_failed_worktree_can_be_removed(self):
        broken = self.worktrees / "persona-7-broken"
        broken.mkdir()
        (broken / "partial-artifact").write_text("from failed checkout")
        with patch.object(pr_watch_recovery, "run", side_effect=[
                subprocess.CompletedProcess([], 128, "", "not a working tree"),
                subprocess.CompletedProcess([], 0, "", ""),
                subprocess.CompletedProcess([], 0, "", "")]):
            pr_watch_recovery.remove_worktree(self.checkout, self.state, broken)
        self.assertFalse(broken.exists())

    def test_empty_mkdtemp_reservation_can_be_removed(self):
        broken = self.worktrees / "persona-7-empty"
        broken.mkdir()
        with patch.object(pr_watch_recovery, "run", side_effect=[
                subprocess.CompletedProcess([], 128, "", "not a working tree"),
                subprocess.CompletedProcess([], 0, "", ""),
                subprocess.CompletedProcess([], 0, "", "")]):
            pr_watch_recovery.remove_worktree(self.checkout, self.state, broken)
        self.assertFalse(broken.exists())

    def test_successful_git_remove_with_a_remaining_directory_is_not_reclaimed(self):
        remaining = self.worktrees / "persona-7-remaining"
        remaining.mkdir()
        with patch.object(pr_watch_recovery, "run", return_value=subprocess.CompletedProcess([], 0, "", "")):
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_recovery.remove_worktree(self.checkout, self.state, remaining)
        self.assertTrue(remaining.exists())

    def test_failed_worktree_removal_is_not_counted_as_reclaimed(self):
        stale = self.worktrees / "persona-7-old"
        stale.mkdir()
        pr_watch_recovery.atomic_leases(self.state, {"worktree_leases": {
            str(stale): {"active": False, "heartbeat": 1}}})
        with patch.object(pr_watch_recovery, "remove_worktree", side_effect=pr_watch_common.WatchError("registered")):
            self.assertEqual(pr_watch_recovery.reclaim_worktrees(self.checkout, self.state), 0)
        self.assertTrue(stale.exists())

    def test_successful_removal_counts_even_if_lease_persistence_fails(self):
        stale = self.worktrees / "persona-7-old"
        stale.mkdir()
        pr_watch_recovery.atomic_leases(self.state, {"worktree_leases": {
            str(stale): {"active": False, "heartbeat": 1}}})
        state = type("State", (), {"lock": __import__("threading").RLock(),
                                   "data": json.loads(self.state.read_text())})()
        def remove(checkout, state_file, path):
            stale.rmdir()
        with patch.object(pr_watch_recovery, "remove_worktree", side_effect=remove), \
                patch.object(pr_watch_recovery, "atomic_leases", side_effect=OSError("disk full")):
            self.assertEqual(pr_watch_recovery.reclaim_worktrees(self.checkout, self.state, state=state), 1)
        self.assertFalse(stale.exists())
        self.assertNotIn(str(stale), state.data["worktree_leases"])

    def test_companion_is_reclaimed_with_its_main_worktree(self):
        stale = self.worktrees / "persona-7-old"
        stale.mkdir()
        companion = Path(str(stale) + "-base-check")
        companion.mkdir()
        (companion / "evidence").write_text("full base checkout")
        state = type("State", (), {"lock": __import__("threading").RLock(),
                                   "data": {"worktree_leases": {
                                       str(stale): {"active": False, "heartbeat": 1},
                                       str(companion): {"active": False, "heartbeat": 1},
                                   }}})()
        removed = []
        def remove(checkout, state_file, path):
            removed.append(path)
            (companion / "evidence").unlink(missing_ok=True)
            path.rmdir()
        with patch.object(pr_watch_recovery, "remove_worktree", side_effect=remove):
            self.assertEqual(pr_watch_recovery.reclaim_worktrees(self.checkout, self.state, state=state), 1)
        self.assertEqual(removed, [companion, stale])
        self.assertNotIn(str(companion), state.data["worktree_leases"])
        self.assertNotIn(str(stale), state.data["worktree_leases"])

    def test_companion_removal_failure_retains_the_lease(self):
        stale = self.worktrees / "persona-7-old"
        stale.mkdir()
        companion = Path(str(stale) + "-base-check")
        companion.mkdir()
        (companion / "evidence").write_text("do not forget")
        state = type("State", (), {"lock": __import__("threading").RLock(),
                                   "data": {"worktree_leases": {
                                       str(stale): {"active": False, "heartbeat": 1},
                                       str(companion): {"active": False, "heartbeat": 1},
                                   }}})()
        with patch.object(pr_watch_recovery, "remove_worktree", side_effect=pr_watch_common.WatchError("still registered")):
            self.assertEqual(pr_watch_recovery.reclaim_worktrees(self.checkout, self.state, state=state), 0)
        self.assertIn(str(companion), state.data["worktree_leases"])
        self.assertTrue(companion.exists())

    def test_recent_main_lease_without_companion_is_not_reclaimed(self):
        recent = self.worktrees / "persona-7-recent"
        recent.mkdir()
        state = type("State", (), {"lock": __import__("threading").RLock(),
                                   "data": {"worktree_leases": {
                                       str(recent): {"active": False, "heartbeat": time.time()},
                                   }}})()
        with patch.object(pr_watch_recovery, "remove_worktree") as remove:
            self.assertEqual(pr_watch_recovery.reclaim_worktrees(self.checkout, self.state, state=state), 0)
        remove.assert_not_called()
        self.assertTrue(recent.exists())

    def test_reclamation_skips_leases_from_other_repositories(self):
        other = self.worktrees / "persona-extensions-7-old"
        other.mkdir()
        pr_watch_recovery.atomic_leases(self.state, {"worktree_leases": {
            str(other): {"repository": "example/other", "active": False, "heartbeat": 1}}})
        with patch.object(pr_watch_recovery, "remove_worktree") as remove:
            self.assertEqual(pr_watch_recovery.reclaim_worktrees(self.checkout, self.state, repo="example/one"), 0)
        remove.assert_not_called()
        self.assertTrue(other.exists())

    def test_one_failed_lease_does_not_abort_other_reclamation(self):
        stale = self.worktrees / "persona-7-old"
        stale.mkdir()
        broken = self.worktrees / "persona-8-broken"
        broken.mkdir()
        pr_watch_recovery.atomic_leases(self.state, {"worktree_leases": {
            str(stale): {"active": False, "heartbeat": 1},
            str(broken): {"active": False, "heartbeat": 1}}})
        def remove(checkout, state, path):
            if path == stale:
                raise OSError("read failure")
            broken.rmdir()
        with patch.object(pr_watch_recovery, "remove_worktree", side_effect=remove):
            self.assertEqual(pr_watch_recovery.reclaim_worktrees(self.checkout, self.state), 1)

    def test_only_empty_loose_objects_are_removed_and_pack_temps_are_preserved(self):
        loose = self.checkout / ".git/objects/ab"
        loose.mkdir(parents=True)
        empty = loose / ("c" * 38)
        empty.touch()
        nonempty = loose / ("d" * 38)
        nonempty.write_bytes(b"do-not-remove")
        pack = self.checkout / ".git/objects/pack"
        pack.mkdir(exist_ok=True)
        temporary = pack / "tmp_pack_test"
        temporary.touch()
        old = 1
        os.utime(temporary, (old, old))
        self.assertEqual(pr_watch_recovery.recover_object_debris(self.checkout), 1)
        self.assertFalse(empty.exists())
        self.assertTrue(nonempty.exists())
        self.assertTrue(temporary.exists())
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_recovery.recover_object_debris(self.worktrees)

    def test_rename_refusal_can_be_resolved_from_the_exact_remote_ref(self):
        env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                   GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                   GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")
        origin = self.root / "origin.git"
        subprocess.run(["git", "init", "-q", "--bare", str(origin)], cwd=self.root, env=env, check=True, capture_output=True)
        subprocess.run(["git", "remote", "add", "origin", str(origin)], cwd=self.checkout, env=env, check=True, capture_output=True)
        commits = {}
        for letter, ref in (("a", "refs/heads/main"), ("b", "refs/pull/7/head"), ("c", "refs/pull/7/head")):
            (self.checkout / "fixture").write_text(letter + "\n")
            subprocess.run(["git", "add", "fixture"], cwd=self.checkout, env=env, check=True, capture_output=True)
            subprocess.run(["git", "commit", "-qm", letter], cwd=self.checkout, env=env, check=True, capture_output=True)
            sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=self.checkout, env=env, text=True).strip()
            commits[letter] = sha
            subprocess.run(["git", "push", "-q", "origin", "HEAD:" + ref], cwd=self.checkout, env=env, check=True, capture_output=True)
        original_run = pr_watch_common.run
        for letter, refspec in (("a", "refs/heads/main"), ("b", "+refs/pull/7/head:refs/pr-watch/heads/7"), ("c", "pull/7/head")):
            remote_ref = "refs/heads/main" if letter == "a" else "refs/pull/7/head"
            calls = []
            def controlled(argv, **kwargs):
                calls.append(argv)
                if argv[:2] == ["git", "fetch"] and "diff.renames=false" not in argv:
                    return subprocess.CompletedProcess(argv, 2, "", "exhaustive rename detection was skipped")
                if argv[:2] == ["git", "ls-remote"]:
                    return subprocess.CompletedProcess(argv, 0, commits[letter] + "\t" + remote_ref + "\n", "")
                return original_run(argv, **kwargs)
            with patch.object(pr_watch_common, "run", side_effect=controlled):
                self.assertEqual(pr_watch_common.checked(["git", "fetch", "-q", "origin", refspec], cwd=self.checkout),
                                 commits[letter] + "\n")
            self.assertEqual(calls[1], ["git", "ls-remote", "origin", remote_ref])
            self.assertEqual(calls[2][:8], ["git", "-c", "diff.renames=false", "fetch", "-q", "--no-write-fetch-head", "origin", commits[letter]])
            if ":" in refspec:
                self.assertEqual(subprocess.check_output(["git", "rev-parse", "refs/pr-watch/heads/7"], cwd=self.checkout, text=True).strip(), commits[letter])
            else:
                self.assertEqual(subprocess.check_output(["git", "rev-parse", "FETCH_HEAD"], cwd=self.checkout, text=True).strip(), commits[letter])

    def test_fetch_recovery_rejects_ambiguous_or_unrelated_errors(self):
        with patch.object(pr_watch_common, "run", side_effect=[
                subprocess.CompletedProcess([], 2, "", "rename detection was skipped"),
                subprocess.CompletedProcess([], 0, "a" * 40 + "\trefs/heads/main^{}\n" + "b" * 40 + "\trefs/heads/main\n", "")]):
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_common.checked(["git", "fetch", "-q", "origin", "refs/heads/main"])
        with patch.object(pr_watch_common, "run", return_value=subprocess.CompletedProcess([], 128, "", "fatal: corrupt")) as execute:
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_common.checked(["git", "fetch", "-q", "origin", "refs/heads/main"])
        self.assertEqual(execute.call_count, 1)

    def test_fetch_recovery_writes_a_real_linked_worktree_fetch_head(self):
        env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                   GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                   GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")
        (self.checkout / "fixture").write_text("base\n")
        subprocess.run(["git", "add", "fixture"], cwd=self.checkout, env=env, check=True, capture_output=True)
        subprocess.run(["git", "commit", "-qm", "base"], cwd=self.checkout, env=env, check=True, capture_output=True)
        sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=self.checkout, env=env, text=True).strip()
        linked = self.root / "linked"
        subprocess.run(["git", "worktree", "add", "--detach", str(linked), sha], cwd=self.checkout,
                       env=env, check=True, capture_output=True)
        self.addCleanup(lambda: subprocess.run(["git", "worktree", "remove", "--force", str(linked)],
                                              cwd=self.checkout, env=env, capture_output=True))
        subprocess.run(["git", "remote", "add", "origin", str(self.checkout)], cwd=linked,
                       env=env, check=True, capture_output=True)
        git_path = subprocess.check_output(["git", "rev-parse", "--git-path", "FETCH_HEAD"],
                                           cwd=linked, env=env, text=True).strip()
        destination = (linked / git_path).resolve()
        self.assertEqual(destination.parent, (self.checkout / ".git/worktrees/linked").resolve())
        original_run = pr_watch_common.run
        def controlled(argv, **kwargs):
            if argv[:2] == ["git", "fetch"] and "diff.renames=false" not in argv:
                return subprocess.CompletedProcess(argv, 2, "", "exhaustive rename detection was skipped")
            if argv[:2] == ["git", "ls-remote"]:
                return subprocess.CompletedProcess(argv, 0, sha + "\trefs/heads/main\n", "")
            return original_run(argv, **kwargs)
        # The repair helpers' empty-refmap fetch (#10584) takes the same recovery.
        for argv in (["git", "fetch", "-q", "origin", "refs/heads/main"], pr_watch_repair.fetch_branch("main")):
            with self.subTest(argv=argv):
                destination.unlink(missing_ok=True)
                with patch.object(pr_watch_common, "run", side_effect=controlled):
                    self.assertEqual(pr_watch_common.checked(argv, cwd=linked), sha + "\n")
                self.assertEqual(subprocess.check_output(["git", "rev-parse", "FETCH_HEAD"],
                                                        cwd=linked, env=env, text=True).strip(), sha)
                self.assertTrue(destination.is_file())

    def test_repair_fetch_leaves_the_shared_tracking_ref_alone(self):
        # Worktrees share the checkout's refs; concurrent repair fetches must not
        # race to update refs/remotes/origin/main (#10584).
        env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                   GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                   GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")
        def git(*args, cwd=self.checkout):
            return subprocess.run(["git", *args], cwd=cwd, env=env, check=True,
                                  capture_output=True, text=True).stdout.strip()
        upstream = self.root / "upstream.git"
        git("init", "-q", "--bare", "-b", "main", str(upstream), cwd=self.root)
        git("commit", "-q", "--allow-empty", "-m", "base")
        base = git("rev-parse", "HEAD")
        git("remote", "add", "origin", str(upstream))
        git("push", "-q", "origin", "HEAD:refs/heads/main")
        git("fetch", "-q", "origin")
        self.assertEqual(git("rev-parse", "refs/remotes/origin/main"), base)
        git("commit", "-q", "--allow-empty", "-m", "later")
        later = git("rev-parse", "HEAD")
        git("push", "-q", "origin", "HEAD:refs/heads/main")
        # A push updates the tracking ref itself; start from the stale value.
        git("update-ref", "refs/remotes/origin/main", base)
        linked = self.root / "linked"
        git("worktree", "add", "-q", "--detach", str(linked), base)
        self.addCleanup(lambda: subprocess.run(["git", "worktree", "remove", "--force", str(linked)],
                                              cwd=self.checkout, env=env, capture_output=True))
        with patch.dict(os.environ, {"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull}):
            tip = pr_watch_repair.fetch_current_main({"baseRefName": "main"}, str(linked), base)
        self.assertEqual(tip, later)
        self.assertEqual(git("rev-parse", "refs/remotes/origin/main"), base)

    def test_repair_commit_message_has_no_tool_attribution(self):
        source = (HERE / "pr_watch_repair.py").read_text()
        self.assertNotIn("Co-Authored-By:", source)
        self.assertNotIn("Kimi", source)
        self.assertNotIn("Moonshot", source)


if __name__ == "__main__":
    unittest.main()
