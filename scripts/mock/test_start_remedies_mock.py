"""The populated hub's #10365 start-remedy fixtures and exact request bodies."""
import json
import time
import unittest

from test_readiness_mock import Hub, hub


class StartRemediesMockTests(unittest.TestCase):
    def test_features_and_coded_fixtures(self):
        h = hub(self, MOCK_TASKS="1", MOCK_START_REMEDIES="1")
        _, health = h.call("GET", "/api/v1/health")
        for feature in ("task_retry_launcher", "start_continue", "todo_start_answer"):
            self.assertIn(feature, health["features"])
        _, todos = h.call("GET", "/api/v1/todos")
        by_id = {t["id"]: t for t in todos["todos"]}
        self.assertEqual(by_id["todo_remedy_worktree"].get("plan_id"), "")
        for pid, code in (("plan_remedy_launcher", "launcher_missing"),
                          ("plan_remedy_worktree", "worktree_path_taken"),
                          ("plan_remedy_question", "shell_waiting")):
            _, body = h.call("GET", "/api/v1/task-plans/" + pid)
            task = body["plan"]["tasks"][0]
            self.assertEqual(task["error_code"], code)
            self.assertTrue(task["error_args"])
        _, launcher = h.call("GET", "/api/v1/task-plans/plan_remedy_launcher")
        self.assertEqual(launcher["plan"]["tasks"][0]["retry_with"], ["codex"])
        _, plain = hub(self, MOCK_TASKS="1").call("GET", "/api/v1/health")
        self.assertNotIn("task_retry_launcher", plain["features"])

    def test_retry_is_bodyless_without_plan_id(self):
        h = hub(self, MOCK_TASKS="1", MOCK_START_REMEDIES="1")
        status, body = h.call("POST", "/api/v1/todos/todo_remedy_worktree/retry")
        self.assertEqual(status, 200, body)
        self.assertIsNone(body["todo"]["done_at"])
        _, log = h.call("GET", "/mock/start-remedies")
        self.assertEqual(log["requests"], [{"route": "retry", "id": "todo_remedy_worktree", "raw_body": "", "body": {}}])

    def test_retry_sends_only_the_launcher(self):
        h = hub(self, MOCK_TASKS="1", MOCK_START_REMEDIES="1")
        status, body = h.call("POST", "/api/v1/todos/todo_remedy_launcher/retry", {"launcher": "codex"})
        self.assertEqual(status, 200, body)
        _, log = h.call("GET", "/mock/start-remedies")
        self.assertEqual(log["requests"][-1]["raw_body"], json.dumps({"launcher": "codex"}))
        for _ in range(30):
            _, todos = h.call("GET", "/api/v1/todos")
            todo = next(t for t in todos["todos"] if t["id"] == "todo_remedy_launcher")
            if todo["agents"]:
                break
            time.sleep(0.1)
        self.assertEqual(todo["agents"][0]["agent"], "codex")
        _, plan = h.call("GET", "/api/v1/task-plans/plan_remedy_launcher")
        self.assertNotIn("error_code", plan["plan"]["tasks"][0])

    def test_both_startup_answer_routes(self):
        h = hub(self, MOCK_TASKS="1", MOCK_START_REMEDIES="1")
        status, body = h.call("POST", "/api/v1/todos/todo_remedy_question/start/answer", {"choice": "q"})
        self.assertEqual(status, 200, body)
        self.assertIsNone(body["todo"]["done_at"])
        status, refused = h.call("POST", "/api/v1/todos/todo_remedy_question/start/answer", {"choice": "not-offered"})
        self.assertEqual((status, refused["code"]), (409, "start_answer_closed"))
        sid = "a1f3c2d4-0000-4000-8000-000000000104"
        status, body = h.call("POST", "/api/v1/sessions/macbook/" + sid + "/start/continue", {"choice": "no"})
        self.assertEqual(status, 200, body)
        self.assertNotIn("start_error", body["session"])
        _, log = h.call("GET", "/mock/start-remedies")
        self.assertEqual([r["route"] for r in log["requests"]], ["task_answer", "task_answer", "session_continue"])
        self.assertEqual(log["requests"][0]["body"], {"choice": "q"})
        self.assertEqual(log["requests"][-1]["body"], {"choice": "no"})

    def test_tool_search_turn_error(self):
        h = hub(self, MOCK_TOOL_SEARCH="1")
        _, assistant = h.call("GET", "/api/v1/assistant")
        main = next(c for c in assistant["conversations"] if c["session_id"] == assistant["main"])
        self.assertEqual(main["turn_error_code"], "tool_search_unsupported")
        self.assertIn("not representable", main["turn_error"])
        self.assertIn("tool_reference", main["turn_error"])


if __name__ == "__main__":
    unittest.main()
