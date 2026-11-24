"""Synthetic Health intake records and detail pages for clients and bundled demos (#13176)."""
from copy import deepcopy
from datetime import datetime, timedelta, timezone
import json
import math
import os
import threading
import uuid

HUB = None

HEALTH_INTAKE_ENABLED = True
HEALTH_INTAKE_LOCK = threading.RLock()
HEALTH_WATER_EXTENSION_MODE = "pending"
HEALTH_WATER_EXTENSION_PENDING = True
HEALTH_INTAKE_SETTINGS = {}


def health_intake_reset():
    """Restore the seeded records, settings and initial migration state together."""
    global HEALTH_WATER_EXTENSION_PENDING
    with HEALTH_INTAKE_LOCK:
        HEALTH_INTAKE_ENTRIES.clear()
        HEALTH_INTAKE_ENTRIES.update(health_intake_seed())
        HEALTH_WATER_EXTENSION_PENDING = HEALTH_WATER_EXTENSION_MODE == "pending"
        HEALTH_INTAKE_SETTINGS.clear()
        HEALTH_INTAKE_SETTINGS.update({
            "water": {"goal_ml": 2000, "cup_ml": 250, "remind": False, "start": "09:00", "end": "21:00",
                      "interval_minutes": 60, "weekends": True, "stop_when_done": True},
            "protein": {"goal_g": 60},
        })


def health_intake_seed():
    """Fictional records for the hub and bundled demo, always on today's date."""
    at = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
    return {str(uuid.UUID(int=i)): {"id": str(uuid.UUID(int=i)), "kind": kind, "amount": amount,
            "at": at, "note": "", "seq": i, "created_at": at}
            for i, (kind, amount) in enumerate([("water", 500), ("water", 250), ("protein", 36)], 1)}


HEALTH_INTAKE_ENTRIES = {}
health_intake_reset()


def health_intake_snapshot(days=7):
    with HEALTH_INTAKE_LOCK:
        today = datetime.now(timezone.utc).date()
        active = [e for e in HEALTH_INTAKE_ENTRIES.values() if "deleted_at" not in e]
        def day(e):
            return datetime.fromisoformat(e["at"].replace("Z", "+00:00")).astimezone(timezone.utc).date()
        def total(kind, date):
            return round(sum(e["amount"] for e in active if e["kind"] == kind and day(e) == date), 1)
        def portion(kind, goal):
            entries = [dict(e) for e in active if e["kind"] == kind and day(e) == today]
            entries.sort(key=lambda e: (-datetime.fromisoformat(e["at"].replace("Z", "+00:00")).timestamp(), e["id"]))
            return {"today": total(kind, today), "goal": goal, "entries": entries}
        result = {"water": portion("water", HEALTH_INTAKE_SETTINGS["water"]["goal_ml"]),
                  "protein": portion("protein", HEALTH_INTAKE_SETTINGS["protein"]["goal_g"]), "zone": "UTC",
                  "days": [{"date": str(d), "water": total("water", d), "protein": total("protein", d)}
                           for d in [today - timedelta(days=i) for i in reversed(range(days))]]}
        if HEALTH_WATER_EXTENSION_PENDING:
            result["water_extension"] = "pending"
        return result


def health_intake_route(h, method, path, query, body=b""):
    global HEALTH_INTAKE_ENABLED, HEALTH_WATER_EXTENSION_PENDING
    base = "/api/v1/assistant/health/intake"
    if path == "/mock/health-intake":
        with HEALTH_INTAKE_LOCK:
            if "enabled" in query: HEALTH_INTAKE_ENABLED = query["enabled"][0] != "0"
            if query.get("reset") == ["1"]:
                health_intake_reset()
        h.send_json({"enabled": HEALTH_INTAKE_ENABLED})
        return True
    if path != base and not path.startswith(base + "/"): return False
    if query.get("space") != ["health"] or h.headers.get("X-Persona-Manager"):
        h.send_json({"error": "Intake must use the default manager's health space"}, 400)
        return True
    if not HEALTH_INTAKE_ENABLED:
        h.send_json({"error": "Update your hub first"}, 403)
        return True
    try:
        with HEALTH_INTAKE_LOCK:
            if method == "POST" and path == base + "/import":
                if HEALTH_WATER_EXTENSION_MODE == "none":
                    h.send_json({"error": "Water extension is not installed", "code": "water_import_failed"}, 404)
                    return True
                imported = 0
                if HEALTH_WATER_EXTENSION_PENDING:
                    now = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
                    for index, amount in enumerate((200, 250), 1):
                        identity = "00000000-0000-5000-8000-%012d" % index
                        if identity not in HEALTH_INTAKE_ENTRIES:
                            HEALTH_INTAKE_ENTRIES[identity] = {
                                "id": identity, "kind": "water", "amount": amount, "at": now, "note": "",
                                "seq": max((e["seq"] for e in HEALTH_INTAKE_ENTRIES.values()), default=0) + 1,
                                "created_at": now,
                            }
                            imported += 1
                    HEALTH_WATER_EXTENSION_PENDING = False
                    HEALTH_INTAKE_SETTINGS["water"].update(goal_ml=2400, interval_minutes=90, remind=True)
                    HUB.broadcast({"type": "health_intake"})
                h.send_json({"imported": imported, "extension_disabled": True,
                             "settings": deepcopy(HEALTH_INTAKE_SETTINGS)})
                return True
            if method == "GET" and path == base + "/changes":
                after = int(query.get("after", ["0"])[0])
                limit = int(query.get("limit", ["100"])[0])
                if after < 0 or not 1 <= limit <= 1000: raise ValueError("Invalid changes cursor or limit")
                changes = sorted((dict(e) for e in HEALTH_INTAKE_ENTRIES.values() if e["seq"] > after), key=lambda e: e["seq"])
                page = changes[:limit]
                h.send_json({"changes": page, "next": page[-1]["seq"] if page else after, "has_more": len(changes) > limit})
                return True
            if method == "GET" and path == base:
                days = int(query.get("days", ["7"])[0])
                if not 1 <= days <= 366: raise ValueError("days must be 1–366")
                h.send_json(health_intake_snapshot(days))
                return True
            if method == "POST" and path == base + "/ui":
                import health_intake_ui
                h.send_json(health_intake_ui.answer(json.loads(body or b"{}")))
                return True
            if method == "POST" and path == base:
                draft = json.loads(body or b"{}")
                identity = str(uuid.UUID(draft["id"]))
                kind = draft["kind"]
                amount = draft.get("amount", 250 if kind == "water" else None)
                if kind not in ("water", "protein") or isinstance(amount, bool) or not isinstance(amount, (int, float)):
                    raise ValueError("Choose water or protein and enter an amount")
                if not math.isfinite(amount) or not (1 if kind == "water" else 0.1) <= amount <= (3000 if kind == "water" else 200) or abs(amount * 10 - round(amount * 10)) > 0.000001:
                    raise ValueError("Amount is outside the allowed range or precision")
                now = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
                at = datetime.fromisoformat(draft.get("at", now).replace("Z", "+00:00"))
                if at.tzinfo is None: raise ValueError("Timestamp needs a timezone")
                note = draft.get("note", "")
                if not isinstance(note, str) or "\0" in note or len(note.encode()) > 4096: raise ValueError("Invalid note")
                entry = HEALTH_INTAKE_ENTRIES.setdefault(identity, {"id": identity, "kind": kind, "amount": amount,
                    "at": at.astimezone(timezone.utc).isoformat().replace("+00:00", "Z"), "note": note,
                    "seq": max((e["seq"] for e in HEALTH_INTAKE_ENTRIES.values()), default=0) + 1, "created_at": now})
            elif method == "DELETE" and path.startswith(base + "/"):
                identity = str(uuid.UUID(path[len(base) + 1:]))
                entry = HEALTH_INTAKE_ENTRIES.get(identity)
                if entry is None:
                    h.send_json({"error": "Intake entry not found"}, 404)
                    return True
                if "deleted_at" not in entry:
                    entry["deleted_at"] = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
                    entry["seq"] = max(e["seq"] for e in HEALTH_INTAKE_ENTRIES.values()) + 1
            else:
                h.send_json({"error": "Unknown intake route"}, 404)
                return True
            snapshot = health_intake_snapshot()
            response = {"today": {k: snapshot[k]["today"] for k in ("water", "protein")}, "zone": snapshot["zone"]}
            if method == "POST": response["entry"] = dict(entry)
        HUB.broadcast({"type": "health_intake"})
        h.send_json(response)
    except (ValueError, KeyError, TypeError) as error:
        h.send_json({"error": str(error)}, 400)
    return True


def route(h, req):
    return health_intake_route(h, req.method, req.path, req.query, req.body)


def register(d):
    global HUB, HEALTH_INTAKE_ENABLED, HEALTH_WATER_EXTENSION_MODE
    HUB = d.hub
    HEALTH_INTAKE_ENABLED = os.environ.get("MOCK_HEALTH_INTAKE") != "off"
    HEALTH_WATER_EXTENSION_MODE = os.environ.get("MOCK_WATER_EXTENSION", "pending")
    health_intake_reset()
    d.help("MOCK_HEALTH_INTAKE=off  omit Health intake and refuse its routes (#13176)")
    d.help("MOCK_WATER_EXTENSION=none|migrated  omit the water migration card; default pending (#13636)")
    d.on_reset(health_intake_reset)
    d.feature("health_intake", when=lambda: HEALTH_INTAKE_ENABLED)
    for method in ("GET", "POST", "DELETE"):
        d.route(method, r"/api/v1/assistant/health/intake(?:/.*)?", route)
        d.route(method, "/mock/health-intake", route)
