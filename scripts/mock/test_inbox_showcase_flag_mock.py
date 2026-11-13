"""MOCK_INBOX_SHOWCASE=1 against its edges: the list under the mock's other query parameters, ids it does not have,
the default inbox when the flag is off, the switch in --help, and the precedence when MOCK_INBOX_EVENTS is on.

The flag serves spec/fixtures/inbox/showcase.json's items exactly, so the list has neither `manager` nor `version`
(what the hub stamps) and the detail has no `context`; with the flag off every one of those is there, as before.
"""
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent.parent
SHOWCASE_PATH = ROOT / "spec" / "fixtures" / "inbox" / "showcase.json"
# The mock's own inbox when the flag is off (scripts/mock/mock_hub.py's INBOX): only its first id is pinned, so a later
# edit to the default scenario does not break this switch's test for nothing.
DEFAULT_FIRST_ID = "lark:f1:100"


class Hub:
    def __init__(self, **env):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            self.port = sock.getsockname()[1]
        self.base = "http://127.0.0.1:%d" % self.port
        self.home = tempfile.TemporaryDirectory(prefix="inbox-showcase-flag-")
        self.log = tempfile.TemporaryFile(dir=self.home.name)
        clean = {k: v for k, v in os.environ.items() if not k.startswith("MOCK_")}
        persona_home = Path(self.home.name) / "persona-home"
        persona_home.mkdir()
        self.process = subprocess.Popen(
            ["python3", "-I", str(HERE / "mock_hub.py")],
            env=dict(clean, PORT=str(self.port), HOME=self.home.name, PERSONA_HOME=str(persona_home), **env),
            cwd=self.home.name, stdout=self.log, stderr=self.log)
        for _ in range(100):
            if self.process.poll() is not None:
                self.log.seek(0)
                out = self.log.read().decode(errors="replace")[-2000:]
                self.stop()
                raise RuntimeError("mock hub exited: %s" % out)
            try:
                with urlopen(self.base + "/api/v1/health", timeout=1):
                    break
            except (URLError, TimeoutError, ConnectionError):
                time.sleep(0.1)
        else:
            self.stop()
            raise RuntimeError("mock hub startup timed out")

    def stop(self):
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
        self.log.close()
        self.home.cleanup()

    def get(self, path, headers=None):
        try:
            with urlopen(Request(self.base + path, headers=headers or {}), timeout=5) as res:
                return res.status, json.load(res)
        except HTTPError as e:
            return e.code, json.load(e)


class ShowcaseOnTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.fixture = json.loads(SHOWCASE_PATH.read_text(encoding="utf-8"))
        cls.items = cls.fixture["items"]
        cls.hub = Hub(MOCK_INBOX_SHOWCASE="1")

    @classmethod
    def tearDownClass(cls):
        cls.hub.stop()

    def test_the_list_is_the_fixture_under_the_other_query_parameters(self):
        # `sources` is the one query parameter the mock's list reads today (inbox_for); `chain` and an unknown key are
        # ignored by it, and a manager header must not add `manager` to the showcase's items.
        for path, headers in (("/api/v1/inbox?sources=all", None), ("/api/v1/inbox?sources=", None),
                              ("/api/v1/inbox?chain=last", None), ("/api/v1/inbox?sources=all&chain=last", None),
                              ("/api/v1/inbox", {"X-Persona-Manager": "m-3fa21c"}),
                              ("/api/v1/inbox", {"X-Persona-Manager": "no-such-manager"})):
            with self.subTest(path=path, headers=headers):
                status, body = self.hub.get(path, headers)
                self.assertEqual(status, 200)
                self.assertEqual(body["items"], self.items)
                self.assertNotIn("triage", body)  # the fixture's list has no triage block

    def test_the_list_does_not_carry_what_the_hub_stamps(self):
        status, body = self.hub.get("/api/v1/inbox")
        self.assertEqual(status, 200)
        for item in body["items"]:
            self.assertNotIn("manager", item)
            self.assertNotIn("version", item)

    def test_an_id_the_mock_does_not_have_is_404(self):
        for path in ("/api/v1/inbox/sc-9", "/api/v1/inbox/lark:f1:100", "/api/v1/inbox/missing"):
            with self.subTest(path=path):
                status, body = self.hub.get(path)
                self.assertEqual(status, 404)
                self.assertEqual(body["code"], "not_found")

    def test_the_detail_is_the_fixture_item_without_the_hubs_context(self):
        for expected in self.items:
            with self.subTest(item=expected["id"]):
                status, body = self.hub.get("/api/v1/inbox/" + expected["id"])
                self.assertEqual(status, 200)
                self.assertEqual(body, expected)
                self.assertNotIn("context", body)

    def test_inbox_events_on_do_not_win_over_the_showcase(self):
        hub = Hub(MOCK_INBOX_SHOWCASE="1", MOCK_INBOX_EVENTS="1")
        self.addCleanup(hub.stop)
        status, body = hub.get("/api/v1/inbox")
        self.assertEqual(status, 200)
        self.assertEqual(body["items"], self.items)
        status, body = hub.get("/api/v1/inbox/sc-1")
        self.assertEqual(status, 200)
        self.assertEqual(body, self.items[0])

    def test_help_lists_the_switch(self):
        out = subprocess.run(["python3", "-I", str(HERE / "mock_hub.py"), "--help"], capture_output=True, text=True, timeout=60)
        self.assertEqual(out.returncode, 0, out.stderr[-2000:])
        self.assertIn("MOCK_INBOX_SHOWCASE=1", out.stdout)


class ShowcaseOffTests(unittest.TestCase):
    def test_the_default_inbox_keeps_the_hubs_stamps(self):
        hub = Hub()
        self.addCleanup(hub.stop)
        status, body = hub.get("/api/v1/inbox")
        self.assertEqual(status, 200)
        ids = [x["id"] for x in body["items"]]
        self.assertEqual(ids[0], DEFAULT_FIRST_ID)
        self.assertEqual(len(ids), 7)  # the default inbox lists seven open items
        self.assertFalse(any(x.startswith("sc-") for x in ids))
        for item in body["items"]:
            self.assertIn("version", item)
            self.assertIn("manager", item)
        status, body = hub.get("/api/v1/inbox/" + DEFAULT_FIRST_ID)
        self.assertEqual(status, 200)
        self.assertIn("context", body)
        self.assertEqual(body["context"], body["preview"])


if __name__ == "__main__":
    unittest.main()
