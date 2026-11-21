"""A project's lessons from finished tasks in the mock hub (server/internal/assistant/task_lessons_routes.go, #12295),
a domain of scripts/mock/mock_domains.py: GET /api/v1/kb/projects/{id}/lessons, PUT and DELETE …/lessons/{lesson},
answered with the list after the change, and 409 lesson_changed for a lesson that is not the newest version any more.
Synthetic lessons for weather-app only. scripts/mock/test_project_lessons_mock.py drives it.
"""
import os
import secrets

import mock_domains

HELP = ("MOCK_PROJECT_LESSONS=none|missing  a project's lessons from finished tasks: none at all / a hub from before "
        "them (404)")
HUB = None  # mock_hub.py's globals (mock_domains.Hub), from register

LESSONS = {}  # project slug -> lessons, most repeated first
GONE = {}  # project slug -> ids that were the lesson before an edit or a delete (409 lesson_changed)


def fixtures():
    """weather-app's lessons: one repeated three times, one corrected by the owner, one not seen for months."""
    def lesson(id, kind, text, seen, days, sources, stale=False, edited=False):
        at = HUB.ago(days=days)
        return {"id": id, "kind": kind, "text": text, "seen": seen, "first_seen_at": HUB.ago(days=days + 20),
                "last_seen_at": at, "stale": stale, "edited": edited, "sources": sources}
    weather = [{"todo_id": "todo_weather_fix", "title": "修 · 中号组件降雨条看不清"}, {"todo_id": "todo_weather_old", "title": "小组件按小时刷新"}]
    return {"weather-app": [
        lesson("lesson-weather-1", "missing_context", "小组件的布局问题要在原版模拟器上复现，瘦身的模拟器看不出来。", 3, 1, weather),
        lesson("lesson-weather-2", "repeated_mistake", "改完小组件布局先跑一遍中号和大号的截图测试再交。", 2, 2, weather[:1]),
        lesson("lesson-weather-3", "improvement", "降雨条的颜色先对照深色模式，再定浅色的。", 1, 3, weather[1:], edited=True),
        lesson("lesson-weather-4", "wasted_step", "不用整包重编，只编小组件的目标就够验证。", 1, 120, weather[1:], stale=True),
    ]}


def reset():
    LESSONS.clear()
    GONE.clear()
    if os.environ.get("MOCK_PROJECT_LESSONS", "") != "none":
        LESSONS.update(fixtures())


def view(slug):
    return {"enabled": bool(HUB.ASSISTANT_CONFIG.get("task_lessons")), "lessons": LESSONS.get(slug, [])}


def route(h, req):
    if os.environ.get("MOCK_PROJECT_LESSONS", "") == "missing":
        return h.send_json({"error": "not found"}, 404)
    slug, lesson = req.match.group(1), req.match.group(2)
    if not any(e["id"] == slug for e in HUB.KB.get("projects", [])):
        return h.send_json({"error": "not found"}, 404)
    if lesson is None:
        if req.method != "GET":
            return False
        return h.send_json(view(slug))
    if req.method not in ("PUT", "DELETE"):
        return False
    lessons = LESSONS.setdefault(slug, [])
    i = next((i for i, l in enumerate(lessons) if l["id"] == lesson), -1)
    if i < 0:
        if lesson in GONE.get(slug, set()):
            return h.send_json({"error": "the lesson changed since it was read: look at it again", "code": "lesson_changed", **view(slug)}, 409)
        return h.send_json({"error": "no such lesson"}, 404)
    GONE.setdefault(slug, set()).add(lesson)
    if req.method == "DELETE":
        del lessons[i]
        return h.send_json(view(slug))
    try:
        text = " ".join(str(req.json().get("text", "")).split())[:200]
    except ValueError:
        return h.send_json({"error": "bad json"}, 400)
    if not text:
        return h.send_json({"error": "a lesson needs text: delete it instead"}, 400)
    if text != lessons[i]["text"]:
        lessons[i] = {**lessons[i], "id": "lesson-edit-" + secrets.token_hex(4), "text": text, "edited": True,
                      "last_seen_at": HUB.ago(days=0), "stale": False}
    else:
        GONE[slug].discard(lesson)
    return h.send_json(view(slug))


def register(d):
    global HUB
    HUB = d.hub
    d.help(HELP)
    d.route(mock_domains.METHODS, r"/api/v1/kb/projects/([^/]+)/lessons(?:/([^/]+))?", route)
    d.on_reset(reset)
    reset()
