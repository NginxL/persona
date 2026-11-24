"""Opt-in outside reporting directions (#8236), without changing the default mock screens.

MOCK_OUTSIDE_DIRECTIONS=1 serves one conversation-backed direction, one bound outside direction and one targetless
outside direction. The outside ids never become Persona conversations. The bound Agent is navigation metadata only:
its existing to-do stays with the secretary, and its own idle state does not drive the direction's running mark.
"""
import copy
import os

MODE = os.environ.get("MOCK_OUTSIDE_DIRECTIONS") == "1"
LEGACY = os.environ.get("MOCK_LEGACY_HUB") == "1"

CONVERSATION_NAME = "照片同步"  # i18n-ignore: synthetic hub data
BOUND = "dir_mock_bound_8236"
BOUND_NAME = "终端发布与周末迁移的长期方向"  # i18n-ignore: synthetic hub data
TARGETLESS = "dir_mock_targetless_8236"
TARGETLESS_NAME = "只做进度汇报的长期调研方向"  # i18n-ignore: synthetic hub data
RUNNING_SESSION = "outside-running-0001"


def _decorate(row):
    if row.get("id") not in (BOUND, TARGETLESS):
        return
    row["external"] = True
    if "title" in row:
        row["title"] = row.get("name", "")
    if row["id"] == BOUND:
        row["machine_id"] = "server"
        row["session_id"] = "a1f3c2d4-0000-4000-8000-000000000004"


def _leaders(d, h):
    body = d.hub.mock_leaders_list()
    for row in body["leaders"]:
        _decorate(row)
    return h.send_json(body)


def _tree(d, h):
    body = d.hub.mock_direction_tree()
    for row in body["directions"]:
        _decorate(row)
    return h.send_json(body)


def _install(d):
    leaders = [d.hub.ASK2, BOUND, TARGETLESS]
    d.hub.MOCK_LEADERS[:] = leaders
    d.hub.MOCK_DIRECTION_NAMES.clear()
    d.hub.MOCK_DIRECTION_NAMES.update({
        d.hub.ASK2: CONVERSATION_NAME,
        BOUND: BOUND_NAME,
        TARGETLESS: TARGETLESS_NAME,
    })
    for todo in d.hub.TODOS_INIT + d.hub.TODOS:
        if todo["id"] == "todo_photos":
            todo["supervision"] = d.hub._lead_supervision(d.hub.ASK2)
    for session in d.hub.SESSIONS:
        if session["session_id"] == d.hub.ORDERS:
            # The direction runs because of its own tree task, not because of this navigation target.
            session.update(status="idle", state="idle", started_by_persona=False)
    if not any(session["session_id"] == RUNNING_SESSION for session in d.hub.SESSIONS):
        d.hub.SESSIONS.append(d.hub.sess(
            "macbook", RUNNING_SESSION, "核对发布构建", "/Users/me/code/weather", "working", "working",
            "weather", 1, "outside", 1, 4, "正在核对发布构建。",  # i18n-ignore: synthetic hub data
        ))
    for session in d.hub.SESSIONS:
        if session["session_id"] == RUNNING_SESSION:
            # ORIG_STATE predates this domain's synthetic session, so the base reset cannot restore it.
            session.update(status="working", state="working")


def _outside_todos(d):
    def todo(tid, title, minutes, agents=(), state=""):
        return {
            "id": tid,
            "title": title,  # i18n-ignore: synthetic hub data
            "notes": "",
            "done_at": None,
            "created_at": d.hub.ago(hours=2),
            "updated_at": d.hub.ago(minutes=minutes),
            "spaces": [],
            "agents": list(agents),
            "supervision": d.hub._lead_supervision(BOUND, state),
        }

    working_agent = {
        "machine_id": "macbook",
        "session_id": RUNNING_SESSION,
        "linked_at": d.hub.ago(minutes=20),
        "released_at": None,
        "title": "核对发布构建",  # i18n-ignore: synthetic hub data
        "cwd": "/Users/me/code/weather",
        "agent": "claude",
        "start_error": "",
        "source": "manual",
        "via": "",
    }
    return [
        todo("outside_wait_release", "确认发布窗口", 8, state="needs_user"),
        todo("outside_wait_migration", "选择迁移回滚点", 6, state="needs_user"),
        todo("outside_work", "核对发布构建", 4, agents=[working_agent]),
    ]


def register(d):
    d.help("MOCK_OUTSIDE_DIRECTIONS=1  one conversation, a bound outside direction and a targetless one (#8236)")
    if not MODE or LEGACY:
        return

    _install(d)
    todos = _outside_todos(d)
    d.hub.TODOS_INIT.extend(copy.deepcopy(todos))
    d.hub.TODOS.extend(copy.deepcopy(todos))
    d.on_reset(lambda: _install(d))
    d.route("GET", "/api/v1/assistant/leaders", lambda h, req: _leaders(d, h))
    d.route("GET", "/api/v1/assistant/directions/tree", lambda h, req: _tree(d, h))
