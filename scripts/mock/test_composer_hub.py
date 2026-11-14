"""The Apple composer mock stays isolated and preserves raw model ids (#2200)."""
from contextlib import contextmanager
import copy
import json
import os
from pathlib import Path
import runpy
import socket
import subprocess
import sys
import tempfile
import time
import unittest
from urllib.error import URLError
from urllib.request import Request, urlopen


ENTRY = Path(__file__).with_name("composer_hub.py")


class ComposerFixtureTests(unittest.TestCase):
    def test_only_targeted_fixture_fields_change(self):
        entry = runpy.run_path(str(ENTRY))
        mock = {
            "SESSIONS": [{"session_id": entry["AGENT"], "model": "opus", "agent": "claude", "state": "working"},
                         {"session_id": "unrelated", "model": "other-model", "agent": "other"}],
            "ASSISTANT": [{"session_id": entry["ASSISTANT"], "model": "opus", "assistant": True, "title": "示例对话"}],
            "LAUNCHERS": [{"name": "example-primary", "model": "old"}, {"name": "example-next", "model": "old"},
                          {"name": "example-unchanged", "model": "glm-5.3"}],
        }
        before = copy.deepcopy(mock)
        entry["configure"](mock)
        self.assertEqual(mock["SESSIONS"][0]["model"], entry["LONG_MODEL"])
        self.assertEqual(mock["ASSISTANT"][0]["model"], entry["SHORT_MODEL"])
        self.assertTrue(mock["ASSISTANT"][0]["assistant"])
        self.assertEqual(mock["SESSIONS"][0]["state"], before["SESSIONS"][0]["state"])
        self.assertEqual(mock["SESSIONS"][1], before["SESSIONS"][1])
        self.assertEqual(mock["LAUNCHERS"][2], before["LAUNCHERS"][2])
        self.assertEqual([x["name"] for x in mock["LAUNCHERS"]], [x["name"] for x in before["LAUNCHERS"]])
        configured = copy.deepcopy(mock)
        entry["configure"](mock)
        self.assertEqual(mock, configured)

    def test_real_mock_endpoint_returns_raw_ids(self):
        with serving() as base:
            with urlopen(base + "/api/v1/sessions", timeout=2) as response:
                sessions = json.load(response)["sessions"]
            by_id = {x["session_id"]: x for x in sessions}
            self.assertEqual(by_id["a1f3c2d4-0000-4000-8000-000000000001"]["model"],
                             "gpt-6.1-sol-codex-mini-preview-2026-11-05")
            self.assertEqual(by_id["b2e4d6f8-0000-4000-8000-000000000001"]["model"], "gpt-6.1-sol")
            with urlopen(base + "/api/v1/launchers", timeout=2) as response:
                launchers = json.load(response)["launchers"]
            self.assertEqual([x["model"] for x in launchers[:2]], ["gpt-6.1-sol", "gpt-6.1"])

    def test_a_send_to_the_working_agent_lands_in_its_transcript(self):
        """Without that copy the app keeps the message queued for minutes, across launches, and in the next test its
        queue strip covered the chat on an iPhone (#7683)."""
        path = "/api/v1/sessions/macbook/a1f3c2d4-0000-4000-8000-000000000001"
        with serving() as base:
            body = json.dumps({"text": "sent from the composer", "client_msg_id": "composer-echo-1"}).encode()
            request = Request(base + path + "/messages", data=body, method="POST", headers={"Content-Type": "application/json"})
            with urlopen(request, timeout=2) as response:
                self.assertTrue(json.load(response)["ok"])
            echoed = []
            deadline = time.monotonic() + 10
            while not echoed and time.monotonic() < deadline:
                time.sleep(0.2)
                with urlopen(base + path + "/items", timeout=2) as response:
                    echoed = [x for x in json.load(response)["items"] if x.get("client_msg_id") == "composer-echo-1"]
            self.assertEqual([(x["role"], x["text"]) for x in echoed], [("user", "sent from the composer")])


@contextmanager
def serving():
    """The real entry on a free port, with no environment of the caller's: yields its base URL once it answers, and
    checks that it stops cleanly."""
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    with tempfile.TemporaryFile() as log:
        process = subprocess.Popen([sys.executable, "-I", str(ENTRY)],
                                   env={"PATH": os.environ.get("PATH", ""), "PORT": str(port)}, stdout=log, stderr=log)
        try:
            base = "http://127.0.0.1:%d" % port
            deadline = time.monotonic() + 10
            while True:
                if process.poll() is not None:
                    raise AssertionError("composer mock exited before startup")
                try:
                    with urlopen(base + "/api/v1/sessions", timeout=1):
                        break
                except (URLError, TimeoutError):
                    if time.monotonic() > deadline:
                        raise AssertionError("composer mock startup timed out")
                    time.sleep(0.1)
            yield base
        finally:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
        if process.returncode != 0:
            raise AssertionError("composer mock exited with %d" % process.returncode)


if __name__ == "__main__":
    unittest.main()
