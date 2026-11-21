"""Exercise the project lessons mock (GET, PUT and DELETE /api/v1/kb/projects/{id}/lessons…) without a browser."""
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest
from urllib.error import HTTPError, URLError
from urllib.request import ProxyHandler, Request, build_opener

# The mock is on loopback: a proxy from the environment must not get in the way.
urlopen = build_opener(ProxyHandler({})).open


class ProjectLessonsMockTests(unittest.TestCase):
    mode = ""

    @classmethod
    def setUpClass(cls):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        cls.base = f"http://127.0.0.1:{port}"
        cls.log = tempfile.TemporaryFile()
        cls.process = subprocess.Popen(
            ["python3", "-I", str(Path(__file__).with_name("mock_hub.py"))],
            env=dict(os.environ, PORT=str(port), MOCK_PROJECT_LESSONS=cls.mode), stdout=cls.log, stderr=cls.log)
        cls.addClassCleanup(cls.stop)
        for _ in range(60):
            if cls.process.poll() is not None:
                raise RuntimeError("mock hub exited before startup")
            try:
                with urlopen(cls.base + "/api/v1/health", timeout=1):
                    return
            except (URLError, TimeoutError):
                time.sleep(0.1)
        raise RuntimeError("mock hub startup timed out")

    @classmethod
    def stop(cls):
        cls.process.terminate()
        try:
            cls.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            cls.process.kill()
            cls.process.wait(timeout=5)
        cls.log.close()

    def setUp(self):
        with urlopen(self.base + "/mock/reset", timeout=3):
            pass

    def request(self, path, method="GET", body=None):
        request = Request(self.base + path, data=None if body is None else json.dumps(body).encode(), method=method,
                          headers={"Content-Type": "application/json"})
        with urlopen(request, timeout=3) as response:
            return json.load(response)

    def refused(self, path, method="GET", body=None):
        with self.assertRaises(HTTPError) as error:
            self.request(path, method, body)
        return error.exception.code, json.load(error.exception)


class WithLessons(ProjectLessonsMockTests):
    path = "/api/v1/kb/projects/weather-app/lessons"

    def test_lists_then_edits_and_deletes(self):
        listed = self.request(self.path)
        self.assertFalse(listed["enabled"])
        self.assertEqual([l["kind"] for l in listed["lessons"]], ["missing_context", "repeated_mistake", "improvement", "wasted_step"])
        self.assertTrue(listed["lessons"][-1]["stale"])
        edited = self.request(self.path + "/lesson-weather-1", "PUT", {"text": "  Reproduce widget layouts on the stock simulator. "})
        first = edited["lessons"][0]
        self.assertEqual(first["text"], "Reproduce widget layouts on the stock simulator.")
        self.assertTrue(first["edited"])
        self.assertNotEqual(first["id"], "lesson-weather-1")
        code, body = self.refused(self.path + "/lesson-weather-1", "PUT", {"text": "From an old page."})
        self.assertEqual((code, body["code"], body["lessons"][0]["id"]), (409, "lesson_changed", first["id"]))
        self.assertEqual(self.refused(self.path + "/" + first["id"], "PUT", {"text": " "})[0], 400)
        after = self.request(self.path + "/" + first["id"], "DELETE")
        self.assertEqual(len(after["lessons"]), 3)
        self.assertEqual(self.refused(self.path + "/lesson-none", "DELETE")[0], 404)
        self.assertEqual(self.refused("/api/v1/kb/projects/no-such/lessons")[0], 404)

    def test_follows_the_switch(self):
        self.request("/api/v1/assistant", "PUT", {"task_lessons": True})
        self.assertTrue(self.request("/api/v1/assistant")["task_lessons"])
        self.assertTrue(self.request(self.path)["enabled"])
        self.request("/api/v1/assistant", "PUT", {"task_lessons": False})


class WithoutLessons(ProjectLessonsMockTests):
    mode = "none"

    def test_has_none(self):
        self.assertEqual(self.request("/api/v1/kb/projects/weather-app/lessons")["lessons"], [])


class BeforeLessons(ProjectLessonsMockTests):
    mode = "missing"

    def test_is_a_hub_from_before(self):
        self.assertEqual(self.refused("/api/v1/kb/projects/weather-app/lessons")[0], 404)


del ProjectLessonsMockTests

if __name__ == "__main__":
    unittest.main()
