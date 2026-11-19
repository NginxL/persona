#!/usr/bin/env python3
"""Serve a synthetic Assistant-tray fixture through the ordinary mock hub.

Run with PORT=<unused loopback port>. GET /mock/assistant-trays?clear=1 removes
pending items; ?reset=1 restores them without changing the pairing identity.
All names, task text, paths and replies here are fictional acceptance data.
"""

import copy
import os
import signal
from http.server import ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

os.environ["MOCK_TASKS"] = "1"
import mock_hub as hub


def install():
    """Replace the mock's sample collections, never connect to a real hub."""
    hub._seq[0] = 0
    for index, machine in enumerate(hub.MACHINES):
        machine["name"] = f"Computer {chr(65 + index)}"
    rows = [
        (hub.WIDGET, "完善搜索功能", "working", "正在补搜索回归测试", 2, 4),
        (hub.TESTS, "更新帮助文档", "working", "正在整理使用说明", 3, 4),
        (hub.RELEASE, "检查发布流程", "needs_you", "等待确认发布方案", 1, 4),
        (hub.REVIEW, "修复示例布局", "unread", "已验收 · PR #42", 4, 4),
    ]
    hub.SESSIONS[:] = [hub.sess("macbook", sid, title, "/code/example", "working" if state == "working" else "idle",
                                state, "Example", 1, f"task{index}", index, 2, text, pending=int(state == "needs_you"),
                                plan={"done": done, "total": total, "current": text})
                       for index, (sid, title, state, text, done, total) in enumerate(rows)]
    hub.ASSISTANT[:] = [hub.assistant_sess(hub.ASK1, "Persona", 1, "已派出任务，进度会在面板里更新。")]
    hub.ASSISTANT_CONFIG["main"] = hub.ASK1
    hub.TASK_PLANS.clear()
    hub.TASK_FEED.clear()
    hub.TODOS[:] = []
    tasks = []
    for index, (sid, title, state, text, done, total) in enumerate(rows):
        todo_id = f"todo_tray_{index}"
        task = {"title": title, "prompt": title, "machine": "macbook", "cwd": "/code/example", "todo_id": todo_id,
                "readonly": False, "permission": "acceptEdits"}
        tasks.append(task)
        supervision = {"supervisor": hub.ASK1, "done_when": ["Tests pass", "Changes reviewed"],
                       "state": "accepted" if index == 3 else "needs_user" if index == 2 else "", "round": 0,
                       "reviews": [], "updated_at": hub.ago(minutes=1),
                       "progress": {"kind": "blocked" if index == 2 else "progress", "step": done, "steps": total,
                                    "text": text, "at": hub.ago(minutes=1)}}
        hub.TODOS.append({"id": todo_id, "title": title, "notes": "Synthetic acceptance task", "origin": hub.ASK1,
                          "plan_id": "plan_tray", "created_at": hub.ago(minutes=5), "updated_at": hub.ago(minutes=1),
                          "done_at": hub.ago(minutes=1) if index == 3 else None, "pr_url": "https://github.com/example/project/pull/42" if index == 3 else "",
                          "supervision": supervision, "spaces": [], "quiet": False,
                          "agents": [{"machine_id": "macbook", "session_id": sid, "linked_at": hub.ago(minutes=5),
                                      "released_at": None, "title": title, "cwd": "/code/example", "agent": "claude"}]})
    hub.TASK_PLANS["plan_tray"] = {"id": "plan_tray", "conversation": hub.ASK1, "state": "started",
                                  "created_at": hub.ago(minutes=5), "updated_at": hub.ago(minutes=4), "tasks": tasks}
    hub.TASK_FEED[hub.ASK1] = [{"id": "feed_tray_plan", "conversation": hub.ASK1, "at": hub.ago(minutes=5), "kind": "plan", "ref": "plan_tray", "body": {}}]
    for index in range(4):
        hub.TASK_FEED[hub.ASK1].append({"id": f"feed_tray_task_{index}", "conversation": hub.ASK1,
                                      "at": hub.ago(minutes=4), "kind": "task", "ref": f"todo_tray_{index}", "body": {}})
    hub.TASK_FEED[hub.ASK1].append({"id": "feed_tray_review", "conversation": hub.ASK1, "at": hub.ago(minutes=1),
                                  "kind": "review", "ref": "todo_tray_3", "body": {"title": rows[3][1], "verdict": "accepted", "round": 0,
                                  "note": "Tests passed; PR #42 reviewed", "checks": []}})
    hub.INTERACTIONS[:] = [
        {"id": "ix_tray_permission", "machine_id": "macbook", "session_id": hub.WIDGET, "session_title": rows[0][1],
         "kind": "permission", "created_at": hub.ago(minutes=1), "expires_at": hub.later(minutes=60),
         "tool_name": "Bash", "tool_input": {"command": "npm test", "description": "运行示例项目测试"}},
        {"id": "ix_tray_review", "machine_id": "macbook", "session_id": hub.RELEASE, "session_title": rows[2][1],
         "kind": "question", "created_at": hub.ago(minutes=1), "expires_at": hub.later(minutes=60),
         "questions": [{"question": "发布方案选哪个？", "header": "发布方案", "multi_select": False,
                        "options": [{"label": "先做小范围验证", "description": "先验证，再发布"},
                                    {"label": "稍后再决定", "description": "继续保留草案"}]}]},
    ]
    hub.INBOX[:] = [{"id": "lark:tray:1", "source": "lark", "kind": "group", "account": "example", "account_name": "示例消息",
                    "title": "示例发布讨论", "sender": "示例参与者", "preview": "周四的安排可以吗？", "count": 1,
                    "started_at": hub.ago(minutes=5), "updated_at": hub.ago(minutes=1), "tier": "action", "status": "action", "read": False,
                    "lark": {"feed_id": "tray", "is_group": True, "start_position": "1"},
                    "suggestion": {"kind": "reply", "version": 1, "text": "周四下午可以，确认后我会通知大家。", "handback": False,
                                   "at": hub.ago(minutes=1)},
                    "handling": {"owner": "manager", "state": "done", "since": hub.ago(minutes=1)}}]
    hub.SCHEDULE[:] = [{"id": "tray_patrol", "title": "例行巡检", "prompt": "Check the sample project", "repeat": "interval",
                       "every_minutes": 30, "enabled": True, "push": "asked", "created_by": "assistant",
                       "at": hub.later(minutes=20), "next_run": hub.later(minutes=20), "last_run": hub.ago(minutes=10),
                       "last_status": "ok", "last_error": "", "run_machine_id": "server", "run_session_id": ""}]
    hub.ITEMS[hub.ASK1] = [hub.it("user", "text", 7, text="帮我看看这个示例项目，安排后续任务。")]
    hub.ITEMS[hub.ASK1] += hub.tool("Read", {"file_path": "/code/example/README.md"}, "Read the sample project", 6.5)
    hub.ITEMS[hub.ASK1] += hub.tool("Grep", {"pattern": "TODO", "path": "/code/example"}, "Found four follow-up tasks", 6.3)
    hub.ITEMS[hub.ASK1] += [hub.it("assistant", "text", 6, text="已检查文档和待办，建议完善搜索、补帮助文档，并确认发布流程。"),
                            hub.it("user", "text", 5.5, text="就按这个安排，派出去。")]
    hub.ITEMS[hub.ASK1] += hub.tool("mcp__persona__task_plan", {"tasks": tasks}, "Started four sample tasks", 5)
    hub.ITEMS[hub.ASK1] += [hub.it("assistant", "text", 4.5, text="已派出任务，进度会在面板里更新。"),
                            hub.it("user", "text", 3, text="Routine supervision check", source={"kind": "supervision"},
                                   injected={"kind": "system", "state": "supervise", "level": "triage", "line": "例行督办", "title": "例行督办"}),
                            hub.it("assistant", "text", 2.5, text="后台巡检完成，没有新问题。", narration=True),
                            hub.it("system", "text", 2, text="Context compacted", injected={"kind": "compact", "state": "done", "line": "上下文已压缩"})]
    hub.TODOS_INIT[:] = copy.deepcopy(hub.TODOS)
    for sid, title, _, text, _, _ in rows:
        hub.ITEMS[sid] = [hub.it("user", "text", 5, text=title), hub.it("assistant", "text", 1, text=text)]


def clear():
    hub.INTERACTIONS[:] = []
    for item in hub.INBOX:
        item.pop("suggestion", None)
        item["status"], item["tier"], item["done_at"] = "fyi", "fyi", hub.ago()
    for todo in hub.TODOS:
        if todo.get("supervision", {}).get("state") == "needs_user":
            todo["supervision"]["state"] = ""
            todo["supervision"]["progress"].update(kind="progress", text="确认已收到，继续执行")
    for session in hub.SESSIONS:
        session["pending_interactions"] = 0
        if session["state"] == "needs_you":
            session["state"], session["status"] = "working", "working"


_original_tasks_area = hub.tasks_area


def tasks_area(handler, method, path, raw):
    if method == "GET" and path.startswith("/api/v1/assistant/conversations/") and path.endswith("/feed"):
        conversation = path.split("/")[5]
        items = hub.TASK_FEED.get(conversation, [])
        cards = []
        for plan in hub.TASK_PLANS.values():
            if plan["conversation"] != conversation:
                continue
            for index, task in enumerate(plan["tasks"]):
                todo = next((todo for todo in hub.TODOS if todo["id"] == task.get("todo_id")), {})
                state = "accepted" if todo.get("done_at") else "needs_you" if todo.get("supervision", {}).get("state") == "needs_user" else "working"
                cards.append({"id": f"plan:{plan['id']}:{index}", "todo_id": task.get("todo_id"), "plan_id": plan["id"], "index": index,
                              "at": plan["created_at"], "updated_at": todo.get("updated_at", plan["updated_at"]), "state": state})
        handler.send_json({"items": items, "plans": hub.TASK_PLANS, "task_cards": cards})
        return True
    return _original_tasks_area(handler, method, path, raw)


class FixtureHandler(hub.H):
    def do_GET(self):
        parsed = urlparse(self.path)
        if parsed.path == "/mock/assistant-trays":
            query = parse_qs(parsed.query)
            if query.get("reset") == ["1"]:
                install()
            resolved = []
            if query.get("clear") == ["1"]:
                resolved = [item["id"] for item in hub.INTERACTIONS]
                clear()
            for send, _ in list(hub.LIVE):
                try:
                    send(hub.from_default({"type": "inbox"}))
                    for interaction_id in resolved:
                        send(hub.from_default({"type": "interaction_resolved", "interaction_id": interaction_id,
                                               "status": "answered", "resolution": "Synthetic fixture cleared"}))
                except OSError:
                    pass  # A client may disconnect during this synthetic update.
            hub.push_todos()
            for machine in hub.MACHINES:
                hub.push_machine(machine["id"])
            self.send_json({"pending": len(hub.INTERACTIONS), "drafts": sum(bool(item.get("suggestion")) for item in hub.INBOX),
                            "tasks": len(hub.TODOS)})
            return
        super().do_GET()


def main():
    install()
    hub.tasks_area = tasks_area
    server = ThreadingHTTPServer(("127.0.0.1", hub.PORT), FixtureHandler)
    server.daemon_threads = True
    hub.PORT = server.server_port
    hub.ENDPOINTS.append(hub.endpoint("127.0.0.1", hub.PORT, "http", "lan"))
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    print(f"Synthetic Assistant tray mock ready on loopback port {hub.PORT}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("Assistant tray mock stopped", flush=True)
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
