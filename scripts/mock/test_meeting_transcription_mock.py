"""Scripted meeting PCM uplink, bilingual transcript, gap repair and final minutes; no upstream required."""
import importlib.util
import json
import threading
import unittest
import uuid
from pathlib import Path

from test_meeting_attendance_mock import start
from test_meeting_conversation_mock import Conversation, websocket

HERE = Path(__file__).resolve().parent
# The mock's lines, as the scripted upstream says them (TRANSCRIPTION_SCRIPT): the first is Persona's answer below.
CAP_LINE, PRICING_LINE = "我们先把 export cap 保持在 4 MB。", "Let's revisit pricing next week."  # i18n-ignore: synthetic speech


class Transcription(Conversation):
    def mode(self, **fields):
        return self.call("POST", "/mock/meeting_transcription", fields)

    def detail(self, rid):
        return self.call("GET", f"/api/v1/kb/recordings/{rid}")[1]["detail"]

    def observed(self, rid):
        return self.call("GET", "/mock/meeting_transcription")[1]["recordings"][rid]

    def recording(self, **fields):
        rid = str(uuid.uuid4())
        status, out = self.upload(rid, transcription="hub", room={"attendance": "participant"}, **fields)
        self.assertEqual((status, out["transcription"]), (200, "hub"))
        return rid

    def socket(self, rid, begin=True, timeline=None, **kw):
        ws = websocket(self, f"/api/v1/recordings/{rid}/audio", **kw)
        if begin:
            ws.send({"type": "start", "at": 0, "timeline": timeline or {}})
            self.assertIsNotNone(ws("ready"))
        return ws

    def pcm(self, ws, seconds):
        size = round(seconds * 16000) * 2
        while size:
            n = min(size, 32000)
            ws.send(bytes(n))
            size -= n

    def end(self, ws, at):
        ws.send({"type": "end", "at": at})
        self.assertEqual(ws("ack")["at"], at)
        self.assertEqual(ws("close")["code"], 1000)


class MeetingTranscriptionMockTests(Transcription, unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.base, stop = start({"MOCK_EXTENSIONS": "1"})
        cls.addClassCleanup(stop)

    def setUp(self):
        self.call("POST", "/mock/recording_minutes", {"mode": "pending"})
        self.call("POST", "/mock/meeting_attendance", {"on": True, "kb_read_only": False})
        self.call("POST", "/mock/meeting_conversation", {"on": True, "room": "ok"})
        self.call("POST", "/mock/meeting_live_view", {"on": True})
        self.mode(on=True, ready=True, upstream="ok", fatal="", line_seconds=4, final_seconds=0.1)
        self.call("PUT", "/api/v1/assistant", {"meeting_transcription_default": "device", "meeting_glossary": [], "meeting_transcription_extension": ""})

    def test_features_settings_and_validation_are_atomic(self):
        self.assertIn("meeting_transcription", self.call("GET", "/api/v1/health")[1]["features"])
        cfg = self.call("GET", "/api/v1/assistant")[1]
        self.assertEqual((cfg["meeting_transcription_default"], cfg["meeting_glossary"], cfg["meeting_transcription_extension"], cfg["meeting_transcription_ready"]),
                         ("device", [], "", True))
        cfg = self.call("PUT", "/api/v1/assistant", {"meeting_transcription_default": "hub", "meeting_glossary": [" Alice\n Bo ", "alice bo", "", "导出"],
                                                       "meeting_transcription_extension": "voice/gpt-live", "meeting_transcription_ready": False})[1]
        self.assertEqual((cfg["meeting_glossary"], cfg["meeting_transcription_ready"]), (["Alice Bo", "导出"], True))
        for key, value, code in [("meeting_transcription_default", None, "bad_transcription_mode"),
                                 ("meeting_transcription_default", [], "bad_transcription_mode"),
                                 ("meeting_glossary", ["文" * 41], "bad_glossary"),
                                 ("meeting_glossary", [str(n) for n in range(201)], "bad_glossary"),
                                 ("meeting_glossary", None, "bad_glossary"),
                                 ("meeting_glossary", [3], "bad_glossary"),
                                 ("meeting_transcription_extension", "voice/voice", "bad_transcription_extension"),
                                 ("meeting_transcription_extension", [], "bad_transcription_extension")]:
            status, out = self.call("PUT", "/api/v1/assistant", {"meeting_transcription_default": "device", key: value})
            self.assertEqual((status, out["code"]), (400, code))
            self.assertEqual(self.call("GET", "/api/v1/assistant")[1]["meeting_transcription_default"], "hub")
        for key in ("meeting_transcription_default", "meeting_glossary", "meeting_transcription_extension"):
            self.assertEqual(self.call("PUT", "/api/v1/assistant", {key: cfg[key]}, {"X-Mock-Role": "answer"})[0], 403)
        self.mode(ready=False)
        self.assertFalse(self.call("GET", "/api/v1/assistant")[1]["meeting_transcription_ready"])
        self.call("POST", "/mock/meeting_live_view", {"on": False})
        self.assertNotIn("meeting_transcription", self.call("GET", "/api/v1/health")[1]["features"])
        self.assertNotIn("meeting_transcription_default", self.call("GET", "/api/v1/assistant")[1])

    def test_upload_mode_first_named_wins_and_timeline_is_union(self):
        rid = str(uuid.uuid4())
        for mode in (None, 3, [], "other"):
            self.assertEqual(self.upload(rid, transcription=mode)[1]["transcription"], "device")
        self.mode(ready=False)
        out = self.upload(rid, transcription="hub")[1]
        self.assertEqual((out["transcription"], out["transcription_problem"]), ("device", "unavailable"))
        self.mode(ready=True)
        self.assertEqual(self.upload(rid, transcription="hub")[1]["transcription"], "device")
        self.assertEqual(self.meta(rid)["transcription"], {"mode": "device"})
        note = self.upload(str(uuid.uuid4()), kind="note", transcription="hub")[1]
        self.assertEqual((note["transcription"], note["transcription_problem"]), ("device", "not_meeting"))
        rid = self.recording(timeline={"roles": [{"at": 0, "attendance": "listener"}], "speaking": [{"start": 2, "end": 3, "text": "We keep the cap."}]})
        rev = self.live(rid)["revs"]["transcription"]
        out = self.upload(rid, transcription="device", timeline={"roles": [{"at": 0, "attendance": "participant"}],
                                                                  "speaking": [{"start": 3, "end": 4, "text": "Bo owns it."}, {"start": 2, "end": 3, "text": "We keep the cap."}]})[1]
        self.assertEqual(out["transcription"], "hub")
        # Touching spans are one, their answers joined in time order; the span received again counts once.
        self.assertEqual(self.observed(rid)["timeline"], {"roles": [{"at": 0, "attendance": "participant"}],
                                                          "speaking": [{"start": 2, "end": 4, "text": "We keep the cap. Bo owns it."}]})
        self.assertEqual(self.live(rid)["revs"]["transcription"], rev)
        for bad in ({"roles": [{"at": -1, "attendance": "off"}]}, {"speaking": [{"start": 4, "end": 2}]}, {"speaking": [[0, 1]]},
                    {"speaking": [{"start": 0, "end": 1, "text": 7}]}, {"speaking": [{"start": 0}]}, {"speaking": [{"start": 0, "end": 1}] * 1001}):
            self.upload(rid, timeline=bad)
            self.assertEqual(self.observed(rid)["timeline"]["speaking"], [{"start": 2, "end": 4, "text": "We keep the cap. Bo owns it."}])
        self.upload(rid, timeline={"speaking": [{"start": 6, "end": 7, "text": "答" * 2000}]})
        self.assertEqual(self.observed(rid)["timeline"]["speaking"][1]["text"], "答" * 1333)  # cut at 4000 bytes
        self.assertEqual(self.upload(rid, revision=1, transcription="device")[1]["transcription"], "hub")

    def test_bilingual_lines_interim_revisions_and_final_minutes(self):
        rid = self.recording(language="ja")
        events = websocket(self)
        ws = self.socket(rid)
        self.pcm(ws, 1)
        cap = ws("interim")
        live = self.live(rid, "?parts=interim")
        self.assertEqual(live["interim"], {"start": 0, "text": cap["text"]})
        self.assertGreater(live["revs"]["interim"], 0)
        self.pcm(ws, 3)
        first = ws("line")
        self.assertEqual((first["n"], first["start"], first["end"], first["text"]), (0, 0, 4, "我们先把 export cap 保持在 4 MB。"))
        self.pcm(ws, 1)
        self.assertEqual(ws("ack")["at"], 5)
        self.pcm(ws, 3)
        self.assertEqual(ws("line")["text"], "Let's revisit pricing next week.")
        live = self.live(rid, "?from=0")
        self.assertEqual([s["source"] for s in live["lines"]], ["hub", "hub"])
        self.assertEqual(live["revs"]["lines"], 2)
        event = events("meeting_live", where=lambda f: f["recording_id"] == rid and "lines" in f["changed"])
        self.assertIn("lines", event["revs"])
        self.assertEqual(self.call("POST", f"/api/v1/recordings/{rid}/live", {"from": 0, "segments": []})[1]["code"], "hub_transcribes")
        self.end(ws, 8)
        self.upload(rid, transcription="hub", complete=True, duration=8, segments=[{"start": 0, "end": 8, "text": "weaker device text", "source": "hub"}])
        self.assertEqual(self.meta(rid)["transcription"]["state"], "finishing")
        final = self.wait(lambda: (d := self.detail(rid))["meta"]["transcription"]["state"] == "final" and d, "final minutes")
        self.assertEqual([s["source"] for s in final["segments"]], ["hub", "hub"])
        self.assertEqual(final["minutes"]["points"][1]["text"], "Let's revisit pricing next week.")
        self.assertGreater(final["meta"]["chars"], 0)
        self.assertEqual(self.observed(rid)["held"], [])
        self.assertEqual(self.socket(rid, begin=False)("error")["code"], "transcription_final")
        only = self.live(rid, "?parts=transcription")
        self.assertEqual(set(only), {"transcription", "revs"})
        self.assertGreater(only["revs"]["transcription"], 1)

    def test_echo_hold_back_off_role_and_replacement(self):
        rid = self.recording()
        ws = self.socket(rid)
        ws.send({"type": "speaking", "at": 0, "on": True, "text": "好的。" + CAP_LINE})  # i18n-ignore: synthetic speech
        self.pcm(ws, 4)
        self.assertIsNone(ws("line", wait=0.1))
        self.assertNotIn("interim", self.live(rid))
        ws.send({"type": "speaking", "at": 4, "on": False})
        ws.send({"type": "role", "at": 4, "attendance": "off"})
        self.pcm(ws, 1)
        self.assertIsNotNone(ws("interim"))
        self.assertNotIn("interim", self.live(rid))
        self.pcm(ws, 3)
        self.wait(lambda: self.observed(rid)["bytes"] == 8 * 32000, "stored off-role audio")
        self.assertEqual(self.live(rid)["next"], 0)
        # Persona's echo stays in the hub's transcript, marked; the role-off sentence is there too, unmarked.
        self.assertEqual([(s["text"], s.get("echo", False)) for s in self.detail(rid)["segments"]], [(CAP_LINE, True), (PRICING_LINE, False)])
        replacement = self.socket(rid, begin=False)
        self.assertEqual(ws("error")["code"], "replaced")
        replacement.send({"type": "start", "at": 0, "timeline": {"roles": [{"at": 8, "attendance": "participant"}]}})
        self.assertEqual(replacement("ready"), {"type": "ready", "from": 8, "gaps": [], "next": 0})
        self.pcm(replacement, 4)
        self.assertEqual(replacement("line")["n"], 0)
        self.end(replacement, 12)
        self.upload(rid, complete=True, duration=12)
        final = self.wait(lambda: (d := self.detail(rid))["meta"]["transcription"]["state"] == "final" and d, "the final pass")
        self.assertEqual([s.get("echo", False) for s in final["segments"]], [True, False, False])
        self.assertNotIn("我们先把", final["summary"])
        self.assertEqual(final["meta"]["chars"], sum(len(s["text"]) for s in final["segments"][1:]))
        self.assertNotIn(CAP_LINE.rstrip("。"), json.dumps(final["minutes"], ensure_ascii=False))

    def test_the_note_takers_role_frame(self):
        """A meeting Persona isn't invited to (#8851) sends the role notes: the socket takes it as the hub does, no
        bad_frame, and keeps it on the timeline."""
        rid = self.recording()
        ws = self.socket(rid)
        ws.send({"type": "role", "at": 0, "attendance": "notes"})
        self.pcm(ws, 4)
        self.assertIsNone(ws("error", wait=0.3))
        self.assertIn({"at": 0, "attendance": "notes"}, self.observed(rid)["timeline"]["roles"])
        self.end(ws, 4)

    def test_echo_is_the_answers_words_not_its_time(self):
        rid = self.recording()
        ws = self.socket(rid)
        ws.send({"type": "speaking", "at": 0, "on": True, "text": "Bo will share the notes tomorrow."})
        self.pcm(ws, 4)
        self.assertIsNone(ws("line", wait=0.1))  # held while the span is open
        ws.send({"type": "speaking", "at": 4, "on": False})
        self.assertEqual(ws("line")["text"], CAP_LINE)  # someone else speaking during the answer is the meeting's
        self.assertEqual(self.observed(rid)["timeline"]["speaking"], [{"start": 0, "end": 4, "text": "Bo will share the notes tomorrow."}])
        self.assertNotIn("echo", self.detail(rid)["segments"][0])
        self.end(ws, 4)

    def test_final_pass_progress_while_it_runs(self):
        # #13461: while the pass runs, final_done (0: the mock replays in one step) beside end; neither once final.
        self.mode(final_seconds=3)
        rid = self.recording()
        ws = self.socket(rid)
        self.pcm(ws, 4)
        self.assertEqual(ws("line")["start"], 0)
        self.pcm(ws, 1)
        self.assertEqual(ws("ack")["at"], 5)
        self.pcm(ws, 3)
        self.assertEqual(ws("line")["start"], 4)
        self.end(ws, 8)
        self.upload(rid, transcription="hub", complete=True, duration=8)
        self.assertEqual(self.meta(rid)["transcription"], {"mode": "hub", "state": "finishing", "gaps": [], "final_done": 0, "end": 8})
        final = self.wait(lambda: (t := self.meta(rid)["transcription"])["state"] == "final" and t, "final")
        self.assertNotIn("final_done", final)
        self.assertNotIn("end", final)

    def test_gap_upload_before_final_and_duplicate_audio(self):
        rid = self.recording()
        ws = self.socket(rid)
        ws.send({"type": "seek", "at": 4})
        self.pcm(ws, 4)
        self.assertEqual(ws("line")["start"], 4)
        ws.send({"type": "seek", "at": 4})
        self.pcm(ws, 1)
        self.assertEqual(ws("ack")["at"], 8)
        self.assertEqual(self.live(rid)["next"], 1)
        self.end(ws, 8)
        self.upload(rid, complete=True, duration=8)
        self.assertEqual(self.meta(rid)["transcription"], {"mode": "hub", "state": "finishing", "gaps": [[0, 4]]})
        status, out = self.call("PUT", f"/api/v1/recordings/{rid}/audio?at=0", bytes(4 * 32000))
        self.assertEqual((status, out), (200, {"gaps": []}))
        self.wait(lambda: self.meta(rid)["transcription"]["state"] == "final", "repaired final")
        self.assertEqual(len(self.detail(rid)["segments"]), 2)
        self.assertEqual(self.call("PUT", f"/api/v1/recordings/{rid}/audio?at=0", b"\0\0")[1]["code"], "transcription_final")
        self.assertEqual(self.call("POST", "/mock/recording_trim", {"id": rid})[0], 200)
        self.assertEqual(self.meta(rid)["transcription"], {"mode": "hub", "state": "final", "gaps": []})
        self.assertEqual(self.socket(rid, begin=False)("error")["code"], "transcript_expired")

    def test_problem_recovery_fatal_and_no_interim(self):
        rid = self.recording()
        self.mode(upstream="unavailable")
        ws = self.socket(rid)
        self.assertEqual(ws("problem")["code"], "upstream_unavailable")
        self.pcm(ws, 5)
        self.assertEqual(ws("ack")["at"], 5)
        self.assertEqual(self.live(rid)["next"], 0)
        self.mode(upstream="no_interim")
        self.assertEqual(ws("problem")["code"], "no_interim")
        self.pcm(ws, 4)
        self.assertIsNotNone(ws("line"))
        self.assertNotIn("interim", self.live(rid))
        self.mode(upstream="ok")
        self.assertIsNone(ws("problem", wait=0.3))  # the switch alone recovers nothing: captions do
        self.assertEqual(self.live(rid)["transcription"]["problem"], "no_interim")
        self.pcm(ws, 1)
        self.assertIsNone(ws("problem")["code"])
        self.mode(fatal="upstream_failed")
        self.assertEqual(ws("error"), {"type": "error", "code": "upstream_failed", "fatal": True})
        self.assertEqual(ws("close")["code"], 1008)

    def test_problem_kinds(self):
        # #13446: a refused or busy service is its own problem, not upstream_unavailable; no line goes while it stands.
        rid = self.recording()
        self.mode(upstream="forbidden")
        ws = self.socket(rid)
        self.assertEqual(ws("problem")["code"], "upstream_forbidden")
        self.pcm(ws, 5)
        self.assertEqual(self.live(rid)["next"], 0)
        self.assertEqual(self.live(rid)["transcription"]["problem"], "upstream_forbidden")
        self.mode(upstream="busy")
        self.assertEqual(ws("problem")["code"], "upstream_busy")
        self.assertEqual(self.call("POST", "/mock/meeting_transcription", {"upstream": "made_up"})[0], 400)

    def test_upstream_recovery_waits_for_a_hub_sentence(self):
        rid = self.recording()
        ws = self.socket(rid)
        self.mode(upstream="failed")
        self.assertEqual(ws("problem")["code"], "upstream_failed")
        self.mode(upstream="ok")
        self.assertIsNone(ws("problem", wait=0.3))
        self.assertEqual(self.live(rid)["transcription"]["problem"], "upstream_failed")
        self.pcm(ws, 4)
        self.assertIsNone(ws("problem")["code"])
        self.assertIsNotNone(ws("line"))
        self.assertNotIn("problem", self.live(rid)["transcription"])

    def test_a_repaired_timeline_closes_the_open_speaking_span(self):
        rid = self.recording()
        ws = self.socket(rid)
        ws.send({"type": "speaking", "at": 0, "on": True, "text": "Noted."})
        self.pcm(ws, 1)
        self.wait(lambda: self.observed(rid)["bytes"] == 32000, "the audio before the drop")
        ws.close()  # the speaking off at 1 is lost with the socket
        back = self.socket(rid, timeline={"speaking": [{"start": 0, "end": 1, "text": "Noted."}]})
        self.pcm(back, 4)
        self.assertIsNotNone(back("interim"))
        self.assertEqual(back("line")["start"], 1)
        self.assertEqual(self.observed(rid)["timeline"]["speaking"], [{"start": 0, "end": 1, "text": "Noted."}])
        # An upload's timeline does not close a span while the socket that saw it open is up: its own off comes.
        back.send({"type": "speaking", "at": 5, "on": True, "text": "Again."})
        self.pcm(back, 2)
        self.upload(rid, room={"attendance": "participant"},
                    timeline={"speaking": [{"start": 0, "end": 1, "text": "Noted."}, {"start": 5, "end": 5.5, "text": "Again."}]})
        self.pcm(back, 2)
        self.assertIsNone(back("line", wait=0.3))  # still held for the span
        back.send({"type": "speaking", "at": 7, "on": False})
        self.assertEqual(back("line")["start"], 5)
        self.assertEqual(self.observed(rid)["timeline"]["speaking"],
                         [{"start": 0, "end": 1, "text": "Noted."}, {"start": 5, "end": 7, "text": "Again."}])
        # Once that socket is gone, an upload's timeline repairs a speaking off it lost.
        back.send({"type": "speaking", "at": 9, "on": True, "text": "Once more."})
        self.pcm(back, 4)
        self.wait(lambda: self.observed(rid)["bytes"] == 13 * 32000, "the audio before the drop")
        back.close()
        self.wait(lambda: not self.observed(rid)["socket"], "the socket gone")
        self.assertEqual(self.live(rid)["next"], 2)
        self.upload(rid, room={"attendance": "participant"}, timeline={"speaking": [{"start": 9, "end": 9.5, "text": "Once more."}]})
        self.assertEqual(self.live(rid)["next"], 3)  # the sentence held for it joined the live log

    def test_an_answer_longer_than_the_hold_stays_personas_echo(self):
        rid = self.recording()
        ws = self.socket(rid)
        answer = " ".join((CAP_LINE, PRICING_LINE, "Alice 负责检查 export test。", "Bo will share the notes tomorrow."))  # i18n-ignore: the script
        ws.send({"type": "speaking", "at": 0, "on": True, "text": answer})
        self.pcm(ws, 184)  # past the 180 s hold: what it held goes, and the span stays assumed open
        self.wait(lambda: self.observed(rid)["bytes"] == 184 * 32000, "the audio")
        self.assertIsNone(ws("interim", wait=0.3))  # no caption while it is open
        self.assertEqual(self.live(rid)["next"], 0)  # every sentence so far is Persona's echo
        ws.send({"type": "speaking", "at": 186, "on": False})  # late, and still taken
        self.pcm(ws, 8)
        self.assertEqual(ws("line")["start"], 188)
        self.assertEqual(self.observed(rid)["timeline"]["speaking"], [{"start": 0, "end": 186, "text": answer}])
        segs = self.detail(rid)["segments"]
        self.assertEqual([s.get("echo", False) for s in segs], [True] * 47 + [False])
        self.end(ws, 192)

    def test_a_whole_timeline_fits_the_start_frame(self):
        rid = self.recording()
        said = "答" * 1333 + "a"  # 4000 bytes; i18n-ignore: synthetic speech
        timeline = {"roles": [{"at": i * 30, "attendance": ("listener", "participant")[i % 2]} for i in range(100)],
                    "speaking": [{"start": i * 10, "end": i * 10 + 5, "text": said} for i in range(1000)]}
        start = json.dumps({"type": "start", "at": 0, "timeline": timeline}, ensure_ascii=False).encode()
        self.assertGreater(len(start), 4_000_000)
        ws = self.socket(rid, begin=False)
        ws.send(start, op=1)
        self.assertIsNotNone(ws("ready", wait=10))
        got = self.observed(rid)["timeline"]
        self.assertEqual((len(got["roles"]), len(got["speaking"]), got["speaking"][-1]), (100, 1000, {"start": 9990, "end": 9995, "text": said}))
        self.end(ws, 0)

    def test_a_heard_ask_flushes_the_hub_and_waits_for_fallback_material(self):
        rid = self.recording()
        self.wait(lambda: self.live(rid).get("room", {}).get("state") == "ready", "the room")
        ws = self.socket(rid)
        self.pcm(ws, 3)
        self.wait(lambda: self.observed(rid)["bytes"] == 3 * 32000, "the audio")
        ask = self.call("POST", f"/api/v1/recordings/{rid}/ask", {"id": str(uuid.uuid4()), "at": 3, "live_next": 0})[1]["ask"]
        self.assertNotEqual(ask.get("error"), "nothing_heard")
        line = ws("line")
        self.assertEqual((line["start"], line["end"]), (0, 3))
        self.assertEqual(self.live(rid)["next"], 1)
        self.end(ws, 3)

        other = self.recording()
        self.wait(lambda: self.live(other).get("room", {}).get("state") == "ready", "the room")
        sock = self.socket(other)
        self.mode(fatal="upstream_failed")
        self.assertEqual(sock("error")["code"], "upstream_failed")
        self.assertTrue(self.live(other)["transcription"]["fallback"])
        asked = []
        thread = threading.Thread(target=lambda: asked.append(self.call(
            "POST", f"/api/v1/recordings/{other}/ask", {"id": str(uuid.uuid4()), "at": 9})))
        thread.start()
        self.wait(lambda: thread.is_alive(), "the ask waiting")
        self.upload(other, room={"attendance": "participant"}, segments=[{"start": 6.5, "end": 9.2, "text": "What about pricing?"}])
        thread.join(15)
        status, out = asked[0]
        self.assertEqual(status, 200)
        self.assertNotEqual(out["ask"].get("error"), "nothing_heard")

    def test_final_pass_needs_the_contribution_ready(self):
        rid = self.recording()
        ws = self.socket(rid)
        self.mode(ready=False)
        self.assertEqual(ws("problem")["code"], "upstream_unavailable")
        self.pcm(ws, 4)
        self.assertIsNone(ws("line", wait=0.3))
        self.end(ws, 4)
        self.upload(rid, complete=True, duration=4, segments=[{"start": 0, "end": 4, "text": "Device only."}])
        done = self.wait(lambda: (d := self.detail(rid))["meta"]["transcription"]["state"] in ("final", "failed") and d, "the end")
        self.assertEqual(done["meta"]["transcription"]["state"], "failed")
        self.assertEqual([(s["text"], s.get("source", "device")) for s in done["segments"]], [("Device only.", "device")])

    def test_late_timeline_marks_echo_when_the_replay_cannot_run(self):
        for switch in ({"upstream": "failed"}, {"upstream": "unavailable"}, {"ready": False}, {"fatal": "upstream_failed"}):
            with self.subTest(switch):
                self.mode(upstream="ok", ready=True, fatal="")
                rid = self.recording()
                ws = self.socket(rid)
                self.pcm(ws, 4)
                self.assertIsNotNone(ws("line"))
                self.end(ws, 4)
                self.mode(**switch)
                self.upload(rid, complete=True, duration=4, timeline={"speaking": [{"start": 0, "end": 4, "text": CAP_LINE}]})
                done = self.wait(lambda: (d := self.detail(rid))["meta"]["transcription"]["state"] in ("final", "failed") and d, "the end")
                # The hub's sentence stays, marked Persona's echo: no text of the meeting's, so no minutes.
                self.assertEqual(done["meta"]["transcription"]["state"], "final")
                self.assertEqual([(s["text"], s.get("echo")) for s in done["segments"]], [(CAP_LINE, True)])
                self.assertEqual(done["meta"]["chars"], 0)
                self.assertNotIn("minutes", done)

    def test_transcription_and_interim_events_are_content_free(self):
        rid = self.recording()
        events = websocket(self)
        ws = self.socket(rid)
        self.pcm(ws, 1)
        self.assertIsNotNone(ws("interim"))
        interim = events("meeting_live", where=lambda f: f["recording_id"] == rid and "interim" in f["changed"])
        self.assertEqual(set(interim), {"type", "recording_id", "changed", "revs"})
        self.assertEqual(interim["revs"]["interim"], self.live(rid)["revs"]["interim"])
        self.mode(upstream="failed")
        changed = events("meeting_live", where=lambda f: f["recording_id"] == rid and "transcription" in f["changed"])
        self.assertEqual(changed["revs"]["transcription"], self.live(rid)["revs"]["transcription"])
        self.assertEqual(self.meta(rid)["transcription"]["problem"], "upstream_failed")

    def test_complete_timeline_repairs_final_echo_filter_and_day_ignores_mode(self):
        rid = self.recording()
        ws = self.socket(rid)
        self.pcm(ws, 4)
        self.assertIsNotNone(ws("line"))
        self.end(ws, 4)
        self.upload(rid, complete=True, duration=4, timeline={"speaking": [{"start": 0, "end": 4, "text": CAP_LINE}]})
        self.wait(lambda: self.meta(rid)["transcription"]["state"] == "final", "the final pass")
        self.assertEqual([(s["text"], s.get("echo")) for s in self.detail(rid)["segments"]], [(CAP_LINE, True)])
        day = str(uuid.uuid4())
        out = self.upload(day, kind="day", day="2026-11-10", transcription="hub", timeline={"roles": [{"at": 0, "attendance": "participant"}]})[1]
        self.assertEqual((out["transcription"], out["transcription_problem"]), ("device", "not_meeting"))
        stored = self.call("GET", "/mock/recordings")[1]["recordings"][day]
        self.assertNotIn("timeline", stored)
        self.assertNotIn("transcription", stored)

    def test_recording_refusals_after_upgrade_and_invalid_frames(self):
        missing = str(uuid.uuid4())
        websocket(self, f"/api/v1/recordings/{missing}/audio", headers={"X-Mock-Role": "extension"}, status=403)
        self.assertEqual(self.socket(missing, begin=False)("error")["code"], "recording_not_found")
        self.upload(missing)
        self.assertEqual(self.socket(missing, begin=False)("error")["code"], "not_hub")
        self.call("POST", "/mock/meeting_attendance", {"kb_read_only": True})
        self.assertEqual(self.socket(missing, begin=False)("error")["code"], "kb_read_only")
        self.call("DELETE", f"/api/v1/recordings/{missing}")
        self.assertEqual(self.socket(missing, begin=False)("error")["code"], "recording_deleted")
        self.call("POST", "/mock/meeting_attendance", {"kb_read_only": False})
        for payload, op, begin, claim in [(b"\0\0", 2, False, None), (b"{", 1, True, None), (b"x", 2, True, None), (bytes(65538), 2, True, None),
                                          (b"", 1, True, 8704 * 1024 + 1)]:  # a text frame over 8.5 MiB: refused from its header
            rid = self.recording()
            ws = self.socket(rid, begin=begin)
            ws.send(payload, op=op, claim=claim)
            self.assertEqual(ws("error")["code"], "bad_frame")
        rid = self.recording()
        ws = self.socket(rid)
        ws.send({"type": "future_type"})
        ws.send({"type": "seek", "at": 43200})
        ws.send(b"\0\0")
        self.assertEqual(ws("error")["code"], "audio_full")

    def test_partial_tail_reconnect_and_duplicate_audio_do_not_repeat_lines(self):
        rid = self.recording()
        ws = self.socket(rid)
        self.pcm(ws, 4)
        self.assertEqual(ws("line")["n"], 0)
        ws.send({"type": "seek", "at": 0})
        self.pcm(ws, 8)
        self.assertEqual(ws("line")["n"], 1)
        self.assertEqual(self.live(rid)["next"], 2)
        replacement = self.socket(rid, begin=False)
        self.assertEqual(ws("error")["code"], "replaced")
        replacement.send({"type": "start", "at": 1})
        self.assertEqual(replacement("ready")["next"], 2)
        self.pcm(replacement, 1)
        replacement.send({"type": "end", "at": 9})
        self.assertEqual(replacement("ack")["at"], 9)
        self.assertEqual(replacement("line")["end"], 9)
        self.assertEqual(replacement("close")["code"], 1000)
        self.assertEqual(self.live(rid)["next"], 3)

    def test_fatal_fallback_uses_device_lines_then_hub_positions_continue(self):
        device = {"start": 0, "end": 4, "text": "Device fallback sentence.", "source": "hub"}
        rid = self.recording(segments=[device])
        ws = self.socket(rid)
        self.mode(fatal="upstream_failed")
        self.assertEqual(ws("error")["code"], "upstream_failed")
        live = self.live(rid, "?from=0")
        self.assertTrue(live["transcription"]["fallback"])
        self.assertEqual(live["lines"][0]["source"], "device")
        self.mode(fatal="")
        ws = self.socket(rid)
        self.pcm(ws, 4)
        self.wait(lambda: self.observed(rid)["bytes"] == 4 * 32000, "covered hub sentence")
        self.assertEqual(self.live(rid)["next"], 1)
        self.assertNotIn("fallback", self.live(rid)["transcription"])
        self.pcm(ws, 4)
        self.assertEqual(ws("line")["n"], 1)
        self.end(ws, 8)

    def test_disabled_contribution_is_valid_but_not_ready(self):
        self.assertEqual(self.call("PUT", "/api/v1/extensions/voice/-/enabled", {"enabled": False})[0], 200)
        try:
            status, cfg = self.call("PUT", "/api/v1/assistant", {"meeting_transcription_extension": "voice/gpt-live"})
            self.assertEqual((status, cfg["meeting_transcription_ready"]), (200, False))
        finally:
            self.call("PUT", "/api/v1/extensions/voice/-/enabled", {"enabled": True})

    def test_complete_upload_waits_for_open_socket_and_preserves_device_gaps(self):
        rid = self.recording()
        ws = self.socket(rid)
        self.pcm(ws, 4)
        self.assertIsNotNone(ws("line"))
        self.upload(rid, complete=True, duration=4, segments=[{"start": 8, "end": 12, "text": "Uncovered device span."}])
        self.assertEqual(self.meta(rid)["transcription"]["state"], "finishing")
        self.assertEqual(self.call("PUT", f"/api/v1/recordings/{rid}/audio?at=0", b"\0\0")[0], 200)
        self.end(ws, 4)
        final = self.wait(lambda: (d := self.detail(rid))["meta"]["transcription"]["state"] == "final" and d, "final with device gap")
        self.assertEqual([s["source"] for s in final["segments"]], ["hub", "device"])
        self.assertEqual(final["minutes"]["points"][1]["text"], "Uncovered device span.")

    def test_live_recording_deletion_closes_socket_and_gap_errors_are_ordered(self):
        rid = self.recording()
        ws = self.socket(rid)
        self.call("DELETE", f"/api/v1/recordings/{rid}")
        self.assertEqual(ws("error")["code"], "recording_deleted")
        path = f"/api/v1/recordings/{rid}/audio"
        for body, headers, code in [(bytes((10 << 20) + 2), {"X-Mock-Role": "answer"}, "forbidden"),
                                    (bytes((10 << 20) + 2), {}, "too_large"), (b"x", {}, "recording_deleted")]:
            self.assertEqual(self.call("PUT", path, body, headers)[1]["code"], code)
        self.assertEqual(self.call("PUT", f"/api/v1/recordings/{uuid.uuid4()}/audio", b"x")[1]["code"], "recording_not_found")

    def test_gap_refusal_order_and_failed_without_audio(self):
        rid = self.recording()
        for suffix, body, code, status in [("", b"\0\0", "bad_frame", 400), ("?at=-1", b"\0\0", "bad_frame", 400),
                                          ("?at=nan", b"\0\0", "bad_frame", 400), ("?at=0", b"x", "bad_frame", 400),
                                          ("?at=0", bytes((10 << 20) + 2), "too_large", 413),
                                          ("?at=43200", b"\0\0", "audio_full", 413)]:
            result = self.call("PUT", f"/api/v1/recordings/{rid}/audio{suffix}", body)
            self.assertEqual((result[0], result[1]["code"]), (status, code))
        self.upload(rid, complete=True, duration=0, segments=[{"start": 0, "end": 1, "text": "Only the device heard this.", "source": "hub"}])
        self.assertEqual(self.meta(rid)["transcription"]["state"], "failed")
        self.assertEqual(self.detail(rid)["segments"][0]["source"], "device")
        self.assertEqual(self.detail(rid)["minutes"]["points"][0]["text"], "Only the device heard this.")


class LegacyTranscriptionMockTests(Transcription, unittest.TestCase):
    def test_disabled_and_unready_environment_switches(self):
        for env in ({"MOCK_NO_MEETING_TRANSCRIPTION": "1"}, {"MOCK_NO_MEETING_CONVERSATION": "1"}, {"MOCK_NO_MEETING_LIVE_VIEW": "1"}):
            with self.subTest(env=env):
                self.base, stop = start(env)
                try:
                    self.assertNotIn("meeting_transcription", self.call("GET", "/api/v1/health")[1]["features"])
                    self.assertNotIn("meeting_transcription_ready", self.call("GET", "/api/v1/assistant")[1])
                    rid = str(uuid.uuid4())
                    self.assertNotIn("transcription", self.upload(rid, transcription="hub")[1])
                    self.assertEqual(self.call("GET", f"/api/v1/recordings/{rid}/audio")[0], 404)
                finally: stop()
        self.base, stop = start({"MOCK_MEETING_TRANSCRIPTION_DEFAULT": "hub", "MOCK_MEETING_TRANSCRIPTION_READY": "0"})
        try:
            cfg = self.call("GET", "/api/v1/assistant")[1]
            self.assertEqual((cfg["meeting_transcription_default"], cfg["meeting_transcription_ready"]), ("hub", False))
        finally: stop()

    def test_device_token_header_and_query_authentication(self):
        self.base, stop = start({"MOCK_DEVICE_TOKEN": "fixture-device-token"})
        try:
            rid = str(uuid.uuid4())
            self.call("PUT", f"/api/v1/recordings/{rid}", {"revision": 1, "started_at": "2026-11-10T14:00:00Z", "device": "phone",
                      "kind": "meeting", "transcription": "hub", "segments": []}, {"Authorization": "Bearer fixture-device-token"})
            path = f"/api/v1/recordings/{rid}/audio"
            websocket(self, path, status=403)
            for route, headers in [(path, {"Authorization": "Bearer fixture-device-token"}), (path + "?token=fixture-device-token", {})]:
                ws = websocket(self, route, headers=headers)
                ws.send({"type": "start", "at": 0})
                self.assertIsNotNone(ws("ready"))
                self.end(ws, 0)
        finally: stop()

    def test_offline_refusal_is_before_upgrade(self):
        self.base, stop = start({"MOCK_RECORDINGS_DOWN": "1"})
        try:
            path = f"/api/v1/recordings/{uuid.uuid4()}/audio"
            websocket(self, path, status=503)
            websocket(self, path, headers={"X-Mock-Role": "answer"}, status=403)
        finally: stop()


class MeetingEchoRuleTests(unittest.TestCase):
    """The mock's echo rule (hub mode) says what the hub's and the phone's do: spec/fixtures/meeting-echo."""

    def test_the_shared_cases(self):
        spec = importlib.util.spec_from_file_location("echo_rule_mock", HERE / "mock_hub.py")
        hub = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(hub)
        cases = json.loads((HERE.parents[1] / "spec" / "fixtures" / "meeting-echo" / "cases.json").read_text())["cases"]
        self.assertGreaterEqual(len(cases), 20)
        for c in cases:
            line = c["input"]["line"]
            got = any(hub.echo_by(sp, line["start"], line["end"], line["text"]) for sp in c["input"]["spans"])
            self.assertEqual(got, c["expected"]["echo"], c["name"])


if __name__ == "__main__":
    unittest.main()
