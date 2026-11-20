"""Settings › Knowledge selection in the mock hub (server/internal/kb/selection.go), a domain of
scripts/mock/mock_domains.py that moved out of mock_hub.py (#11543): GET / PUT /api/v1/kb/selection, and
MOCK_KB_SELECTION's older and newer hubs. scripts/mock/test_kb_selection_mock.py drives it.
"""
import json
import os

import mock_domains

HELP = ("MOCK_KB_SELECTION=missing|future|fail  Settings › Knowledge selection: a hub from before it (404) / a document "
        "with a field clients don't know / a save the hub refuses (503)")
HUB = None  # mock_hub.py's globals (mock_domains.Hub), from register

# Settings › Knowledge selection (docs/assistant/knowledge.md "Selection"): the hub's defaults until a PUT replaces them.
KB_SELECTION_MESSAGES = ["mentions", "mine", "dm", "group", "bot", "official", "muted", "collapsed", "done"]
KB_SELECTION_DOCS = ["mentions", "comment_mentions", "authored", "viewed"]
KB_SELECTION = {"doc": None}


def kb_selection_defaults():
    messages = {n: {"on": n in ("mentions", "mine", "dm"), "days": None, "attachment_days": None} for n in KB_SELECTION_MESSAGES}
    messages["mentions"]["include_all"] = True
    messages["mine"]["context"] = 50
    return {"version": 1,
            "messages": {"order": [["mentions", "mine"]] + [[n] for n in KB_SELECTION_MESSAGES[2:]], "rules": messages},
            "docs": {"order": [["mentions", "comment_mentions"], ["authored"], ["viewed"]],
                     "rules": {n: {"on": True, "days": None} for n in KB_SELECTION_DOCS}},
            "items": [], "history": {}}


def kb_selection_problem(doc):
    """Why the hub would refuse the document (kb.Selection.Validate); None when it would take it."""
    def days(d):
        return d is None or (isinstance(d, int) and not isinstance(d, bool) and 1 <= d <= 36500)
    if not isinstance(doc, dict) or set(doc) - {"version", "messages", "docs", "items", "history"} or doc.get("version") != 1:
        return "version or fields"
    for part, names, fields in (("messages", KB_SELECTION_MESSAGES, {"on", "days", "attachment_days", "include_all", "context"}),
                                ("docs", KB_SELECTION_DOCS, {"on", "days"})):
        rules = doc.get(part) or {}
        order = rules.get("order") or []
        if sorted(n for level in order for n in level) != sorted(names) or any(not level for level in order):
            return part + ".order"
        if sorted(rules.get("rules") or {}) != sorted(names):
            return part + ".rules"
        for name, rule in rules["rules"].items():
            if set(rule) - fields or not isinstance(rule.get("on", False), bool):
                return "%s.%s" % (part, name)
            if not days(rule.get("days")) or not days(rule.get("attachment_days")):
                return "%s.%s days" % (part, name)
            if rule.get("include_all") is not None and name != "mentions":
                return "%s.%s include_all" % (part, name)
            context = rule.get("context")
            if context is not None and (name != "mine" or isinstance(context, bool) or
                                        not isinstance(context, int) or not 0 <= context <= 200):
                return "%s.%s context" % (part, name)
    seen = set()
    for item in doc.get("items") or []:
        key = (item.get("source"), item.get("kind"), item.get("id"))
        if set(item) - {"source", "kind", "id", "name", "read", "store", "days"} or not key[0] or not key[2] or key[1] not in ("chat", "doc"):
            return "items"
        if key in seen or (item.get("store") and not item.get("read")) or not days(item.get("days")):
            return "items"
        seen.add(key)
    for source, history in (doc.get("history") or {}).items():
        if not source or set(history) - {"back_days"} or not days(history.get("back_days")):
            return "history"
    return None


def kb_selection_route(h, method, p, raw):
    """GET / PUT /api/v1/kb/selection; True when it answered. MOCK_KB_SELECTION=missing: a hub from before it (404),
    future: a document with a field no client knows yet, fail: the save is refused."""
    if p != "/api/v1/kb/selection":
        return False
    mode = os.environ.get("MOCK_KB_SELECTION", "")
    if mode == "missing":
        return h.send_json({"code": "not_found", "error": "unknown kind"}, 404) or True
    if method == "PUT":
        if HUB.is_read_only(h):
            return h.send_json({"error": "read-only device"}, 403) or True
        if mode == "fail":
            return h.send_json({"error": "知识库暂时写不进去"}, 503) or True
        doc = json.loads(raw or b"{}")
        problem = kb_selection_problem(doc)
        if problem:
            return h.send_json({"code": "invalid", "error": "selection: " + problem}, 400) or True
        KB_SELECTION["doc"] = doc
    doc = KB_SELECTION["doc"] or kb_selection_defaults()
    return h.send_json(dict(doc, later={"added": True}) if mode == "future" else doc) or True


def register(d):
    global HUB
    HUB = d.hub
    d.help(HELP)
    # mock_hub.py's GET and POST chains (every method) asked it with the request's body (b"" for a GET).
    d.route(mock_domains.METHODS, "/api/v1/kb/selection", lambda h, req: kb_selection_route(h, req.method, req.path, req.body))
