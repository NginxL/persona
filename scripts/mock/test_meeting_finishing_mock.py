"""MOCK_MEETING_FINISHING (meeting_finishing_mock.py, #13461, #13537): a complete meeting recording without a transcription
of its own shows a final pass at <done> of <end>; an incomplete one, or one with a transcription of its own, answers as
without the switch."""
import unittest
import uuid

from test_meeting_attendance_mock import start
from test_meeting_conversation_mock import Conversation


class MeetingFinishingMockTests(Conversation, unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.base, stop = start({"MOCK_MEETING_FINISHING": "150,120"})
        cls.addClassCleanup(stop)

    def test_a_complete_meeting_shows_its_final_pass(self):
        rid = str(uuid.uuid4())
        self.assertEqual(self.upload(rid, complete=True)[0], 200)
        # done is capped at end, as the hub's own view caps it
        self.assertEqual(self.meta(rid).get("transcription"),
                         {"mode": "hub", "state": "finishing", "gaps": [], "final_done": 120.0, "end": 120.0})

    def test_an_incomplete_meeting_has_no_final_pass(self):
        rid = str(uuid.uuid4())
        self.assertEqual(self.upload(rid)[0], 200)
        self.assertIsNone(self.meta(rid).get("transcription"))

    def test_a_meeting_with_its_own_transcription_answers_as_before(self):
        rid = str(uuid.uuid4())
        self.assertEqual(self.upload(rid, transcription="device", complete=True)[0], 200)
        self.assertEqual(self.meta(rid).get("transcription"), {"mode": "device"})


if __name__ == "__main__":
    unittest.main()
