"""MOCK_TODO_VIEW (scripts/mock/todo_view_mock.py, #10465): the To-dos view a hub with the redesign's stage 1 serves,
from the design prototype's to-dos. Every answer holds to schema/wire/todo-view.schema.json; the counts are the rows'
sections counted as spec/fixtures/todo-counts/README.md counts them, and the prototype's own numbers (todos.js
EXPECTED, read with Node); the places add up; an unchanged list answers 304; the writes move the views; an older hub
(old) answers 405 and lists none of stage 1's fields; today's answers (accept, Don't handle, retry, a tick on a failed
start, the Widget's plan card) move the views without MOCK_TASKS, and a refused write changes nothing; the hellos under
late lack the view, have it, lack it, have it, and again after /mock/reset; two managers' answers are joined as the hub
joins them.

    python3 -B scripts/mock/test_todo_view_mock.py
"""
import base64
import functools
import importlib.util
import json
import os
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import ProxyHandler, Request, build_opener

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
SCHEMA = "todo-view.schema.json"
DOC = json.loads((ROOT / "schema" / "wire" / SCHEMA).read_text())
TODOS_JS = ROOT / "docs" / "design" / "prototypes" / "todos" / "data" / "todos.js"
OPENER = build_opener(ProxyHandler({}))  # loopback, never through a proxy the shell names

_spec = importlib.util.spec_from_file_location("test_wire_schema", ROOT / "scripts" / "test" / "test_wire_schema.py")
wire = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(wire)
VALIDATOR = wire.ext.Validator({SCHEMA: DOC})


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class Hub:
    """One mock hub on a free port with these MOCK_* variables (the caller's own left out)."""

    def __init__(self, **env):
        self.port = free_port()
        self.base = "http://127.0.0.1:%d" % self.port
        clean = {k: v for k, v in os.environ.items() if not k.startswith("MOCK_")}
        self.log = tempfile.TemporaryFile()
        self.process = subprocess.Popen([sys.executable, "-I", str(HERE / "mock_hub.py")],
                                        env=dict(clean, PORT=str(self.port), **env), stdout=self.log, stderr=self.log)
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise RuntimeError("mock hub exited: " + self.output())
            try:
                self.call("/api/v1/health")
                return
            except OSError:
                time.sleep(0.1)
        self.stop()
        raise RuntimeError("mock hub did not answer in 60 s")

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

    def call(self, path, method="GET", body=None, headers=None):
        """(status, headers, the JSON answer or None)."""
        data = None if body is None else json.dumps(body).encode()
        sent = dict({"Authorization": "Bearer mock", "Content-Type": "application/json"}, **(headers or {}))
        req = Request(self.base + path, data=data, method=method, headers=sent)
        try:
            with OPENER.open(req, timeout=10) as r:
                raw = r.read()
                return r.status, r.headers, json.loads(raw) if raw else None
        except HTTPError as e:
            with e:
                raw = e.read()
                return e.code, e.headers, json.loads(raw) if raw else None

    def get(self, path):
        status, _, body = self.call(path)
        if status != 200:
            raise AssertionError("%s: %d %s" % (path, status, body))
        return body


class Socket:
    """A WebSocket to the mock, enough to read what it sends."""

    def __init__(self, port):
        self.sock = socket.create_connection(("127.0.0.1", port), timeout=10)
        key = base64.b64encode(os.urandom(16)).decode()
        self.sock.sendall(("GET /api/v1/ws HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nUpgrade: websocket\r\n"
                           "Connection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n"
                           % (port, key)).encode())
        self.buf = b""
        while b"\r\n\r\n" not in self.buf:
            chunk = self.sock.recv(4096)
            if not chunk:
                raise AssertionError("no upgrade")
            self.buf += chunk
        head, self.buf = self.buf.split(b"\r\n\r\n", 1)
        if b" 101 " not in head.split(b"\r\n")[0]:
            raise AssertionError(head)

    def read(self, n):
        while len(self.buf) < n:
            chunk = self.sock.recv(65536)
            if not chunk:
                return None
            self.buf += chunk
        out, self.buf = self.buf[:n], self.buf[n:]
        return out

    def frame(self):
        """(opcode, payload) of the next frame; None once the socket is closed."""
        head = self.read(2)
        if head is None:
            return None
        n = head[1] & 0x7F
        if n == 126:
            n = struct.unpack(">H", self.read(2))[0]
        elif n == 127:
            n = struct.unpack(">Q", self.read(8))[0]
        payload = self.read(n) if n else b""
        return None if payload is None else (head[0] & 0x0F, payload)

    def message(self, want, seconds=10):
        """The first text message want takes; None when the socket closes or time runs out first."""
        self.sock.settimeout(seconds)
        try:
            while True:
                f = self.frame()
                if f is None or f[0] == 8:
                    return None
                if f[0] == 1:
                    msg = json.loads(f[1])
                    if want(msg):
                        return msg
        except socket.timeout:
            return None

    def closed(self, seconds):
        """Whether the mock closes the socket within seconds."""
        self.sock.settimeout(seconds)
        try:
            while True:
                f = self.frame()
                if f is None or f[0] == 8:
                    return True
        except socket.timeout:
            return False

    def close(self):
        self.sock.close()


@functools.lru_cache(maxsize=1)
def prototype():
    """The prototype's EXPECTED numbers and its to-dos in rank order ({id, section, project, title}), read with Node."""
    script = ('const vm = require("vm"), fs = require("fs"); const c = {window: {}, console}; vm.createContext(c);'
              'vm.runInContext(fs.readFileSync(process.argv[1], "utf8"), c); const D = c.window.Data;'
              'process.stdout.write(JSON.stringify({expected: D.expected, todos: D.todos.map((t) =>'
              '({id: t.id, section: t.section, project: t.project, title: t.title}))}));')
    out = subprocess.run([shutil.which("node"), "-e", script, str(TODOS_JS)], capture_output=True, text=True,
                         timeout=60, check=True)
    return json.loads(out.stdout)


def d3_counts(rows):
    """The counts of rows as spec/fixtures/todo-counts/README.md counts them, from their views alone."""
    open_ids = {t["id"] for t in rows if t["view"]["phase"] != "done"}
    sections = DOC["$defs"]["TodoListSection"]["enum"]
    out = {"open": 0, "needs_you": 0, "in_progress": 0, "sections": {s: 0 for s in sections}}
    for t in rows:
        if t.get("part_of") and t["part_of"] != t["id"] and t["part_of"] in open_ids:
            continue  # a step its open parent carries
        v = t["view"]
        out["sections"][v["section"]] += 1
        out["open"] += v["phase"] != "done" and v["section"] != "someday"
        out["needs_you"] += v["section"] == "needs_you" and not t.get("quiet")
        out["in_progress"] += v["section"] in ("working", "waiting")
    return out


def add_up(objs):
    out = {}
    for o in objs:
        for k, v in o.items():
            out[k] = add_up([x[k] for x in objs]) if isinstance(v, dict) else sum(x[k] for x in objs)
    return out


class Case(unittest.TestCase):
    ENV = {}

    @classmethod
    def setUpClass(cls):
        cls.hub = Hub(**cls.ENV)
        cls.addClassCleanup(cls.hub.stop)

    def setUp(self):
        self.hub.get("/mock/reset")

    def rows(self):
        return self.hub.get("/api/v1/todos")["todos"]

    def row(self, tid):
        return next(t for t in self.rows() if t["id"] == tid)

    def post(self, path, body=None, **headers):
        return self.hub.call(path, "POST", {} if body is None else body, headers)

    def holds(self, definition, sample, where):
        schema = DOC["$defs"][definition]
        self.assertEqual(VALIDATOR.errors(schema, sample, SCHEMA), [], where)
        self.assertEqual(wire.walk(DOC, schema, sample, "$", set()), [], "%s: fields the schema lacks" % where)
        if definition == "TodoSummary":  # the validator reads no format: every time is RFC 3339 as Go writes it
            times = [sample.get(k) for k in ("since", "next_change_at", "inactive")]
            times += [a.get(k) for a in sample.get("asks", []) for k in ("default_at", "round")]
            args = sample.get("line", {}).get("args", {})
            times += [args.get(k) for k in ("until", "at", "default_at", "resets_at")]
            for t in filter(None, times):
                self.assertRegex(t, r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$", where)


class ViewTests(Case):
    ENV = {"MOCK_TODO_VIEW": "1"}

    def test_the_hello_names_the_view(self):
        features = self.hub.get("/api/v1/health")["features"]
        self.assertIn("todo_view", features)
        self.assertIn("todo_places", features)

    def test_every_answer_holds_to_the_schema(self):
        listed = self.hub.get("/api/v1/todos")
        self.assertTrue(listed["todos"])
        for t in listed["todos"]:
            self.holds("TodoSummary", t["view"], t["id"])
            ids = [a["id"] for a in t["view"].get("asks", [])]
            self.assertEqual(len(ids), len(set(ids)), t["id"])
            item = self.hub.get("/api/v1/todos/" + t["id"])["todo"]
            self.assertEqual(item, {k: v for k, v in t.items() if k != "manager"}, t["id"])
        self.holds("TodoCounts", listed["counts"], "counts")
        places = self.hub.get("/api/v1/todos/places")
        self.holds("TodoPlaces", places, "places")
        self.assertEqual(places["counts"], listed["counts"])

    def test_counts_are_the_rows_sections(self):
        listed = self.hub.get("/api/v1/todos")
        self.assertEqual(listed["counts"], d3_counts(listed["todos"]))

    @unittest.skipUnless(shutil.which("node"), "needs Node to read the prototype's data")
    def test_counts_are_the_prototypes(self):
        e = prototype()["expected"]
        c = self.hub.get("/api/v1/todos")["counts"]
        s = c["sections"]
        self.assertEqual((c["needs_you"], s["can_wait"], s["working"], s["just_finished"], s["waiting"], s["todo"],
                          s["someday"], c["in_progress"], c["open"]),
                         (e["needs"], e["canWait"], e["working"], e["justFinished"], e["waiting"], e["todo"],
                          e["someday"], e["inProgress"], e["open"]))
        places = self.hub.get("/api/v1/todos/places")["places"]
        self.assertEqual({p["project"]: p["needs_you"] for p in places if p["kind"] == "project"}, e["byProject"])

    @unittest.skipUnless(shutil.which("node"), "needs Node to read the prototype's data")
    def test_rows_are_the_prototypes_in_its_order(self):
        proto = prototype()["todos"]
        top = [t for t in self.rows() if not t.get("part_of")]
        self.assertEqual(sorted((t["id"], t["title"], t.get("project")) for t in top),
                         sorted((p["id"], p["title"], p["project"]) for p in proto))
        section = {p["id"]: "needs_you" if p["section"] == "needs" else p["section"] for p in proto}
        self.assertEqual({t["id"]: t["view"]["section"] for t in top}, section)
        ranked = sorted(top, key=lambda t: (list(section.values()).index(t["view"]["section"]), t["view"]["rank"]))
        self.assertEqual([t["id"] for t in ranked], [p["id"] for p in proto])

    def test_places_add_up(self):
        answer = self.hub.get("/api/v1/todos/places")
        rows = {t["id"]: t for t in self.rows()}
        places = {p["id"]: p for p in answer["places"]}
        self.assertEqual(list(places)[:3], ["all", "today", "upcoming"])
        self.assertEqual(places["all"]["needs_you"], answer["counts"]["needs_you"])
        self.assertEqual((places["today"]["needs_you"], places["upcoming"]["needs_you"]), (0, 0))
        projects = [p for p in answer["places"] if p["kind"] == "project"]
        self.assertEqual([p["project"] for p in projects], sorted(p["project"] for p in projects))
        for p in projects:
            self.assertEqual(p["id"], "project:" + p["project"])
            self.assertTrue(p["title"])
            mine = [t for t in rows.values()
                    if t.get("project") == p["project"] and t["view"]["section"] == "needs_you"]
            self.assertEqual(p["needs_you"], len(mine), p["id"])
        loose = [t for t in rows.values()
                 if not t.get("project") and not t.get("part_of") and t["view"]["section"] == "needs_you"]
        self.assertEqual(sum(p["needs_you"] for p in projects) + len(loose), places["all"]["needs_you"])

    def test_the_item_route(self):
        status, _, body = self.hub.call("/api/v1/todos/nope")
        self.assertEqual((status, body["code"]), (404, "todo_not_found"))
        t01 = self.hub.get("/api/v1/todos/t01")["todo"]
        self.assertEqual(t01["view"]["asks"][0]["id"], "todo:decision@" + t01["ask"]["asked_at"])
        self.assertEqual(t01["view"]["steps"], {"done": 2, "total": 5})

    def test_an_unchanged_list_answers_304(self):
        status, headers, _ = self.hub.call("/api/v1/todos")
        tag = headers["ETag"]
        self.assertRegex(tag, r'^W/"t1-[0-9a-f]{32}"$')
        status, headers, body = self.hub.call("/api/v1/todos", headers={"If-None-Match": tag})
        self.assertEqual((status, body, headers["ETag"]), (304, None, tag))
        weak = '"other", ' + tag[2:]  # a list, and the tag without W/: compared weakly
        self.assertEqual(self.hub.call("/api/v1/todos", headers={"If-None-Match": weak})[0], 304)
        self.assertEqual(self.hub.call("/api/v1/todos/t11", "PATCH", {"title": "Renew the passport"})[0], 200)
        status, headers, _ = self.hub.call("/api/v1/todos", headers={"If-None-Match": tag})
        self.assertEqual(status, 200)
        self.assertNotEqual(headers["ETag"], tag)

    def test_a_tick_and_its_undo(self):
        before = self.hub.get("/api/v1/todos")["counts"]
        self.assertEqual(self.hub.call("/api/v1/todos/t11", "PATCH", {"done": True})[0], 200)
        listed = self.hub.get("/api/v1/todos")
        v = next(t for t in listed["todos"] if t["id"] == "t11")["view"]
        self.assertEqual((v["phase"], v["section"], v["reason"], v["rank"], v["line"]),
                         ("done", "just_finished", "closed_unseen", 0, {"key": "completed"}))
        ahead = datetime.fromisoformat(v["next_change_at"].replace("Z", "+00:00"))
        self.assertGreater(ahead, datetime.now(timezone.utc))
        after = listed["counts"]
        self.assertEqual((after["sections"]["todo"], after["sections"]["just_finished"], after["open"]),
                         (before["sections"]["todo"] - 1, before["sections"]["just_finished"] + 1, before["open"] - 1))
        self.hub.call("/api/v1/todos/t11", "PATCH", {"done": False})
        listed = self.hub.get("/api/v1/todos")
        v = next(t for t in listed["todos"] if t["id"] == "t11")["view"]
        self.assertEqual((v["section"], v["rank"]), ("todo", 0))
        self.assertEqual(listed["counts"], before)

    def test_someday_and_back(self):
        before = self.hub.get("/api/v1/todos")["counts"]
        status, _, body = self.hub.call("/api/v1/todos/t12/someday", "PUT")
        self.assertEqual((status, body["todo"]["someday_until"]), (200, "9999-12-31T00:00:00Z"))
        v = self.row("t12")["view"]
        self.assertEqual((v["section"], v["reason"], v["line"]), ("someday", "deferred", {"key": "someday"}))
        self.assertNotIn("next_change_at", v)
        until = (datetime.now(timezone.utc) + timedelta(days=3)).replace(microsecond=0).strftime("%Y-%m-%dT%H:%M:%SZ")
        self.assertEqual(self.hub.call("/api/v1/todos/t12/someday", "PUT", {"until": until})[0], 200)
        v = self.row("t12")["view"]
        self.assertEqual((v["line"], v["next_change_at"]), ({"key": "someday", "args": {"until": until}}, until))
        counts = self.hub.get("/api/v1/todos")["counts"]
        self.assertEqual((counts["sections"]["someday"], counts["open"]),
                         (before["sections"]["someday"] + 1, before["open"] - 1))
        self.assertEqual(self.hub.call("/api/v1/todos/t12/someday", "DELETE")[0], 200)
        self.assertEqual(self.hub.get("/api/v1/todos")["counts"], before)
        # Someday is row 12: a to-do that needs the owner stays where it is
        self.assertEqual(self.hub.call("/api/v1/todos/t01/someday", "PUT")[0], 200)
        self.assertEqual(self.row("t01")["view"]["section"], "needs_you")
        status, _, body = self.hub.call("/api/v1/todos/t08/someday", "PUT")
        self.assertEqual((status, body["code"]), (409, "todo_closed"))
        status, _, body = self.hub.call("/api/v1/todos/t11/someday", "PUT", {"until": "2020-01-01T00:00:00Z"})
        self.assertEqual((status, body["code"]), (400, "someday_until"))
        self.assertEqual(self.hub.call("/api/v1/todos/nope/someday", "PUT")[0], 404)
        reader = {"Authorization": "", "X-Mock-Role": "readonly"}
        self.assertEqual(self.hub.call("/api/v1/todos/t11/someday", "PUT", headers=reader)[0], 403)
        # reopening takes a to-do out of Someday
        self.hub.call("/api/v1/todos/t11/someday", "PUT")
        self.hub.call("/api/v1/todos/t11", "PATCH", {"done": True})
        self.hub.call("/api/v1/todos/t11", "PATCH", {"done": False})
        self.assertEqual(self.row("t11")["view"]["section"], "todo")

    def test_restore_is_no_edit(self):
        before = self.row("t11")
        status, _, body = self.hub.call("/api/v1/todos/t11/restored", "PUT")
        self.assertEqual(status, 200)
        self.assertTrue(body["todo"]["restored_at"])
        self.assertEqual(body["todo"]["updated_at"], before["updated_at"])

    def test_seen_settles_just_finished(self):
        self.assertEqual(self.hub.call("/api/v1/todos/t07/seen", "PUT")[0], 200)
        v = self.row("t07")["view"]
        self.assertEqual((v["section"], v["reason"], v["seen"]), ("just_finished", "finished_recent", True))
        self.assertIn("next_change_at", v)
        self.hub.call("/api/v1/todos/t08/seen", "PUT")
        v = self.row("t08")["view"]
        self.assertEqual((v["phase"], v["section"], v["reason"], v["seen"]), ("done", "done", "completed", True))

    def test_a_finished_row_seen_a_day_ago_offers_mark_done(self):
        self.hub.get("/mock/todo-view?seen_hours=25")
        self.hub.call("/api/v1/todos/t07/seen", "PUT")
        v = self.row("t07")["view"]
        self.assertEqual((v["section"], v["reason"], v["line"]["key"]), ("todo", "finished_seen", "finished_seen"))
        self.hub.get("/mock/reset")
        self.hub.call("/api/v1/todos/t07/seen", "PUT")
        self.assertEqual(self.row("t07")["view"]["reason"], "finished_recent")

    def test_a_parent_follows_its_steps(self):
        t02 = self.row("t02")["view"]
        self.assertEqual((t02["section"], t02["reason"], t02["line"]["args"]["step"], t02["steps"]),
                         ("needs_you", "agent_asks", "Widget", {"done": 1, "total": 3}))
        self.assertTrue(t02["asks"][0]["id"].startswith("step:t02-widget/run:macbook/"))
        self.hub.call("/api/v1/todos/t02-widget", "PATCH", {"done": True})
        listed = self.hub.get("/api/v1/todos")
        t02 = next(t for t in listed["todos"] if t["id"] == "t02")["view"]
        self.assertEqual((t02["section"], t02["reason"], t02["line"]["args"]["step"], t02["steps"]),
                         ("working", "agent_running", "Release notes", {"done": 2, "total": 3}))
        self.assertNotIn("asks", t02)
        self.assertEqual(listed["counts"], d3_counts(listed["todos"]))

    def test_a_choice_answers_t01(self):
        asked = self.row("t01")["ask"]["asked_at"]
        self.assertEqual(self.post("/api/v1/todos/t01/accept", {"asked_at": "2026-01-01T00:00:00Z"})[0], 409)
        self.assertEqual(self.post("/api/v1/todos/t01/accept", {"asked_at": asked, "option": "Elsewhere"})[0], 400)
        self.assertEqual(self.post("/api/v1/todos/t01/accept", {})[0], 400)
        reader = {"Authorization": "", "X-Mock-Role": "readonly"}
        self.assertEqual(self.post("/api/v1/todos/t01/accept", {"asked_at": asked}, **reader)[0], 403)
        self.assertEqual(self.post("/api/v1/todos/t01/accept", {"asked_at": asked, "option": "Kaede"})[2], {"ok": True})
        listed = self.hub.get("/api/v1/todos")
        t01 = next(t for t in listed["todos"] if t["id"] == "t01")
        self.assertNotIn("ask", t01)
        self.assertNotIn("decision_review", t01)
        v = t01["view"]
        self.holds("TodoSummary", v, "t01")
        self.assertEqual((v["section"], v["reason"], v["line"], v["rank"], v["steps"]),
                         ("working", "persona_working", {"key": "persona_working", "args": {"text": "booking Kaede"}},
                          0, {"done": 3, "total": 5}))
        self.assertNotIn("asks", v)
        self.assertEqual(listed["counts"], d3_counts(listed["todos"]))
        self.assertEqual(self.post("/api/v1/todos/t01/accept", {"asked_at": asked})[0], 409, "the round is over")

    def test_the_fixed_answers(self):
        asked = self.row("t01")["ask"]["asked_at"]
        both = {"asked_at": asked, "option": "Kaede", "response": "explain_first"}
        self.assertEqual(self.post("/api/v1/todos/t01/accept", both)[0], 400)
        self.assertEqual(self.post("/api/v1/todos/t01/accept", {"asked_at": asked, "response": "later"})[0], 400)
        said = {"asked_at": asked, "response": "explain_first"}
        self.assertEqual(self.post("/api/v1/todos/t01/accept", said)[0], 200)
        t01 = self.row("t01")  # the round waits, its default no longer taken
        self.assertEqual((t01["ask"]["response"], t01["ask"]["default"]), ("explain_first", "never"))
        self.assertNotIn("deadline", t01["ask"])
        self.assertEqual(t01["view"]["section"], "needs_you")
        self.assertNotIn("default_at", t01["view"]["asks"][0])
        said = {"asked_at": asked, "response": "decide_yourself"}
        self.assertEqual(self.post("/api/v1/todos/t01/accept", said)[0], 200)
        t01 = self.row("t01")
        self.assertNotIn("ask", t01)
        self.assertEqual((t01["view"]["section"], t01["view"]["reason"]), ("todo", "rest"))

    def test_dont_handle_and_its_undo(self):
        before = self.hub.get("/api/v1/todos")["counts"]
        at = self.row("t01")["decision_review"]["review_at"]
        self.assertEqual(self.post("/api/v1/todos/t01/decision-dismiss", {"review_at": "2026-01-01T00:00:00Z"})[0], 409)
        status, _, receipt = self.post("/api/v1/todos/t01/decision-dismiss", {"review_at": at})
        self.assertEqual((status, receipt["review_at"]), (200, at))
        listed = self.hub.get("/api/v1/todos")
        v = next(t for t in listed["todos"] if t["id"] == "t01")["view"]
        self.assertEqual((v["section"], v["reason"]), ("todo", "rest"))
        self.assertEqual(listed["counts"]["needs_you"], before["needs_you"] - 1)
        undo = {"review_at": at, "dismissal_id": receipt["dismissal_id"]}
        self.assertEqual(self.post("/api/v1/todos/t01/decision-undo-dismiss", undo)[0], 200)
        self.assertEqual(self.row("t01")["view"]["section"], "needs_you")
        self.assertEqual(self.hub.get("/api/v1/todos")["counts"], before)

    def test_retry_starts_a_failed_start(self):
        v = self.row("t03")["view"]
        self.assertEqual((v["reason"], [a["kind"] for a in v["asks"]]), ("failed_start", ["retry"]))
        status, _, body = self.post("/api/v1/todos/t03/retry")
        self.assertEqual(status, 200)
        v = body["todo"]["view"]
        self.holds("TodoSummary", v, "t03")
        self.assertEqual((v["section"], v["reason"], v["line"]),
                         ("working", "agent_running", {"key": "working", "args": {
                             "run": "Sort last year’s photos", "text": "Grouping the photos by trip."}}))
        self.assertNotIn("asks", v)
        self.assertEqual(self.row("t03")["view"], v)
        self.assertEqual(self.post("/api/v1/todos/t03/retry")[0], 200, "an open one answers as it is")
        self.assertEqual(self.post("/api/v1/todos/t11/retry")[0], 409, "no origin")
        self.assertEqual(self.post("/api/v1/todos/t08/retry")[0], 409, "closed some other way")
        self.assertEqual(self.post("/api/v1/todos/nope/retry")[0], 404)

    def test_a_tick_closes_a_failed_start(self):
        status, _, body = self.hub.call("/api/v1/todos/t03", "PATCH", {"done": True})
        self.assertEqual((status, body["todo"].get("start_error")), (200, ""))
        listed = self.hub.get("/api/v1/todos")
        v = next(t for t in listed["todos"] if t["id"] == "t03")["view"]
        self.assertEqual((v["phase"], v["section"], v["reason"], v["line"]),
                         ("done", "just_finished", "closed_unseen", {"key": "completed"}))
        done = datetime.fromisoformat(v["since"].replace("Z", "+00:00"))
        self.assertLess(datetime.now(timezone.utc) - done, timedelta(minutes=1))
        self.assertEqual(listed["counts"], d3_counts(listed["todos"]))

    def test_a_refused_write_changes_nothing(self):
        reader = {"Authorization": "", "X-Mock-Role": "readonly"}
        self.assertEqual(self.hub.call("/api/v1/todos/t14", "PATCH", {"done": False}, reader)[0], 403)
        self.assertEqual(self.row("t14")["view"]["section"], "someday")
        self.assertEqual(self.hub.call("/api/v1/todos/t14", "PATCH", {"done": False, "title": " "})[0], 400)
        self.assertEqual(self.row("t14")["view"]["section"], "someday")
        self.assertEqual(self.hub.call("/api/v1/todos/t03", "PATCH", {"done": True}, reader)[0], 403)
        self.assertEqual(self.row("t03")["view"]["reason"], "failed_start")
        self.assertEqual(self.hub.call("/api/v1/todos/t14", "PATCH", {"done": False})[0], 200)
        self.assertEqual(self.row("t14")["view"]["section"], "todo")

    def test_seen_on_open_rows(self):
        self.assertNotIn("seen", self.row("t01")["view"])
        self.hub.call("/api/v1/todos/t01/seen", "PUT")
        self.assertTrue(self.row("t01")["view"]["seen"])
        self.hub.call("/api/v1/todos/t01/seen", "DELETE")
        self.assertNotIn("seen", self.row("t01")["view"])
        # a parent its step leads: seen against the latest activity of both
        self.hub.call("/api/v1/todos/t02/seen", "PUT")
        self.assertTrue(self.row("t02")["view"]["seen"])
        self.hub.call("/api/v1/todos/t02-widget", "PATCH", {"notes": "Small and medium first."})
        v = self.row("t02")["view"]
        self.assertEqual(v["line"]["args"]["step"], "Widget")
        self.assertNotIn("seen", v)

    def test_the_widget_plan_card(self):
        def card():
            cards = self.hub.get("/api/v1/interactions")["interactions"]
            return next((x for x in cards if x["id"] == "ix_todo_widget_plan"), None)

        ask = self.row("t02")["view"]["asks"][0]
        self.assertEqual((card()["kind"], card()["session_id"]), (ask["kind"], ask["run"]["session_id"]))
        self.assertEqual(self.post("/api/v1/interactions/ix_todo_widget_plan/resolve", {"approve": True})[0], 200)
        self.assertIsNone(card())
        listed = self.hub.get("/api/v1/todos")
        rows = {t["id"]: t for t in listed["todos"]}
        v = rows["t02-widget"]["view"]
        self.assertEqual((v["section"], v["reason"], v["line"]),
                         ("working", "agent_running",
                          {"key": "working", "args": {"run": "Widget", "text": "Building the widget."}}))
        self.assertEqual(rows["t02"]["view"]["section"], "working")
        self.assertNotIn("asks", rows["t02"]["view"])
        self.assertEqual(listed["counts"], d3_counts(listed["todos"]))
        self.hub.get("/mock/reset")
        self.assertIsNotNone(card())
        self.assertEqual(self.row("t02")["view"]["section"], "needs_you")

    def test_churn_moves_a_working_line_and_pushes(self):
        tag = self.hub.call("/api/v1/todos")[1]["ETag"]
        ws = Socket(self.hub.port)
        self.addCleanup(ws.close)
        self.assertIsNotNone(ws.message(lambda m: m.get("type") == "hello"))
        self.assertEqual(self.hub.get("/mock/churn?todos=1&n=2&ms=100"), {"ok": True})
        self.assertIsNotNone(ws.message(lambda m: m.get("type") == "todos"))
        deadline = time.monotonic() + 10
        while self.hub.get("/mock/churned")["rounds"] < 2 and time.monotonic() < deadline:
            time.sleep(0.05)
        self.assertEqual(self.row("t05")["view"]["line"]["args"]["text"], "reading it through once more")
        self.assertNotEqual(self.hub.call("/api/v1/todos")[1]["ETag"], tag)


class OldHubTests(Case):
    ENV = {"MOCK_TODO_VIEW": "old"}

    def test_a_hub_from_before_the_view(self):
        features = self.hub.get("/api/v1/health")["features"]
        self.assertNotIn("todo_view", features)
        self.assertNotIn("todo_places", features)
        listed = self.hub.get("/api/v1/todos")
        self.assertNotIn("counts", listed)
        self.assertTrue(listed["todos"])
        self.assertFalse([t["id"] for t in listed["todos"] if "view" in t])
        self.assertFalse([t["id"] for t in listed["todos"] if "someday_until" in t or "restored_at" in t])
        self.assertTrue(next(t for t in listed["todos"] if t["id"] == "t01")["ask"])
        for path in ("/api/v1/todos/t01", "/api/v1/todos/places"):
            self.assertEqual(self.hub.call(path)[0], 405, path)
        self.assertEqual(self.hub.call("/api/v1/todos/t12/someday", "PUT")[0], 404)
        self.assertEqual(self.hub.call("/api/v1/todos/t12/restored", "PUT")[0], 404)


class LateTests(Case):
    ENV = {"MOCK_TODO_VIEW": "late", "MOCK_TODO_VIEW_LATE_SECONDS": "1.5"}

    def test_the_hellos_lack_have_lack_have_the_view(self):
        seen = []
        for _ in range(4):
            ws = Socket(self.hub.port)
            self.addCleanup(ws.close)
            hello = ws.message(lambda m: m.get("type") == "hello")
            self.assertIsNotNone(hello)
            seen.append(("todo_view" in hello["features"], "todo_places" in hello["features"]))
            self.assertEqual(self.hub.get("/api/v1/health")["features"].count("todo_view"), int(seen[-1][0]))
            self.assertTrue(all("view" in t for t in self.rows()))  # the routes answer as under 1 throughout
            if len(seen) < 4:
                self.assertTrue(ws.closed(10), "the mock closes the socket, as a restarting hub does")
        self.assertEqual(seen, [(False, False), (True, True), (False, False), (True, True)])
        self.assertFalse(ws.closed(3), "after the fourth hello the socket stays")
        self.assertEqual(self.hub.get("/mock/todo-view")["phase"], 3)
        self.hub.get("/mock/reset")  # the sequence starts again: the socket closes, and the next hello lacks the view
        self.assertTrue(ws.closed(5))
        again = Socket(self.hub.port)
        self.addCleanup(again.close)
        self.assertNotIn("todo_view", again.message(lambda m: m.get("type") == "hello")["features"])
        self.assertEqual(self.hub.get("/mock/todo-view")["phase"], 0)


class TwoManagersTests(Case):
    ENV = {"MOCK_TODO_VIEW": "1", "MOCK_TODO_MANAGERS": "2"}

    def test_two_managers_answers_joined(self):
        listed = self.hub.get("/api/v1/todos")
        ids = self.hub.get("/mock/todo-view")["managers"]
        self.assertEqual(len(ids), 2)
        rows = listed["todos"]
        managers = [t["manager"] for t in rows]
        self.assertEqual(managers, sorted(managers, key=ids.index))  # the default's rows first
        parent = {t["id"]: t for t in rows}
        for t in rows:
            weather = parent[t.get("part_of") or t["id"]].get("project") == "weather"
            self.assertEqual(t["manager"], ids[1] if weather else ids[0], t["id"])
        per = [d3_counts([t for t in rows if t["manager"] == m]) for m in ids]
        self.assertEqual(listed["counts"], add_up(per))
        self.assertEqual(listed["counts"], d3_counts(rows))  # the split moves no number
        for m in ids:  # each manager ranks its own rows
            mine = [t["view"] for t in rows if t["manager"] == m]
            for section in {v["section"] for v in mine}:
                self.assertEqual(sorted(v["rank"] for v in mine if v["section"] == section),
                                 list(range(sum(v["section"] == section for v in mine))), (m, section))
        places = self.hub.get("/api/v1/todos/places")
        self.holds("TodoPlaces", places, "places")
        fixed = [p for p in places["places"] if p["kind"] != "project"]
        self.assertEqual([p["id"] for p in fixed], ["all", "today", "upcoming"])
        self.assertFalse([p for p in fixed if "manager" in p])
        self.assertEqual(fixed[0]["needs_you"], listed["counts"]["needs_you"])
        own = {p["project"]: p["manager"] for p in places["places"] if p["kind"] == "project"}
        self.assertEqual(own, {"home": ids[0], "kyoto": ids[0], "weather": ids[1]})
        self.assertEqual(places["counts"], listed["counts"])


if __name__ == "__main__":
    unittest.main()
