#!/usr/bin/env python3
"""Resident, multi-repository semantic PR review and exact-head merging.

This is maintainer infrastructure, not a Persona hub service. A fresh config is
review-only. GitHub writes and merging require explicit local opt-in.
"""
import argparse
import fcntl
import json
import os
import queue
import shutil
import sys
import tempfile
import threading
import time
import traceback
from contextlib import contextmanager
from pathlib import Path

# python -I removes the script's directory from sys.path. These modules are the
# trusted sibling implementation, not imports from an untrusted PR checkout.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from pr_watch_common import WatchError, atomic_json, bounded, checked, fingerprint, gh_json, pr_snapshot, repository, run, SHA, verification_env
from pr_watch_merge import linked_issue, merge as generic_merge
import pr_watch_merge
from pr_watch_review import review as semantic_review
from pr_watch_verify import verify

FORMAT_VERSION = 1


def load_config(path):
    try:
        raw = json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise WatchError("cannot read config: %s" % exc)
    if not isinstance(raw, dict) or not isinstance(raw.get("repositories"), list):
        raise WatchError("config needs a repositories array")
    seen = set()
    repos = []
    for item in raw["repositories"]:
        if not isinstance(item, dict):
            raise WatchError("repository config must be an object")
        repo = repository(item.get("name"))
        if repo in seen:
            raise WatchError("duplicate repository: " + repo)
        seen.add(repo)
        root = item.get("checkout")
        if not isinstance(root, str) or not Path(root).is_absolute():
            raise WatchError("checkout must be an absolute local path")
        base = item.get("base", "main")
        if not isinstance(base, str) or not base or base.startswith("-") or any(c in base for c in " ~^:?*[\\"):
            raise WatchError("invalid base branch")
        if not isinstance(item.get("verify"), dict):
            raise WatchError("verify config must be an object")
        if item.get("merge", False) is not True and item.get("merge", False) is not False:
            raise WatchError("merge must be a boolean")
        repos.append({"name": repo, "checkout": str(Path(root).expanduser()), "base": base,
                      "verify": item["verify"], "merge": item.get("merge", False),
                      "privacy": item.get("privacy"), "notify_command": item.get("notify_command")})
    reviewer = raw.get("reviewer", {})
    if not isinstance(reviewer, dict):
        raise WatchError("reviewer config must be an object")
    webhook = raw.get("webhook", {})
    if not isinstance(webhook, dict):
        raise WatchError("webhook config must be an object")
    return {"repositories": repos, "reviewer": reviewer, "webhook": webhook,
            "poll_seconds": int(raw.get("poll_seconds", 60)), "workers": int(raw.get("workers", 2)),
            "state": raw.get("state", str(Path(path).parent / "state.json"))}


class State:
    def __init__(self, path):
        self.path = Path(path)
        self.lock = threading.RLock()
        if self.path.exists():
            try:
                self.data = json.loads(self.path.read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError) as exc:
                raise WatchError("cannot read watcher state: %s" % exc)
        else:
            self.data = {}
        if not isinstance(self.data, dict):
            raise WatchError("watcher state must be an object")
        if self.data.get("version") not in (None, FORMAT_VERSION):
            raise WatchError("unsupported watcher state version")
        self.data["version"] = FORMAT_VERSION
        self.data.setdefault("prs", {})

    def get(self, repo, number):
        with self.lock:
            item = self.data["prs"].get(repo + "#" + str(number), {})
            return json.loads(json.dumps(item))

    def put(self, repo, number, head, **fields):
        key = repo + "#" + str(number)
        with self.lock:
            item = self.data["prs"].setdefault(key, {})
            evidence_key = fields.get("evidence_key")
            if item.get("head") != head or (evidence_key and item.get("evidence_key") != evidence_key):
                item.clear()
                item["head"] = head
            item.update(fields)
            item["updated_at"] = int(time.time())
            atomic_json(self.path, self.data)
            return dict(item)


class Watcher:
    def __init__(self, config, dry_run=False, verbose=True):
        self.config = config
        self.repositories = {item["name"]: item for item in config["repositories"]}
        self.dry_run = dry_run
        self.verbose = verbose
        self.state = State(config["state"])
        self.tasks = queue.Queue()
        self.pending = {}
        self.active = {}
        self.wake = threading.Event()
        self.fatal = None
        self.queue_lock = threading.Lock()
        self.git_lock = threading.Lock()
        self.finish_lock = threading.Lock()
        self.merge_locks = {}
        self.merge_locks_guard = threading.Lock()
        self.log_lock = threading.Lock()
        self.stopping = threading.Event()
        self.wake = threading.Event()
        self.instance = None
        self.events = None
        self.workers = []

    def log(self, message):
        if not self.verbose:
            return
        with self.log_lock:
            print(time.strftime("%Y-%m-%d %H:%M:%S ") + message, flush=True)

    def single_instance(self):
        # Record this configuration's SHA. An old numeric-only state is not
        # silently reused after a repository mapping or gate changes.
        digest = fingerprint("configuration", 0, "0" * 40, "config", self.config)
        marker = Path(self.config["state"]).with_suffix(".config")
        marker.parent.mkdir(parents=True, exist_ok=True)
        if marker.exists() and marker.read_text(encoding="utf-8").strip() != digest:
            raise WatchError("watcher configuration changed; use a fresh state file or an explicit migration")
        marker.write_text(digest + "\n", encoding="utf-8")
        os.chmod(str(marker), 0o600)
        path = Path(self.config["state"]).with_suffix(".lock")
        path.parent.mkdir(parents=True, exist_ok=True)
        self.instance = path.open("a+")
        try:
            fcntl.flock(self.instance.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise WatchError("another watcher instance already holds the service lock")

    def discover(self):
        try:
            for repo, config in self.repositories.items():
                raw = gh_json(["pr", "list", "--repo", repo, "--state", "open", "--json",
                               "number,headRefOid,isDraft,baseRefName,mergeable", "--limit", "200"])
                if not isinstance(raw, list):
                    raise WatchError("PR discovery did not return an array")
                for item in raw:
                    if not isinstance(item, dict) or not isinstance(item.get("number"), int):
                        raise WatchError("invalid PR discovery entry")
                    if item.get("isDraft") or item.get("baseRefName") != config["base"]:
                        continue
                    if item.get("mergeable") == "CONFLICTING":
                        continue
                    if not isinstance(item.get("headRefOid"), str) or not SHA.fullmatch(item["headRefOid"]):
                        raise WatchError("PR discovery entry has no valid head SHA")
                    self.enqueue(repo, item["number"], item["headRefOid"], "poll")
        except WatchError as error:
            self.log("discovery failed: " + str(error))
            self.fatal = error
            self.wake.set()
            raise

    def enqueue(self, repo, number, sha, source):
        if repo not in self.repositories or not isinstance(number, int) or number < 1:
            return
        key = (repo, number)
        with self.queue_lock:
            if key in self.active:
                old = self.pending.get(key)
                if old != sha:
                    self.pending[key] = sha
                    self.log("%s#%s: newer head noticed while busy" % (repo, number))
                return
            self.active[key] = {"started": time.monotonic(), "phase": "queued", "source": source}
        self.tasks.put(key)
        self.log("%s#%s queued (%s)" % (repo, number, source))

    def phase(self, key, phase):
        with self.queue_lock:
            if key in self.active:
                self.active[key]["phase"] = phase
                self.active[key]["started"] = time.monotonic()

    @contextmanager
    def merging(self, repo):
        """Generic merges share the file lock; Persona delegates it to merge-pr.sh."""
        with self.merge_locks_guard:
            lock = self.merge_locks.setdefault(repo, threading.Lock())
        with lock:
            if repo == repository("nwverse/persona"):
                yield
            else:
                with pr_watch_merge.merge_lock(self.repositories[repo]["checkout"]):
                    yield

    def worktree(self, repo, number, pr):
        config = self.repositories[repo]
        root = config["checkout"]
        state_root = Path(self.config["state"]).resolve().parent / "worktrees"
        state_root.mkdir(parents=True, exist_ok=True)
        worktree = Path(tempfile.mkdtemp(prefix="%s-%s-" % (repo.split("/")[1], number), dir=state_root))
        try:
            with self.git_lock:
                identity = checked(["gh", "repo", "view", repo, "--json", "nameWithOwner", "--jq", ".nameWithOwner"], cwd=root)
                if identity.strip() != repo:
                    raise WatchError("checkout does not belong to " + repo)
                base = pr["baseRefOid"]
                checked(["git", "fetch", "-q", "origin", "refs/heads/" + pr["baseRefName"]], cwd=root, timeout=180)
                fetched_base = checked(["git", "rev-parse", "FETCH_HEAD"], cwd=root).strip()
                if fetched_base != base:
                    raise WatchError("base moved while preparing the PR")
                checked(["git", "fetch", "-q", "origin", "pull/%s/head" % number], cwd=root, timeout=180)
                if checked(["git", "rev-parse", "FETCH_HEAD"], cwd=root).strip() != pr["headRefOid"]:
                    raise WatchError("head moved while preparing the PR")
                # The temporary directory exists only as an mkdtemp reservation.
                worktree.rmdir()
                checked(["git", "worktree", "add", "--detach", str(worktree), pr["headRefOid"]], cwd=root, timeout=180)
            return worktree
        except BaseException:
            shutil.rmtree(str(worktree), ignore_errors=True)
            raise

    def remove_worktree(self, repo, worktree):
        if not worktree:
            return
        with self.git_lock:
            result = run(["git", "worktree", "remove", "--force", str(worktree)],
                         cwd=self.repositories[repo]["checkout"], timeout=90)
            if result.returncode:
                self.log("temporary worktree cleanup failed: " + bounded(result.stderr, 300))

    def comment_findings(self, repo, number, pr, findings):
        body = "Automated semantic review at `%s` found %d issue(s). This head was not merged.\n\nFix the findings and push a new commit for a fresh review." % (
            pr["headRefOid"][:9], len(findings))
        comments = [{"path": f["path"], "line": f["line"], "side": "RIGHT",
                     "body": "**%s** — %s\n\n**Suggestion:** %s" % (f["severity"], f["summary"], f["suggestion"])}
                    for f in findings]
        payload = {"commit_id": pr["headRefOid"], "event": "COMMENT", "body": body, "comments": comments}
        result = run(["gh", "api", "repos/%s/pulls/%s/reviews" % (repo, number), "--input", "-"],
                     input_text=json.dumps(payload), timeout=90)
        if result.returncode:
            # A moved diff anchor must not discard the useful review or permit a merge.
            fallback = body + "\n\n" + "\n\n".join(
                "%d. **%s `%s:%s`** — %s\n   **Suggestion:** %s" %
                (index, f["severity"], f["path"], f["line"], f["summary"], f["suggestion"])
                for index, f in enumerate(findings, 1))
            result = run(["gh", "pr", "review", str(number), "--repo", repo, "--comment", "--body", bounded(fallback, 55000)], timeout=90)
        if result.returncode:
            raise WatchError("could not post review findings: " + bounded(result.stderr, 1000))

    def comment(self, repo, number, body):
        result = run(["gh", "pr", "comment", str(number), "--repo", repo, "--body", bounded(body, 55000)], timeout=90)
        if result.returncode:
            raise WatchError("could not post PR comment: " + bounded(result.stderr, 1000))

    def success_review(self, repo, number, pr, results):
        body = ("Automated independent semantic review at `%s` found no blocking defects.\n\n"
                "Local checks completed successfully:\n%s\n\n"
                "This records only the checks listed above; it does not claim unrun device or visual acceptance.") % (
            pr["headRefOid"][:9], "\n".join("- `%s`" % r["name"] for r in results))
        result = run(["gh", "pr", "review", str(number), "--repo", repo, "--comment", "--body", body], timeout=90)
        if result.returncode:
            raise WatchError("could not post successful review: " + bounded(result.stderr, 1000))

    def notify(self, config, number, head, outcome):
        command = config.get("notify_command")
        if not command:
            return
        if not isinstance(command, list) or not command or not all(isinstance(x, str) and x for x in command):
            raise WatchError("notify_command must be an argv array")
        result = run(command + [config["name"], str(number), head, outcome], timeout=90)
        if result.returncode:
            self.log("notification failed: " + bounded(result.stderr, 300))

    def merge(self, config, pr):
        repo, number, root = config["name"], pr["number"], config["checkout"]
        if self.dry_run or not config["merge"]:
            self.log("%s#%s passed; merge disabled" % (repo, number))
            return False
        if repo == repository("nwverse/persona"):
            # merge-pr.sh owns the shared lock for this path. Acquiring it in
            # another process while self.merging() holds it would deadlock.
            wrapper = str(Path(__file__).resolve().parents[2] / "scripts" / "hooks" / "merge-pr.sh")
            result = run(["bash", wrapper, "--expect-head", pr["headRefOid"],
                          "--expect-base", pr["baseRefOid"], str(number)], cwd=root, timeout=600)
            if result.returncode:
                raise WatchError("exact-head merge failed: " + bounded(result.stderr or result.stdout, 2000))
        else:
            privacy = config.get("privacy")
            generic_merge(repo, number, root, privacy=privacy, timeout=600,
                          expected_head=pr["headRefOid"], expected_base=pr["baseRefOid"])
        if pr_snapshot(repo, number)["state"] != "MERGED":
            raise WatchError("merge returned without a merged PR")
        self.log("%s#%s merged at %s" % (repo, number, pr["headRefOid"][:9]))
        return True

    def mergeable(self, pr):
        if pr["state"] != "OPEN" or pr["isDraft"]:
            return False
        if pr["mergeable"] == "CONFLICTING":
            return False
        if pr["mergeable"] == "UNKNOWN":
            # Let the next round observe a definite state; never interpret an
            # unknown GitHub state as either permission or a conflict.
            return False
        if pr["mergeable"] != "MERGEABLE" or pr.get("mergeStateStatus") not in ("CLEAN", "HAS_HOOKS", "UNSTABLE"):
            raise WatchError("PR is not mergeable; update its branch or resolve conflicts")
        if pr.get("reviewDecision") == "CHANGES_REQUESTED":
            raise WatchError("PR has an outstanding changes request")
        return True

    def process(self, repo, number):
        config = self.repositories[repo]
        key = (repo, number)
        self.phase(key, "reading PR")
        try:
            pr = pr_snapshot(repo, number)
        except WatchError as error:
            self.log("%s#%s snapshot unavailable; no state or merge decision: %s" % (repo, number, bounded(error, 500)))
            return
        if pr["baseRefName"] != config["base"] or pr["state"] != "OPEN" or pr["isDraft"]:
            return
        head = pr["headRefOid"]
        old = self.state.get(repo, number)
        evidence_key = repo + "#" + str(number) + "@" + pr["baseRefOid"] + ".." + head
        if old.get("evidence_key") != evidence_key:
            old = {}
        if old.get("head") == head and old.get("retry_after", 0) > time.time():
            return
        if old.get("head") == head and old.get("outcome") in ("findings", "failed", "merged", "passed"):
            if self.dry_run or old.get("review_posted") or old.get("outcome") == "merged":
                return
            # A dry run keeps semantic/test evidence but has no external side
            # effects. A live run resumes publication and the merge gate.
            if old.get("semantic") == "findings":
                self.comment_findings(repo, number, pr, old.get("findings", []))
                self.state.put(repo, number, head, evidence_key=evidence_key, review_posted=True)
                return
            if old.get("outcome") == "failed":
                self.comment(repo, number, "Automated review at `%s` did not pass. Not merged.\n\n```text\n%s\n```" %
                             (head[:9], bounded(old.get("failure", "verification failed"), 4500)))
                self.state.put(repo, number, head, evidence_key=evidence_key, failure_posted=True)
                return
        if not self.mergeable(pr):
            return
        phase = "merge"
        if old.get("head") == head and old.get("outcome") == "merge_failed":
            if old.get("retry_after", 0) > time.time():
                return
            if old.get("blocking"):
                return
            self.phase(key, "retrying exact-head merge")
            with self.merging(repo):
                current = pr_snapshot(repo, number)
                if not self.mergeable(current) or current["headRefOid"] != head or current["baseRefOid"] != pr["baseRefOid"]:
                    return
                self.finish_merge(config, current, old)
            return
        worktree = None
        phase = "preparing"
        evidence_key = repo + "#" + str(number) + "@" + pr["baseRefOid"] + ".." + head
        try:
            self.phase(key, "preparing isolated checkout")
            worktree = self.worktree(repo, number, pr)
            phase = "semantic"
            current = self.state.get(repo, number)
            if current.get("evidence_key") != evidence_key or current.get("format") != FORMAT_VERSION:
                current = self.state.put(repo, number, head, evidence_key=evidence_key,
                                         base=pr["baseRefOid"], format=FORMAT_VERSION, outcome="reviewing")
            if current.get("semantic") != "passed" and current.get("semantic") != "findings":
                self.phase(key, "independent semantic review")
                findings = semantic_review(repo, pr, str(worktree), self.config["reviewer"])
                semantic = "findings" if findings else "passed"
                current = self.state.put(repo, number, head, semantic=semantic, findings=findings,
                                         review_fingerprint=fingerprint(repo, number, head, "semantic", findings))
            findings = current.get("findings", [])
            if current.get("semantic") == "findings":
                if not current.get("review_posted"):
                    self.phase(key, "posting actionable findings")
                    if not self.dry_run:
                        self.comment_findings(repo, number, pr, findings)
                    current = self.state.put(repo, number, head, evidence_key=evidence_key,
                                             review_posted=not self.dry_run, outcome="findings")
                self.log("%s#%s has %d semantic finding(s); not merged" % (repo, number, len(findings)))
                return
            if current.get("verification") != "passed":
                self.phase(key, "repository verification")
                phase = "verification"
                results = verify(repo, pr, str(worktree), config["verify"], environ=verification_env())
                current = self.state.put(repo, number, head, verification="passed", checks=results)
            with self.merging(repo):
                self.phase(key, "rechecking exact head")
                current_pr = pr_snapshot(repo, number)
                if (current_pr["headRefOid"] != head or current_pr["baseRefOid"] != pr["baseRefOid"]
                        or not self.mergeable(current_pr)):
                    self.log("%s#%s moved during review; old result discarded" % (repo, number))
                    return
                if not linked_issue(current_pr["body"], repo):
                    raise WatchError("PR has no linked issue; not merged")
                if not current.get("review_posted"):
                    self.phase(key, "posting successful review")
                    if not self.dry_run:
                        self.success_review(repo, number, current_pr, current.get("checks", []))
                    current = self.state.put(repo, number, head, evidence_key=evidence_key,
                                             review_posted=not self.dry_run)
                self.finish_merge(config, current_pr, current)
        except WatchError as error:
            # A missing semantic answer never becomes approval. Verification
            # failures are durable for this head; infrastructure errors get a
            # bounded retry before one concise public failure comment.
            attempts = int(old.get("review_attempts", 0)) + 1
            retry = (phase in ("preparing", "semantic", "merge") and attempts < 3
                     and old.get("verification") != "passed" and phase != "merge")
            outcome = "retry" if retry else ("merge_failed" if phase == "merge" else "failed")
            self.state.put(repo, number, head, evidence_key=evidence_key, outcome=outcome,
                           review_attempts=attempts, retry_after=time.time() + min(300, 30 * (2 ** (attempts - 1))),
                           failure=str(error))
            if not retry and not old.get("failure_posted"):
                if not self.dry_run:
                    self.comment(repo, number, "Automated review at `%s` could not complete. Not merged.\n\n```text\n%s\n```" %
                                 (head[:9], bounded(error, 4500)))
                self.state.put(repo, number, head, evidence_key=evidence_key, failure_posted=True)
            self.log("%s#%s %s failed: %s" % (repo, number, phase, bounded(error, 700)))
        finally:
            self.remove_worktree(repo, worktree)

    def finish_merge(self, config, pr, current):
        repo, number, head = config["name"], pr["number"], pr["headRefOid"]
        try:
            merged = self.merge(config, pr)
            outcome = "merged" if merged else "passed"
            self.state.put(repo, number, head, outcome=outcome, merged=merged)
            if not self.dry_run:
                self.notify(config, number, head, outcome)
        except WatchError as error:
            blocking = "not mergeable" in str(error) or "conflict" in str(error) or "issue" in str(error)
            retry = 0 if blocking else time.time() + min(900, 60 * (2 ** min(int(current.get("merge_attempts", 0)), 4)))
            self.state.put(repo, number, head, outcome="merge_failed", blocking=blocking,
                           merge_attempts=int(current.get("merge_attempts", 0)) + 1,
                           retry_after=retry, failure=str(error))
            self.log("%s#%s merge blocked: %s" % (repo, number, bounded(error, 700)))

    def worker(self):
        while True:
            key = self.tasks.get()
            if key is None:
                self.tasks.task_done()
                return
            repo, number = key
            try:
                self.process(repo, number)
            except Exception as error:
                # Programming errors are logged, never interpreted as a pass.
                self.log("%s#%s worker error: %s: %s" % (repo, number, type(error).__name__, bounded(error, 500)))
                if self.verbose:
                    traceback.print_exc()
            finally:
                requeue = False
                with self.queue_lock:
                    self.active.pop(key, None)
                    latest = self.pending.pop(key, None)
                    if latest is not None:
                        requeue = True
                self.tasks.task_done()
                if requeue and not self.stopping.is_set():
                    self.enqueue(repo, number, latest, "updated head")

    def heartbeat(self):
        while not self.stopping.wait(60):
            with self.queue_lock:
                detail = ", ".join("%s#%s %s %ss" % (repo, number, item["phase"], int(time.monotonic() - item["started"]))
                                   for (repo, number), item in sorted(self.active.items()))
            self.log("workers: " + (detail or "idle"))

    def start_workers(self):
        count = self.config["workers"]
        if not 1 <= count <= 16:
            raise WatchError("workers must be between 1 and 16")
        self.workers = [threading.Thread(target=self.worker, daemon=True) for _ in range(count)]
        for worker in self.workers:
            worker.start()

    def start_events(self):
        from pr_watch_events import Events
        settings = self.config["webhook"]
        secret = os.environ.get("PR_WATCH_WEBHOOK_SECRET", "")
        self.events = Events(secret, int(settings.get("port", 18777)), set(self.repositories), self.enqueue, self.log)
        self.events.start()

    def run(self, once=False):
        self.single_instance()
        self.start_workers()
        threading.Thread(target=self.heartbeat, daemon=True).start()
        self.start_events()
        poll = self.config["poll_seconds"]
        if not 30 <= poll <= 3600:
            raise WatchError("poll_seconds must be between 30 and 3600")
        try:
            while not self.stopping.is_set():
                self.discover()
                if once:
                    self.tasks.join()
                    break
                self.wake.wait(poll)
                self.wake.clear()
                if self.fatal:
                    raise self.fatal
        finally:
            self.stopping.set()
            for _ in self.workers:
                self.tasks.put(None)
            for worker in self.workers:
                worker.join(timeout=30)
            if self.events:
                self.events.close()
            if self.instance:
                self.instance.close()


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", default=os.environ.get("PR_WATCH_CONFIG", str(Path.home() / ".config" / "persona" / "pr-watch.json")))
    parser.add_argument("--once", action="store_true", help="one discovery round, then wait for its queued work")
    parser.add_argument("--dry-run", action="store_true", help="review and test locally, but never comment, notify or merge")
    parser.add_argument("--repository", action="append", help="restrict this run to a configured owner/name; repeatable")
    parser.add_argument("--pr", type=int, action="append", help="restrict this run to PR numbers; requires --repository")
    args = parser.parse_args(argv)
    config = load_config(args.config)
    if args.repository:
        unknown = set(args.repository) - {r["name"] for r in config["repositories"]}
        if unknown:
            raise WatchError("repository is not configured: " + ", ".join(sorted(unknown)))
        config["repositories"] = [r for r in config["repositories"] if r["name"] in args.repository]
    if args.pr:
        if len(config["repositories"]) != 1:
            raise WatchError("--pr requires exactly one selected repository")
        selected = set(args.pr)
        watcher = Watcher(config, args.dry_run)
        watcher.discover = lambda: [watcher.enqueue(config["repositories"][0]["name"], number, "", "manual") for number in sorted(selected)]
    else:
        watcher = Watcher(config, args.dry_run)
    watcher.run(args.once)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except WatchError as error:
        sys.exit("pr-watch: " + str(error))
