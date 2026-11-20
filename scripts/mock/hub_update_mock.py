"""The hub's own update in the mock hub (server/internal/hub/update, docs/operations/hub-update.md), a domain of
scripts/mock/mock_domains.py that moved out of mock_hub.py (#11649): GET /api/v1/hub/update and …/history, PUT
…/update/settings and POST …/update, and GET /mock/hub_update?mode=, which HubNoticeUITests uses to start a mode afresh.
"""
import json
import os

HUB = None  # mock_hub.py's globals (mock_domains.Hub), from register


def ago(minutes=0, hours=0, days=0):
    """mock_hub.py's ago (its clock, NOW)."""
    return HUB.ago(minutes=minutes, hours=hours, days=days)


# MOCK_HUB_UPDATE=available|waiting|notice|held|docker|docker-pinned|beta: the hub's version and its updating itself
# (docs/operations/hub-update.md): a newer release to install (auto-update off), an update waiting for two busy agents, "Automatic updates enabled"
# told for the first time, a newer release auto-update holds back (its code does not contain the hub's commit, with
# the held line in the history), or a Docker hub with a newer image (on :latest, or on a pinned version tag). behind
# (#8108): up to date against a build-time source whose newest release is older than the hub (source_behind). Unset:
# GET /api/v1/hub/update is 404 (a hub from before), no line anywhere.
HUB_UPDATE_MODE = os.environ.get("MOCK_HUB_UPDATE", "")
HUB_UPDATE = {}


def hub_update_state():
    if not HUB_UPDATE:
        latest = {"version": "v0.3.1-4-gdef5678-202611030900", "build": 202611030900, "released": ago(minutes=40),
                  "notes": "会话列表改成增量推送，更省电；灵动岛偶尔不更新的问题修好了。", "newer": True, "bad": False}
        HUB_UPDATE.update({"version": "v0.3.0-12-gabc1234-202611021500", "build": 202611021500, "channel": "release",
                           "can_update": True, "latest": latest, "state": "available", "busy": [], "auto": False,
                           "auto_notice": False, "last_check": ago(minutes=20), "confirmed": True})
        if HUB_UPDATE_MODE == "beta":
            HUB_UPDATE.update(source="beta", version="0.6.2", latest={"version": "0.7.0-beta.1", "build": 202611171300,
                                                                     "newer": True, "bad": False})
        elif HUB_UPDATE_MODE == "waiting":
            HUB_UPDATE.update(state="waiting_idle", auto=True, busy=[
                {"machine_id": "macbook", "machine": "MacBook", "session_id": "a1f3c2d4-0000-4000-8000-000000000001", "title": "给天气 App 加主屏幕小组件"},
                {"machine_id": "server", "machine": "构建服务器", "session_id": "b2", "title": "整理发布脚本"}])
        elif HUB_UPDATE_MODE == "held":
            HUB_UPDATE.update(auto=True)
            HUB_UPDATE["latest"] = dict(latest, held=HELD_REASON)
        elif HUB_UPDATE_MODE == "notice":
            HUB_UPDATE.update(state="idle", auto=True, auto_notice=True)
            HUB_UPDATE["latest"] = dict(latest, version=HUB_UPDATE["version"], build=HUB_UPDATE["build"], newer=False)
        elif HUB_UPDATE_MODE == "behind":
            HUB_UPDATE.update(state="idle", auto=True, update_url="https://site.example.com/dl", update_url_from="build", source_behind=True)
            HUB_UPDATE["latest"] = {"version": "v0.1.17-177-g563f7fde5b-202611071005", "build": 202611071005,
                                    "released": "2026-11-07T10:05:00Z", "newer": False, "bad": False}
        elif HUB_UPDATE_MODE in ("docker", "docker-pinned"):
            pinned = HUB_UPDATE_MODE == "docker-pinned"
            HUB_UPDATE.update(version="0.3.0", build=0, channel="docker", can_update=False, auto=False,
                              why_not="Docker 里的 hub 由新镜像更新（docker compose pull && docker compose up -d）",
                              image={"name": "ghcr.io/owner/persona", "tag": "0.3.0" if pinned else "latest", "pinned": pinned})
            HUB_UPDATE["latest"] = {"version": "0.3.1", "build": 0, "newer": True, "bad": False}
    return HUB_UPDATE


HELD_REASON = "新版本（提交 def56789）不包含这个 hub 正在运行的提交 abc12345：发布用的代码比这里的旧，或在另一条分支上，自动装了会退回"


def hub_update_history():
    held = [{"from_version": "v0.3.0-12-gabc1234-202611021500", "to_version": "v0.3.1-4-gdef5678-202611030900", "to_build": 202611030900,
             "started": ago(minutes=20), "ended": ago(minutes=20), "result": "held", "reason": HELD_REASON, "auto": True}]
    return (held if HUB_UPDATE_MODE == "held" else []) + HUB_UPDATE_HISTORY  # newest first, like the hub


def set_hub_update_mode(mode):
    global HUB_UPDATE_MODE
    HUB_UPDATE_MODE = mode
    HUB_UPDATE.clear()


HUB_UPDATE_HISTORY = []  # register makes it, on the hub's clock


def hub_update_get(h, req):
    """GET /api/v1/hub/update (404 without MOCK_HUB_UPDATE: a hub from before) and GET …/history."""
    p = req.path
    if p == "/api/v1/hub/update":
        return h.send_json(hub_update_state()) if HUB_UPDATE_MODE else h.send_json({"error": "not found"}, 404)
    if p == "/api/v1/hub/update/history": return h.send_json({"history": hub_update_history()})
    return False


def hub_update_write(h, req):
    """POST / PUT / PATCH / DELETE on a path that starts /api/v1/hub/update, with MOCK_HUB_UPDATE: the state after
    PUT …/settings or POST …/update, the state as it is for any other. Without it the request goes on to mock_hub.py."""
    p = req.path
    if p.startswith("/api/v1/hub/update") and HUB_UPDATE_MODE:
        s, body = hub_update_state(), json.loads(h.last_body or b"{}")
        if p == "/api/v1/hub/update/settings" and h.command == "PUT":
            if "source" in body:
                s["source"] = body["source"]
                s["latest"] = None
            if "auto" in body: s.update(auto=bool(body["auto"]), auto_notice=False)
            if body.get("auto_notice_seen"): s["auto_notice"] = False
        elif p == "/api/v1/hub/update" and h.command == "POST":
            if not s["can_update"]: return h.send_json({"error": "这个 hub 不能自己更新：" + s.get("why_not", "")}, 409)
            s.update(state="restarting" if body.get("now") else "waiting_idle")
        return h.send_json(s)
    return False


def mode_control(h, req):
    """GET /mock/hub_update?mode=<a MOCK_HUB_UPDATE mode>: that mode from the start (HubNoticeUITests)."""
    set_hub_update_mode((req.query.get("mode") or [""])[0])
    return h.send_json({"ok": True})


def register(d):
    global HUB, HUB_UPDATE_HISTORY
    HUB = d.hub
    HUB_UPDATE_HISTORY = [
        {"from_version": "v0.2.9-30-g1111111-202610311200", "to_version": "v0.3.0-12-gabc1234-202611021500", "to_build": 202611021500,
         "started": ago(hours=20), "ended": ago(hours=20), "result": "updated", "auto": True},
        {"from_version": "v0.2.9-30-g1111111-202610311200", "to_version": "v0.3.0-9-g2222222-202611011800", "to_build": 202611011800,
         "started": ago(hours=40), "ended": ago(hours=40), "result": "rolled_back", "reason": "这些机器 2 分钟内没连回来：构建服务器", "auto": True},
    ]
    d.route("GET", r"/api/v1/hub/update(/history)?", hub_update_get)
    d.route(["POST", "PUT", "PATCH", "DELETE"], r"/api/v1/hub/update.*", hub_update_write)
    d.route("GET", "/mock/hub_update", mode_control)
