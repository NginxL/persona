"""The mock hub's primary device for phone calls follows the hub's contract (GET /api/v1/auth/tokens `phone`,
PUT /api/v1/phone/primary): candidates are the active phone credentials, Automatic is "", a device that offers no phone
tools is refused with 409 not_a_phone, a missing credential_id is 400, a view-only credential is 403."""
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


class MockHub:
    """One mock hub process on a free port, with the given environment on top of this one's."""

    def __init__(self, test, **env):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        self.base = f"http://127.0.0.1:{port}"
        self.log = tempfile.TemporaryFile()
        self.process = subprocess.Popen(
            ["python3", "-I", str(Path(__file__).with_name("mock_hub.py"))],
            env=dict(os.environ, PORT=str(port), **env), stdout=self.log, stderr=self.log)
        test.addCleanup(self.stop)
        for _ in range(60):
            if self.process.poll() is not None:
                raise RuntimeError("mock hub exited before startup")
            try:
                with urlopen(self.base + "/api/v1/health", timeout=1):
                    return
            except (URLError, TimeoutError):
                time.sleep(0.1)
        raise RuntimeError("mock hub startup timed out")

    def stop(self):
        self.process.terminate()
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=5)
        self.log.close()

    def request(self, method, path, body=None, headers=None):
        request = Request(self.base + path, method=method,
                          data=None if body is None else json.dumps(body).encode(),
                          headers={"Content-Type": "application/json", **(headers or {})})
        try:
            with urlopen(request, timeout=5) as response:
                return response.status, json.loads(response.read() or b"{}")
        except HTTPError as error:
            with error:
                return error.code, json.loads(error.read() or b"{}")

    def phone(self):
        status, body = self.request("GET", "/api/v1/auth/tokens")
        assert status == 200, status
        return body.get("phone")


class PhonePrimaryMockTests(unittest.TestCase):
    def test_defaultIsAutomaticWithTheActivePhonesAsCandidates(self):
        hub = MockHub(self)
        # The revoked iPad and the watch are not phones that can be chosen.
        self.assertEqual(hub.phone(), {"primary": "", "candidates": ["dev_phone", "dev_work"]})

    def test_choosingAndGoingBackToAutomatic(self):
        hub = MockHub(self)
        status, body = hub.request("PUT", "/api/v1/phone/primary", {"credential_id": "dev_work"})
        self.assertEqual((status, body), (200, {"primary": "dev_work", "candidates": ["dev_phone", "dev_work"]}))
        self.assertEqual(hub.phone()["primary"], "dev_work", "the next read shows it")
        status, body = hub.request("PUT", "/api/v1/phone/primary", {"credential_id": ""})
        self.assertEqual((status, body["primary"]), (200, ""))
        self.assertEqual(hub.phone()["primary"], "")

    def test_aDeviceWithoutPhoneToolsIsRefused(self):
        hub = MockHub(self)
        for credential in ("dev_watch", "dev_ipad", "dev_nobody"):
            status, body = hub.request("PUT", "/api/v1/phone/primary", {"credential_id": credential})
            self.assertEqual(status, 409, credential)
            self.assertEqual(body["code"], "not_a_phone")
            self.assertTrue(body["error"])
        self.assertEqual(hub.phone()["primary"], "", "a refused choice changes nothing")

    def test_aMissingCredentialIsABadRequest(self):
        hub = MockHub(self)
        self.assertEqual(hub.request("PUT", "/api/v1/phone/primary", {})[0], 400)
        self.assertEqual(hub.request("PUT", "/api/v1/phone/primary", {"credential_id": 7})[0], 400)

    def test_aViewOnlyDeviceCannotChoose(self):
        hub = MockHub(self)
        status, _ = hub.request("PUT", "/api/v1/phone/primary", {"credential_id": "dev_phone"},
                                headers={"Authorization": "Bearer dev_watch"})
        self.assertEqual(status, 403)
        self.assertEqual(hub.phone()["primary"], "")

    def test_startsWithTheDeviceTheEnvironmentChose(self):
        hub = MockHub(self, MOCK_PHONE_PRIMARY="dev_phone")
        self.assertEqual(hub.phone()["primary"], "dev_phone")

    def test_aChosenDeviceThatIsNotActiveReadsAsAutomatic(self):
        hub = MockHub(self, MOCK_PHONE_PRIMARY="dev_ipad")
        self.assertEqual(hub.phone()["primary"], "")

    def test_aHubBeforeItHasNoPhoneAndNoRoute(self):
        hub = MockHub(self, MOCK_PHONE="none")
        self.assertIsNone(hub.phone())
        self.assertEqual(hub.request("PUT", "/api/v1/phone/primary", {"credential_id": "dev_phone"})[0], 404)

    def test_refuseAnswersEveryChoiceNotAPhone(self):
        hub = MockHub(self, MOCK_PHONE="refuse")
        for credential in ("dev_work", ""):
            status, body = hub.request("PUT", "/api/v1/phone/primary", {"credential_id": credential})
            self.assertEqual((status, body["code"]), (409, "not_a_phone"))
        self.assertEqual(hub.phone()["primary"], "")

    def test_singleLeavesOneCandidate(self):
        hub = MockHub(self, MOCK_PHONE="single")
        self.assertEqual(hub.phone(), {"primary": "", "candidates": ["dev_phone"]})


if __name__ == "__main__":
    unittest.main()
