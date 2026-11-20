"""The optional paging fixture preserves legacy reads, filters and exact lookup."""
import importlib.util
import os
from pathlib import Path
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("paged_sessions_mock", Path(__file__).with_name("mock_hub.py"))
hub = importlib.util.module_from_spec(spec)
spec.loader.exec_module(hub)


class PagedSessionsMockTests(unittest.TestCase):
    def setUp(self):
        env = patch.dict(os.environ, {"MOCK_SESSIONS_LIST": "1"})
        env.start()
        self.addCleanup(env.stop)

    def read(self, **query):
        body, status = hub.paged_sessions({key: [str(value)] for key, value in query.items()})
        self.assertEqual(status, 200, body)
        return body

    def test_paging_is_opt_in_and_keeps_unqueried_reads_complete(self):
        self.assertIn("sessions_list", hub.features())
        self.assertIn("sessions_lookup", hub.features())
        full = self.read()
        self.assertEqual(set(full), {"sessions"})
        with patch.dict(os.environ, {"MOCK_SESSIONS_LIST": "0"}):
            self.assertNotIn("sessions_list", hub.features())
            self.assertNotIn("sessions_lookup", hub.features())
            self.assertEqual(self.read(summary=1, limit=2), full)

    def test_paged_rows_counts_and_exact_lookup_use_the_same_collection(self):
        full = self.read()["sessions"]
        counts = self.read(counts=1)
        self.assertEqual(counts["total"], len(full))
        for dimension in ("machine", "group", "project", "state"):
            self.assertEqual(sum(counts[dimension].values()), len(full))
        first = self.read(summary=1, limit=2)
        self.assertEqual(len(first["sessions"]), 2)
        self.assertTrue(first["has_more"])
        self.assertIn("waiting", first["sessions"][0])
        second = self.read(summary=1, limit=2, cursor=first["next_cursor"])
        self.assertEqual(second["revision"], first["revision"])
        self.assertNotEqual(first["sessions"][0]["session_id"], second["sessions"][0]["session_id"])
        row = first["sessions"][0]
        exact = self.read(summary=0, limit=50, machine_id=row["machine_id"], session_id=row["session_id"])
        self.assertEqual(exact["total"], 1)
        self.assertFalse(exact["has_more"])
        self.assertNotIn("next_cursor", exact)
        self.assertNotIn("waiting", exact["sessions"][0])
        self.assertEqual(exact["sessions"][0]["session_id"], row["session_id"])
        self.assertEqual(self.read(summary=0, session_id="missing")["sessions"], [])

    def test_directory_hidden_rows_leave_lists_but_not_the_exact_lookup(self):
        # As the hub does (server/internal/sessionlist hidden_test.go): a directory-hidden session leaves the default
        # pages and counts, include_hidden=1 shows it, and the exact machine/session lookup always answers its row.
        row = next(s for s in self.read()["sessions"] if s.get("cwd") and not s.get("assistant"))
        with patch.dict(hub.AGENT_DIRS_HIDDEN, {row["machine_id"]: [{"pattern": row["cwd"], "enabled": True}]}):
            ids = lambda page: [(s["machine_id"], s["session_id"]) for s in page["sessions"]]
            key = (row["machine_id"], row["session_id"])
            self.assertNotIn(key, ids(self.read(summary=1, limit=50)))
            self.assertIn(key, ids(self.read(summary=1, limit=50, include_hidden=1)))
            exact = self.read(summary=0, machine_id=row["machine_id"], session_id=row["session_id"])
            self.assertEqual(ids(exact), [key])
            self.assertTrue(exact["sessions"][0]["hidden_by_directory"])
            self.assertEqual(exact["total"], 1)
            # one session id without its machine is no exact lookup: still a filtered list
            self.assertNotIn(key, ids(self.read(summary=1, session_id=row["session_id"])))

    def test_filter_counts_match_filtered_pages(self):
        row = self.read()["sessions"][0]
        filters = {"machine_id": row["machine_id"], "state": row["state"], "q": row["title"]}
        page = self.read(summary=1, limit=200, **filters)
        counts = self.read(counts=1, **filters)
        self.assertGreater(page["total"], 0)
        self.assertEqual(counts["total"], page["total"])
        self.assertTrue(all(s["machine_id"] == row["machine_id"] and s["state"] == row["state"] for s in page["sessions"]))

    def test_agents_only_scopes_counts_pages_and_deltas(self):
        self.assertIn("sessions_agents_only", hub.features())
        assistant = hub.ASSISTANT[0]
        agent = hub.SESSIONS[0]
        health = {**agent, "session_id": "test-health", "cwd": "/home/me/.persona/health-m-123/reports"}
        with patch.object(hub, "SESSIONS", [*hub.SESSIONS, health]), patch.dict(assistant), patch.dict(agent):
            base = self.read(agents_only=1, summary=1, limit=200)
            count = self.read(agents_only=1, counts=1)
            unscoped = self.read(summary=1, limit=200)
            self.assertEqual(count["total"], base["total"])
            self.assertEqual(sum(count["state"].values()), base["total"])
            for hidden in (assistant, health):
                self.assertIn(hidden["session_id"], [s["session_id"] for s in unscoped["sessions"]])
                self.assertNotIn(hidden["session_id"], [s["session_id"] for s in base["sessions"]])
            assistant["last_text"] = "assistant changed"
            health["last_text"] = "health changed"
            agent["assistant"] = True
            delta = self.read(agents_only=1, since=base["revision"], limit=200)
            self.assertEqual(delta["sessions"], [])
            self.assertEqual(delta["remove"], [hub.LIST_TUPLE(agent)])
            self.assertEqual(delta["total"], base["total"] - 1)

    def test_unchanged_revision_has_no_delta_and_stale_revision_rebaselines(self):
        first = self.read(summary=1, limit=2)
        delta = self.read(summary=1, since=first["revision"])
        self.assertEqual(delta["sessions"], [])
        self.assertEqual(delta["remove"], [])
        self.assertFalse(delta["has_more"])
        for query, code in (({"since": ["expired"]}, "revision_expired"), ({"cursor": ["expired:2"]}, "cursor_expired")):
            body, status = hub.paged_sessions(query)
            self.assertEqual(status, 409)
            self.assertEqual(body["code"], code)

    def test_since_answers_changed_entered_and_removed_rows_from_the_journal(self):
        sessions = hub.SESSIONS
        saved = [dict(s) for s in sessions]
        self.addCleanup(lambda: sessions.__setitem__(slice(None), saved))
        base = self.read(summary=0, limit=200)
        self.assertFalse(base["has_more"])
        self.assertEqual(hub.current_list_revision(), base["revision"])
        changed, gone = sessions[0], sessions[1]
        changed["last_text"] = "a newer line"
        gone_key = hub.LIST_TUPLE(hub.decorated_session(gone))
        del sessions[1]
        delta = self.read(summary=0, limit=200, since=base["revision"])
        self.assertNotEqual(delta["revision"], base["revision"])
        self.assertEqual(hub.current_list_revision(), delta["revision"])
        self.assertEqual([s["session_id"] for s in delta["sessions"]], [changed["session_id"]])
        self.assertEqual(delta["sessions"][0]["last_text"], "a newer line")
        self.assertEqual(delta["remove"], [gone_key])
        self.assertEqual(delta["total"], base["total"] - 1)
        # A filtered delta: a row leaving the filter is a removal there, one entering it an upsert.
        state = changed["state"]
        before = self.read(summary=1, limit=200, state=state)
        changed["state"] = "unknown" if state != "unknown" else "idle"
        left = self.read(summary=1, limit=200, state=state, since=before["revision"])
        self.assertIn(hub.LIST_TUPLE(hub.decorated_session(changed)), left["remove"])
        # One slot a change: removals and rows page together under the target revision.
        small = self.read(summary=0, limit=1, since=base["revision"])
        self.assertTrue(small["has_more"])
        rest = self.read(summary=0, limit=200, since=base["revision"], cursor=small["next_cursor"])
        self.assertEqual(rest["revision"], small["revision"])
        self.assertEqual(len(small["sessions"]) + len(small["remove"]) + len(rest["sessions"]) + len(rest["remove"]), 2)

    def test_journal_is_bounded_like_the_hub(self):
        sessions = hub.SESSIONS
        saved = [dict(s) for s in sessions]
        self.addCleanup(lambda: sessions.__setitem__(slice(None), saved))
        oldest = self.read(summary=1, limit=1)["revision"]
        for k in range(hub.LIST_JOURNAL_SIZE + 1):
            sessions[0]["last_text"] = "round %d" % k
            hub.current_list_revision()
        body, status = hub.paged_sessions({"summary": ["1"], "since": [oldest]})
        self.assertEqual((status, body["code"]), (409, "revision_expired"))
        self.assertLessEqual(len(hub.LIST_JOURNAL), hub.LIST_JOURNAL_SIZE)

    def test_strict_summary_rows_drop_chat_arrays_like_the_hub(self):
        run = {"task_id": "w", "name": "audit", "running": True, "phases": ["A", "B"], "phase_index": 2, "agents": [{"id": "a"}]}
        row = hub.summary_row({"session_id": "s", "workflows": [run], "background": [{"id": "w", "kind": "workflow"},
                                                                               {"id": "b", "kind": "shell"}]})
        self.assertFalse({"workflows", "background", "subagents"} & set(row))
        self.assertEqual((row["running_workflows"], row["background_count"], row["background_tasks_count"]), (1, 2, 1))
        self.assertEqual(row["workflow_progress"], {"task_id": "w", "name": "audit", "running": True, "phase_index": 2, "phase_count": 2})
        self.assertTrue(row["waiting"])


if __name__ == "__main__":
    unittest.main()
