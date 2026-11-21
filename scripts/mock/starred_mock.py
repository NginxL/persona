"""The web Starred page's loopback fixture. Separate assistants get separate collections."""
import datetime
import json
import os
import re
import threading
import uuid
from urllib.parse import unquote


BASE = "/api/v1/assistant/starred"
BACKUP = BASE + "/import"
BACKUP_MACHINE = "persona-starred-backup"
BACKUP_BODY_MAX = 8 << 20
MAX_STARS = 300
SOURCE_FIELDS = ("machine_id", "session_id", "item_id", "conversation", "source_fingerprint")
POSITION = "line900#0"
LONG_USER = "fixture-long-user#0"
LONG_USER_VISIBLE = "User-visible long note " + "🐈" * 6500
VERSIONS = {"original": ("Original words from the reused line", "a" * 32),
            "replacement": ("Replacement words from the reused line", "b" * 32)}


class StarredBook:
    def __init__(self):
        self.stars = []
        self.revision = 0

    def add_or_preview(self, messages, revision):
        """Compute a portable restore without exposing its source as a live conversation."""
        known = {star["id"] for star in self.stars}
        for star in self.stars:
            if (star["machine_id"] == BACKUP_MACHINE and star["item_id"] == "line0#0" and
                    not star.get("source_fingerprint") and not star.get("source_manager") and
                    re.fullmatch(r"backup\.sm[0-9a-f]{32}", star["session_id"])):
                known.add(star["session_id"].removeprefix("backup."))
        new = [message for message in messages if message["key"] not in known]
        saved = len(messages) - len(new)
        available = max(0, MAX_STARS - len(self.stars))
        if revision is None:
            return {"revision": self.revision, "total": len(messages), "to_add": len(new),
                    "already_saved": saved, "available": available}, 200, False
        if revision != self.revision:
            return {"error": "The saved messages changed. Preview the backup again.", "code": "conflict"}, 409, False
        if len(new) > available:
            return {"error": "There is not enough room for this backup.", "code": "bad_request"}, 400, False
        if new:
            restored = [
                {k: v for k, v in message.items() if k != "key"} | {
                    "id": "sm" + uuid.uuid4().hex, "machine_id": BACKUP_MACHINE,
                    "session_id": "backup." + message["key"], "item_id": "line0#0",
                }
                for message in new
            ]
            self.stars.extend(restored)
            self.stars.sort(key=lambda star: _backup_time(star["starred_at"]), reverse=True)
            self.revision += 1
        return {"revision": self.revision, "added": len(new), "already_saved": saved}, 200, bool(new)


def _backup_time(value, allow_zero=False):
    if not isinstance(value, str) or not re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})", value):
        raise ValueError("A timestamp must be RFC 3339")
    at = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if not allow_zero and at == datetime.datetime.min.replace(tzinfo=datetime.timezone.utc):
        raise ValueError("A timestamp must be set")
    return at


def _unique_object(pairs):
    out = {}
    for key, value in pairs:
        if key in out:
            raise ValueError("Duplicate member")
        out[key] = value
    return out


def _backup_request(req, preview):
    body = json.loads(req.body, object_pairs_hook=_unique_object)
    fields = {"format", "version", "messages"} if preview else {"format", "version", "messages", "revision"}
    if (not isinstance(body, dict) or set(body) != fields or body["format"] != "persona.starred" or
            type(body["version"]) is not int or body["version"] != 1):
        raise ValueError("Unsupported backup")
    revision = body.get("revision")
    if not preview and (type(revision) is not int or not 0 <= revision < (1 << 53) - 1):
        raise ValueError("Invalid preview revision")
    messages = body["messages"]
    if not isinstance(messages, list) or len(messages) > MAX_STARS:
        raise ValueError("Invalid messages")
    required = {"key", "conversation", "role", "title", "speaker", "text", "starred_at"}
    seen = set()
    for row in messages:
        if (not isinstance(row, dict) or not required <= row.keys() <= required | {"written_at", "omitted_chars"} or
                not isinstance(row["key"], str) or not re.fullmatch(r"sm[0-9a-f]{32}", row["key"]) or row["key"] in seen or
                row["conversation"] not in ("assistant", "agent") or row["role"] not in ("user", "assistant") or
                not all(isinstance(row[k], str) for k in ("title", "speaker", "text")) or
                len(row["text"]) > 4000 or "\x00" in row["text"]):
            raise ValueError("Invalid message")
        for name in ("title", "speaker"):
            label = row[name]
            normalized = " ".join(label.split())
            clipped_at_space = len(label) == 200 and label.endswith(" ") and label[:-1] == normalized
            if "\x00" in label or len(label) > 200 or (label != normalized and not clipped_at_space):
                raise ValueError("Invalid title or speaker")
        omitted = row.get("omitted_chars", 0)
        if type(omitted) is not int or not 0 <= omitted <= 1_000_000_000:
            raise ValueError("Invalid omitted character count")
        if not row["text"].strip() and not (len(row["text"]) == 4000 and omitted > 0):
            raise ValueError("Missing saved text")
        _backup_time(row["starred_at"])
        if "written_at" in row:
            _backup_time(row["written_at"], allow_zero=True)
        seen.add(row["key"])
    return messages, revision


def register(d):
    hub = d.hub
    d.help("MOCK_STARRED=1|old  Enable starred-snapshot routes (1) or simulate an older assistant (old)")
    mode = os.environ.get("MOCK_STARRED", "old")
    d.help("MOCK_STARRED_BACKUP=1  Enable portable backup preview and import with MOCK_STARRED=1")
    backup_enabled = mode == "1" and os.environ.get("MOCK_STARRED_BACKUP") == "1"
    if mode == "1":
        d.feature("starred_messages")
    books = {}
    lock = threading.Lock()
    unreadable = False

    def selected_manager(h):
        other = hub.other_manager(h)
        if other is None:
            return hub.MANAGERS["default"]
        manager, problem = other
        if problem is not None:
            h.send_json(*problem)
            return None
        return manager

    def invalidate(manager):
        event = {"type": "assistant_starred"}
        if manager != "assistant":
            event["manager"] = manager
        hub.broadcast(event)

    def backup(h, req):
        if not backup_enabled:
            if mode == "1" and req.path == BACKUP:
                # An older manager still exposes DELETE /starred/{id}, which matches this path with another method.
                h.send_json({"error": "Method not allowed"}, 405, headers={"Allow": "DELETE"})
            else:
                h.send_json({"error": "Not found"}, 404)
            return True
        manager = selected_manager(h)
        if manager is None:
            return True
        if hub.is_read_only(h):
            h.send_json({"error": "Full credentials required.", "code": "forbidden"}, 403)
            return True
        if req.method == "GET":
            h.send_json({"can_import": True})
            return True
        if len(req.body) > BACKUP_BODY_MAX:
            h.send_json({"error": "The backup is too large.", "code": "too_large"}, 413)
            return True
        try:
            messages, revision = _backup_request(req, req.path == BACKUP + "/preview")
        except (ValueError, UnicodeError):
            h.send_json({"error": "The backup is invalid.", "code": "bad_request"}, 400)
            return True
        with lock:
            book = books.setdefault(manager, StarredBook())
            body, status, changed = book.add_or_preview(messages, revision)
        if changed:
            invalidate(manager)
        h.send_json(body, status)
        return True

    def route(h, req):
        if mode != "1":
            h.send_json({"error": "Not found"}, 404)
            return True
        manager = selected_manager(h)
        if manager is None:
            return True
        changed = False
        with lock:
            book = books.setdefault(manager, StarredBook())
            stars = book.stars
            if req.method == "GET":
                selected = stars[:]
                if session := (req.query.get("session") or [""])[0]:
                    selected = [s for s in selected if f"{s['machine_id']}/{s['session_id']}" == session]
                if req.query.get("index") == ["1"]:
                    refs = [{k: s[k] for k in (*SOURCE_FIELDS, "id") if k in s} for s in selected]
                    body = {"refs": refs, "can_write": True}
                else:
                    body = {"stars": selected, "can_write": True}
            elif req.method == "POST":
                item = req.json()
                key = tuple(item.get(k) for k in SOURCE_FIELDS)
                if not all(key[:4]) or not str(item.get("text", "")).strip():
                    h.send_json({"error": "A star needs a message"}, 400)
                    return True
                star = next((s for s in stars if tuple(s.get(k) for k in SOURCE_FIELDS) == key), None)
                if star is None:
                    star = dict(item, id="sm" + uuid.uuid4().hex, starred_at=hub.ago())
                    stars.insert(0, star)
                    changed = True
                body = {"star": star}
            else:
                identifier = req.match.group(1)
                removed = next((star for star in stars if star["id"] == identifier), None)
                if removed is not None:
                    stars.remove(removed)
                    changed = True
                body = {"removed": removed}
            if changed:
                book.revision += 1
        if changed:
            invalidate(manager)
        h.send_json(body)
        return True

    def source(h, req):
        nonlocal unreadable
        phase = req.json().get("phase")
        if mode != "1" or phase not in (*VERSIONS, "reset", "stale", "long-user", "unreadable"):
            h.send_json({"error": "Not found"}, 404)
            return True
        with lock:
            unreadable = phase == "unreadable"
            if unreadable:
                h.send_json({"ok": True})
                return True
            if phase == "stale":
                # Same-size overwrite: the node's regular page is unchanged, but Full can't verify the raw source.
                hub.FULL.pop(POSITION, None)
                h.send_json({"ok": True})
                return True
            items = [item for item in hub.ITEMS[hub.REVIEW] if item["id"] not in (POSITION, LONG_USER)]
            hub.FULL.pop(POSITION, None)
            hub.FULL.pop(LONG_USER, None)
            if phase == "long-user":
                raw = '<persona-context kind="skill-instructions">' + "h" * 25000 + "</persona-context>\n" + LONG_USER_VISIBLE
                size = len(raw.encode())
                cut = raw[:24 * 1024] + f"\n…（已截断，点开看全文，共 {size} 字节）"
                item = hub.it("user", "text", 1, id=LONG_USER, seq=901, text=cut, full_size=size)
                items.append(item)
                hub.FULL[LONG_USER] = {k: v for k, v in item.items() if k != "full_size"} | {"text": raw}
            elif phase != "reset":
                text, proof = VERSIONS[phase]
                item = hub.it("user", "text", 1, id=POSITION, seq=900, text=text, source_fingerprint=proof)
                items.append(item)
                hub.FULL[POSITION] = item
            hub.ITEMS[hub.REVIEW] = items
            hub.HISTORY_NOW["token"] = hub.HISTORY if phase == "reset" else "starred-source-" + phase
        h.send_json({"ok": True})
        return True

    def full_source(h, req):
        if mode != "1":
            return False
        session, item_id = (unquote(part) for part in req.match.groups())
        if session == hub.REVIEW and item_id == POSITION and unreadable:
            h.send_json({"error": "Synthetic transcript read failure"}, 502)
            return True
        if item_id in hub.FULL or re.fullmatch(r"line[0-9]+#[0-9]+", item_id):
            return False  # The normal mock handles saved full values and unverified positions.
        # A real node can return an uncut stable item from the same transcript.
        item = next((value for value in hub.find_items(session) if value["id"] == item_id and not value.get("full_size")), None)
        if item is None:
            return False
        h.send_json(item)
        return True

    d.route(("POST",), "/mock/starred-source", source)
    d.route(("GET",), r"/api/v1/sessions/[^/]+/([^/]+)/items/([^/]+)/full", full_source)
    d.route(("GET", "POST"), BACKUP, backup)
    d.route(("POST",), BACKUP + "/preview", backup)
    d.route(("GET", "POST"), BASE, route)
    d.route(("DELETE",), BASE + r"/([^/]+)", route)
