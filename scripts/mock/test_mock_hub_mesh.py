#!/usr/bin/env python3
"""The mock hub's mesh fields (scripts/mock/mock_hub.py, docs/archive/operations/plan-hub-mesh.md 2.6): absent by default, shaped like the
contract when MOCK_HUB_NAME and the other MOCK_* mesh variables are set.

    python3 scripts/mock/test_mock_hub_mesh.py      # two free ports the kernel picks (MOCK_MESH_TEST_PORTS=a,b to choose)

Each class starts its own mock hubs and stops them (then checks the port is free) when it is done.
"""
import base64
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import unittest
from urllib.error import HTTPError, URLError
from urllib.parse import quote
from urllib.request import urlopen
import uuid

MOCK = Path(__file__).with_name("mock_hub.py")
LOOPBACK = "127.0.0.1"


def free_ports(n):
    """n ports free now, the kernel's choice: a fixed one can be taken as the source port of any outgoing connection
    where the ephemeral range covers it (10000-65535 on the hub release machine)."""
    socks = [socket.socket() for _ in range(n)]
    try:
        for s in socks:
            s.bind((LOOPBACK, 0))
        return [s.getsockname()[1] for s in socks]
    finally:
        for s in socks:
            s.close()


PORTS = ([int(x) for x in os.environ["MOCK_MESH_TEST_PORTS"].split(",")] if os.environ.get("MOCK_MESH_TEST_PORTS")
         else free_ports(2))
MESH_KEYS = ("hub_id", "hub_name", "machine_uid", "route")
NAMES = {"server": "工作服务器", "macbook": "工作 MacBook"}


def listening(port):
    try:
        with socket.create_connection((LOOPBACK, port), timeout=0.5):
            return True
    except OSError:
        return False


class Mock:
    """One mock hub on a port with extra environment variables (any MOCK_* of the caller's shell left out)."""

    def __init__(self, port, **env):
        if listening(port):
            raise RuntimeError(f"port {port} is already in use")
        self.port, self.base = port, f"http://{LOOPBACK}:{port}"
        self.log = tempfile.TemporaryFile()
        clean = {k: v for k, v in os.environ.items() if not k.startswith("MOCK_")}
        self.process = subprocess.Popen([sys.executable, "-I", str(MOCK)], env=dict(clean, PORT=str(port), **env),
                                        stdout=self.log, stderr=self.log)
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise RuntimeError("mock hub exited: " + self.output())
            try:
                self.get("/api/v1/health")
                return
            except (URLError, OSError):
                time.sleep(0.1)
        self.stop()
        raise RuntimeError("mock hub did not answer in 30 s")

    def output(self):
        self.log.seek(0)
        return self.log.read().decode(errors="replace")[-2000:]

    def stop(self):
        self.process.terminate()
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=5)
        self.log.close()
        deadline = time.monotonic() + 5
        while listening(self.port) and time.monotonic() < deadline:
            time.sleep(0.1)
        if listening(self.port):
            raise RuntimeError(f"port {self.port} is still in use after stopping the mock hub")

    def get(self, path):
        with urlopen(self.base + path, timeout=5) as response:
            return json.load(response)

    def get_status(self, path):
        try:
            with urlopen(self.base + path, timeout=5) as response:
                return response.status, json.load(response)
        except HTTPError as e:
            with e:
                return e.code, json.loads(e.read() or b"null")

    def ws(self, quiet=1.0, limit=10.0):
        """The JSON frames a new WebSocket gets on connect (hello first), read until the hub goes quiet."""
        sock = socket.create_connection((LOOPBACK, self.port), timeout=limit)
        key = base64.b64encode(os.urandom(16)).decode()
        sock.sendall((f"GET /api/v1/ws HTTP/1.1\r\nHost: {LOOPBACK}:{self.port}\r\nUpgrade: websocket\r\n"
                      f"Connection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n").encode())
        f = sock.makefile("rb")
        try:
            if b" 101 " not in f.readline():
                raise RuntimeError("no WebSocket upgrade")
            while f.readline() not in (b"\r\n", b""):
                pass
            sock.settimeout(quiet)
            frames, end = [], time.monotonic() + limit
            try:
                while time.monotonic() < end:
                    head = f.read(2)
                    if len(head) < 2:
                        break
                    n = head[1] & 0x7F
                    if n == 126:
                        n = int.from_bytes(f.read(2), "big")
                    elif n == 127:
                        n = int.from_bytes(f.read(8), "big")
                    data = f.read(n)
                    if head[0] & 0x0F == 1:
                        frames.append(json.loads(data))
            except (socket.timeout, TimeoutError):
                pass
            return frames
        finally:
            f.close()
            sock.close()


def by_type(frames, kind):
    return [x for x in frames if x.get("type") == kind]


class MockCase(unittest.TestCase):
    ENVS = [{}]  # one dict of environment variables per mock hub, on PORTS in order

    @classmethod
    def setUpClass(cls):
        cls.hubs = []
        for port, env in zip(PORTS, cls.ENVS):
            hub = Mock(port, **env)
            cls.addClassCleanup(hub.stop)
            cls.hubs.append(hub)
        cls.hub = cls.hubs[0]


class DefaultTests(MockCase):
    """No variables: nothing of the mesh anywhere (other UI tests and screenshots depend on it)."""

    def test_health_and_endpoints(self):
        health = self.hub.get("/api/v1/health")
        uuid.UUID(health["hub_id"])  # random each run
        self.assertNotIn("hub_name", health)
        self.assertNotIn("mesh", health)
        self.assertNotIn("hub_name", self.hub.get("/api/v1/endpoints"))

    def test_machines_sessions_interactions(self):
        machines = self.hub.get("/api/v1/machines")["machines"]
        self.assertEqual([m["id"] for m in machines], ["server", "macbook", "studio", "nas"])
        for m in machines:
            for k in MESH_KEYS:
                self.assertNotIn(k, m)
        self.assertEqual(machines[0]["name"], "服务器")
        self.assertIn("studio", {s["machine_id"] for s in self.hub.get("/api/v1/sessions")["sessions"]})
        self.assertIn("studio", {x["machine_id"] for x in self.hub.get("/api/v1/interactions")["interactions"]})

    def test_hello_and_pushes(self):
        frames = self.hub.ws()
        hello = frames[0]
        self.assertEqual(hello["type"], "hello")
        self.assertNotIn("hub_name", hello)
        self.assertNotIn("mesh", hello)
        machines = by_type(frames, "machines")[0]["machines"]
        self.assertEqual(len(machines), 4)
        for m in machines:
            for k in MESH_KEYS:
                self.assertNotIn(k, m)

    def test_mock_mesh(self):
        state = self.hub.get("/mock/mesh")
        self.assertIsNone(state["hub_name"])
        self.assertEqual((state["peers"], state["owners"]), ([], {}))


class SingleHubTests(MockCase):
    """MOCK_HUB_NAME alone: a hub with no peers, its machines all its own."""
    ENVS = [{"MOCK_HUB_ID": "hub-a", "MOCK_HUB_NAME": "家里"}]

    def test_health_and_endpoints(self):
        health = self.hub.get("/api/v1/health")
        self.assertEqual((health["hub_id"], health["hub_name"]), ("hub-a", "家里"))
        self.assertNotIn("mesh", health)  # the unauthenticated health never has it
        endpoints = self.hub.get("/api/v1/endpoints")
        self.assertEqual((endpoints["hub_id"], endpoints["hub_name"]), ("hub-a", "家里"))

    def test_hello_lists_only_itself(self):
        hello = self.hub.ws()[0]
        self.assertEqual(hello["hub_name"], "家里")
        self.assertEqual(hello["mesh"], {"hubs": [{
            "hub_id": "hub-a", "name": "家里", "online": True, "self": True,
            "endpoints": [f"http://{LOOPBACK}:{self.hub.port}"], "tls_port": 0, "fp": ""}]})

    def test_machines(self):
        machines = {m["id"]: m for m in self.hub.get("/api/v1/machines")["machines"]}
        self.assertEqual(set(machines), {"server", "macbook", "studio", "nas"})
        for m in machines.values():
            self.assertEqual((m["hub_id"], m["hub_name"]), ("hub-a", "家里"))
            uuid.UUID(m["machine_uid"])
            self.assertEqual(m["route"]["hops"], 0)
        self.assertEqual(machines["server"]["route"]["via"], "local")
        self.assertEqual(machines["macbook"]["route"]["via"], "ssh")
        self.assertIsInstance(machines["macbook"]["route"]["rtt_ms"], int)
        self.assertNotIn("rtt_ms", machines["nas"]["route"])  # offline: not measured
        self.assertEqual(machines["server"]["name"], "服务器")


class StandaloneFilterTests(MockCase):
    """MOCK_MACHINE_NAMES and MOCK_MACHINES_KEEP without MOCK_HUB_NAME: renamed and fewer machines, no mesh fields."""
    ENVS = [{"MOCK_MACHINE_NAMES": json.dumps(NAMES, ensure_ascii=False), "MOCK_MACHINES_KEEP": "server, macbook"}]

    def test_machines(self):
        machines = self.hub.get("/api/v1/machines")["machines"]
        self.assertEqual([(m["id"], m["name"]) for m in machines], [("server", "工作服务器"), ("macbook", "工作 MacBook")])
        for m in machines:
            for k in MESH_KEYS:
                self.assertNotIn(k, m)
        self.assertNotIn("hub_name", self.hub.get("/api/v1/health"))
        default = self.hub.get("/api/v1/metrics")["machines"]
        work = self.hub.get("/api/v1/metrics?manager=m-3fa21c")["machines"]
        self.assertEqual([m["machine_id"] for m in default], ["server"])
        self.assertEqual([m["machine_id"] for m in work], ["macbook"])

    def test_sessions_and_interactions(self):
        sessions = self.hub.get("/api/v1/sessions")["sessions"]
        self.assertTrue(sessions)
        self.assertLessEqual({s["machine_id"] for s in sessions}, {"server", "macbook"})
        interactions = self.hub.get("/api/v1/interactions")["interactions"]
        self.assertTrue(interactions)
        self.assertLessEqual({x["machine_id"] for x in interactions}, {"server", "macbook"})
        history = self.hub.get("/api/v1/interactions/history")["interactions"]
        self.assertLessEqual({x["machine_id"] for x in history}, {"server", "macbook"})

    def test_pushes(self):
        frames = self.hub.ws()
        self.assertNotIn("mesh", frames[0])
        self.assertEqual([m["name"] for m in by_type(frames, "machines")[0]["machines"]], ["工作服务器", "工作 MacBook"])
        self.assertEqual({x["machine_id"] for x in by_type(frames, "sessions")}, {"server", "macbook"})
        pushed = by_type(frames, "interaction")
        self.assertTrue(pushed)
        self.assertLessEqual({x["interaction"]["machine_id"] for x in pushed}, {"server", "macbook"})


def group_env(me, name, peer, peer_name, peer_port, owner):
    return {"MOCK_HUB_ID": me, "MOCK_HUB_NAME": name,
            "MOCK_MESH_PEERS": json.dumps([{"hub_id": peer, "name": peer_name,
                                            "endpoints": [f"http://{LOOPBACK}:{peer_port}"]}], ensure_ascii=False),
            "MOCK_MACHINE_OWNER": json.dumps(owner), "MOCK_MACHINES_KEEP": "server,macbook",
            "MOCK_MACHINE_NAMES": json.dumps(NAMES, ensure_ascii=False)}


class GroupTests(MockCase):
    """Two hubs of one group (B and C peers of each other): B owns the server, C the MacBook."""
    ENVS = [group_env("hub-b", "公司", "hub-c", "公司 2", PORTS[1], {"macbook": "hub-c"}),
            group_env("hub-c", "公司 2", "hub-b", "公司", PORTS[0], {"server": "hub-b"})]

    def test_health(self):
        health = self.hubs[0].get("/api/v1/health")
        self.assertEqual((health["hub_id"], health["hub_name"]), ("hub-b", "公司"))
        self.assertNotIn("mesh", health)

    def test_hello_mesh(self):
        hello = self.hubs[0].ws()[0]
        self.assertEqual((hello["hub_id"], hello["hub_name"]), ("hub-b", "公司"))
        self.assertEqual(hello["mesh"]["hubs"], [
            {"hub_id": "hub-b", "name": "公司", "online": True, "self": True,
             "endpoints": [f"http://{LOOPBACK}:{PORTS[0]}"], "tls_port": 0, "fp": ""},
            {"hub_id": "hub-c", "name": "公司 2", "online": True, "self": False,
             "endpoints": [f"http://{LOOPBACK}:{PORTS[1]}"], "tls_port": 0, "fp": ""}])

    def test_machines_and_routes(self):
        for machines in (self.hubs[0].get("/api/v1/machines")["machines"],
                         by_type(self.hubs[0].ws(), "machines")[0]["machines"]):
            got = {m["id"]: m for m in machines}
            self.assertEqual(list(got), ["server", "macbook"])
            self.assertEqual((got["server"]["hub_id"], got["server"]["hub_name"], got["server"]["name"]),
                             ("hub-b", "公司", "工作服务器"))
            self.assertEqual({k: v for k, v in got["server"]["route"].items() if k != "rtt_ms"}, {"via": "local", "hops": 0})
            self.assertEqual((got["macbook"]["hub_id"], got["macbook"]["hub_name"]), ("hub-c", "公司 2"))
            self.assertEqual({k: v for k, v in got["macbook"]["route"].items() if k != "rtt_ms"}, {"via": "peer", "hops": 1})
            for m in got.values():
                self.assertIsInstance(m["route"]["rtt_ms"], int)

    def test_both_hubs_list_the_same_computers(self):
        b = {m["id"]: m for m in self.hubs[0].get("/api/v1/machines")["machines"]}
        c = {m["id"]: m for m in self.hubs[1].get("/api/v1/machines")["machines"]}
        self.assertEqual(list(b), list(c))
        for mid in b:
            self.assertEqual((b[mid]["machine_uid"], b[mid]["hub_id"], b[mid]["name"]),
                             (c[mid]["machine_uid"], c[mid]["hub_id"], c[mid]["name"]))
        self.assertEqual((c["macbook"]["route"]["via"], c["macbook"]["route"]["hops"]), ("ssh", 0))
        self.assertEqual((c["server"]["route"]["via"], c["server"]["route"]["hops"]), ("peer", 1))

    def test_keep_filters_sessions_and_interactions(self):
        keep = {"server", "macbook"}
        self.assertLessEqual({s["machine_id"] for s in self.hubs[0].get("/api/v1/sessions")["sessions"]}, keep)
        self.assertLessEqual({x["machine_id"] for x in self.hubs[0].get("/api/v1/interactions")["interactions"]}, keep)
        frames = self.hubs[0].ws()
        self.assertEqual({x["machine_id"] for x in by_type(frames, "sessions")}, keep)
        self.assertLessEqual({x["interaction"]["machine_id"] for x in by_type(frames, "interaction")}, keep)

    def test_mock_mesh_changes_peers(self):
        hub = self.hubs[0]
        before = hub.get("/mock/mesh")
        self.assertEqual((before["hub_id"], before["hub_name"], before["owners"]), ("hub-b", "公司", {"macbook": "hub-c"}))
        self.assertEqual([p["hub_id"] for p in before["peers"]], ["hub-c"])
        try:
            more = json.dumps(before["peers"] + [{"hub_id": "hub-d", "online": False}])
            state = hub.get("/mock/mesh?peers=" + quote(more))
            self.assertEqual([p["hub_id"] for p in state["peers"]], ["hub-c", "hub-d"])
            self.assertEqual(state["peers"][1], {"hub_id": "hub-d", "name": "hub-d", "online": False, "self": False,
                                                 "endpoints": [], "tls_port": 0, "fp": ""})
            self.assertEqual([h["hub_id"] for h in hub.ws()[0]["mesh"]["hubs"]], ["hub-b", "hub-c", "hub-d"])
            for bad in ("{}", "[1]", '[{"hub_id":"hub-b"}]', "not json"):
                status, body = hub.get_status("/mock/mesh?peers=" + quote(bad))
                self.assertEqual(status, 400, bad)
                self.assertIn("error", body)
            # The MacBook's owner leaves the group: the MacBook and its sessions go with it.
            hub.get("/mock/mesh?peers=" + quote("[]"))
            self.assertEqual([m["id"] for m in hub.get("/api/v1/machines")["machines"]], ["server"])
            self.assertEqual({s["machine_id"] for s in hub.get("/api/v1/sessions")["sessions"]}, {"server"})
        finally:
            hub.get("/mock/mesh?peers=" + quote(json.dumps(before["peers"])))
        self.assertEqual(hub.get("/mock/mesh"), before)


class ConfigErrorTests(unittest.TestCase):
    def test_owner_must_be_a_peer(self):
        clean = {k: v for k, v in os.environ.items() if not k.startswith("MOCK_")}
        env = dict(clean, PORT=str(PORTS[0]), MOCK_HUB_NAME="公司", MOCK_MACHINE_OWNER='{"macbook":"hub-x"}')
        run = subprocess.run([sys.executable, "-I", str(MOCK)], env=env, capture_output=True, text=True, timeout=30)
        self.assertNotEqual(run.returncode, 0)
        self.assertIn("MOCK_MACHINE_OWNER", run.stderr)


if __name__ == "__main__":
    unittest.main()
