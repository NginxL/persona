"""The mock hub's hand-off of a meeting to an Agent after it (#8852, docs/assistant/meeting-minutes.md "Handing a meeting
to an Agent (#8852)"), as assistant/meeting_handoff.go has it: refused while the meeting goes on and with nothing to
hand, sent to an Agent as the owner's message, each try kept in detail.meta.handoffs with its problem when it failed,
and MOCK_HANDOFF_FAIL keeping every try that passes the checks as failed."""
import time
import unittest
import uuid

from test_meeting_attendance_mock import Hub, start

LINES = [{"start": 5, "end": 9, "text": "Ada: Today we look at the export flow."}]
ENDED = "2026-11-10T15:00:00+08:00"
WIDGET = "a1f3c2d4-0000-4000-8000-000000000001"


class Handoff(Hub):
    def handoff(self, rid, **body):
        return self.call("POST", f"/api/v1/recordings/{rid}/handoff", {"machine_id": "macbook", "session_id": WIDGET, **body})

    def kept(self, rid):
        return self.call("GET", f"/api/v1/kb/recordings/{rid}")[1]["detail"]["meta"].get("handoffs", [])


class MeetingHandoffMockTests(Handoff, unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.base, stop = start({})
        cls.addClassCleanup(stop)

    def test_refused_while_running_or_with_nothing_to_hand(self):
        rid = str(uuid.uuid4())
        self.upload(rid, segments=LINES)
        status, out = self.handoff(rid, transcript=True)
        self.assertEqual((status, out["code"]), (409, "meeting_running"))
        self.upload(rid, segments=LINES, complete=True, ended_at=ENDED, revision=int(time.time() * 1000) + 5)
        status, out = self.handoff(rid)
        self.assertEqual((status, out["code"]), (409, "no_material"))  # no minutes, and the transcript not asked for
        self.assertEqual(self.call("POST", f"/api/v1/recordings/{rid}/handoff", {"machine_id": "macbook"})[0], 400)
        self.assertEqual(self.handoff(str(uuid.uuid4()), transcript=True)[0], 404)
        self.assertEqual(self.kept(rid), [])

    def test_sent_to_an_agent_and_kept(self):
        rid = str(uuid.uuid4())
        self.upload(rid, segments=LINES, complete=True, ended_at=ENDED)
        before = len(self.call("GET", f"/api/v1/sessions/macbook/{WIDGET}/items")[1]["items"])
        status, out = self.handoff(rid, title="  Weather\nwidget ", text="Fix the export", transcript=True)
        self.assertEqual(status, 200)
        h = out["handoff"]
        self.assertEqual({k: h[k] for k in ("machine_id", "session_id", "title", "transcript", "state")},
                         {"machine_id": "macbook", "session_id": WIDGET, "title": "Weather widget", "transcript": True, "state": "sent"})
        self.assertNotIn("problem", h)
        items = self.call("GET", f"/api/v1/sessions/macbook/{WIDGET}/items")[1]["items"]
        self.assertEqual(len(items), before + 1)
        self.assertEqual(items[-1]["role"], "user")
        self.assertTrue(items[-1]["text"].endswith("Fix the export"))
        self.assertEqual(self.kept(rid), [h])

    def test_failed_tries_kept_with_their_problem(self):
        rid = str(uuid.uuid4())
        self.upload(rid, segments=LINES, complete=True, ended_at=ENDED)
        persona = next(s for s in self.call("GET", "/api/v1/sessions")[1]["sessions"] if s.get("assistant"))
        cases = [({"machine_id": persona["machine_id"], "session_id": persona["session_id"]}, "not_agent"),
                 ({"machine_id": "nowhere", "session_id": WIDGET}, "not_allowed"),
                 ({"session_id": "not-listed"}, "unavailable")]
        for body, problem in cases:
            out = self.handoff(rid, transcript=True, **body)[1]["handoff"]
            self.assertEqual((out["state"], out["problem"]), ("failed", problem), body)
        self.assertEqual([h["problem"] for h in self.kept(rid)], ["not_agent", "not_allowed", "unavailable"])

    def test_failing_on_request(self):
        rid = str(uuid.uuid4())
        self.upload(rid, segments=LINES, complete=True, ended_at=ENDED)
        self.call("POST", "/mock/meeting_handoff", {"fail": "send_failed"})
        try:
            self.assertEqual(self.handoff(rid, transcript=True)[1]["handoff"]["problem"], "send_failed")
        finally:
            self.call("POST", "/mock/meeting_handoff", {"fail": ""})
        self.assertEqual(self.handoff(rid, transcript=True)[1]["handoff"]["state"], "sent")


class FailingSendMockTests(Handoff, unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.base, stop = start({"MOCK_HANDOFF_FAIL": "send_failed"})
        cls.addClassCleanup(stop)

    def test_every_try_fails(self):
        rid = str(uuid.uuid4())
        self.upload(rid, segments=LINES, complete=True, ended_at=ENDED)
        out = self.handoff(rid, transcript=True)[1]["handoff"]
        self.assertEqual((out["state"], out["problem"]), ("failed", "send_failed"))
        self.assertEqual(self.kept(rid)[-1]["problem"], "send_failed")


if __name__ == "__main__":
    unittest.main()
