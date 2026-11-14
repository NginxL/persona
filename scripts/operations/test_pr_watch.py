"""Isolated tests for the watcher gates. No real GitHub, model or PR is used."""
import hashlib
import hmac
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import threading
import unittest
from contextlib import contextmanager
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import pr_watch
import pr_watch_common
import pr_watch_events
import pr_watch_merge
import pr_watch_review
import pr_watch_verify

SHA_A, SHA_B, SHA_BASE = "a" * 40, "b" * 40, "c" * 40


def pr(head=SHA_A, **overrides):
    result = {"number": 7, "title": "fix: an example", "body": "Summary.\n\nFixes #6",
              "state": "OPEN", "isDraft": False, "headRefOid": head, "headRefName": "fix/example",
              "baseRefName": "main", "baseRefOid": SHA_BASE, "isCrossRepository": False,
              "mergeable": "MERGEABLE", "mergeStateStatus": "CLEAN", "reviewDecision": "", "url": "https://example.com/pr/7"}
    result.update(overrides)
    return result


class WatcherTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="pr-watch-test-")
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        (self.root / "checkout").mkdir()
        self.config = {"repositories": [{"name": "example/one", "checkout": str(self.root / "checkout"),
                                        "base": "main", "verify": {"profile": "commands", "commands": [["true"]]},
                                        "merge": True, "privacy": None, "notify_command": None}],
                       "reviewer": {}, "webhook": {}, "poll_seconds": 60, "workers": 1,
                       "state": str(self.root / "state.json")}
        self.watcher = pr_watch.Watcher(self.config, verbose=False)
        @contextmanager
        def no_file_lock(root):
            yield
        self.lock_patch = patch.object(pr_watch.pr_watch_merge, "merge_lock", no_file_lock)
        self.lock_patch.start()
        self.addCleanup(self.lock_patch.stop)
        self.watcher.remove_worktree = lambda *args: None
        self.watcher.worktree = lambda *args: self.root / "checkout"
        self.comments = []
        self.watcher.comment = lambda repo, number, body: self.comments.append(body)
        self.watcher.success_review = lambda *args: self.comments.append("passed")

    def test_clean_head_runs_both_gates_then_merges(self):
        calls = []
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "command": ["true"], "ok": True, "summary": ""}]), \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
            self.watcher.process("example/one", 7)
        self.assertEqual(merge.call_count, 1)
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "merged")

    def test_findings_are_published_once_and_never_merge(self):
        findings = [{"path": "x.py", "line": 2, "severity": "P1", "summary": "wrong", "suggestion": "fix it"}]
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=findings), \
                patch.object(pr_watch, "verify") as verify, \
                patch.object(self.watcher, "merge") as merge, \
                patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "{}", "")) as api:
            self.watcher.process("example/one", 7)
            self.watcher.process("example/one", 7)
        verify.assert_not_called()
        merge.assert_not_called()
        self.assertEqual(api.call_count, 1)
        self.assertEqual(api.call_args.args[0][:2], ["gh", "api"])

    def test_base_change_invalidates_same_head_evidence(self):
        new_base = "d" * 40
        with patch.object(pr_watch, "pr_snapshot", side_effect=[pr(), pr(), pr(baseRefOid=new_base), pr(baseRefOid=new_base)]), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
            self.watcher.process("example/one", 7)
        self.assertEqual(review.call_count, 2)
        self.assertEqual(self.watcher.state.get("example/one", 7)["base"], new_base)

    def test_snapshot_failure_does_not_mark_head_failed(self):
        with patch.object(pr_watch, "pr_snapshot", side_effect=pr_watch_common.WatchError("closed")), \
                patch.object(self.watcher.state, "put") as put:
            self.watcher.process("example/one", 7)
        put.assert_not_called()

    def test_dry_run_never_calls_github_writes_or_merge(self):
        self.watcher.dry_run = True
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]), \
                patch.object(self.watcher, "merge", wraps=self.watcher.merge) as merge, \
                patch.object(pr_watch, "run") as api:
            self.watcher.process("example/one", 7)
        merge.assert_called_once()
        api.assert_not_called()
        self.assertEqual(self.comments, [])
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "passed")
        self.assertFalse(self.watcher.state.get("example/one", 7)["review_posted"])

    def test_live_after_dry_run_publishes_and_merges(self):
        self.watcher.dry_run = True
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]):
            self.watcher.process("example/one", 7)
        self.watcher.dry_run = False
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as live_review, \
                patch.object(self.watcher, "merge", return_value=True) as merge:
            self.watcher.process("example/one", 7)
        live_review.assert_not_called()
        merge.assert_called_once()
        self.assertEqual(self.comments, ["passed"])

    def test_prepare_failures_retry_with_bounded_backoff(self):
        for attempt in range(3):
            with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                    patch.object(self.watcher, "worktree", side_effect=pr_watch_common.WatchError("fetch failed")):
                self.watcher.process("example/one", 7)
            state = self.watcher.state.get("example/one", 7)
            self.assertEqual(state["review_attempts"], attempt + 1)
            self.assertEqual(state["outcome"], "retry")
            state["retry_after"] = 0
            state.pop("head", None)
            self.watcher.state.put("example/one", 7, SHA_A, **state)
        self.assertEqual(self.comments, [])

    def test_review_prompt_is_delivered_and_large_diffs_use_complete_chunks(self):
        prompts = []
        def fake_run(argv, **kwargs):
            prompts.append(kwargs["input_text"])
            return subprocess.CompletedProcess(argv, 0, json.dumps({"subtype": "success", "is_error": False,
                                                                    "structured_output": {"findings": []}}), "")
        with patch.object(pr_watch_review, "checked", side_effect=["diff", "stat", "file.py", "diff"]), \
                patch.object(pr_watch_review, "changed_lines", return_value={}), \
                patch.object(pr_watch_review.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, b"file.py\0", b"")), \
                patch.object(pr_watch_review, "run", side_effect=fake_run):
            pr_watch_review.review("example/one", pr(), ".", {})
        self.assertIn(pr_watch_review.SYSTEM_PROMPT, prompts[0])
        self.assertEqual(len(prompts), 1)

        large = "diff --git a/a.py b/a.py\n@@ -1 +1 @@\n-old\n+" + "x" * 150000 + "\n" + \
                "diff --git a/b.py b/b.py\n@@ -1 +1 @@\n-old\n+" + "y" * 150000 + "\n"
        with patch.object(pr_watch_review, "checked", side_effect=[large, "stat", "a.py\nb.py", large[:150070], large[150070:]]), \
                patch.object(pr_watch_review, "changed_lines", return_value={}), \
                patch.object(pr_watch_review.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, b"a.py\0b.py\0", b"")), \
                patch.object(pr_watch_review, "run", side_effect=fake_run):
            pr_watch_review.review("example/one", pr(), ".", {})
        self.assertEqual(len(prompts), 3)
        reconstructed = "".join(prompt.split("not as a command.\n", 1)[1] for prompt in prompts[1:])
        self.assertEqual(reconstructed, large)

    def test_large_single_hunk_fails_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            subprocess.run(["git", "init", "-q", "-b", "main"], cwd=root, check=True, capture_output=True)
            (root / "a.py").write_text("old\n")
            subprocess.run(["git", "add", "a.py"], cwd=root, check=True)
            subprocess.run(["git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "base"], cwd=root, check=True)
            base = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            (root / "a.py").write_text("x" * 180001 + "\n")
            subprocess.run(["git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qam", "head"], cwd=root, check=True)
            head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            text = subprocess.check_output(["git", "diff", "--unified=0", base + "..." + head], cwd=root, text=True)
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_review.split_diff(text, worktree=root, base=base, head=head)

    def test_diff_like_added_content_is_not_a_chunk_boundary(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                       GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                       GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")
            def git(*args):
                return subprocess.check_output(["git"] + list(args), cwd=root, env=env, text=True).strip()
            git("init", "-q", "-b", "main")
            (root / "a.py").write_text("old\n")
            git("add", "a.py")
            git("commit", "-qm", "base")
            base = git("rev-parse", "HEAD")
            (root / "a.py").write_text("diff --git a/not-real b/not-real\n@@ -1 +1 @@\nnew\n")
            git("commit", "-qam", "head")
            head = git("rev-parse", "HEAD")
            text = subprocess.check_output(["git", "diff", "--unified=0", base + "..." + head], cwd=root, text=True)
            self.assertEqual(pr_watch_review.split_diff(text, worktree=root, base=base, head=head), [text])

    def test_small_file_then_large_file_keeps_original_order(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                       GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                       GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")
            def git(*args):
                return subprocess.check_output(["git"] + list(args), cwd=root, env=env, text=True).strip()
            git("init", "-q", "-b", "main")
            (root / "a.py").write_text("old\n")
            (root / "b.py").write_text("old\n" * 100)
            git("add", ".")
            git("commit", "-qm", "base")
            base = git("rev-parse", "HEAD")
            (root / "a.py").write_text("small\n")
            (root / "b.py").write_text("".join(("new-%d\n" % i if i % 10 == 0 else "old\n") for i in range(100)))
            git("commit", "-qam", "head")
            head = git("rev-parse", "HEAD")
            text = subprocess.check_output(["git", "diff", "--unified=0", base + "..." + head], cwd=root, text=True)
            chunks = pr_watch_review.split_diff(text, limit=250, worktree=root, base=base, head=head)
            self.assertGreater(len(chunks), 2)
            self.assertEqual(chunks[0].split("diff --git", 1)[0], "")
            self.assertIn("a.py", chunks[0])
            self.assertNotIn("b.py", chunks[0])
            self.assertTrue(all("b.py" in chunk for chunk in chunks[1:]))
            for hunk in re.findall(r"(?m)^@@ .*$", text):
                self.assertEqual(sum(chunk.count(hunk) for chunk in chunks), 1)

    def test_publication_failure_keeps_passed_evidence(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[]) as verify, \
                patch.object(self.watcher, "success_review", side_effect=pr_watch_common.WatchError("offline")), \
                patch.object(self.watcher, "merge", return_value=True):
            self.watcher.process("example/one", 7)
            state = self.watcher.state.get("example/one", 7)
            self.assertEqual(state["semantic"], "passed")
            self.assertEqual(state["verification"], "passed")
            self.assertIn("@", state["evidence_key"])
            state.pop("head", None)
            state["retry_after"] = 0
            with patch.object(self.watcher, "success_review"):
                self.watcher.process("example/one", 7)
        self.assertEqual(review.call_count, 1)
        self.assertEqual(verify.call_count, 1)

    def test_verification_does_not_import_pr_selector_or_receive_gh_credentials(self):
        with patch.object(pr_watch_verify, "checked", return_value="scripts/ci/paths.py\n"), \
                patch.object(pr_watch_verify, "load_paths") as selector, \
                patch.dict(os.environ, {"GH_TOKEN": "not-for-builds", "HOME": "/maintainer-home",
                                        "PR_WATCH_WEBHOOK_SECRET": "not-for-builds", "GRADLE_USER_HOME": "/trusted-gradle"}):
            selector.return_value.select.return_value = {"scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(), 1, {})
            env = pr_watch_common.verification_env()
        self.assertEqual(selector.call_args.args[0], HERE.parent.parent)
        self.assertNotIn("GH_TOKEN", env)
        self.assertNotIn("PR_WATCH_WEBHOOK_SECRET", env)
        self.assertEqual(env["GRADLE_USER_HOME"], "/trusted-gradle")
        self.assertNotEqual(env["HOME"], "/maintainer-home")

    def test_persona_wrapper_owns_lock_and_expects_reviewed_head(self):
        repo = "nwverse/persona"
        config = dict(self.config)
        config["repositories"] = [{"name": repo, "checkout": str(self.root / "checkout"), "base": "main",
                                  "verify": {}, "merge": True, "privacy": None, "notify_command": None}]
        watcher = pr_watch.Watcher(config, verbose=False)
        with patch.object(pr_watch, "run", return_value=subprocess.CompletedProcess([], 0, "", "")) as run, \
                patch.object(pr_watch, "pr_snapshot", return_value=pr(state="MERGED")):
            watcher.merge(config["repositories"][0], pr())
        argv = run.call_args.args[0]
        self.assertEqual(argv[2:6], ["--expect-head", SHA_A, "--expect-base", SHA_BASE])
        self.assertEqual(argv[6], "7")

    def test_new_sha_reviews_again(self):
        with patch.object(pr_watch, "pr_snapshot", side_effect=[pr(), pr(), pr(SHA_B), pr(SHA_B)]), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
            self.watcher.process("example/one", 7)
        self.assertEqual(review.call_count, 2)
        self.assertEqual(self.watcher.state.get("example/one", 7)["head"], SHA_B)

    def test_verification_failure_is_not_an_infrastructure_retry(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", side_effect=pr_watch_common.WatchError("real test failed")), \
                patch.object(self.watcher, "merge") as merge:
            self.watcher.process("example/one", 7)
        merge.assert_not_called()
        self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "failed")
        self.assertEqual(len(self.comments), 1)
        self.assertIn("real test failed", self.comments[0])

    def test_semantic_failure_retries_with_backoff_without_comment_spam(self):
        for attempt in range(3):
            with patch.object(pr_watch, "pr_snapshot", return_value=pr()), \
                    patch.object(pr_watch, "semantic_review", side_effect=pr_watch_common.WatchError("invalid output")), \
                    patch.object(self.watcher, "merge") as merge:
                self.watcher.process("example/one", 7)
            merge.assert_not_called()
            self.assertEqual(self.watcher.state.get("example/one", 7)["outcome"], "retry")
            self.watcher.state.data["prs"]["example/one#7"]["retry_after"] = 0
        self.assertEqual(self.comments, [])

    def test_conflicting_pr_waits_without_worker_or_requeue(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(mergeable="CONFLICTING", mergeStateStatus="DIRTY")), \
                patch.object(pr_watch, "semantic_review") as review:
            self.watcher.process("example/one", 7)
        review.assert_not_called()
        with patch.object(pr_watch, "gh_json", return_value=[{"number": 7, "headRefOid": SHA_A, "isDraft": False,
                                                               "baseRefName": "main", "mergeable": "CONFLICTING"}]), \
                patch.object(self.watcher, "enqueue") as enqueue:
            self.watcher.discover()
        enqueue.assert_not_called()

    def test_failure_summary_retains_actual_errors(self):
        result = subprocess.CompletedProcess([], 1, "PASS earlier lane\n" + "\n".join("ordinary" for _ in range(200)) +
                                               "\n--- FAIL: TestRealDefect\npanic: actual cause\n", "")
        summary = pr_watch_verify.result_summary(result)
        self.assertIn("--- FAIL: TestRealDefect", summary)
        self.assertIn("panic: actual cause", summary)

    def test_failure_summary_keeps_unknown_diagnostics_and_final_test_names(self):
        plain = subprocess.CompletedProcess([], 1, "slot order: generated row is absent from the baseline", "")
        self.assertIn("absent from the baseline", pr_watch_verify.result_summary(plain))
        passing = "\n".join("PASS: handles no error and client errors %d" % n for n in range(100))
        failed = passing + "\nFailed tests:\n--- FAIL: TestActualAssertion\n    got 1, want 2\n"
        summary = pr_watch_verify.result_summary(subprocess.CompletedProcess([], 1, failed, ""))
        self.assertIn("TestActualAssertion", summary)
        self.assertIn("got 1, want 2", summary)
        self.assertNotIn("handles no error and client errors 0", summary)
        python_failure = "Traceback (most recent call last):\n  File \"test_x.py\", line 2\n    self.assertEqual(1, 2)\nAssertionError: 1 != 2\nFAILED (failures=1)"
        summary = pr_watch_verify.result_summary(subprocess.CompletedProcess([], 1, python_failure, ""))
        self.assertIn("AssertionError: 1 != 2", summary)
        other = subprocess.CompletedProcess([], 1, "output before\nError: compiler rejected input\n1 tests failed.", "")
        summary = pr_watch_verify.result_summary(other)
        self.assertIn("compiler rejected input", summary)

    def test_unknown_mergeability_waits(self):
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(mergeable="UNKNOWN")), \
                patch.object(pr_watch, "semantic_review") as review:
            self.watcher.process("example/one", 7)
        review.assert_not_called()

    def test_moved_head_discards_passed_checks(self):
        with patch.object(pr_watch, "pr_snapshot", side_effect=[pr(), pr(SHA_B)]), \
                patch.object(pr_watch, "semantic_review", return_value=[]), \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]), \
                patch.object(self.watcher, "merge") as merge:
            self.watcher.process("example/one", 7)
        merge.assert_not_called()

    def test_another_persons_review_never_skips_verification(self):
        # The service reads only its own versioned state, not an arbitrary GitHub review.
        with patch.object(pr_watch, "pr_snapshot", return_value=pr(reviewDecision="COMMENTED")), \
                patch.object(pr_watch, "semantic_review", return_value=[]) as review, \
                patch.object(pr_watch, "verify", return_value=[{"name": "test", "ok": True, "command": [], "summary": ""}]), \
                patch.object(self.watcher, "merge", return_value=False):
            self.watcher.process("example/one", 7)
        review.assert_called_once()

    def test_inline_review_uses_stdin_payload_and_falls_back(self):
        findings = [{"path": "x.py", "line": 2, "severity": "P1", "summary": "wrong", "suggestion": "fix"}]
        requests = []
        def fake_run(argv, **kwargs):
            requests.append((argv, kwargs))
            if argv[:2] == ["gh", "api"]:
                return subprocess.CompletedProcess(argv, 422, "", "bad anchor")
            return subprocess.CompletedProcess(argv, 0, "{}", "")
        with patch.object(pr_watch, "run", side_effect=fake_run):
            self.watcher.comment_findings("example/one", 7, pr(), findings)
        payload = json.loads(requests[0][1]["input_text"])
        self.assertEqual(payload["comments"][0]["line"], 2)
        self.assertEqual(payload["commit_id"], SHA_A)
        self.assertEqual(requests[1][0][:2], ["gh", "pr"])

    def test_lock_excludes_a_second_process(self):
        first = pr_watch.Watcher(self.config, verbose=False)
        second = pr_watch.Watcher(self.config, verbose=False)
        first.single_instance()
        self.addCleanup(first.instance.close)
        with self.assertRaises(pr_watch_common.WatchError):
            second.single_instance()
        second.instance.close()

    def test_config_has_no_implicit_merge_or_repositories(self):
        path = self.root / "config.json"
        path.write_text(json.dumps({"repositories": [{"name": "example/one", "checkout": str(self.root), "verify": {}}]}))
        config = pr_watch.load_config(path)
        self.assertFalse(config["repositories"][0]["merge"])
        self.assertEqual(len(config["repositories"]), 1)


class ReviewTests(unittest.TestCase):
    def test_schema_and_changed_line_validation(self):
        envelope = {"subtype": "success", "is_error": False, "permission_denials": [],
                    "structured_output": {"findings": [{"path": "a.py", "line": 2, "severity": "P1",
                                                        "summary": "wrong", "suggestion": "fix"}]}}
        self.assertEqual(len(pr_watch_review.parse_result(json.dumps(envelope), {"a.py": {2}})), 1)
        for anchors in ({"a.py": {1}}, {"other.py": {2}}):
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_review.parse_result(json.dumps(envelope), anchors)
        envelope["permission_denials"] = [{"tool": "Bash"}]
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_review.parse_result(json.dumps(envelope), {"a.py": {2}})
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_review.parse_result("not json", {"a.py": {2}})

    def test_diff_anchors_include_only_added_lines(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull,
                       GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                       GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")
            def git(*args):
                subprocess.run(["git"] + list(args), cwd=root, env=env, check=True, capture_output=True)
            git("init", "-q", "-b", "main")
            (root / "a.py").write_text("one\ntwo\n")
            git("add", "a.py")
            git("commit", "-qm", "base")
            base = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            (root / "a.py").write_text("one\nchanged\nthree\n")
            git("commit", "-qam", "head")
            head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            self.assertEqual(pr_watch_review.changed_lines(root, base, head), {"a.py": {2, 3}})


class EventTests(unittest.TestCase):
    def test_webhook_authentication_repository_and_callback(self):
        secret = "x" * 32
        body = json.dumps({"action": "synchronize", "repository": {"full_name": "example/one"},
                           "pull_request": {"number": 7, "draft": False, "head": {"sha": SHA_A}}}).encode()
        signature = "sha256=" + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()
        event = pr_watch_events.parse_pull_request(body, signature, "pull_request", secret, {"example/one"})
        self.assertEqual(event, ("example/one", 7, SHA_A))
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_events.parse_pull_request(body, "sha256=bad", "pull_request", secret, {"example/one"})
        with self.assertRaises(pr_watch_common.WatchError):
            pr_watch_events.parse_pull_request(body, signature, "pull_request", secret, {"example/other"})


class VerifyTests(unittest.TestCase):
    def test_persona_apple_cannot_be_silently_skipped(self):
        path = "apple/Persona/App/View.swift"
        with patch.object(pr_watch_verify, "checked", side_effect=[path + "\n", "+import SwiftUI"]), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"apple": True, "scripts": True}
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_verify.persona_checks(".", pr(), 1, {})
        with patch.object(pr_watch_verify, "checked", side_effect=[path + "\n", "+import SwiftUI"]), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"apple": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(), 1, {"apple_command": ["verify", "{head}"],
                                                                      "ui_evidence_command": ["visual", "{worktree}"]})
        self.assertIn(("trusted Apple verification", ["verify", SHA_A]), commands)
        self.assertIn(("visual evidence", ["visual", "."]), commands)

    def test_non_ui_apple_paths_are_explicit_and_diff_is_checked(self):
        path = "apple/Packages/PersonaKit/Sources/PersonaKit/StreamChunks.swift"
        with patch.object(pr_watch_verify, "checked", return_value="+chunks.removeFirst()\n"):
            self.assertTrue(pr_watch_verify.non_ui_apple_change(".", SHA_BASE, SHA_A, [path]))
        for changed in ("+import SwiftUI\n+struct ChunkView: View {}", "+Text(localized: \"Ready\")",
                        "+NavigationStack { Content() }", "+.sheet(isPresented: $shown) { Content() }",
                        "+.padding(8)", "+Image(systemName: \"checkmark\")"):
            with patch.object(pr_watch_verify, "checked", return_value=changed):
                self.assertFalse(pr_watch_verify.non_ui_apple_change(".", SHA_BASE, SHA_A, [path]))
        for changed in ("+import SwiftUI", "+NavigationStack { Content() }"):
            with patch.object(pr_watch_verify, "checked", return_value=changed):
                self.assertFalse(pr_watch_verify.non_ui_apple_change(
                    ".", SHA_BASE, SHA_A, ["apple/Persona/Views/Session/FilesPane.swift"]))
        with patch.object(pr_watch_verify, "checked", return_value="+.padding(8)"):
            self.assertFalse(pr_watch_verify.non_ui_apple_change(
                ".", SHA_BASE, SHA_A, ["apple/PersonaUITests/MinutesTests.swift", "apple/Persona/Views/Session/FilesPane.swift"]))
        for names in ([path, "web/src/views/ChatView.tsx"],
                      ["apple/Shared/Localization/Localizable.xcstrings"]):
            self.assertFalse(pr_watch_verify.non_ui_apple_change(".", SHA_BASE, SHA_A, names))
        with patch.object(pr_watch_verify, "checked", return_value="+import SwiftUI"):
            self.assertFalse(pr_watch_verify.non_ui_apple_change(
                ".", SHA_BASE, SHA_A, ["apple/Persona/App/TestsSupport/Feature.swift"]))
        with patch.object(pr_watch_verify, "checked", return_value="+Text(ready)"):
            self.assertFalse(pr_watch_verify.non_ui_apple_change(
                ".", SHA_BASE, SHA_A, ["apple/Persona/Recording/RecordingMinutesView.swift"]))

        self.assertTrue(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["apple/PersonaUITests/MinutesTests.swift"]))
        with patch.object(pr_watch_verify, "checked", return_value="+chunks.removeFirst()\n"):
            self.assertTrue(pr_watch_verify.non_ui_apple_change(
                ".", SHA_BASE, SHA_A, ["apple/Persona/App/TasksStore.swift"]))
        self.assertTrue(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["native/app-core/crates/persona-core-runtime/src/lib.rs",
                                    "android/core/rustcore/src/main/kotlin/persona/core/rustcore/Codec.kt",
                                    "schema/extension/manifest.json"]))
        self.assertFalse(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["native/app-core/crates/persona-core-ffi/src/lib.rs"]))
        self.assertFalse(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["native/app-core/crates/persona-core-typegen/src/main.rs"]))
        self.assertFalse(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["native/app-core/crates/persona-core-runtime/src/wire.rs"]))
        self.assertFalse(pr_watch_verify.non_ui_apple_change(
            ".", SHA_BASE, SHA_A, ["web/src/views/ChatView.tsx"]))

    def test_eligible_non_ui_apple_change_runs_remaining_lanes(self):
        names = "apple/Packages/PersonaKit/Sources/PersonaKit/StreamChunks.swift\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "non_ui_apple_change", return_value=True) as classifier, \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"apple": True, "scripts": True, "server": True}
            commands = pr_watch_verify.persona_checks(".", pr(), 1, {})
        self.assertEqual(commands, [("CI server", ["bash", "scripts/ci/server.sh"]),
                                    ("CI scripts", ["bash", "scripts/ci/scripts.sh"]),
                                    ("Apple non-UI review policy", ["true"])])
        classifier.assert_called_once()

    def test_shared_core_lane_is_not_dropped(self):
        names = "native/app-core/crates/persona-core-runtime/src/lib.rs\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "non_ui_apple_change", return_value=True), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"apple": True, "core": True, "android": True, "scripts": True}
            commands = pr_watch_verify.persona_checks(".", pr(), 1, {})
        self.assertEqual(commands, [("CI android", ["bash", "scripts/ci/android.sh"]),
                                    ("CI scripts", ["bash", "scripts/ci/scripts.sh"]),
                                    ("CI core", ["bash", "scripts/ci/core.sh"]),
                                    ("Apple non-UI review policy", ["true"])])

    def test_apple_ffi_does_not_use_the_shared_non_ui_exception(self):
        names = "native/app-core/crates/persona-core-ffi/src/lib.rs\n"
        with patch.object(pr_watch_verify, "checked", return_value=names), \
                patch.object(pr_watch_verify, "load_paths") as selector:
            selector.return_value.select.return_value = {"apple": True, "core": True, "scripts": True}
            with self.assertRaises(pr_watch_common.WatchError):
                pr_watch_verify.persona_checks(".", pr(), 1, {})

    def test_extension_shared_changes_use_full_ci(self):
        with patch.object(pr_watch_verify, "extension_modules", return_value=None), \
                patch.object(pr_watch_verify, "checked", return_value="scripts/ci.sh\n"):
            self.assertEqual(pr_watch_verify.extension_checks(".", pr(), 1),
                             [("extension repository CI", ["bash", "scripts/ci.sh"])])


class MergeTests(unittest.TestCase):
    def test_generic_merge_retains_exact_refs_for_the_privacy_scanner(self):
        commands = []
        def fake_checked(argv, **kwargs):
            commands.append(argv)
            if argv[:2] == ["git", "rev-parse"]:
                return (SHA_BASE if "bases" in argv[2] else SHA_A) + "\n"
            return ""
        with patch.object(pr_watch_merge, "pr_snapshot", side_effect=[pr(), pr(state="MERGED")]), \
                patch.object(pr_watch_merge, "checked", side_effect=fake_checked), \
                patch.object(pr_watch_merge, "run", return_value=subprocess.CompletedProcess([], 0, "", "")) as execute:
            pr_watch_merge.merge("example/one", 7, ".", privacy="scanner.py",
                                 expected_head=SHA_A, expected_base=SHA_BASE)
        self.assertIn(["git", "fetch", "-q", "origin", "+pull/7/head:refs/pr-watch/heads/7"], commands)
        self.assertIn(["git", "fetch", "-q", "origin", "+pull/7/head:refs/pr-watch/heads/7"], commands)
        self.assertIn(["git", "fetch", "-q", "origin", "+refs/heads/main:refs/pr-watch/bases/7"], commands)
        scan = execute.call_args_list[0].args[0]
        self.assertIn(SHA_BASE + ".." + SHA_A, scan)

    def test_issue_link_parser_ignores_code_and_other_repositories(self):
        self.assertEqual(pr_watch_merge.linked_issue("```\nFixes #1\n```\nRefs #2", "example/one"), [2])
        self.assertEqual(pr_watch_merge.linked_issue("Fixes example/two#3", "example/one"), [])
        self.assertEqual(pr_watch_merge.linked_issue("See https://github.com/example/one/issues/4", "example/one"), [4])


if __name__ == "__main__":
    unittest.main()
