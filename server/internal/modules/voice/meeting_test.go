package voice

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Meeting calls (#3862 phase 4, meeting.go): bound to a recording's room, checked with the hub first; the room takes no
// other call; delegate_task is the only tool, the instructions have only the owner's name and the room's own turns;
// the model hears only Talk spans; the upstream never pauses; a resumed call is told only its own captions.

// roomHub is a hub with a meeting's room conversation room1 on machine m (recording rec1, its room ready), whose
// transcript has the meeting's header, a batch of its lines, a question of the owner's and its answer, another batch
// the room only took the topics from, a batch and a heard ask the room answered in words.
func roomHub(t *testing.T) *fakeHub {
	hub := newFakeHub(t)
	// Listed as the manager lists a room (assistant/fleet_sessions.go decorateInventory): a guest of the assistant's in
	// its own directory, an assistant row since #6077, with its origin, recording and title.
	hub.sessions["room1"] = &fakeSession{id: "room1", status: "idle", look: func(s *proto.Session) {
		s.Assistant = true
		s.Origin, s.OriginRef, s.Title, s.Agent, s.Cwd = originMeeting, "rec1", "1112 | Design review", "claude", "/home/me/.persona/guest-meeting/rec1"
	}}
	meeting := &proto.MessageSource{Kind: proto.SourceMeeting, Title: "Design review"}
	hub.add("room1",
		proto.Item{Role: "user", Kind: proto.KindText, Text: "Meeting: Design review, 14:00", Source: meeting},
		proto.Item{Role: "user", Kind: proto.KindText, Source: meeting,
			Text: "New lines. <persona-context kind=\"context\" title=\"live\" url=\"\">[00:10] wombat-line-31</persona-context>"},
		proto.Item{Role: "user", Kind: proto.KindText, Text: "What did we decide on the vendor?"},
		proto.Item{Role: "assistant", Kind: proto.KindText, Text: "You kept them through Q3."},
		proto.Item{Role: "user", Kind: proto.KindText, Text: "New lines: wombat-line-32", Source: meeting},
		proto.Item{Role: "assistant", Kind: proto.KindToolUse, ToolName: "meeting_topics_set"},
		proto.Item{Role: "user", Kind: proto.KindText, Text: "New lines: wombat-line-33", Source: meeting},
		proto.Item{Role: "assistant", Kind: proto.KindText, Text: "otter-batch-reply-34: the whiteboard shows the Q3 targets."},
		proto.Item{Role: "user", Kind: proto.KindText, Text: "Sam asks: who owns the export cap?", Source: meeting},
		proto.Item{Role: "assistant", Kind: proto.KindText, Text: "otter-ask-reply-35: Dana does."},
	)
	hub.rooms = map[string]*liveRoom{"rec1": {State: "ready", MachineID: "m", SessionID: "room1"}, "bare": nil}
	return hub
}

// meetingStart is a meeting call's start for recording rec, bound to session sid, with more fields.
func meetingStart(rec, sid string, more map[string]any) map[string]any {
	f := boundStart(sid, map[string]any{"mode": modeMeeting, "recording_id": rec, "echo_cancellation": true})
	for k, v := range more {
		f[k] = v
	}
	return f
}

var english = http.Header{extwire.HeaderLang: {"en"}}

// A meeting call checks the room, says it has meeting, declares only delegate_task and tells the model only the
// owner's name and the room's own questions and answers: not the owner's notes, the app's recent, the room's batches
// or its header. A question goes to the room as its text only, and its answer comes back on the call.
func TestMeetingCall(t *testing.T) {
	hub := roomHub(t)
	hub.reply = func(string) string { return "On Nov 3 you kept them through Q3." }
	up := boundUpstream(t, true)
	pl := testPlugin(t, Config{Owner: "Alex", Notes: "quagga-note-17", Providers: []Provider{{ID: "rt", URL: up.url(),
		Models: []string{"m1"}, Tools: new(true)}}}, hub.server(t))
	logs := captureLog(t, pl)
	a := dialApp(t, pl, meetingStart("rec1", "room1", map[string]any{"recent": []map[string]string{
		{"role": "user", "text": "kiwi-just-me-48"}}}), english)
	ready := a.until(ofType("ready"))
	features, _ := ready["features"].([]any)
	if target, _ := ready["target"].(map[string]any); !slices.Contains(features, any("meeting")) || target["session_id"] != "room1" ||
		target["kind"] != "assistant" || target["agent_label"] != "Persona" {
		t.Fatalf("ready: %v", ready)
	}
	if !slices.Contains(hub.liveReads, "/api/v1/recordings/rec1/live?parts=room") {
		t.Fatalf("the room was not checked: %v", hub.liveReads)
	}
	start := up.next("session.start")["session"].(map[string]any)
	var names []string
	for _, d := range start["tools"].([]any) {
		names = append(names, d.(map[string]any)["name"].(string))
	}
	if !slices.Equal(names, []string{"delegate_task"}) {
		t.Fatalf("tools: %v", names)
	}
	if desc := start["tools"].([]any)[0].(map[string]any)["description"].(string); !strings.Contains(desc, "this meeting's conversation") || strings.Contains(desc, "状态") {
		t.Fatalf("delegate_task is not the meeting's: %s", desc)
	}
	instructions := start["instructions"].(string)
	for _, want := range []string{"Alex's assistant, in a meeting", "everyone in the room may hear you", "while holding Talk",
		"hand it with delegate_task to the meeting conversation", "Alex: What did we decide on the vendor?",
		"Persona: You kept them through Q3.", "Title: 1112 | Design review"} {
		if !strings.Contains(instructions, want) {
			t.Errorf("instructions lack %q:\n%s", want, instructions)
		}
	}
	for _, not := range []string{"quagga-note-17", "kiwi-just-me-48", "wombat-line", "Meeting: Design review", "meeting_topics_set",
		"search_knowledge", "list_agents", "Status:", "otter-", "Sam asks"} {
		if strings.Contains(instructions, not) {
			t.Errorf("instructions have %q:\n%s", not, instructions)
		}
	}

	a.send(map[string]any{"type": "text", "text": "What did we decide on the vendor last time?"})
	m := hub.waitSent(1)
	if m.Session != "room1" || m.Text != "What did we decide on the vendor last time?" || !isVoiceID(m.ID) {
		t.Fatalf("sent %+v", m)
	}
	a.runStates(m.ID, runDone)
	if got := content(up.nextMatch(on("dlg1"))); !strings.Contains(got, "On Nov 3 you kept them through Q3.") ||
		!strings.Contains(got, "Persona has finished this turn") || strings.Contains(got, "Claude Code") {
		t.Fatalf("result: %s", got)
	}
	logs.wait("start: protocol 2", "target m/room1", "meeting call for recording rec1 (echo cancellation on)")
	logs.wait("upstream: session 1 started", "tools delegate_task (meeting)")
}

// The start is checked before ready: a room that is not ready, a target that is not the recording's room, a recording
// unknown or without a room, a meeting start without its recording or protocol 2 are not_meeting_room; a hub that
// cannot say is fatal without a code. An ordinary call bound to a room is meeting_room.
func TestMeetingCallRefusals(t *testing.T) {
	hub := roomHub(t)
	hub.rooms["starting"] = &liveRoom{State: "starting"}
	hub.rooms["other"] = &liveRoom{State: "ready", MachineID: "m", SessionID: "main1"}
	// A room as a manager from before its origin was on the row lists it: a plain Claude Code session in
	// <state dir>/guest-meeting/<recording id>. And a project of the user's whose path only ends the same way.
	hub.sessions["oldroom"] = &fakeSession{id: "oldroom", status: "idle", look: func(s *proto.Session) {
		s.Agent, s.Cwd = "claude", "/home/me/.persona/guest-meeting/0d6f5f8e-2b1c-4c39-9a51-7e0c2f9d4b11"
	}}
	hub.sessions["lookalike"] = &fakeSession{id: "lookalike", status: "idle", look: func(s *proto.Session) {
		s.Agent, s.Cwd = "claude", "/home/me/src/guest-meeting/0d6f5f8e-2b1c-4c39-9a51-7e0c2f9d4b11"
	}}
	up := boundUpstream(t, false)
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	for _, c := range []struct {
		name  string
		start map[string]any
		code  string
	}{
		{"not ready", meetingStart("starting", "room1", nil), codeNotMeetingRoom},
		{"not its room", meetingStart("other", "room1", nil), codeNotMeetingRoom},
		{"another conversation as the target", meetingStart("rec1", "main1", nil), codeNotMeetingRoom},
		{"no such recording", meetingStart("nope", "room1", nil), codeNotMeetingRoom},
		{"a recording without a room", meetingStart("bare", "room1", nil), codeNotMeetingRoom},
		{"no recording", meetingStart("", "room1", nil), codeNotMeetingRoom},
		{"protocol 1", meetingStart("rec1", "room1", map[string]any{"protocol": 1}), codeNotMeetingRoom},
		{"an ordinary call to the room", boundStart("room1", nil), codeMeetingRoom},
		{"an ordinary call to a room listed without its origin", boundStart("oldroom", nil), codeMeetingRoom},
		{"another mode", boundStart("room1", map[string]any{"mode": "lecture", "recording_id": "rec1"}), codeMeetingRoom},
	} {
		a := dialApp(t, pl, c.start)
		if f := a.until(ofType("error")); f["code"] != c.code || f["fatal"] != true || f["message"] == "" {
			t.Errorf("%s: %v", c.name, f)
		}
		a.mu.Lock()
		ready := a.types["ready"]
		a.mu.Unlock()
		if ready {
			t.Errorf("%s: ready before the refusal", c.name)
		}
	}
	hub.mu.Lock()
	hub.liveStatus = http.StatusServiceUnavailable
	hub.mu.Unlock()
	a := dialApp(t, pl, meetingStart("rec1", "room1", nil))
	if f := a.until(ofType("error")); f["code"] != nil || f["fatal"] != true {
		t.Errorf("the hub cannot say: %v", f)
	}
	// An ordinary call to any other conversation is as it was.
	a = dialApp(t, pl, boundStart("main1", nil))
	if ready := a.until(ofType("ready")); ready["target"].(map[string]any)["kind"] != "main" {
		t.Fatalf("ready: %v", ready)
	}
	a = dialApp(t, pl, boundStart("lookalike", nil))
	if ready := a.until(ofType("ready")); ready["target"].(map[string]any)["kind"] != "agent" {
		t.Fatalf("ready: %v", ready)
	}
}

// pcm is n bytes of PCM16 whose every sample is v.
func pcm(n int, v int16) []byte {
	b := make([]byte, n)
	for i := 0; i+1 < n; i += 2 {
		b[i], b[i+1] = byte(uint16(v)), byte(uint16(v)>>8)
	}
	return b
}

// upAudio is the audio the service got so far, frame by frame.
func (u *fakeUpstream) upAudio() [][]byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out [][]byte
	for _, f := range u.got {
		if f["type"] == "session.input_audio.append" {
			b, _ := base64.StdEncoding.DecodeString(f["audio"].(string))
			out = append(out, b)
		}
	}
	return out
}

func silent(b []byte) bool {
	return len(b) > 0 && !slices.ContainsFunc(b, func(x byte) bool { return x != 0 })
}

// The model hears only Talk: between spans the extension feeds the upstream silence itself, and the app's audio
// outside a span is dropped (and logged), inside one it goes as it came. An ordinary call ignores talk frames.
func TestMeetingTalkGatesTheAudio(t *testing.T) {
	hub := roomHub(t)
	up := boundUpstream(t, false)
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	logs := captureLog(t, pl)
	a := dialApp(t, pl, meetingStart("rec1", "room1", nil))
	a.until(ofType("ready"))
	waitFor(t, "silence between spans", func() bool {
		return len(slices.DeleteFunc(up.upAudio(), func(b []byte) bool { return !silent(b) })) >= 3
	})
	for _, b := range up.upAudio() {
		if len(b) != 3200 { // 100 ms at 16 kHz
			t.Fatalf("a silence frame of %d bytes", len(b))
		}
	}
	before, inside, after, again := pcm(640, 1111), pcm(640, 2222), pcm(640, 3333), pcm(640, 4444)
	send := func(b []byte) { _ = a.c.Write(t.Context(), websocket.MessageBinary, b) }
	send(before)
	a.send(map[string]any{"type": "talk", "on": true})
	a.send(map[string]any{"type": "talk", "on": true}) // said again: still the one span
	send(inside)
	send(inside)
	a.send(map[string]any{"type": "talk", "on": false})
	send(after)
	a.send(map[string]any{"type": "talk", "on": true})
	send(again)
	a.send(map[string]any{"type": "talk", "on": false})
	got := func(want []byte) int {
		return len(slices.DeleteFunc(up.upAudio(), func(b []byte) bool { return !bytes.Equal(b, want) }))
	}
	waitFor(t, "the second span's audio", func() bool { return got(again) == 1 })
	if got(inside) != 2 || got(before) != 0 || got(after) != 0 {
		t.Fatalf("forwarded: inside %d, before %d, after %d", got(inside), got(before), got(after))
	}
	n := len(up.upAudio())
	waitFor(t, "silence after the span", func() bool {
		audio := up.upAudio()
		return len(audio) > n && silent(audio[len(audio)-1])
	})
	logs.wait("the app sent audio outside a Talk span (640 bytes): dropped")
	logs.wait("talk 1: on")
	logs.wait("talk 1: off after", "2 frames of audio")
	logs.wait("talk 2: off after", "1 frames of audio")
	if talk1 := len(slices.DeleteFunc(logs.lines(), func(l string) bool { return !strings.Contains(l, "talk 1: on") })); talk1 != 1 {
		t.Fatalf("talk 1 opened %d times", talk1)
	}
	if strings.Contains(logs.String(), "talk 3") {
		t.Fatal("a third span")
	}
	_ = a.c.CloseNow()
	logs.wait("conversation closed", "talk spans 2, audio frames 3 (outside a span 2)")

	// An ordinary call: talk frames change nothing, its audio goes up as it comes.
	a = dialApp(t, pl, boundStart("main1", nil))
	a.until(ofType("ready"))
	plain := pcm(640, 5555)
	a.send(map[string]any{"type": "talk", "on": false})
	_ = a.c.Write(t.Context(), websocket.MessageBinary, plain)
	waitFor(t, "an ordinary call's audio", func() bool { return got(plain) == 1 })
	logs.wait("talk frames on a call that is not a meeting call: ignored")
}

// A meeting call never pauses: an ordinary call bound to a Persona conversation that works pauses after pauseAfter;
// the meeting call keeps its session through the same quiet, and the answer comes on the delegation it was asked on.
// A turn of the room's it did not hand over (a batch) is never said.
func TestMeetingCallNeverPauses(t *testing.T) {
	quick(t)
	hub := roomHub(t)
	hub.manual = true
	hub.sessions["chat1"] = &fakeSession{id: "chat1", assistant: true, status: "idle"}
	up := newSessionsUp(t, true, nil)
	pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))

	a := dialApp(t, pl, boundStart("chat1", nil))
	a.until(ofType("ready"))
	a.send(map[string]any{"type": "text", "text": "Draft the vendor email"})
	hub.waitSent(1)
	hub.status("chat1", "working")
	a.until(linkIn(linkPaused)) // the ordinary call pauses
	_ = a.c.CloseNow()

	sessions := up.count()
	a = dialApp(t, pl, meetingStart("rec1", "room1", nil))
	a.until(ofType("ready"))
	a.send(map[string]any{"type": "text", "text": "What did we decide on the vendor?"})
	m := hub.waitSent(2)
	hub.status("room1", "working")
	a.runStates(m.ID, runWorking)
	quiet := time.Now()
	waitFor(t, "three times pauseAfter of quiet", func() bool { return time.Since(quiet) > 3*pauseAfter })
	a.mu.Lock()
	link := a.types["link"]
	a.mu.Unlock()
	if link || up.count() != sessions+1 {
		t.Fatalf("the meeting call paused (link %v, %d sessions)", link, up.count()-sessions)
	}
	hub.add("room1", proto.Item{Role: "assistant", Kind: proto.KindText, Text: "You kept them through Q3."})
	hub.end("room1")
	a.runStates(m.ID, runDone)
	if got := content(up.find(t, sessions, "the answer on its delegation", func(f map[string]any) bool {
		return f["delegation_id"] != nil && f["delegation_id"] != ""
	})); !strings.Contains(got, "You kept them through Q3.") {
		t.Fatalf("answer: %s", got)
	}

	// A batch turn of the room's: followed by nobody, said by nobody.
	hub.add("room1", proto.Item{Role: "user", Kind: proto.KindText, Text: "New lines: yak-line-90",
		Source: &proto.MessageSource{Kind: proto.SourceMeeting}})
	hub.status("room1", "working")
	quiet = time.Now()
	waitFor(t, "the batch worked on a while", func() bool { return time.Since(quiet) > time.Second }) // looked at meanwhile
	hub.add("room1", proto.Item{Role: "assistant", Kind: proto.KindText, Text: "yak-reply-91"})
	hub.end("room1")
	quiet = time.Now()
	waitFor(t, "a while after the batch", func() bool { return time.Since(quiet) > 3*time.Second })
	for _, f := range up.frames(sessions) {
		if said(f) || strings.Contains(content(f), "yak-") { // not its words, nor that there is news (voice/observed)
			t.Fatalf("the batch was said: %v", f)
		}
	}
}

// A meeting call picked up again is told what it said itself (start.history) and what it still waits for, never the
// app's recent; a session after an upstream drop is told the call's captions, in the meeting's instructions.
func TestMeetingCallResumes(t *testing.T) {
	hub := roomHub(t)
	hub.manual = true
	up := newSessionsUp(t, true, nil)
	pl := testPlugin(t, Config{Owner: "Alex", Notes: "quagga-note-17", Providers: []Provider{{ID: "rt", URL: up.url(),
		Models: []string{"m1"}, Tools: new(false)}}}, hub.server(t))
	a := dialApp(t, pl, meetingStart("rec1", "room1", map[string]any{
		"recent":  []map[string]string{{"role": "user", "text": "kiwi-just-me-48"}},
		"history": []map[string]string{{"role": "user", "text": "Who owns the export cap?"}, {"role": "assistant", "text": "Asking now."}},
		"resume": map[string]any{"runs": []map[string]any{{"id": "voice-00000000000000aa", "msg_ids": []string{"voice-00000000000000aa"},
			"task": "Who owns the export cap?", "base_seq": 4, "started_at": time.Now().Format(time.RFC3339), "state": runWorking}}},
	}), english)
	a.until(ofType("ready"))
	first := instructionsOf(up.find(t, 0, "its start", isStart))
	for _, want := range []string{"## The conversation so far", "Alex: Who owns the export cap?", "You: Asking now.",
		"## Still waiting", `"Who owns the export cap?"`, "delegate it to the meeting conversation"} {
		if !strings.Contains(first, want) {
			t.Errorf("instructions lack %q:\n%s", want, first)
		}
	}
	for _, not := range []string{"kiwi-just-me-48", "quagga-note-17"} {
		if strings.Contains(first, not) {
			t.Errorf("instructions have %q", not)
		}
	}
	a.send(map[string]any{"type": "text", "text": "And the pricing?"})
	hub.waitSent(1)
	up.drop(0)
	a.until(linkIn(linkLive))
	second := instructionsOf(up.find(t, 1, "its start", isStart))
	if !strings.Contains(second, "Alex: And the pricing?") || !strings.Contains(second, "in a meeting") ||
		strings.Contains(second, "kiwi-just-me-48") || strings.Contains(second, "quagga-note-17") {
		t.Fatalf("after the drop:\n%s", second)
	}
}

// A meeting call reads no marks: a delegation that starts with 「状态：」, 「停止：」 or 「回答：」 goes to the room as
// the question it is, word for word. A status would say the room's last turn (a batch's, a heard ask's), which this
// call did not hand over, and a stop would cut the room's work on the meeting short.
func TestMeetingCallReadsNoMarks(t *testing.T) {
	hub := roomHub(t)
	hub.reply = func(string) string { return "Noted." }
	up := boundUpstream(t, true)
	pl := testPlugin(t, Config{Owner: "Alex", Providers: []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}, Tools: new(true)}}},
		hub.server(t))
	a := dialApp(t, pl, meetingStart("rec1", "room1", nil))
	a.until(ofType("ready"))
	hub.status("room1", "working")
	for i, task := range []string{"状态：到哪了", "停止：先别说了", "回答：用第二个方案", "Status: where are we?"} {
		a.send(map[string]any{"type": "text", "text": task})
		if m := hub.waitSent(i + 1); m.Session != "room1" || m.Text != task {
			t.Fatalf("%q went as %+v", task, m)
		}
	}
	hub.mu.Lock()
	stopped := slices.Clone(hub.interrupts)
	hub.mu.Unlock()
	if len(stopped) != 0 {
		t.Fatalf("the room was stopped: %v", stopped)
	}
}

// #5904 in a meeting call: what it hands the room goes as the owner's words, so a start's speech_gate neither turns
// the user-speech gate off nor brings Send Anyway. A question nobody was heard asking is refused unprompted, never sent
// to the room, not kept for a resend, and the model does not offer the button.
func TestMeetingCallKeepsTheSpeechGate(t *testing.T) {
	setVar(t, &gateWait, 300*time.Millisecond)
	for _, gate := range []bool{false, true} {
		t.Run(fmt.Sprint("speech_gate ", gate), func(t *testing.T) {
			hub := roomHub(t)
			up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
				if f["type"] == "session.start" {
					u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
					delegate(u, c, "dlg1", "供应商定了什么") // nobody spoke: no Talk, nothing typed
				}
			})
			pl := testPlugin(t, liveProvider(up), hub.server(t))
			logs := captureLog(t, pl)
			a := dialApp(t, pl, meetingStart("rec1", "room1", map[string]any{"speech_gate": gate}))
			a.until(ofType("ready"))
			refused := a.until(runIn(runRefused))
			id, _ := refused["id"].(string)
			if refused["code"] != "unprompted" || refused["resendable"] != nil {
				t.Fatalf("refused: %v", refused)
			}
			if got := content(up.nextMatch(on("dlg1"))); !strings.Contains(got, unpromptedWords) || strings.Contains(got, "仍然发送") {
				t.Errorf("told: %s", got)
			}
			start := logs.wait("call 1: start:", "meeting call for recording rec1")
			if off := strings.Contains(start, "speech gate off asked: not for a meeting call"); off == gate || strings.Contains(start, ", speech gate off,") {
				t.Errorf("start: %s", start)
			}
			logs.wait("call 1: delegation dlg1: the user-speech gate refused it (unprompted)")
			a.send(map[string]any{"type": "resend", "id": id})
			logs.wait("call 1: resend " + id + ": no such refused run")
			if msgs := hub.messages(); len(msgs) != 0 {
				t.Fatalf("sent to the room: %+v", msgs)
			}
		})
	}
}

// A Talk span the app never closes is closed by the extension past talkMost: the upstream hears its silence again
// even while the app sends nothing, the app's audio after that is dropped, and the next talk{on} opens a span as ever.
func TestMeetingTalkSpanIsCapped(t *testing.T) {
	setVar(t, &talkMost, 400*time.Millisecond)
	hub := roomHub(t)
	up := boundUpstream(t, false)
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	logs := captureLog(t, pl)
	a := dialApp(t, pl, meetingStart("rec1", "room1", nil))
	a.until(ofType("ready"))
	got := func(want []byte) int {
		return len(slices.DeleteFunc(up.upAudio(), func(b []byte) bool { return !bytes.Equal(b, want) }))
	}
	send := func(b []byte) { _ = a.c.Write(t.Context(), websocket.MessageBinary, b) }
	inside, late, again := pcm(640, 1212), pcm(640, 3434), pcm(640, 5656)
	a.send(map[string]any{"type": "talk", "on": true})
	send(inside)
	waitFor(t, "the span's audio", func() bool { return got(inside) == 1 })
	logs.wait("talk 1: no talk off after", "1 frames of audio: closed, the upstream hears silence until the next talk on")
	n := len(up.upAudio())
	waitFor(t, "silence after the span was closed", func() bool {
		audio := up.upAudio()
		return len(audio) > n+2 && silent(audio[len(audio)-1])
	})
	send(late)
	a.send(map[string]any{"type": "talk", "on": false}) // the off that was lost, late: nothing to close
	a.send(map[string]any{"type": "talk", "on": true})
	send(again)
	a.send(map[string]any{"type": "talk", "on": false})
	waitFor(t, "the next span's audio", func() bool { return got(again) == 1 })
	if got(late) != 0 {
		t.Fatal("audio after the closed span went to the upstream")
	}
	logs.wait("the app sent audio outside a Talk span (640 bytes): dropped")
	logs.wait("talk 2: off after", "1 frames of audio")
	_ = a.c.CloseNow()
	logs.wait("conversation closed", "talk spans 2 (1 closed past 400ms), audio frames 2 (outside a span 1)")
}

// The room's snapshot keeps the owner's own questions and the room's words after each, up to the next message: a
// reply to a batch, a photo or a heard ask goes with what it answered, and so does one whose question is before the
// window or was cut off it.
func TestRoomRecentPairsTheReplies(t *testing.T) {
	meeting := &proto.MessageSource{Kind: proto.SourceMeeting}
	own := func(text string) proto.Item { return proto.Item{Role: "user", Kind: proto.KindText, Text: text} }
	reply := func(text string) proto.Item { return proto.Item{Role: "assistant", Kind: proto.KindText, Text: text} }
	items := []proto.Item{
		reply("orphan-1"), // its question is before the window
		own("Q1"), reply("A1"),
		{Role: "user", Kind: proto.KindText, Text: "New lines", Source: meeting}, reply("batch-2"),
		{Role: "user", Kind: proto.KindText, Text: "A photo", Source: meeting}, reply("photo-3"),
		own("Q2"), {Role: "assistant", Kind: proto.KindToolUse, ToolName: "meeting_topics_set"}, reply("A2"),
		{Role: "user", Kind: proto.KindText, Text: "Sam asks", Source: meeting}, reply("ask-4"),
	}
	var lines []string
	for _, l := range roomRecent(i18n.En, items) {
		lines = append(lines, l.Role+":"+l.Text)
	}
	if want := []string{"user:Q1", "assistant:A1", "user:Q2", "assistant:A2"}; !slices.Equal(lines, want) {
		t.Fatalf("recent %v, want %v", lines, want)
	}
	// Cut to recentMost: a reply whose question went is left out with it.
	items = []proto.Item{own("Q0"), reply("A0a"), reply("A0b")}
	for i := range (recentMost - 2) / 2 {
		items = append(items, own(fmt.Sprintf("Q%d", i+1)), reply(fmt.Sprintf("A%d", i+1)))
	}
	recent := roomRecent(i18n.En, items)
	if len(recent) != recentMost-2 || recent[0].Text != "Q1" {
		t.Fatalf("cut: %v", recent)
	}
}
