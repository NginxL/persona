"""A repeating task whose next occurrence can be controlled by browser tests."""

import datetime
import os
import threading
import uuid


TASKS = "/api/v1/assistant/tasks"
TASK_ID = "fixture-skip-occurrence"
TASK = TASKS + "/" + TASK_ID
PERIOD = datetime.timedelta(minutes=30)


def _iso(value):
    return value.isoformat(timespec="seconds").replace("+00:00", "Z")


def _stamp(value):
    if not isinstance(value, str):
        return None
    try:
        parsed = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None
    return parsed if parsed.tzinfo is not None else None


class Schedule:
    def __init__(self):
        now = datetime.datetime.now(datetime.timezone.utc)
        self.next = (now + PERIOD).replace(second=0, microsecond=0)
        self.skip = None
        self.token = ""
        self.runs = []

    def task(self, supported):
        view = {
            "id": TASK_ID, "title": "Review notes", "prompt": "Prepare the review notes.",
            "repeat": "interval", "every_minutes": 30, "enabled": True,
            "at": _iso(self.next), "next_run": _iso(self.next),
        }
        if supported and self.skip is None:
            view["can_skip_next"] = True
        if self.skip is not None:
            view["skip_next_at"] = _iso(self.skip)
            view["skip_next_token"] = self.token
        return view

    def add_run(self, due, outcome, **fields):
        self.runs.insert(0, {
            "id": f"skip-fixture-{len(self.runs)}", "kind": "routine", "ref_id": TASK_ID,
            "title": "Review notes", "due": _iso(due), "at": _iso(due),
            "status": outcome, **fields,
        })


def register(d):
    d.help("MOCK_SKIP_OCCURRENCE=1|legacy  Enable a repeat task for skip-next browser tests")
    mode = os.environ.get("MOCK_SKIP_OCCURRENCE", "")
    if mode not in ("1", "legacy"):
        return
    supported = mode == "1"
    hub = d.hub
    books = {}
    lock = threading.Lock()

    def selected(h):
        other = hub.other_manager(h)
        if other is None:
            return hub.MANAGERS["default"]
        manager, problem = other
        if problem:
            h.send_json(*problem)
            return None
        return manager

    def change(h, req):
        manager = selected(h)
        if manager is None:
            return True
        if req.method != "GET" and hub.is_read_only(h):
            h.send_json({"error": "Full credentials required."}, 403)
            return True
        changed = False
        with lock:
            book = books.setdefault(manager, Schedule())
            if req.path == TASKS:
                response, status = {"tasks": [book.task(supported)]}, 200
            elif req.path == "/mock/skip-occurrence/reset":
                book = Schedule()
                books[manager] = book
                response, status = {"task": book.task(supported)}, 200
            elif req.path == "/api/v1/assistant/runs":
                response, status = {"runs": list(book.runs)}, 200
            elif req.path == TASK + "/run":
                now = datetime.datetime.now(datetime.timezone.utc)
                book.add_run(now, "ok", session_id=hub.ASK2, machine_id="server", manual=True)
                response, status, changed = {"session_id": hub.ASK2, "machine_id": "server"}, 200, True
            elif req.path == "/mock/skip-occurrence/retime":
                book.next += PERIOD
                book.skip = None
                book.token = ""
                # Simulate an edit the first client has not received yet.
                response, status = {"task": book.task(supported)}, 200
            elif req.path == "/mock/skip-occurrence/consume" and supported and book.skip is not None:
                book.add_run(book.skip, "skipped", owner_skipped=True)
                book.next += PERIOD
                book.skip = None
                book.token = ""
                response, status, changed = {"task": book.task(supported)}, 200, True
            elif req.path != TASK + "/skip-next" or not supported:
                response, status = {"error": "Unavailable"}, 404
            else:
                if req.method == "POST":
                    try:
                        body = req.json()
                        raw = body.get("at") if isinstance(body, dict) else None
                    except ValueError:
                        raw = None
                else:
                    raw = req.query.get("at", [""])[0]
                at = _stamp(raw)
                expected = book.next if req.method == "POST" else book.skip
                token = req.query.get("token", [""])[0]
                if at is None:
                    response, status = {"error": "Supply a scheduled time."}, 400
                elif at != expected or (req.method == "DELETE" and token != book.token):
                    response, status = {"error": "The next scheduled run changed. Review the new time."}, 409
                else:
                    if req.method == "DELETE":
                        book.skip, book.token = None, ""
                    elif book.skip is None:
                        book.skip, book.token = book.next, uuid.uuid4().hex
                    response, status, changed = {"task": book.task(supported)}, 200, True
        if changed:
            event = {"type": "assistant_schedule"}
            if manager != hub.MANAGERS["default"]:
                event["manager"] = manager
            hub.broadcast(event)
        h.send_json(response, status)
        return True

    d.route(("GET",), TASKS, change)
    d.route(("GET",), "/api/v1/assistant/runs", change)
    d.route(("POST",), TASK + "/run", change)
    d.route(("POST", "DELETE"), TASK + "/skip-next", change)
    d.route(("POST",), "/mock/skip-occurrence/reset", change)
    d.route(("POST",), "/mock/skip-occurrence/retime", change)
    d.route(("POST",), "/mock/skip-occurrence/consume", change)
