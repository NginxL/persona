"""Saved prompts in the mock hub (Settings › Saved Prompts, #7329; server/internal/assistant/saved/prompts.go), a domain
of scripts/mock/mock_domains.py that moved out of mock_hub.py (#11649): GET and POST /api/v1/assistant/saved-prompts, PUT
…/order, PUT and DELETE …/{id}, each change pushed to every socket as assistant_saved_prompts. web/e2e/saved-prompts.mjs
and SavedPromptsUITests drive it.
"""
import json
import os
import unicodedata
import uuid

import mock_domains

HELP = ("MOCK_SAVED_PROMPTS=1|old  Settings › Saved Prompts and every chat's / list: three saved prompts (1), or an "
        "assistant from before them, its routes answering 404 (old) (#7329)")
HUB = None  # mock_hub.py's globals (mock_domains.Hub), from register


def ago(minutes=0, hours=0, days=0):
    """mock_hub.py's ago (its clock, NOW)."""
    return HUB.ago(minutes=minutes, hours=hours, days=days)


# Saved prompts (Settings › Saved Prompts, #7329): the user's own "/" prompts, as the manager keeps them
# (assistant/saved/prompts.go). None by default, so the "/" list of every other screenshot stays as it was;
# MOCK_SAVED_PROMPTS=1 has three; MOCK_SAVED_PROMPTS=old is an assistant from before them (its routes answer 404).
SAVED_PROMPTS = []  # register makes them, on the hub's clock


def saved_prompt_name_ok(name):
    """One word of letters, digits, combining marks and - _ . (the manager's savedPromptNameRune), at most 32."""
    return 0 < len(name) <= 32 and all(c in "-_." or unicodedata.category(c)[0] == "L" or unicodedata.category(c) in ("Nd", "Mn") for c in name)


def push_saved_prompts():
    for send, _ in list(HUB.LIVE):
        try:
            send(HUB.from_default({"type": "assistant_saved_prompts"}))
        except Exception:
            pass


def save_saved_prompt(body, pid):
    """Adds a saved prompt (pid None) or changes prompt pid, as the manager does: (answer, status)."""
    name = str(body.get("name") or "").strip().removeprefix("/")
    text = str(body.get("text") or "").replace("\r\n", "\n").strip()
    if not saved_prompt_name_ok(name):
        return {"error": "A prompt needs a name of at most 32 letters, digits, “-”, “_” or “.”, with no spaces."}, 400
    if not text or len(text) > 8000:
        return {"error": "A prompt needs some text, at most 8000 characters."}, 400
    if any(x["name"].lower() == name.lower() and x["id"] != pid for x in SAVED_PROMPTS):
        return {"error": f"There is already a prompt called “/{name}”."}, 409
    now = ago()
    if pid:
        for x in SAVED_PROMPTS:
            if x["id"] == pid:
                x.update(name=name, text=text, updated_at=now)
                push_saved_prompts()
                return {"prompt": x}, 200
        return {"error": "That saved prompt is gone."}, 404
    if len(SAVED_PROMPTS) >= 100:
        return {"error": "You can save at most 100 prompts."}, 400
    prompt = {"id": f"sp{uuid.uuid4().hex[:8]}", "name": name, "text": text, "created_at": now, "updated_at": now}
    SAVED_PROMPTS.append(prompt)
    push_saved_prompts()
    return {"prompt": prompt}, 200


def saved_prompts_route(h, p, method):
    """The saved prompts' routes; False when p is not one of them."""
    base = "/api/v1/assistant/saved-prompts"
    if p != base and not p.startswith(base + "/"):
        return False
    if os.environ.get("MOCK_SAVED_PROMPTS") == "old":
        h.send_json({"error": "not found"}, 404)
    elif p == base and method == "GET":
        h.send_json({"prompts": SAVED_PROMPTS})
    elif p == base and method == "POST":
        h.send_json(*save_saved_prompt(json.loads(h.last_body or b"{}"), None))
    elif p == base + "/order" and method == "PUT":
        ids = json.loads(h.last_body or b"{}").get("ids") or []
        if sorted(ids) != sorted(x["id"] for x in SAVED_PROMPTS) or len(set(ids)) != len(ids):
            h.send_json({"error": "The saved prompts changed on another device. Reload the list and try again."}, 409)
        else:
            SAVED_PROMPTS[:] = [next(x for x in SAVED_PROMPTS if x["id"] == i) for i in ids]
            push_saved_prompts()
            h.send_json({"prompts": SAVED_PROMPTS})
    elif method == "PUT":
        h.send_json(*save_saved_prompt(json.loads(h.last_body or b"{}"), p.rsplit("/", 1)[1]))
    elif method == "DELETE":
        pid = p.rsplit("/", 1)[1]
        SAVED_PROMPTS[:] = [x for x in SAVED_PROMPTS if x["id"] != pid]
        push_saved_prompts()
        h.send_json({"ok": True})
    else:
        h.send_json({"error": "method not allowed"}, 405)
    return True


def route(h, req):
    """Every method under /api/v1/assistant/saved-prompts. mock_hub.py's GET chain asked saved_prompts_route after its check
    of the manager a request names (X-Persona-Manager or ?manager=, assistant_for_manager), which answers first for
    one the hub would not reach (unknown: 404, offline: 503); such a GET goes on to it still."""
    if req.method == "GET" and (other := HUB.assistant_for_manager(h)) is not None and other[1] != 200:
        return False
    return saved_prompts_route(h, req.path, req.method)


def register(d):
    global HUB, SAVED_PROMPTS
    HUB = d.hub
    d.help(HELP)
    SAVED_PROMPTS = [
        {"id": "sp1", "name": "review", "text": "Review this diff for security issues. List each finding with its file and line, and say how bad it is.",
         "created_at": ago(days=4), "updated_at": ago(days=4)},
        {"id": "sp2", "name": "release-notes", "text": "Write the release notes for the commits since the last tag: what changed for users first, then the fixes.",
         "created_at": ago(days=2), "updated_at": ago(days=1)},
        {"id": "sp3", "name": "standup", "text": "Write today's standup from my commits and merged PRs:\n- done\n- next\n- blocked",
         "created_at": ago(hours=5), "updated_at": ago(hours=5)},
    ] if os.environ.get("MOCK_SAVED_PROMPTS") == "1" else []
    d.route(mock_domains.METHODS, r"/api/v1/assistant/saved-prompts(/.*)?", route)
