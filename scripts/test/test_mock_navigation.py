"""The mock hub's navigation record (#9071, #9745, #10905) over its real HTTP routes: Overview's arrangement (its cards
and its own sections) is kept beside the start page and the sidebar's pages, checked like the manager checks it, and the made-up cards to arrange are listed
and drawn; MOCK_NAVIGATION=no-overview is a manager from before the field, MOCK_NAVIGATION=off a hub without the route.

    python3 scripts/test/test_mock_navigation.py
"""
import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class Mock:
    """A disposable mock hub on a free port, started with `env`."""

    def __init__(self, **env):
        self.port = free_port()
        self.home = tempfile.TemporaryDirectory(prefix="mock-navigation-")
        full = {k: v for k, v in os.environ.items() if not k.lower().endswith("_proxy") and not k.startswith("MOCK_")}
        full.update(HOME=self.home.name, PORT=str(self.port), **env)
        self.proc = subprocess.Popen([sys.executable, str(ROOT / "scripts/mock/mock_hub.py")], env=full,
                                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            try:
                with socket.create_connection(("127.0.0.1", self.port), timeout=0.5):
                    return
            except OSError:
                time.sleep(0.1)
        self.close()
        raise RuntimeError("the mock hub did not start within 60 s")

    def close(self):
        self.proc.terminate()
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait(timeout=10)
        self.home.cleanup()

    def call(self, method, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(f"http://127.0.0.1:{self.port}{path}", data=data, method=method,
                                     headers={"Authorization": "Bearer mock", "Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                return resp.status, json.loads(resp.read() or b"{}")
        except urllib.error.HTTPError as e:
            with e:
                return e.code, json.loads(e.read() or b"{}")


ARRANGEMENT = [{"extension": "example-today", "id": "agenda", "span": "full"},
               {"extension": "example-quota", "id": "left", "hidden": True}]


class MockNavigationTests(unittest.TestCase):
    def test_overview_kept_beside_the_other_fields(self):
        hub = Mock(MOCK_EXTENSIONS="1", MOCK_OVERVIEW_CARDS="1")
        self.addCleanup(hub.close)
        code, ui = hub.call("GET", "/api/v1/extensions/-/ui?placement=overview_card")
        self.assertEqual(code, 200)
        self.assertEqual([(u["extension"], u["id"]) for u in ui["ui"]],
                         [("example-gateway", "today"), ("example-quota", "left"), ("example-today", "needs"), ("example-today", "agenda")])
        code, view = hub.call("POST", "/api/v1/extensions/example-today/-/ui/agenda", {"context": {"size": "compact"}})
        self.assertEqual(code, 200)
        self.assertTrue(view["view"]["sections"])

        self.assertEqual(hub.call("GET", "/api/v1/navigation"), (200, {}))
        code, got = hub.call("PUT", "/api/v1/navigation", {"overview": ARRANGEMENT})
        self.assertEqual((code, got.get("overview")), (200, ARRANGEMENT))
        hub.call("PUT", "/api/v1/navigation", {"start": "home"})
        hub.call("PUT", "/api/v1/navigation", {"pages": {"order": [], "shown": [], "hidden": ["kb"]}})
        code, got = hub.call("GET", "/api/v1/navigation")
        self.assertEqual((code, got.get("overview"), got.get("start")), (200, ARRANGEMENT, "home"))
        self.assertEqual(hub.call("PUT", "/api/v1/navigation", {"overview": []})[1].get("overview"), [])

        for bad in ({}, {"overview": {}}, {"overview": [1]}, {"overview": [{"extension": "Example", "id": "x"}]},
                    {"overview": [{"extension": "example-quota", "id": "left", "span": "wide"}]},
                    {"overview": [{"extension": "example-quota", "id": "left"}, {"extension": "example-quota", "id": "left"}]}):
            self.assertEqual(hub.call("PUT", "/api/v1/navigation", bad)[0], 400, bad)
        self.assertEqual(hub.call("GET", "/api/v1/navigation")[1].get("overview"), [])

    def test_overview_sections(self):
        """Overview's own sections (#10905) are entries of the same list, {"section", "hidden"?}, checked as the manager does."""
        hub = Mock(MOCK_EXTENSIONS="1", MOCK_OVERVIEW_CARDS="1")
        self.addCleanup(hub.close)
        arranged = [{"section": "in_progress"}, *ARRANGEMENT, {"section": "needs_you"}, {"section": "working", "hidden": True}]
        code, got = hub.call("PUT", "/api/v1/navigation", {"overview": arranged})
        self.assertEqual((code, got.get("overview")), (200, arranged))
        for bad in ({"overview": [{"section": "In-progress"}]}, {"overview": [{"section": "in_progress", "span": "full"}]},
                    {"overview": [{"section": "in_progress", "extension": "example-quota", "id": "left"}]},
                    {"overview": [{"section": "in_progress"}, {"section": "in_progress"}]}, {"overview": [{"section": 1}]}):
            self.assertEqual(hub.call("PUT", "/api/v1/navigation", bad)[0], 400, bad)
        self.assertEqual(hub.call("GET", "/api/v1/navigation")[1].get("overview"), arranged)

    def test_a_manager_from_before_the_field(self):
        hub = Mock(MOCK_NAVIGATION="no-overview")
        self.addCleanup(hub.close)
        code, got = hub.call("PUT", "/api/v1/navigation", {"overview": ARRANGEMENT})
        self.assertEqual((code, got.get("error")), (400, "start or pages is required"))  # #9351's own words
        code, got = hub.call("PUT", "/api/v1/navigation", {"start": "kb", "overview": ARRANGEMENT})
        self.assertEqual(code, 200)
        self.assertNotIn("overview", got)

    def test_a_hub_without_the_route(self):
        hub = Mock(MOCK_NAVIGATION="off")
        self.addCleanup(hub.close)
        self.assertEqual(hub.call("GET", "/api/v1/navigation")[0], 404)
        self.assertEqual(hub.call("PUT", "/api/v1/navigation", {"overview": ARRANGEMENT})[0], 404)


if __name__ == "__main__":
    unittest.main()
