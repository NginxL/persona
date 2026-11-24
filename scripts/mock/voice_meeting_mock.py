"""MOCK_VOICE's meeting calls (#3862 phase 4, docs/assistant/meeting-minutes.md "Phase 4: realtime voice in the meeting";
docs/assistant/voice.md "App → extension"): the voice extension's `meeting` feature on the protocol 2 socket of
voice_mock.py, against mock_hub.py's own meeting state (its rooms, MEETING_CONV). voice_mock.py loads it in `install`;
everything here is made up.

  GET …/voice/info, ready          `meeting` in the features (MOCK_VOICE_MEETING=off, or GET /mock/voice_meeting?meeting=off:
                                   an extension from before phase 4, which knows neither the mode nor the refusal below)
  start {"mode": "meeting"}        a meeting call: protocol 2, `recording_id` and `target` naming that recording's room,
                                   which must be `ready` (as the extension reads GET …/live); anything else is the fatal
                                   error not_meeting_room. `echo_cancellation` is logged
  start without the mode           a protocol 2 start bound to a room (origin meeting) is the fatal error meeting_room
  {"type":"talk","on":true|false}  a Talk span. Audio frames are counted inside and outside spans; one outside a span is
                                   what the extension replaces by silence (logged once per run of them). After `talk off`
                                   with audio in the span the call answers by script: the user's caption (partial, final),
                                   the question handed to the room as a run (sending → sent → working → done), the
                                   question and the room's answer in the room's thread, the answer as a caption and 24 kHz
                                   silence as its audio. An empty span is answered by nothing; a talk frame without `on`
                                   is off, one whose `on` is not a bool changes nothing. Talk frames in an ordinary call
                                   are ignored (voice_mock.py counts them as talks_ignored)
  text                             a typed question: the same script from its caption on
  GET /mock/voice_meeting          what the app sent to meeting calls: their starts (the newest as `start`), the
                                   refusals, the talk frames and spans, audio frames inside and outside spans, the questions
                                   answered; ?reset=1 forgets them, ?meeting=on|off switches the feature
A meeting call never pauses its upstream (no link paused: decision 7 keeps it connected for the whole meeting) and
never ends after idle minutes. The pace is voice_mock.py's (MOCK_VOICE_PACE).
"""
import json
import os
import struct
import threading
import time
import uuid

import voice_mock

STATE = {"on": os.environ.get("MOCK_VOICE_MEETING", "") != "off"}


def _fresh():
    return {"calls": 0, "starts": [], "refused": [], "talk_on": 0, "talk_off": 0, "talk_repeats": 0, "spans": 0,
            "spans_empty": 0, "talking": False, "audio_in": 0, "audio_in_bytes": 0, "audio_out": 0, "texts": [],
            "questions": [], "answers": 0, "closes": 0}


LOG = _fresh()
# Fields of a start that /mock/voice_meeting shows (the wire's: docs/assistant/meeting-minutes.md "Phase 4: realtime voice in the meeting").
START_FIELDS = ("protocol", "mode", "recording_id", "target", "echo_cancellation", "model", "voice", "recent", "history", "resume")
# The script's steps after a question, in seconds (times voice_mock.PACE): the caption, the hand-over, the answer, the
# end of its audio.
PARTIAL, FINAL, SENDING, SENT, DONE, QUIET = 0.4, 0.8, 1.0, 1.3, 2.2, 4.2
# Fatal errors close the socket as the extension does (websocket.StatusInternalError).
FATAL_CLOSE = 1011


def install(hub):
    """The route for /mock/voice_meeting (voice_mock.py's route asks it first) and the features with `meeting`."""
    old_features = voice_mock.features

    def features():
        return old_features() + (["meeting"] if STATE["on"] else [])

    # voice_mock.py's /info, ready and serve read the module's `features` when they run.
    voice_mock.features = features
    hub.MOCK_HELP.append("MOCK_VOICE_MEETING=off  The voice extension without meeting calls (#3862 phase 4): no `meeting` in its "
                         "features, a start with mode meeting and one bound to a room are ordinary calls; by default a meeting call "
                         "on a ready room answers each Talk span by script (scripts/mock/voice_meeting_mock.py); GET "
                         "/mock/voice_meeting shows what the app sent (?reset=1, ?meeting=on|off)")

    def route(handler, q):
        how = (q.get("meeting") or [None])[0]
        if how in ("on", "off"):
            STATE["on"] = how == "on"
        if (q.get("reset") or [""])[0] in ("1", "true"):
            LOG.clear()
            LOG.update(_fresh())
            voice_mock.LOG["talks_ignored"] = 0
        handler.send_json(dict(LOG, meeting=STATE["on"], start=LOG["starts"][-1] if LOG["starts"] else None,
                               talks_ignored=voice_mock.LOG.get("talks_ignored", 0), pace=voice_mock.PACE))
        return True
    return route


def room_session(hub, target):
    """The room conversation (origin meeting) a start's target names, None for any other."""
    mid, sid = target.get("machine_id", ""), target.get("session_id", "")
    return next((s for s in hub.ASSISTANT if s["machine_id"] == mid and s["session_id"] == sid and s.get("origin") == "meeting"), None)


def take(hub, h, first):
    """Whether this module answers the socket whose first frame is `first`: a meeting call, or an ordinary protocol 2
    call bound to a room, which a room never takes (docs/assistant/meeting-minutes.md "Phase 4: realtime voice in the meeting")."""
    if not STATE["on"] or first.get("type") != "start":
        return False
    if first.get("mode") == "meeting":
        MeetingCall(hub, h, first).run()
        return True
    target = first.get("target")
    if (first.get("protocol") or 1) >= 2 and isinstance(target, dict) and room_session(hub, target):
        call = voice_mock.Call(hub, h, first)
        refuse(call, "meeting_room", "a room takes only meeting calls", "这是会议的对话，只接会议通话")
        return True
    return False


def refuse(call, code, why, message):
    """The fatal error `code`, and the socket closed after it."""
    LOG["refused"].append({"code": code, "why": why, "mode": call.start.get("mode"), "recording_id": call.start.get("recording_id"),
                           "target": call.start.get("target")})
    call.hub.LOG.write("  VOICE meeting: fatal %s (%s)\n" % (code, why))
    call.hub.LOG.flush()
    try:
        call.say({"type": "error", "code": code, "message": message, "fatal": True})
        call.frame(0x8, struct.pack(">H", FATAL_CLOSE))
    except OSError:
        pass
    call.open = False
    call.h.close_connection = True


class MeetingCall(voice_mock.Call):
    """One meeting call: bound to the room for its whole length, hearing only Talk spans."""

    def __init__(self, hub, h, start):
        super().__init__(hub, h, start)
        self.rid = str(start.get("recording_id") or "").strip().lower()
        self.talking = False
        self.heard = 0  # audio frames in the open span
        self.outside = False  # inside a run of audio frames outside a span (logged once)
        self.asked = 0
        LOG["calls"] += 1
        LOG["starts"].append({k: start.get(k) for k in START_FIELDS})

    def problem(self):
        """Why this start is not a meeting call on a ready room ("" when it is), as the extension finds it with GET …/live."""
        hub = self.hub
        if (self.start.get("protocol") or 1) < 2:
            return "not protocol 2"
        if not self.mid or not self.sid:
            return "no target"
        if not self.rid:
            return "no recording_id"
        if self.rid in hub.RECORDINGS_DELETED or self.rid not in hub.RECORDINGS:
            return "no such recording"
        m = hub.MEETING_CONV.get(self.rid) if hub.meeting_conversation_on() else None
        view = hub.room_view(m)
        if not view:
            return "not a room meeting"
        if view.get("machine_id") != self.mid or view.get("session_id") != self.sid:
            return "the target is not this recording's room"
        if view["state"] != "ready":
            return "the room is %s" % view["state"]
        return ""

    def zh(self):
        lang = str((self.hub.RECORDINGS.get(self.rid) or {}).get("language") or "")
        return lang.lower().startswith("zh")

    # the answer to one question, by script

    def answer(self, typed=None):
        self.asked += 1
        threading.Thread(target=self._answer, args=(self.asked, typed), daemon=True).start()

    def _answer(self, n, typed):
        zh = self.zh()
        question = typed or ("上次导出上限是怎么定的？" if zh else "What did we decide on the export cap?")
        reply = ("上次定的是保留 4 MB 的导出上限，回归测试由 Bo 负责。" if zh else
                 "You kept the 4 MB export cap; Bo owns the regression test.")
        uid, aid, run = "u%d" % n, "a%d" % n, "voice-" + uuid.uuid4().hex[:16]
        began = time.monotonic()
        steps = [] if typed else [(PARTIAL, lambda: self.caption(uid, "user", "上次导出上限" if zh else "What did we decide", final=False)),
                                  (FINAL, lambda: self.caption(uid, "user", question))]
        state = {"sent": False}

        def sent():
            # delegate_task: the question into the room as the owner's message (voice-…), as POST …/messages sends it
            room = self.hub.room_view(self.hub.MEETING_CONV.get(self.rid))
            if not room or room.get("state") != "ready":
                self.run_frame(run, "failed", code="meeting_ended", message="The meeting has ended: ask in its follow-up.")
                return
            self.hub.conv_append(self.mid, self.sid, [self.hub.conv_item("user", question, client_msg_id=run)])
            self._state("working")
            self.left_working = True
            self.run_frame(run, "sent")
            self.run_frame(run, "working")
            state["sent"] = True

        def done():
            if not state["sent"]:
                return
            self.hub.conv_append(self.mid, self.sid, [self.hub.conv_item("assistant", reply)])
            self._state("idle")
            self.left_working = False
            self.run_frame(run, "done")
            LOG["answers"] += 1  # before the caption: a test reads the log as soon as the caption arrives
            self.caption(aid, "assistant", reply)
            self.speak(True)

        steps += [(SENDING, lambda: self.run_frame(run, "sending", task=question)), (SENT, sent), (DONE, done),
                  (QUIET, lambda: self.speak(False))]
        LOG["questions"].append(question)
        for at, step in steps:
            if self.cut.wait(max(began + at * voice_mock.PACE - time.monotonic(), 0)) or not self.open:
                return
            try:
                step()
            except OSError:
                return

    # the socket

    def talk(self, on):
        if on is True and not self.talking:
            self.talking, self.heard = True, 0
            LOG.update(talking=True, talk_on=LOG["talk_on"] + 1, spans=LOG["spans"] + 1)
        elif on is False and self.talking:
            self.talking = False
            LOG.update(talking=False, talk_off=LOG["talk_off"] + 1)
            if self.heard:
                self.answer()
            else:
                LOG["spans_empty"] += 1  # the model heard nothing: nothing to answer
        else:
            LOG["talk_repeats"] += 1  # on while on, off while off, or not a bool (a frame the extension cannot read): nothing

    def run(self):
        why = self.problem()
        if why:
            refuse(self, "not_meeting_room", why, "这不是这场会议正在进行的会议对话")
            return
        self.hub.LOG.write("  VOICE meeting call: %s, echo cancellation %s\n" % (self.rid, self.start.get("echo_cancellation")))
        self.hub.LOG.flush()
        self.say({"type": "ready", "protocol": 2, "features": voice_mock.features(), "input_rate": 16000, "output_rate": 24000,
                  "model": self.start.get("model") or self.hub.VOICE_MODELS["default"], "voice": self.start.get("voice"),
                  "target": {"machine_id": self.mid, "session_id": self.sid, "kind": "assistant",
                             "title": (self.session or {}).get("title", ""), "agent_label": "Persona", "machine_name": self.mid}})
        r = self.h.rfile
        try:
            while True:
                frame = voice_mock._read_frame(r)
                if frame is None:
                    break
                op, payload = frame
                if op == 2:
                    if self.talking:
                        self.heard += 1
                        LOG["audio_in"] += 1
                        LOG["audio_in_bytes"] += len(payload)
                        self.outside = False
                    else:
                        LOG["audio_out"] += 1
                        if not self.outside:  # what the extension feeds the upstream instead is silence
                            self.outside = True
                            self.hub.LOG.write("  VOICE meeting: audio outside a Talk span, replaced by silence\n")
                            self.hub.LOG.flush()
                    continue
                if op == 8:
                    break
                if op == 9:
                    self.frame(0xA, payload)
                    continue
                if op != 1:
                    continue
                try:
                    msg = json.loads(payload or b"{}")
                except ValueError:
                    continue
                kind = msg.get("type") if isinstance(msg, dict) else None
                self.hub.LOG.write("  VOICE meeting %s\n" % kind)
                self.hub.LOG.flush()
                if kind == "talk":
                    self.talk(msg.get("on", False))  # as the extension reads it: no `on` is false
                elif kind == "text":
                    text = str(msg.get("text") or "").strip()
                    if text:
                        LOG["texts"].append(text)
                        self.caption("t%d" % len(LOG["texts"]), "user", text)
                        self.answer(text)
                elif kind == "interrupt":
                    voice_mock.LOG["interrupts"] += 1
                    if self.speaking:
                        self.speak(False)
                        self.say({"type": "clear"})
        except OSError:
            pass
        finally:
            self.open = False
            self.cut.set()
            self.h.close_connection = True
            if self.talking:
                LOG["talking"] = False
            if self.left_working:
                self._state("idle")
            LOG["closes"] += 1
