"""MOCK_AGENT_REVIEW=1 (#1808): an explicit independent session review for browser acceptance.

GET /mock/agent-review controls and inspects the fixture:
  state=card|none, count=1|0, running=1|0, changes=1|0, refuse=<code>, reset=1
The unmodified query returns the current state plus the starts and card answers the page made.
"""
import threading

MACHINE = "macbook"
SESSION = "a1f3c2d4-0000-4000-8000-000000000006"
REVIEWER = "e5f6a7b8-0000-4000-8000-000000000003"

FINDINGS = """**P0 · Forecast.swift:42** — 重试只认 HTTP 429，超时（URLError.timedOut）不重试，弱网下刷新会直接失败。
建议把超时加进可重试错误集合，并给重试上限。

**P1 · Units.swift:17** — `celsius` 是全局常量，测试里换不了区域；格式化跟系统走而不是用户设置。
建议把单位作为参数传给 formatter。

**P2 · docs/hourly.md:8** — 09:00 一行的降水概率写成了 91%，和正文“全天不超过 40%”矛盾。"""

REFUSALS = {
    "session_busy": ("这个 Agent 正在工作，等它停下来再审查", "session_busy"),
    "no_change": ("这个目录里没有可审查的改动", "no_change"),
    "review_running": ("这个会话已经有一场独立审查在进行", "review_running"),
    "read_only_unsupported": ("这个 Agent 的启动器没法以强制只读方式启动独立审查", "read_only_unsupported"),
    "not_supported": ("这台电脑现在没法启动独立审查", "not_supported"),
}

STATE = {"card": False, "count": True, "running": False, "changes": True, "refuse": ""}
LOCK = threading.RLock()
STARTS = []
ANSWERS = []


def ago(minutes):
    import datetime
    return (datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(minutes=minutes)).isoformat().replace("+00:00", "Z")


def card():
    with LOCK:
        count, up = STATE["count"], STATE["card"]
    if not up:
        return None
    ix = {
        "id": "review_a1b2c3", "machine_id": MACHINE, "session_id": SESSION,
        "kind": "question", "tool_name": "persona_session_review", "summary": "3 条发现" if count else "审查完成",
        "created_at": ago(2), "expires_at": ago(-7 * 24 * 60), "resumable": True,
        "questions": [{
            "question": "独立审查", "header": "审查完成", "multi_select": False,
            "options": [{"label": "发给 Agent"}, {"label": "不发了", "action": "defer"}],
        }],
        "review_report": FINDINGS, "reviewer_session_id": REVIEWER,
    }
    if count:
        ix["review_count"] = 3
    return ix


def state():
    with LOCK:
        running, changes = STATE["running"], STATE["changes"]
    return {"running": running, "has_changes": changes, "started_at": ago(3) if running else ""}


def control(q):
    with LOCK:
        if q.get("reset") == ["1"]:
            STARTS.clear()
            ANSWERS.clear()
        if "state" in q:
            STATE["card"] = q["state"][0] == "card"
        if "count" in q:
            STATE["count"] = q["count"][0] == "1"
        if "running" in q:
            STATE["running"] = q["running"][0] == "1"
        if "changes" in q:
            STATE["changes"] = q["changes"][0] == "1"
        if "state" in q or "running" in q or "changes" in q:
            STATE["refuse"] = ""
        if "refuse" in q:
            STATE["refuse"] = q["refuse"][0] if q["refuse"][0] in REFUSALS else ""
        return {"state": dict(STATE), "starts": STARTS, "answers": ANSWERS}


def start(machine, session):
    with LOCK:
        if STATE["refuse"]:
            words, code = REFUSALS[STATE["refuse"]]
            return {"error": words, "code": code, "delivered": "none"}, 409
        STATE["running"] = True
        STARTS.insert(0, {"machine": machine, "session": session})
        del STARTS[20:]
    return {"running": True, "reviewer_session_id": REVIEWER, "started_at": ago(0)}, 200


def note_answer(resolution):
    with LOCK:
        ANSWERS.insert(0, resolution)
        del ANSWERS[20:]
