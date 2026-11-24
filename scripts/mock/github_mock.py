"""GitHub's own routes in the mock hub (server/internal/modules/github), a domain of scripts/mock/mock_domains.py that
moved out of mock_hub.py (#11543): the sign-in (GET / PUT /api/v1/connectors/github/auth, POST …/login and …/logout) and
the thread page of MOCK_GITHUB_INBOX=1's pull request (GET …/thread and …/media, POST …/thread/reply), whose people's
pictures it also draws on the hub's shared avatars route (GET /api/v1/avatars/github/…, #10923).

mock_hub.py keeps the connector itself: GITHUB_CONNECTOR, listed in GET /api/v1/connectors and changed by PUT
…/github/settings and by its MOCK_GITHUB* variants as the hub loads, and MOCK_GITHUB_INBOX=1's inbox rows. The routes here
read and change that connector through the hub (HUB.GITHUB_CONNECTOR), as the code did inside mock_hub.py.
"""
from datetime import datetime, timedelta, timezone
import json
import os
from urllib.parse import parse_qs, urlparse

import mock_domains

HUB = None  # mock_hub.py's globals (mock_domains.Hub), from register

# MOCK_GITHUB=device: GitHub signed in neither way, its device-flow sign-in from the app (modules/github/login.go):
# straight to the code ABCD-1234 (the hub uses the GitHub CLI's client ID); it is entered after two asks (one second
# apart).
GITHUB_DEVICE = {"polls": 0}

# GET/PUT /api/v1/connectors/github/auth: where GitHub's token comes from (a computer's gh, or the device flow) and
# each computer's gh login. MOCK_GITHUB=scopes: MacBook's gh lacks repo and notifications.
GH_FULL = ["gist", "read:org", "repo", "workflow"]
GH_MISSING = [{"scope": "repo", "affects": ["私有仓库的 PR、issue、评论、代码搜索读不到", "私有仓库的 CI 失败和日志读不到", "评论和 review 发不了"]},
              {"scope": "notifications", "affects": ["收件箱收不到 GitHub 的通知"]}]
GH_NARROW = os.environ.get("MOCK_GITHUB") == "scopes"
GH_MACHINES = [
    {"id": "mbp", "name": "MacBook", "state": "signed_in", "account": "alice", "scopes": ["read:org"] if GH_NARROW else GH_FULL,
     **({"missing": GH_MISSING} if GH_NARROW else {})},
    {"id": "studio", "name": "Mac Studio", "state": "not_signed_in"},
    {"id": "server", "name": "服务器", "state": "offline"},
]
GITHUB_AUTH = {"source": "device" if os.environ.get("MOCK_GITHUB") == "device" else "machine",
               "machine": None if os.environ.get("MOCK_GITHUB") == "device" else "mbp"}


def github_auth_view():
    v = {"source": GITHUB_AUTH["source"], "machines": GH_MACHINES, "scopes_known": False}
    if GITHUB_AUTH["source"] == "machine":
        v["machine"] = GITHUB_AUTH["machine"]
        m = next((x for x in GH_MACHINES if x["id"] == GITHUB_AUTH["machine"]), {})
        if m.get("state") == "signed_in":
            v.update(scopes=m["scopes"], scopes_known=True)
            if m.get("missing"): v["missing"] = m["missing"]
    elif HUB.GITHUB_CONNECTOR.get("status") == "ok":
        v.update(scopes=["repo"], scopes_known=True)
    return v


def set_github_auth(body):
    GITHUB_CONNECTOR = HUB.GITHUB_CONNECTOR  # the hub's, which MOCK_GITHUB=device replaced as it loaded
    now = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
    for k in ("account", "via", "error", "last_sync"): GITHUB_CONNECTOR.pop(k, None)
    if body.get("source") == "device":
        GITHUB_AUTH.update(source="device", machine=None)
        GITHUB_DEVICE["polls"] = 0
        GITHUB_CONNECTOR.update(status="not_signed_in", error="还没登录 GitHub：在下面选一台电脑上的 gh 登录，或者用设备码登录")
        return True
    m = next((x for x in GH_MACHINES if x["id"] == body.get("machine")), None)
    if not m: return False
    GITHUB_AUTH.update(source="machine", machine=m["id"])
    if m["state"] == "signed_in":
        GITHUB_CONNECTOR.update(status="ok", account=m["account"], via="来自 %s 的 gh 登录" % m["name"], last_sync=now)
    else:
        GITHUB_CONNECTOR.update(status="not_signed_in", via="来自 %s 的 gh 登录" % m["name"],
                                error={"not_signed_in": "%s 上的 gh 没有登录 github.com" % m["name"], "offline": "GitHub 未授权（%s 离线）：它连上后会自动再读" % m["name"]}.get(m["state"], m["state"]))
    return True


# GitHub's thread page (modules/github/thread.go, MOCK_GITHUB_INBOX=1): the made-up pull request
# example-org/example-repo#12 as the hub reads it — header, description, comments, reviews, comments on a line, checks
# and events; a reply is added as a comment of mine. Other threads (a CI run) cannot be opened, as on the hub.
GH_AVATAR_COLORS = {"alice": (214, 120, 160), "bob": (90, 150, 220), "sam": (110, 180, 120), "ci-bot[bot]": (140, 140, 150)}
GH_SENT = []


def gh_thread_page():
    now = datetime.now(timezone.utc)
    def at(**kw):
        return (now - timedelta(**kw)).replace(microsecond=0).isoformat().replace("+00:00", "Z")
    def who(login):
        return {"id": login, "name": login, "avatar": "avatar:%s:4" % login}
    hunk = ("@@ -36,9 +36,12 @@ func upload(ctx context.Context, f File) error {\n"
            " \tfor attempt := 0; attempt < 3; attempt++ {\n"
            " \t\tif err = send(ctx, f); err == nil {\n"
            " \t\t\treturn nil\n"
            " \t\t}\n"
            "-\t\ttime.Sleep(time.Second)\n"
            "+\t\ttime.Sleep(backoff(attempt))")
    log = "\n".join(["=== RUN   TestUploadRetries", "    retry_test.go:58: attempt 1 failed: connection reset",
                      "    retry_test.go:58: attempt 2 failed: connection reset", "    retry_test.go:71: waited 7s, want at most 6s",
                      "--- FAIL: TestUploadRetries (7.01s)", "FAIL", "FAIL\texample.com/uploader\t7.214s", "Error: Process completed with exit code 1."])
    msgs = [
        {"id": "e1", "at": at(days=2, hours=3), "sender": who("alice"), "event": True, "text": "alice 加了标签 enhancement"},
        {"id": "e2", "at": at(days=2, hours=3, minutes=-1), "sender": who("alice"), "event": True, "text": "alice 请 sam review"},
        {"id": "c1", "at": at(days=2, hours=2), "sender": who("ci-bot[bot]"), "format": "markdown",
         "text": "预览已部署：https://example.com/preview/12", "url": "https://github.com/example-org/example-repo/pull/12#issuecomment-1"},
        {"id": "r1", "at": at(days=1, hours=6), "sender": who("bob"), "kind": "review", "review": "changes_requested", "format": "markdown",
         "text": "退避要有**上限**，不然网络一直不好时会越等越久。", "url": "https://github.com/example-org/example-repo/pull/12#pullrequestreview-1"},
        {"id": "rc1", "at": at(days=1, hours=6, minutes=-1), "sender": who("bob"), "kind": "code", "format": "markdown",
         "code": {"path": "uploader/retry.go", "line": 42, "hunk": hunk},
         "text": "这里最好加一点抖动（jitter），免得一批上传同时重试。", "url": "https://github.com/example-org/example-repo/pull/12#discussion_r1"},
        {"id": "rc2", "at": at(days=1, hours=2), "sender": who("alice"), "kind": "code", "format": "markdown",
         "code": {"path": "uploader/retry.go", "line": 42}, "reply_to": {"id": "rc1", "from": "bob", "text": "这里最好加一点抖动（jitter），免得一批上传同时重试。"},
         "text": "好，加上了 `rand.Int63n`，上限 8 秒。", "url": "https://github.com/example-org/example-repo/pull/12#discussion_r2"},
        {"id": "e3", "at": at(days=1, hours=1), "sender": who("alice"), "event": True, "text": "alice 强制推送了分支"},
        {"id": "ck1", "at": at(hours=20), "sender": {"name": "github-actions"}, "kind": "check",
         "check": {"name": "lint", "state": "success", "url": "https://github.com/example-org/example-repo/actions/runs/1/job/1"}},
        {"id": "ck2", "at": at(hours=19, minutes=50), "sender": {"name": "github-actions"}, "kind": "check",
         "check": {"name": "test", "state": "failure", "url": "https://github.com/example-org/example-repo/actions/runs/1/job/2", "log": log}},
        {"id": "ck3", "at": at(hours=19, minutes=45), "sender": {"name": "github-actions"}, "kind": "check",
         "check": {"name": "build", "state": "success", "url": "https://github.com/example-org/example-repo/actions/runs/1/job/3"}},
        {"id": "r2", "at": at(hours=3), "sender": who("bob"), "kind": "review", "review": "approved", "format": "markdown",
         "text": "上限和抖动都好了，LGTM。", "url": "https://github.com/example-org/example-repo/pull/12#pullrequestreview-2"},
        {"id": "c2", "at": at(minutes=25), "sender": who("alice"), "format": "markdown", "text": "请看一下重试的退避时间",
         "url": "https://github.com/example-org/example-repo/pull/12#issuecomment-2"},
    ] + GH_SENT
    chat = {"id": "example-org/example-repo#12", "name": "Add retries to the example uploader", "kind": "pr",
            "url": "https://github.com/example-org/example-repo/pull/12", "reply": "github_comment", "state": "open",
            "author": who("alice"), "labels": [{"name": "enhancement", "color": "a2eeef"}, {"name": "uploader", "color": "d4c5f9"},
                                               {"name": "needs review", "color": "fbca04"}],
            "branch": {"head": "feat/upload-retries", "base": "main"}, "body_format": "markdown",
            "body": "上传失败时按指数退避重试三次。\n\n## 改了什么\n- 新增 `uploader/retry.go`：等 1、2、4 秒再试\n"
                    "- 上传失败的错误里带上试了几次\n\n## 怎么测\n`go test ./uploader/...`"}
    return chat, msgs


def github_thread_area(h, method, p, raw):
    """GitHub's thread page (MOCK_GITHUB_INBOX=1); True when it answered."""
    if not os.environ.get("MOCK_GITHUB_INBOX") or not p.startswith("/api/v1/connectors/github/"):
        return False
    q = parse_qs(urlparse(h.path).query)
    what = p.rsplit("/", 1)[1]
    if what == "thread" and method == "GET":
        if q.get("thread", [""])[0] != "example-org/example-repo#12":
            return h.send_json({"error": "notification:7 is not a pull request or an issue"}, 502) or True
        chat, msgs = gh_thread_page()
        limit, before = int(q.get("limit", ["30"])[0]), q.get("before", [""])[0]
        end = next((i for i, m in enumerate(msgs) if m["id"] == before), len(msgs)) if before else len(msgs)
        start = max(0, end - limit)
        out = {"chat": chat, "messages": msgs[start:end]}
        if start > 0:
            out["before"] = msgs[start]["id"]
        return h.send_json(HUB.thread_avatars("github", out)) or True
    if what == "media" and method == "GET":
        parts = q.get("ref", [""])[0].split(":")
        if len(parts) != 3 or parts[1] not in GH_AVATAR_COLORS:
            return h.send_json({"error": "不认识这个媒体"}, 502) or True
        data = HUB.png(96, 96, GH_AVATAR_COLORS[parts[1]])
        h.send_response(200)
        h.send_header("Content-Type", "image/png"); h.send_header("Content-Length", str(len(data)))
        h.send_header("Cache-Control", "private, max-age=31536000, immutable")
        h.end_headers(); h.wfile.write(data)
        return True
    if p.endswith("/thread/reply") and method == "POST":  # from the user's device the real hub posts it at once (#1190)
        body = json.loads(raw or b"{}")
        GH_SENT.append({"id": "c%d" % (100 + len(GH_SENT)), "at": datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
                        "sender": {"id": "sam", "name": "sam", "avatar": "avatar:sam:4"}, "mine": True, "format": "markdown",
                        "text": body.get("text", "")})
        HUB.SENT.append({"thread": body.get("thread", ""), "action": body.get("action", ""), "text": body.get("text", "")})
        return h.send_json({"ok": True, "result": "Commented on example-org/example-repo#12"}) or True
    return False


def github_area(h, method, p, raw):
    """GitHub's device-flow sign-in (MOCK_GITHUB=device); True when it answered."""
    if p == "/api/v1/connectors/github/auth" and (os.environ.get("MOCK_GITHUB") in ("device", "scopes") or os.environ.get("MOCK_GITHUB_LOGIN")):
        if method == "PUT" and not set_github_auth(json.loads(raw or b"{}")):
            return h.send_json({"error": "unknown machine"}, 400) or True
        return h.send_json(github_auth_view()) or True
    if os.environ.get("MOCK_GITHUB") != "device" or method != "POST" or p not in ("/api/v1/connectors/github/login", "/api/v1/connectors/github/logout"):
        return False
    GITHUB_CONNECTOR = HUB.GITHUB_CONNECTOR  # the hub's, which MOCK_GITHUB=device replaced as it loaded
    if p.endswith("/logout"):
        GITHUB_CONNECTOR.update(status="not_signed_in", error="还没登录 GitHub：在下面选一台电脑上的 gh 登录，或者用设备码登录")
        GITHUB_CONNECTOR.pop("account", None); GITHUB_CONNECTOR.pop("via", None)
        return h.send_json({"ok": True}) or True
    body = json.loads(raw or b"{}")
    step, d = body.get("step") or "", GITHUB_DEVICE
    device = {"step": "device", "kind": "device_code", "url": "https://github.com/login/device", "user_code": "ABCD-1234", "interval": 1,
              "hint": "在 GitHub 的验证页输入这个验证码并授权（授权页上显示的是「GitHub CLI」）；授权后这里会自动继续。"}
    if step == "device":
        d["polls"] += 1
        if d["polls"] < 3 or os.environ.get("MOCK_GITHUB_WAIT"):
            return h.send_json(device) or True
        GITHUB_CONNECTOR.update(status="ok", account="example-user", via="设备码登录",
                                last_sync=datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"))
        GITHUB_CONNECTOR.pop("error", None)
        return h.send_json({"done": True, "account": "example-user"}) or True
    if step:
        return h.send_json({"error": "unknown step %r" % step}, 502) or True
    d["polls"] = 0
    return h.send_json(device) or True


def github_avatar(h, req):
    """GET /api/v1/avatars/github/{value}?size= (#10923): the thread page's people (MOCK_GITHUB_INBOX=1), drawn at the size
    asked; any other value goes on to mock_hub.py's avatars_area, which refuses it as the hub does."""
    if not os.environ.get("MOCK_GITHUB_INBOX"):
        return False
    _, value, size = HUB.avatar_asked(h, req.path)
    parts = (value or "").split(":")
    if len(parts) != 3 or parts[0] != "avatar" or parts[1] not in GH_AVATAR_COLORS:
        return False
    return HUB.send_avatar(h, HUB.png(size, size, GH_AVATAR_COLORS[parts[1]]))


def route(h, req):
    """Every method under /api/v1/connectors/github/: mock_hub.py's GET and POST chains asked the thread page first and
    the sign-in next, and went on with the request when neither answered (PUT …/github/settings is still theirs)."""
    return (github_thread_area(h, req.method, req.path, req.body)
            or github_area(h, req.method, req.path, req.body))


def register(d):
    global HUB
    HUB = d.hub
    d.route(mock_domains.METHODS, r"/api/v1/connectors/github/.*", route)
    d.route("GET", r"/api/v1/avatars/github/[^/]+", github_avatar)
