"""MOCK_INBOX_MANY (#9053): many made-up items per Inbox section for the section bar's screenshots and long-list runs.

Both forms (one number for every section, or section:count pairs), the sections the items land in (the hub's `status`,
so In progress shows), three source groups, update times spread over many days, done items inside the list's 3 days,
the combination with MOCK_INBOX_EVENTS, and the switch in --help.
"""
from collections import Counter
from datetime import datetime, timedelta, timezone
import unittest

from test_inbox_showcase_flag_mock import HERE, Hub
import subprocess


def section(item):
    if item.get("done_at"): return "done"
    return item.get("status") or item.get("tier")


def many(items):
    return [x for x in items if x["id"].startswith("many:")]


class ManyPairsTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.hub = Hub(MOCK_INBOX_MANY="action:30,working:6,fyi:40,noise:20,done:25", MOCK_MANAGERS="0")
        status, body = cls.hub.get("/api/v1/inbox?sources=all")
        assert status == 200, body
        cls.items = body["items"]

    @classmethod
    def tearDownClass(cls):
        cls.hub.stop()

    def test_each_section_gets_its_count(self):
        self.assertEqual(Counter(section(x) for x in many(self.items)),
                         Counter(action=30, working=6, fyi=40, noise=20, done=25))

    def test_open_items_carry_the_hubs_status_and_working_ones_a_handling_chat(self):
        for item in many(self.items):
            with self.subTest(item=item["id"]):
                if item.get("done_at"):
                    self.assertNotIn("status", item)
                    continue
                self.assertIn(item["status"], ("action", "working", "fyi", "noise"))
                if item["status"] == "working":
                    self.assertEqual(item["tier"], "action")  # an old app files it under Needs you
                    self.assertIn(item["handling"]["state"], ("looking", "agent", "waiting"))

    def test_three_source_groups_and_many_days(self):
        self.assertEqual({x["source"] for x in many(self.items)}, {"lark", "mail", "github"})
        now = datetime.now(timezone.utc)
        days = {(now - datetime.fromisoformat(x["updated_at"].replace("Z", "+00:00"))).days
                for x in many(self.items) if section(x) == "fyi"}
        self.assertIn(0, days)
        self.assertGreaterEqual(max(days), 30)  # reaches "1+ month"

    def test_done_items_fall_in_the_lists_three_days(self):
        now = datetime.now(timezone.utc)
        for item in many(self.items):
            if item.get("done_at"):
                at = datetime.fromisoformat(item["done_at"].replace("Z", "+00:00"))
                self.assertLess(now - at, timedelta(days=3))

    def test_the_first_needs_you_item_relates_to_a_done_and_a_collapsed_one(self):
        first = next(x for x in self.items if x["id"] == "many:action:0")
        self.assertEqual([r["id"] for r in first["related"]], ["many:done:0", "many:noise:0"])
        by_id = {x["id"]: x for x in self.items}
        for r in first["related"]:
            self.assertEqual(r["title"], by_id[r["id"]]["title"])
        self.assertEqual([x["id"] for x in many(self.items) if x.get("related")], ["many:action:0"])
        # an event (it has a chain), so its page draws the Related line
        self.assertEqual([c["kind"] for c in first["chain"]], ["received"])

    def test_ids_are_unique(self):
        ids = [x["id"] for x in self.items]
        self.assertEqual(len(ids), len(set(ids)))


class ManyFormsTests(unittest.TestCase):
    def test_one_number_is_every_section(self):
        hub = Hub(MOCK_INBOX_MANY="4", MOCK_MANAGERS="0")
        self.addCleanup(hub.stop)
        status, body = hub.get("/api/v1/inbox?sources=all")
        self.assertEqual(status, 200)
        self.assertEqual(Counter(section(x) for x in many(body["items"])),
                         Counter(action=4, working=4, fyi=4, noise=4, done=4))

    def test_it_adds_to_the_inbox_events(self):
        hub = Hub(MOCK_INBOX_MANY="fyi:3", MOCK_INBOX_EVENTS="1", MOCK_MANAGERS="0")
        self.addCleanup(hub.stop)
        status, body = hub.get("/api/v1/inbox?sources=all")
        self.assertEqual(status, 200)
        self.assertEqual(len(many(body["items"])), 3)
        self.assertGreater(len(body["items"]), 3)  # the events stay

    def test_off_adds_nothing(self):
        hub = Hub(MOCK_MANAGERS="0")
        self.addCleanup(hub.stop)
        status, body = hub.get("/api/v1/inbox?sources=all")
        self.assertEqual(status, 200)
        self.assertEqual(many(body["items"]), [])

    def test_help_lists_the_switch(self):
        out = subprocess.run(["python3", "-I", str(HERE / "mock_hub.py"), "--help"], capture_output=True, text=True, timeout=60)
        self.assertEqual(out.returncode, 0, out.stderr[-2000:])
        self.assertIn("MOCK_INBOX_MANY=", out.stdout)


if __name__ == "__main__":
    unittest.main()
