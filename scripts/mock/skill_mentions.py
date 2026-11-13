#!/usr/bin/env python3
"""Synthetic Web-only skill-picker evidence. Never connects to a real hub or runtime."""

import os
import signal
import time
from http.server import ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

os.environ["MOCK_MANAGERS"] = "0"
import mock_hub as hub

STATE = "ready"
SENDS = []
CLAUDE_CHAT = "c1111111-0000-4000-8000-000000000001"
CODEX_CHAT = "d2222222-0000-4000-8000-000000000002"
SKILLS = [
    {"id": "fixture-project", "name": "review", "description": "Inspect security boundaries and regressions", "source": "project · ."},
    {"id": "fixture-user", "name": "review", "description": "Review a document for clarity", "source": "user · ~/example/skills"},
    {"id": "fixture-design", "name": "interface-design", "description": "Refine accessible, responsive interfaces", "source": "project · ."},
]


class Handler(hub.H):
    def do_GET(self):
        global STATE
        parsed = urlparse(self.path)
        if parsed.path == "/mock/skills":
            STATE = parse_qs(parsed.query).get("state", ["ready"])[0]
            self.send_json({"state": STATE})
            return
        if parsed.path == "/mock/skill-sends":
            self.send_json({"sends": SENDS})
            return
        if parsed.path == "/api/v1/assistant/skills" or parsed.path.endswith("/skills") and parsed.path.startswith("/api/v1/sessions/"):
            state = STATE
            if state == "loading":
                time.sleep(2)
            if state == "error":
                self.send_json({"error": "Synthetic catalog unavailable", "code": "unavailable"}, 503)
            else:
                self.send_json({"catalog_version": 1, "complete": state != "partial", "skills": SKILLS})
            return
        super().do_GET()

    def do_POST(self):
        if urlparse(self.path).path.endswith("/messages"):
            import json
            body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
            SENDS.append({"path": self.path, "body": body})
            if STATE == "refuse":
                self.send_json({"error": "Selected skill is unavailable; remove it and select it again", "code": "skill_unavailable", "delivered": "none", "retryable": False}, 409)
            else:
                self.send_json({"ok": True})
            return
        super().do_POST()


def main():
    hub.SESSIONS[:] = [
        hub.sess("macbook", CLAUDE_CHAT, "Example Claude chat", "/example/project", "idle", "unread", "Example", 1, "Claude", 1, 1, "", launcher="claude"),
        hub.sess("macbook", CODEX_CHAT, "Example Codex chat", "/example/project", "idle", "unread", "Example", 1, "Codex", 2, 1, "", agent="codex", launcher="codex"),
    ]
    for session in hub.SESSIONS:
        hub.ITEMS[session["session_id"]] = []
    hub.ASSISTANT[:] = [hub.assistant_sess(hub.ASK1, "Example conversation", 1, "Synthetic skill selection example")]
    hub.ITEMS[hub.ASK1] = []
    hub.TODOS[:] = []
    hub.INTERACTIONS[:] = []
    hub.INBOX[:] = []
    for index, machine in enumerate(hub.MACHINES):
        machine["name"] = f"Example computer {index + 1}"
    server = ThreadingHTTPServer(("127.0.0.1", hub.PORT), Handler)
    server.daemon_threads = True
    hub.ENDPOINTS.append(hub.endpoint("127.0.0.1", server.server_port, "http", "lan"))
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    print(f"Synthetic skill fixture ready on loopback port {server.server_port}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
