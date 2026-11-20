"""Loopback mock Hub for failed-start-cleanup.mjs. Usage: python3 -B web/e2e/failed-start-cleanup-fixture.py [port]"""
import os
from pathlib import Path
import sys
from urllib.parse import urlsplit

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "scripts" / "mock"))
os.environ["MOCK_AGENT_ARCHIVES"] = "1"
import mock_hub as hub

OFFLINE = "studio"
ONLINE = "macbook"
FAILED = "f0116003-0000-4000-8000-000000000001"
ACTIVE = "f0116003-0000-4000-8000-000000000002"
HISTORY = "f0116003-0000-4000-8000-000000000003"
SENTINEL = "Review the release notes before publishing."
ZERO_TIME = "0001-01-01T00:00:00Z"
failed = dict(machine_id=OFFLINE, session_id=FAILED, agent="claude", title="Review release notes",
              started_by_persona=True, status="unknown", state="unknown", updated_at=ZERO_TIME,
              pane_id="original-pane", cwd="/example/release")
active = dict(machine_id=ONLINE, session_id=ACTIVE, agent="claude", title="Prepare documentation",
              started_by_persona=True, status="working", state="working", updated_at=hub.ago(minutes=1),
              capabilities={"read": True, "send": True, "close": True})
history = dict(machine_id=OFFLINE, session_id=HISTORY, agent="claude", title="Earlier notes",
               started_by_persona=True, status="offline", state="offline", updated_at=hub.ago(minutes=120))
mutations = []
machine = next(m for m in hub.MACHINES if m["id"] == OFFLINE)
machine["name"] = "Studio"


def reset():
    hub.SESSIONS[:] = [failed.copy(), active.copy(), history.copy()]
    hub.SESSION_DELETED.clear()
    hub.ITEMS[FAILED] = [hub.it("user", "text", 10, text=SENTINEL)]
    hub.ITEMS[HISTORY] = [hub.it("assistant", "text", 120, text="The earlier review is complete.")]
    machine["online"] = False
    mutations.clear()


def relist():
    # A node reporting new work reintroduces the same identity and appends to its local transcript.
    assert any(s["session_id"] == FAILED for s in hub.SESSION_DELETED)
    assert not any(s["session_id"] == FAILED for s in hub.SESSIONS)
    machine["online"] = True
    hub.SESSIONS.append(dict(failed, status="working", state="working", updated_at=hub.ago()))
    hub.ITEMS[FAILED].append(hub.it("assistant", "text", 0, text="Review resumed on Studio."))
    for send, _ in list(hub.LIVE):
        send({"type": "machines", "machines": hub.out_machines()})
    hub.push_machine(OFFLINE)


class Handler(hub.H):
    def do_GET(self):
        path = urlsplit(self.path).path
        if path == "/mock/failed-start/reset":
            reset()
        elif path == "/mock/failed-start/relist":
            relist()
        elif path != "/mock/failed-start/state":
            return super().do_GET()
        return self.send_json({
            "mutations": mutations, "listed": any(s["session_id"] == FAILED for s in hub.SESSIONS),
            "transcript": [i["text"] for i in hub.ITEMS[FAILED]],
        })

    def do_POST(self):
        path = urlsplit(self.path).path
        target = "/api/v1/sessions/" + OFFLINE + "/" + FAILED
        if path == target or path.startswith(target + "/") and not path.endswith("/seen"):
            role = "readonly" if hub.is_read_only(self) else "full" if self.headers.get("Authorization") == "Bearer mock" else "unpaired"
            mutations.append([self.command, path, role])
        return super().do_POST()

    do_DELETE = do_POST
    do_PUT = do_POST
    do_PATCH = do_POST


if __name__ == "__main__":
    reset()
    with hub.ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1]) if len(sys.argv) > 1 else 0), Handler) as server:
        hub.PORT = server.server_port
        hub.ENDPOINTS.append(hub.endpoint("127.0.0.1", hub.PORT, "http", "lan"))
        print(f"Failed-start fixture on http://127.0.0.1:{hub.PORT}", flush=True)
        server.serve_forever()
