"""Domain spaces for the web's review screenshots, a domain of scripts/mock/mock_domains.py (#13500).

MOCK_SPACES=1: the assistant answers the track API (docs/web/web.md "Domain spaces"): the built-in health space and two
made-up spaces, qa-plain (no conversations, no categories) and qa-finance (two conversations, the finance category),
the kinds space and orchestration, each space's SPACE.md with a managed state block, a conversation's categories and
what each extension may read. Writes change the mock's copy only; GET /mock/reset puts the fixtures back.
"""
import copy
import hashlib
import os

HELP = ("MOCK_SPACES=1  the track API (#13500): the spaces qa-plain and qa-finance with SPACE.md, the kinds space and "
        "orchestration, conversation categories and extension trust; /mock/reset puts them back")
MODE = os.environ.get("MOCK_SPACES") == "1"
BASE = "/api/v1/assistant"


def kind_config(group, order, icon, categories=()):
    return {"schema": 1, "template": "space", "context": "", "default_categories": list(categories), "tag_focus": [],
            "rhythms": [], "attention": {"push_cap": "", "deep_default": "", "enter_mode": ""}, "supervision": "",
            "usage": [], "assembly": "", "capture_target": False, "channels": [],
            "sidebar": {"group": group, "order": order, "icon": icon}}


KINDS = [
    {"slug": "space", "title": "Space", "config": kind_config("spaces", 10, "layers"), "builtin": True, "revision": 1},
    {"slug": "orchestration", "title": "Main chat", "config": kind_config("", 0, ""), "builtin": True, "revision": 1},
]


def track(slug, title, icon, description, categories=(), builtin=False):
    return {"slug": slug, "kind": "space", "title": title, "icon": icon, "description": description,
            "default_categories": list(categories), "template": "health" if slug == "health" else "space",
            "config": kind_config("spaces", 10, "layers", categories), "builtin": builtin, "archived": False, "revision": 1}


TRACKS = [
    track("health", "Health", "leaf", "", ["health"], builtin=True),
    track("qa-plain", "Reading list", "book", "Books to read and notes on the ones finished"),
    track("qa-finance", "Household money", "wallet", "Bills, budgets and the yearly tax return", ["finance"]),
]

SPACE_MD = {
    "qa-plain": "# Reading list\n\n## What this space is for\n\nBooks I want to read, and short notes once I finish one.\n\n"
                "<!-- persona:managed:state:begin -->\n<!-- persona:managed:state:end -->\n",
    "qa-finance": "# Household money\n\n## What this space is for\n\nMonthly bills, the family budget and the yearly tax "
                  "return.\n\n## How to answer\n\nShow amounts in the local currency and round to whole units. Ask before "
                  "assuming an expense is shared.\n\n## File conventions\n\nReceipts go under receipts/<year>/.\n\n"
                  "<!-- persona:managed:state:begin -->\nLast budget check: November. Two bills are due this week.\n"
                  "<!-- persona:managed:state:end -->\n",
}

STATE = {"qa-plain": "", "qa-finance": "Last budget check: November. Two bills are due this week."}

TRUST = [{"extension": "example-notes", "trust": "third_party", "categories": ["finance"]}]


def digest(text):
    return hashlib.sha256(text.encode()).hexdigest()


def space_md(slug, files):
    return {"content": files[slug], "digest": digest(files[slug]), "state": STATE.get(slug, "")}


def register(d):
    d.help(HELP)
    if not MODE:
        return
    hub = d.hub
    convs = [
        dict(hub.assistant_sess("qafin001-0000-4000-8000-000000000001", "November budget", 35,
                                "Groceries are 40 over the plan; the rest is on track."),
             cwd="/home/me/.persona/tracks/qa-finance", space="qa-finance"),
        dict(hub.assistant_sess("qafin002-0000-4000-8000-000000000002", "Tax return checklist", 60 * 28,
                                "Three documents are still missing: the bank statement and two receipts."),
             cwd="/home/me/.persona/tracks/qa-finance", space="qa-finance"),
    ]
    hub.ASSISTANT.extend(copy.deepcopy(convs))
    st = {}

    def fresh():
        st.update(tracks=copy.deepcopy(TRACKS), files=dict(SPACE_MD), trust=copy.deepcopy(TRUST), cats={})
    fresh()

    def find(slug):
        return next((x for x in st["tracks"] if x["slug"] == slug), None)

    def body(req):
        try:
            return req.json()
        except ValueError:
            return None

    def tracks_list(h, req):
        h.send_json({"tracks": st["tracks"]})

    def tracks_create(h, req):
        b = body(req) or {}
        slug = b.get("slug", "")
        if not slug or find(slug) or not b.get("title"):
            return h.send_json({"error": "invalid track"}, 400)
        t = track(slug, b["title"], b.get("icon", ""), b.get("description", ""), b.get("default_categories") or [])
        t["kind"] = b.get("kind", "space")
        st["tracks"].append(t)
        st["files"][slug] = "# %s\n\n## What this space is for\n\n<!-- persona:managed:state:begin -->\n" \
                            "<!-- persona:managed:state:end -->\n" % b["title"]
        h.send_json(t)

    def track_change(h, req):
        t = find(req.match["slug"])
        b = body(req) or {}
        if not t:
            return h.send_json({"error": "not found"}, 404)
        if b.get("revision") != t["revision"]:
            return h.send_json({"error": "revision changed"}, 409)
        for k in ("title", "icon", "description", "default_categories", "archived"):
            if k in b:
                t[k] = b[k]
        t["revision"] += 1
        h.send_json(t)

    def md(h, req):
        slug = req.match["slug"]
        if slug not in st["files"]:
            return h.send_json({"error": "not found"}, 404)
        if req.method == "PUT":
            b = body(req) or {}
            if b.get("base_digest") != digest(st["files"][slug]):
                return h.send_json({"error": "SPACE.md changed"}, 409)
            st["files"][slug] = b.get("content", "")
        h.send_json(space_md(slug, st["files"]))

    def categories(h, req):
        sid = req.match["sid"]
        conv = next((c for c in hub.ASSISTANT if c["session_id"] == sid), None)
        t = find((conv or {}).get("space", "")) if conv else None
        floor = list((t or {}).get("default_categories") or [])
        cur = st["cats"].setdefault(sid, {"categories": list(floor), "floor": floor, "removed": []})
        if req.method == "PUT":
            b = body(req) or {}
            for c in b.get("add") or []:
                if c not in cur["categories"]:
                    cur["categories"].append(c)
                cur["removed"] = [x for x in cur["removed"] if x != c]
            for c in b.get("remove") or []:
                if c in cur["categories"] and c not in floor:
                    cur["categories"].remove(c)
                    cur["removed"].append(c)
        h.send_json(cur)

    def trust_list(h, req):
        h.send_json({"items": st["trust"]})

    def trust_set(h, req):
        ext = req.match["id"]
        b = body(req) or {}
        row = {"extension": ext, "trust": "owner" if b.get("trust") == "owner" else "third_party",
               "categories": list(b.get("categories") or [])}
        st["trust"] = [x for x in st["trust"] if x["extension"] != ext] + [row]
        h.send_json(row)

    d.route("GET", BASE + "/tracks", tracks_list)
    d.route("POST", BASE + "/tracks", tracks_create)
    d.route("PUT", BASE + r"/tracks/(?P<slug>[a-z][a-z0-9-]*)", track_change)
    d.route("GET", BASE + "/track-kinds", lambda h, req: h.send_json({"kinds": KINDS}))
    d.route(["GET", "PUT"], BASE + r"/tracks/(?P<slug>[a-z][a-z0-9-]*)/space-md", md)
    d.route(["GET", "PUT"], BASE + r"/conversations/(?P<sid>[^/]+)/categories", categories)
    d.route("GET", BASE + "/extension-trust", trust_list)
    d.route("PUT", BASE + r"/extension-trust/(?P<id>[^/]+)", trust_set)

    def reset():
        fresh()
        ids = {c["session_id"] for c in convs}
        hub.ASSISTANT[:] = [c for c in hub.ASSISTANT if c["session_id"] not in ids] + copy.deepcopy(convs)
    d.on_reset(reset)
