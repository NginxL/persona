"""Filing sessions under a direction and reading one direction (#11391, #11516): the hub's `direction_sessions`.

MOCK_DIRECTION_SESSIONS=1 (with MOCK_LEADERS=1) lists the feature, answers
`PUT /api/v1/assistant/sessions/{mid}/{sid}/direction` and `GET /api/v1/assistant/directions/{id}`, and adds the live
filed sessions to the tree (`sessions`), as docs/protocol/protocol.md describes them. The first leader ("Persona 产品")
starts with a session of every standing (docs/assistant/unified-entry.md "Where a session stands"): two chats and an
Agent filed by the owner, an Agent its leader opened, one stopped on a connected computer (`offline`), and one each
`computer_offline`, `list_unknown`, `not_listed` and `computer_removed`. Two Agents join SESSIONS for it.
MOCK_DIRECTION_SESSIONS=missing lists the feature but answers the routes 404 without their codes, as a manager that
reconnected at an older version does. Unset: a hub without the feature (the routes are not there).

It answers as the hub does (`server/internal/assistant/direction_sessions*.go`): placed sessions by computer, then session
id (the app orders them itself); a refusal with the hub's own sentence in the request's language (its locale files);
a null write decided before any check; Undo putting back exactly the entry the session's last write replaced, title
and filing time included, without its row; On Its Own without the row for a session already filed, every other write
needing its row; the background supervisor refused; a listed session filed with its row's title. A write that
changes the book pushes `directions`, as the hub publishes on a change.
With MOCK_DIRECTION_SESSIONS on, GET /api/v1/assistant gives the second manager (工作) a conversation of its own. The
app does not show it on 工作's page (before #11516 and after), so DirectionScreensUITests' "manager" off case compares
that page's Main Chat row instead. The case runs on a second mock started with MOCK_SESSIONS_LIST=1 as well
(PERSONA_OFF_MANAGER_HUB): the paged list brings nothing to that page, but it is the setup the case's main-vs-branch
pairs were taken with.

GET /mock/direction_sessions?… changes how it answers from now on (each optional; GET /mock/reset puts all back):
  routes=missing|on       the routes answer 404 without their codes / as above
  feature=off|on          leave `direction_sessions` out of the hello and /api/v1/health / list it
  refuse=<code>           the next PUT is refused with that code (full_access_required, forbidden, session_not_found,
                          direction_full, direction_supervised_task, direction_unavailable, …), nothing changed
  read=fail|gone|ok       the single read answers 503 direction_unavailable / 404 direction_not_found / as it is
  delay=<seconds>         the single read waits that long first (the page draws from the tree meanwhile)
  put_delay=<seconds>     each write waits that long before it answers (Moving… on the rows meanwhile)
  sessions_unavailable=1  the single read says the placed sessions could not be read
  ended=<id>              that direction ends (unpinned), as POST /api/v1/assistant/directions/{id}/end from the app
                          does: out of the leaders and the tree, `directions` pushed, its single read with `ended_at`,
                          still known for writes (409 direction_ended); …/make or /mock/reset pins it again
The answer is the book as it stands: {"book", "options"}. A lost answer is POST /mock/fault {"fault":"lost_receipt"}.
"""
import json
import os
import threading
import time
from datetime import datetime, timedelta, timezone

MODE = os.environ.get("MOCK_DIRECTION_SESSIONS", "")
FEATURE = "direction_sessions"
MAX_PER_DIRECTION = 50

# The hub's own refusal sentences, in its locale files (server/internal/i18n/locales/<lang>/assistant-direction_sessions.json),
# by the refusal's code; a code the file does not word (the hub's own 403 forbidden) keeps a line of its own.
LOCALES = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "server", "internal", "i18n", "locales")
LOCALE_KEY = {"direction_supervised_task": "supervised_task", "direction_main_conversation": "main_conversation",
              "direction_background_supervisor": "background_supervisor",
              "direction_leader_conversation": "leader_conversation", "direction_space_mismatch": "space_mismatch",
              "direction_unavailable": "unavailable"}
FALLBACK = {"forbidden": "this device can only read and answer"}  # the hub's own (server/internal/hub/http.go)
STATUS = {"full_access_required": 403, "forbidden": 403, "session_not_found": 404, "machine_not_found": 404,
          "direction_not_found": 404, "direction_unavailable": 503, "bad_request": 400}


def _sentences(lang):
    try:
        with open(os.path.join(LOCALES, lang, "assistant-direction_sessions.json"), encoding="utf-8") as f:
            return json.load(f)
    except (OSError, ValueError):
        return {}


SENTENCES = {lang: _sentences(lang) for lang in ("en", "zh-Hans", "ja")}


def sentence(code, lang):
    words = SENTENCES.get(lang, {}).get(LOCALE_KEY.get(code, code)) or SENTENCES["en"].get(LOCALE_KEY.get(code, code))
    return (words or FALLBACK.get(code) or code).replace("{max}", str(MAX_PER_DIRECTION))


def _iso(t):
    return t.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _now():
    return datetime.now(timezone.utc)


def register(d):
    d.help("MOCK_DIRECTION_SESSIONS=1|missing  filing sessions under a direction and the single read (#11516; "
           "with MOCK_LEADERS=1); missing: listed, but the routes answer 404 without their codes; "
           "GET /mock/direction_sessions?routes=&feature=&refuse=&read=&delay=&put_delay=&sessions_unavailable=&ended=")
    if MODE not in ("1", "missing"):
        # A hub without the feature has neither route (the mock's catch-all would answer the write 200).
        def absent(h, req):
            if req.match.groupdict().get("id") in ("tree", "offers"):
                return False
            return h.send_json({"error": "not found"}, 404)
        d.route("PUT", r"/api/v1/assistant/sessions/(?P<mid>[^/]+)/(?P<sid>[^/]+)/direction", absent)
        d.route("GET", r"/api/v1/assistant/directions/(?P<id>[^/]+)", absent)
        return
    hub = d.hub
    lock = threading.Lock()
    leaders = list(hub.MOCK_LEADERS)
    first = leaders[0] if leaders else ""
    computer = "macbook"
    # Two Agents of the first direction's: one the owner filed, at work; one its leader opened, waiting on you.
    extra = [  # i18n-ignore: mock data
        hub.sess(computer, "dirs0001-0000-4000-8000-000000000001", "整理首页改版的设计稿", "/Users/me/code/site", "working",
                 "working", "site", 4, "1", 1, 2, "正在对比三个方案的间距。"),
        hub.sess("studio", "dirs0002-0000-4000-8000-000000000002", "写新版引导页的文案", "/Users/me/code/site", "blocked",
                 "needs_you", "site", 4, "2", 2, 6, "两个标题你更喜欢哪个？", pending=1),
    ]
    hub.SESSIONS.extend(extra)
    chats = [c["session_id"] for c in hub.ASSISTANT
             if c["session_id"] not in leaders and c["session_id"] != hub.ASSISTANT_CONFIG.get("main")
             and c.get("space") != "health"]
    ago = lambda **kw: _iso(_now() - timedelta(**kw))  # noqa: E731

    def initial_book():
        if not first:
            return {}
        book = {
            f"{computer}/{extra[0]['session_id']}": {"direction": first, "title": extra[0]["title"], "at": ago(hours=2)},
            f"{computer}/a1f3c2d4-0000-4000-8000-000000000007": {"direction": first, "title": "菜谱网站加搜索",  # i18n-ignore: mock data
                                                                 "at": ago(days=1)},
            "nas/dirs0005-0000-4000-8000-000000000005": {"direction": first, "title": "备份照片到 NAS", "at": ago(days=2)},  # i18n-ignore: mock data
            # A long title, as real ones often are: the page's row cuts it on its line, its word beside it.
            "server/dirs0006-0000-4000-8000-000000000006": {"direction": first,  # i18n-ignore: mock data
                                                            "title": "看一下服务器的磁盘为什么一周就满了，顺便清理旧日志和构建缓存",  # i18n-ignore: mock data
                                                            "at": ago(hours=5), "standing": "list_unknown"},
            f"{computer}/dirs0007-0000-4000-8000-000000000007": {"direction": first, "title": "旧版落地页的截图",  # i18n-ignore: mock data
                                                                 "at": ago(days=3), "standing": "not_listed"},
            "old-laptop/dirs0008-0000-4000-8000-000000000008": {"direction": first, "title": "旧电脑上的草稿",  # i18n-ignore: mock data
                                                                "at": ago(days=12), "standing": "computer_removed"},
        }
        for i, sid in enumerate(chats[0:3:2]):  # two everyday chats the owner put here (帮我约周五 stays in none)
            title = next((c.get("title", "") for c in hub.ASSISTANT if c["session_id"] == sid), "")
            book[f"{hub.ASSISTANT_CONFIG.get('machine_id') or 'server'}/{sid}"] = {"direction": first, "title": title,
                                                                                  "at": ago(hours=20 + i)}
        return book

    def initial_origins():
        # what the first leader opened: the session the owner did not file
        return {f"studio/{extra[1]['session_id']}": {"leader": first, "title": extra[1]["title"], "at": ago(minutes=30)}} if first else {}

    # replaced: per session, the entry its last write replaced or took out (None: it had none), for that write's Undo.
    state = {"book": initial_book(), "origins": initial_origins(), "replaced": {}, "options": {}}
    original_leaders = list(hub.MOCK_LEADERS)

    def options():
        return state["options"]

    def missing():
        return MODE == "missing" or options().get("routes") == "missing"

    d.feature(FEATURE, when=lambda: options().get("feature") != "off")

    def lang(h):
        al = (h.headers.get("Accept-Language") or "").lower()
        return "zh-Hans" if al.startswith("zh") else "ja" if al.startswith("ja") else "en"

    def refuse(h, code, status=None):
        return h.send_json({"error": sentence(code, lang(h)), "code": code}, status or STATUS.get(code, 409))

    def leaders_now():
        return list(hub.MOCK_LEADERS)

    def session_row(machine, sid):
        for s in hub.SESSIONS:
            if s["machine_id"] == machine and s["session_id"] == sid:
                return s
        for c in hub.ASSISTANT:
            if c["session_id"] == sid and (c.get("machine_id") or hub.ASSISTANT_CONFIG.get("machine_id")) == machine:
                return c
        return None

    def online(machine):
        m = next((x for x in hub.MACHINES if (x.get("id") or x.get("machine_id")) == machine), None)
        return None if m is None else bool(m.get("online"))

    def standing(key, entry):
        machine, sid = key.split("/", 1)
        if entry.get("standing"):
            return entry["standing"]
        up = online(machine)
        if up is None:
            return "computer_removed"
        if not up:
            return "computer_offline"
        row = session_row(machine, sid)
        return (row or {}).get("state") or ("not_listed" if row is None else "idle")

    def followers():
        out = set()
        for t in hub.TODOS:
            if t.get("done_at") or not (t.get("supervision") or {}).get("supervisor"):
                continue
            for a in t.get("agents", []):
                if not a.get("released_at"):
                    out.add(f"{a['machine_id']}/{a['session_id']}")
        return out

    def placements():
        """Each placed session's direction, source, title and time: the owner's live filings first, then what a leader
        opened, unless the owner set that one on its own."""
        out = {}
        follow = followers()
        for key, e in state["book"].items():
            if e.get("direction") and key not in follow:
                out[key] = dict(e, source="owner")
        for key, o in state["origins"].items():
            if key in state["book"] or key in follow:
                continue
            out[key] = {"direction": o["leader"], "title": o["title"], "at": o["at"], "source": "leader"}
        return out

    def placed(direction):
        rows = []
        for key, e in placements().items():
            if e["direction"] != direction or key.split("/", 1)[1] == direction:
                continue
            machine, sid = key.split("/", 1)
            rows.append({"machine_id": machine, "session_id": sid, "title": e.get("title", ""), "source": e["source"],
                         "at": e.get("at", ""), "state": standing(key, e)})
        rows.sort(key=lambda r: (r["machine_id"], r["session_id"])) # the hub's order (direction_sessions_read.go)
        return rows

    def tree(h, req):
        if os.environ.get("MOCK_LEGACY_HUB") or hub.assistant_for_manager(h) is not None:
            return False  # the hub's own answer (another manager's page has no tree)
        body = hub.mock_direction_tree()
        if missing() or options().get("feature") == "off":
            return h.send_json(body)
        in_tasks = {f"{a['machine_id']}/{a['session_id']}" for dd in body["directions"] for t in dd["tasks"]
                    for a in _walk(t["agents"])}
        with lock:
            for dd in body["directions"]:
                if dd.get("secretary"):
                    continue
                live = [r for r in placed(dd["id"]) if r["state"] in ("working", "needs_you")
                        and f"{r['machine_id']}/{r['session_id']}" not in in_tasks]
                if live:
                    dd["sessions"] = live
        return h.send_json(body)

    def read(h, req):
        did = req.match["id"]
        if did in ("tree", "offers"):
            return False
        if hub.assistant_for_manager(h) is not None:
            return False
        if missing():
            return h.send_json({"error": "not found"}, 404)
        opts = options()
        if opts.get("delay"):
            time.sleep(float(opts["delay"]))
        if opts.get("read") == "fail":
            return refuse(h, "direction_unavailable", 503)
        if opts.get("read") == "gone" or (did not in leaders_now() and did not in hub.ENDED_LEADERS):
            return refuse(h, "direction_not_found", 404)
        with lock:
            rows = [] if opts.get("sessions_unavailable") else placed(did)
        out = {"id": did, "name": hub.MOCK_DIRECTION_NAMES.get(did, ""), "notes": hub.MOCK_DIRECTION_NOTES.get(did, ""),
               "sessions": rows, "ended_tasks": [], "more": False}
        if opts.get("sessions_unavailable"):
            out["sessions_unavailable"] = True
        if did in hub.ENDED_LEADERS:
            out["ended_at"] = ago(minutes=1)
        return h.send_json(out)

    def file(h, req):
        """As the hub decides a write (server/internal/assistant/direction_sessions.go decideDirectionFiling)."""
        if hub.assistant_for_manager(h) is not None:
            return False
        if missing():
            return h.send_json({"error": "not found"}, 404)
        if hub.is_read_only(h):
            return refuse(h, "forbidden", 403)
        code = options().pop("refuse", None)
        if code:
            return refuse(h, code)
        try:
            body = json.loads(req.body or b"{}")
        except ValueError:
            return refuse(h, "bad_request", 400)
        target = body["direction"] if "direction" in body else ""  # no key: on its own
        if target is not None and not isinstance(target, str):
            return refuse(h, "bad_request", 400)
        if options().get("put_delay"):
            time.sleep(float(options()["put_delay"]))
        machine, sid = req.match["mid"], req.match["sid"]
        key = f"{machine}/{sid}"
        with lock:
            book, replaced, ended = state["book"], state["replaced"], hub.ENDED_LEADERS
            before = book.get(key)
            previous = before.get("direction") if before else None
            if target is None:
                # Taken out of the book, decided before any check: it asks nothing of the session. The entry stays,
                # for its Undo.
                if before is not None:  # a second null write keeps what an Undo of the first puts back
                    replaced[key] = before
                    del book[key]
                return publish(h, {"direction": None, "previous": previous, "changed": before is not None})
            last = replaced.get(key, False)  # False: no write of this session to undo
            undo = bool(last) and last.get("direction") == target
            if not undo and (before is None or target != ""):
                # Its row, unless it is an Undo or a filed session set on its own.
                if online(machine) is None:
                    return refuse(h, "machine_not_found", 404)
                if session_row(machine, sid) is None:
                    return refuse(h, "session_not_found", 404)
            # These hold for every write, an Undo's too.
            if sid == hub.ASSISTANT_CONFIG.get("main"):
                return refuse(h, "direction_main_conversation")
            if hub.BACKGROUND_SUPERVISOR and sid == hub.BACKGROUND_SUPERVISOR:
                return refuse(h, "direction_background_supervisor")
            if sid in leaders_now() or sid in ended:
                return refuse(h, "direction_leader_conversation")
            if key in followers():
                return refuse(h, "direction_supervised_task")
            if target:
                if target not in leaders_now() and target not in ended:
                    return refuse(h, "direction_not_found", 404)
                if target in ended and not undo:
                    return refuse(h, "direction_ended")
            if undo:
                entry = dict(last)  # exactly the entry the last write replaced: direction, title, filing time
            elif previous == target and before is not None:
                entry = before  # the same place again
            else:
                row = session_row(machine, sid) or {}
                # A listed session's title is its row's; one not listed (set on its own) keeps the one it was filed with.
                entry = {"direction": target, "title": row.get("title") or (before or {}).get("title", ""),
                         "at": _iso(_now())}
                if before and before.get("standing"):
                    entry["standing"] = before["standing"]
            changed = entry is not before and entry != before
            if changed and target and not undo and \
                    sum(1 for e in book.values() if e.get("direction") == target) >= MAX_PER_DIRECTION:
                return refuse(h, "direction_full")
            if changed:
                replaced[key] = before
                book[key] = entry
        return publish(h, {"direction": target, "previous": previous, "changed": changed})

    def publish(h, answer):
        """The answer; `directions` pushed only when the book changed, as the hub publishes."""
        if answer["changed"]:
            hub.broadcast(hub.from_default({"type": "directions"}))
        return h.send_json(answer)

    def control(h, req):
        opts = options()
        for name in ("routes", "feature", "refuse", "read", "delay", "put_delay", "sessions_unavailable"):
            if name in req.query:
                value = req.query[name][0]
                if value in ("", "on", "ok", "0"):
                    opts.pop(name, None)
                else:
                    opts[name] = value
        if "ended" in req.query and req.query["ended"][0] in hub.MOCK_LEADERS:
            # Unpinned: out of the leaders (the tree, /api/v1/assistant/leaders), as the hub's end does; still
            # known for writes (409 direction_ended) and its chat still a leader conversation for filing.
            ended = req.query["ended"][0]
            hub.MOCK_LEADERS.remove(ended)
            hub.ENDED_LEADERS.add(ended)
            hub.broadcast(hub.from_default({"type": "directions"}))
        return h.send_json({"book": state["book"], "options": opts})

    def reset():
        state.update(book=initial_book(), origins=initial_origins(), replaced={}, options={})
        hub.MOCK_LEADERS[:] = original_leaders
        hub.ENDED_LEADERS.clear()

    def other_assistant(h, req):
        """The second manager (工作) with a conversation of its own (the app does not show it on 工作's page)."""
        other = hub.assistant_for_manager(h)
        if other is None or other[1] != 200 or req.query.get("space") or (h.headers.get("X-Persona-Manager") or "") != "m-3fa21c":
            return False
        body, status = other
        chat = dict(hub.assistant_sess("work0001-0000-4000-8000-000000000001", "整理出差报销单", 90,  # i18n-ignore: mock data
                                       "发票都齐了，明天交。"), origin="user", manager="m-3fa21c")  # i18n-ignore: mock data
        return h.send_json(dict(body, conversations=[chat]), status)

    d.route("GET", r"/api/v1/assistant/directions/tree", tree)
    d.route("GET", r"/api/v1/assistant/directions/(?P<id>[^/]+)", read)
    d.route("PUT", r"/api/v1/assistant/sessions/(?P<mid>[^/]+)/(?P<sid>[^/]+)/direction", file)
    d.route("GET", r"/mock/direction_sessions", control)
    d.route("GET", r"/api/v1/assistant", other_assistant)
    d.on_reset(reset)


def _walk(agents):
    for a in agents:
        yield a
        yield from _walk(a.get("children") or [])

