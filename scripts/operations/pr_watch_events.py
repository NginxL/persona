"""Optional authenticated GitHub pull_request webhooks for the PR watcher."""
import hashlib
import hmac
import http.server
import json
import threading

from pr_watch_common import SHA, WatchError


def parse_pull_request(body, signature, event, secret, allowed_repositories):
    """Validate an event before it can wake a task; polling remains the fallback."""
    if not secret or len(secret) < 32:
        raise WatchError("webhook secret must contain at least 32 characters")
    if len(body) > 1_000_000:
        raise WatchError("webhook body is too large")
    expected = "sha256=" + hmac.new(secret.encode(), body, hashlib.sha256).hexdigest()
    if not hmac.compare_digest(signature, expected):
        raise WatchError("invalid webhook signature")
    if event != "pull_request":
        return None
    try:
        payload = json.loads(body)
    except (UnicodeDecodeError, json.JSONDecodeError):
        raise WatchError("invalid webhook JSON")
    if not isinstance(payload, dict):
        raise WatchError("invalid webhook payload")
    repo = payload.get("repository", {}).get("full_name")
    pr = payload.get("pull_request", {})
    if repo not in allowed_repositories or not isinstance(pr, dict):
        raise WatchError("webhook repository is not configured")
    if payload.get("action") not in ("opened", "reopened", "synchronize", "ready_for_review", "edited"):
        return None
    number, head = pr.get("number"), pr.get("head", {}).get("sha")
    if not isinstance(number, int) or isinstance(number, bool) or number < 1 or not isinstance(head, str) or not SHA.fullmatch(head):
        raise WatchError("webhook has no valid PR identity")
    if pr.get("draft"):
        return None
    return repo, number, head


class Events:
    def __init__(self, secret, port, repositories, enqueue, log):
        self.secret, self.port, self.repositories = secret, port, repositories
        self.enqueue, self.log = enqueue, log
        self.server = None

    def start(self):
        if not self.secret:
            self.log("GitHub webhook disabled; minute polling remains active")
            return
        if not 1024 <= self.port <= 65535:
            raise WatchError("webhook port is invalid")
        adapter = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                if self.path != "/github":
                    self.send_error(404)
                    return
                try:
                    length = int(self.headers.get("Content-Length", "0"))
                    if length <= 0 or length > 1_000_000:
                        raise WatchError("invalid webhook content length")
                    body = self.rfile.read(length)
                    task = parse_pull_request(body, self.headers.get("X-Hub-Signature-256", ""),
                                              self.headers.get("X-GitHub-Event", ""),
                                              adapter.secret, adapter.repositories)
                    if task:
                        adapter.enqueue(task[0], task[1], task[2], "webhook")
                    self.send_response(204)
                except (WatchError, ValueError) as error:
                    adapter.log("webhook rejected: " + str(error))
                    self.send_error(403)
                self.end_headers()

            def log_message(self, *args):
                pass

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", self.port), Handler)
        thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        thread.start()
        self.log("GitHub webhook listening on loopback; use an authenticated HTTPS reverse proxy")

    def close(self):
        if self.server:
            self.server.shutdown()
            self.server.server_close()
