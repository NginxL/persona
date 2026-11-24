package voice

import (
	"fmt"
	"io"
	"log"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/proto"
)

// Holding a call (#9290, docs/assistant/voice.md 4.9 "Suspension", suspend.go holdFrame): the app's hold frame pauses
// the upstream at once and nothing wakes it until the hold is off.

func hold(on bool) map[string]any { return map[string]any{"type": "hold", "on": on} }

func asList(v any) []any { l, _ := v.([]any); return l }

// never reads the app's frames for d: none of them may match.
func (a *app) never(d time.Duration, what string, match func(map[string]any) bool) {
	a.t.Helper()
	deadline := time.After(d)
	for {
		select {
		case f, ok := <-a.frames:
			if !ok {
				a.t.Fatal("the plugin closed the conversation")
			}
			if match(f) {
				a.t.Fatalf("%s: %v", what, f)
			}
		case <-deadline:
			return
		}
	}
}

// A hold pauses an idle conversation's upstream at once, long before pauseAfter; a hold said again changes nothing;
// its off wakes the upstream (waking, then live, a woken session), and an off said again changes nothing. The log
// has the hold, the pause for it, the wake and the count on the call's last line.
func TestHoldPausesAtOnce(t *testing.T) {
	setVar(t, &pauseAfter, time.Hour)
	hub := agentHub(t)
	up := newSessionsUp(t, false, nil)
	pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
	logs := captureLog(t, pl)
	a := dialApp(t, pl, boundStart("a1", nil))
	if ready := a.until(ofType("ready")); !slices.Contains(asList(ready["features"]), any("hold")) {
		t.Fatalf("ready: %v", ready)
	}
	held := time.Now()
	a.send(hold(true))
	a.until(linkIn(linkPaused))
	if took := time.Since(held); took > 2*time.Second {
		t.Fatalf("paused %v after the hold", took)
	}
	up.find(t, 0, "session.close", ofType("session.close"))
	logs.wait("call 1: hold 1: on")
	logs.wait("call 1: paused the upstream (1): session 1 closed, the call is on hold")

	a.send(hold(true))
	a.never(500*time.Millisecond, "a hold said again", func(f map[string]any) bool { return f["type"] == "link" })
	a.send(hold(false))
	a.until(linkIn(linkWaking))
	a.until(linkIn(linkLive))
	logs.wait("call 1: upstream: session 2 started in", "(woken)")
	logs.wait("call 1: hold 1: off after")
	logs.wait("call 1: waking the upstream: the call is off hold")
	a.send(hold(false))
	a.never(500*time.Millisecond, "an off said again", func(f map[string]any) bool { return f["type"] == "link" })
	if n := up.count(); n != 2 {
		t.Fatalf("%d sessions", n)
	}
	for _, line := range logs.lines() {
		if strings.Contains(line, "hold 2") {
			t.Fatalf("a hold said again counted: %s", line)
		}
	}
	a.send(hold(true)) // a second hold
	a.until(linkIn(linkPaused))
	logs.wait("call 1: hold 2: on")
	_ = a.c.Close(websocket.StatusNormalClosure, "bye")
	if end := logs.wait("call 1: conversation closed"); !strings.Contains(end, "pauses 2, nudges 0, holds 2") {
		t.Errorf("the call's last line: %s", end)
	}
}

// While the call is on hold nothing wakes its upstream: the agent's result, a card, a line the user types and the
// user's voice. What is to be said and the typed line are kept and go to the session the hold's off wakes; the
// user's voice is dropped (they spoke to someone else): the new session never gets it.
func TestHoldKeepsWhatComes(t *testing.T) {
	card := proto.Interaction{ID: "q1", Kind: proto.IxQuestion, MachineID: "m", SessionID: "a1",
		Questions: []proto.Question{{Question: "数据库用哪个？", Options: []proto.QuestionOption{{Label: "Postgres"}, {Label: "SQLite"}}}}}
	for _, tt := range []struct {
		name  string
		while func(t *testing.T, a *app, hub *fakeHub)
		after func(t *testing.T, a *app, up *sessionsUp)
	}{
		{"a result", func(t *testing.T, a *app, hub *fakeHub) {
			hub.add("a1", proto.Item{Role: "assistant", Kind: proto.KindText, Text: "集成测试全部通过。"})
			hub.end("a1")
		}, func(t *testing.T, a *app, up *sessionsUp) {
			if got := content(up.find(t, 1, "the result, said", said)); !strings.Contains(got, "集成测试全部通过") {
				t.Fatalf("said: %s", got)
			}
		}},
		{"a card", func(t *testing.T, a *app, hub *fakeHub) {
			hub.raise(card)
		}, func(t *testing.T, a *app, up *sessionsUp) {
			a.until(func(f map[string]any) bool { return f["type"] == "ask" && f["state"] == askAsking })
			up.find(t, 1, "the question", func(f map[string]any) bool { return strings.Contains(content(f), "数据库用哪个") })
		}},
		{"a typed line", func(t *testing.T, a *app, hub *fakeHub) {
			a.send(map[string]any{"type": "text", "text": "数字是几"})
			a.until(finalLine("user", "数字是几")) // its caption, at once
		}, func(t *testing.T, a *app, up *sessionsUp) {
			up.find(t, 1, "the typed line", func(f map[string]any) bool {
				return f["type"] == "session.commentary.append" && f["delegation_id"] == nil && content(f) == "数字是几"
			})
		}},
		{"the user's voice", func(t *testing.T, a *app, hub *fakeHub) {
			a.speak(time.Second, true)
		}, func(t *testing.T, a *app, up *sessionsUp) {
			// What the app sends now goes to the woken session after anything of the hold's that would have gone to it
			// (kept for the wake, it goes out first).
			_ = a.c.Write(t.Context(), websocket.MessageBinary, tone(320, 2000))
			waitFor(t, "the woken session's audio", func() bool { amps, _ := up.audio(1); return slices.Contains(amps, 2000) })
			if amps, _ := up.audio(1); slices.Contains(amps, 3000) {
				t.Fatal("the voice of the hold went to the new session")
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setVar(t, &lookEvery, 100*time.Millisecond)
			setVar(t, &pauseAfter, time.Hour)
			hub := workingAgent(t)
			up := newSessionsUp(t, false, nil)
			pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
			logs := captureLog(t, pl)
			a := dialApp(t, pl, boundStart("a1", nil))
			a.until(ofType("ready"))
			hub.subscribed("a1")
			logs.followed()
			a.send(hold(true))
			a.until(linkIn(linkPaused))
			tt.while(t, a, hub)
			a.never(time.Second, "woken on hold", linkIn(linkWaking))
			if n := up.count(); n != 1 {
				t.Fatalf("%d sessions on hold", n)
			}
			a.send(hold(false))
			a.until(linkIn(linkLive))
			tt.after(t, a, up)
			if old, _ := up.audio(0); slices.Contains(old, 3000) {
				t.Fatal("the voice of the hold went to the paused session")
			}
		})
	}
}

// A hold pauses even while a card is asked by voice, which holds a quiet pause off: back from the hold, the new
// session asks it again, once.
func TestHoldWhileACardIsAsked(t *testing.T) {
	setVar(t, &lookEvery, 100*time.Millisecond)
	hub := workingAgent(t)
	hub.raise(proto.Interaction{ID: "q1", Kind: proto.IxQuestion, MachineID: "m", SessionID: "a1",
		Questions: []proto.Question{{Question: "数据库用哪个？", Options: []proto.QuestionOption{{Label: "Postgres"}, {Label: "SQLite"}}}}})
	up := newSessionsUp(t, false, nil)
	pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
	logs := captureLog(t, pl)
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	a.until(func(f map[string]any) bool { return f["type"] == "ask" && f["state"] == askAsking })
	a.send(hold(true))
	a.until(linkIn(linkPaused))
	a.send(hold(false))
	a.until(linkIn(linkLive))
	waitFor(t, "the question in the new session", func() bool { return cardAsks(up, 1) > 0 })
	askedOnce(t, a, hub, up, logs, 1)
}

// A hold that comes as the upstream wakes (the user took the call off hold and put it on hold again at once) pauses
// the woken session once it is up, long before pauseAfter; its off wakes it again.
func TestHoldWhileWaking(t *testing.T) {
	setVar(t, &pauseAfter, time.Hour)
	hub := agentHub(t)
	up := newSessionsUp(t, false, nil)
	pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	a.send(hold(true))
	a.until(linkIn(linkPaused))
	a.send(hold(false))
	a.send(hold(true))
	a.until(linkIn(linkWaking))
	a.until(linkIn(linkLive))
	a.until(linkIn(linkPaused))
	if n := up.count(); n != 2 {
		t.Fatalf("%d sessions", n)
	}
	up.find(t, 1, "session.close", ofType("session.close"))
	a.send(hold(false))
	a.until(linkIn(linkLive))
	if n := up.count(); n != 3 {
		t.Fatalf("%d sessions", n)
	}
}

// A hold that comes as the upstream reconnects after a drop pauses it once the new session is up, and what came
// meanwhile (the agent's result) is not said to that session: it goes to the one the hold's off wakes.
func TestHoldWhileReconnecting(t *testing.T) {
	setVar(t, &lookEvery, 100*time.Millisecond)
	setVar(t, &pauseAfter, time.Hour)
	hub := workingAgent(t)
	up := newSessionsUp(t, false, func(n int) bool { return n == 1 }) // the first reconnect fails: the next is 2 s later
	pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
	logs := captureLog(t, pl)
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	hub.subscribed("a1")
	logs.followed()
	up.drop(0)
	a.until(linkIn(linkReconnecting))
	a.send(hold(true))
	hub.add("a1", proto.Item{Role: "assistant", Kind: proto.KindText, Text: "集成测试全部通过。"})
	hub.end("a1")
	a.until(linkIn(linkLive))
	a.until(linkIn(linkPaused))
	if slices.ContainsFunc(up.frames(2), said) {
		t.Fatal("said to the session that came up on hold")
	}
	a.send(hold(false))
	a.until(linkIn(linkLive))
	if got := content(up.find(t, 3, "the result, said", said)); !strings.Contains(got, "集成测试全部通过") {
		t.Fatalf("said: %s", got)
	}
}

// A line the user types on hold as the upstream reconnects after a drop is kept, not refused as not sent: it goes to
// the session the hold's off wakes, once, never to the one that came up on hold; or, when the hold is off before the
// reconnected session is up, to that one once it is.
func TestHoldTypedWhileReconnecting(t *testing.T) {
	for _, early := range []bool{false, true} { // the hold's off: once the reconnected session paused, or before it is up
		t.Run(fmt.Sprint("off before it is up=", early), func(t *testing.T) {
			setVar(t, &pauseAfter, time.Hour)
			hub := agentHub(t)
			up := newSessionsUp(t, false, func(n int) bool { return n == 1 }) // the first reconnect fails: the next is 2 s later
			pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
			logs := captureLog(t, pl)
			a := dialApp(t, pl, boundStart("a1", nil))
			a.until(ofType("ready"))
			up.drop(0)
			a.until(linkIn(linkReconnecting))
			a.send(hold(true))
			a.send(map[string]any{"type": "text", "text": "数字是几"})
			a.until(finalLine("user", "数字是几")) // its caption, at once
			typed := func(n int) int { return typedIn(up, n, "数字是几") }
			to := 3 // session 1 was refused, 2 came up on hold, 3 is woken
			if early {
				a.send(hold(false))
				a.until(linkIn(linkLive))
				to = 2
			} else {
				a.until(linkIn(linkLive))
				a.until(linkIn(linkPaused))
				up.find(t, 2, "session.close", ofType("session.close")) // all it got is in
				if typed(2) != 0 {
					t.Fatal("the line went to the session that came up on hold")
				}
				a.send(hold(false))
				a.until(linkIn(linkLive))
			}
			waitFor(t, "the typed line", func() bool { return typed(to) > 0 })
			wokeThrough(t, a, up, logs, to)
			if typed(2)+typed(3) != 1 {
				t.Fatalf("sent %d times (session 2: %d)", typed(2)+typed(3), typed(2))
			}
			if a.got("error") {
				t.Fatal("the app was told the line was not sent")
			}
		})
	}
}

// The call goes on hold again as it wakes (off, and on again before the woken session is up), and the user types a
// line during that hold: the woken session is paused before anything kept goes to it, and the line goes to the one
// the next off wakes, once.
func TestHoldAgainAsItWakes(t *testing.T) {
	setVar(t, &pauseAfter, time.Hour)
	hub := agentHub(t)
	up := newSessionsUp(t, false, nil)
	release := up.slowStart(t, 1)
	pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
	logs := captureLog(t, pl)
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	a.send(hold(true))
	a.until(linkIn(linkPaused))
	a.send(hold(false))
	a.until(linkIn(linkWaking))
	a.send(hold(true))
	a.send(map[string]any{"type": "text", "text": "数字是几"})
	a.until(finalLine("user", "数字是几")) // read after the hold: both came before the woken session is up
	release()
	a.until(linkIn(linkLive))
	a.until(linkIn(linkPaused))
	up.find(t, 1, "session.close", ofType("session.close"))
	logs.wait("call 1: paused the upstream (2): session 2 closed, the call is on hold")
	if n := typedIn(up, 1, "数字是几"); n != 0 {
		t.Fatal("the line typed on hold went to the session woken as the hold came")
	}
	a.send(hold(false))
	a.until(linkIn(linkLive))
	waitFor(t, "the typed line", func() bool { return typedIn(up, 2, "数字是几") > 0 })
	wokeThrough(t, a, up, logs, 2)
	if n := typedIn(up, 2, "数字是几"); n != 1 || up.count() != 3 {
		t.Fatalf("sent %d times, %d sessions", n, up.count())
	}
}

// A card asked by voice, then a session that comes up on hold and goes before anything is said to it (woken as the
// call went on hold again, or reconnected after a drop on hold): the session the hold's off wakes asks it once, though
// two sessions came up since it was asked, and the user's voice holds the question back as that one comes up.
func TestHoldAsksTheCardOnce(t *testing.T) {
	card := proto.Interaction{ID: "q1", Kind: proto.IxQuestion, MachineID: "m", SessionID: "a1",
		Questions: []proto.Question{{Question: "数据库用哪个？", Options: []proto.QuestionOption{{Label: "Postgres"}, {Label: "SQLite"}}}}}
	for _, how := range []string{"woken", "reconnected"} {
		t.Run(how, func(t *testing.T) {
			setVar(t, &lookEvery, 100*time.Millisecond)
			setVar(t, &pauseAfter, time.Hour)
			hub := workingAgent(t)
			hub.raise(card)
			up := newSessionsUp(t, false, func(n int) bool { return how == "reconnected" && n == 1 })
			release := func() {}
			if how == "woken" {
				release = up.slowStart(t, 1)
			}
			pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
			logs := captureLog(t, pl)
			a := dialApp(t, pl, boundStart("a1", nil))
			a.until(ofType("ready"))
			a.until(func(f map[string]any) bool { return f["type"] == "ask" && f["state"] == askAsking })
			last := 2
			if how == "woken" {
				a.send(hold(true))
				a.until(linkIn(linkPaused))
				a.send(hold(false))
				a.until(linkIn(linkWaking))
				a.send(hold(true))
				logs.wait("call 1: hold 2: on") // before the woken session is up
				release()
			} else {
				up.drop(0)
				a.until(linkIn(linkReconnecting))
				a.send(hold(true))
				last = 3 // session 1 was refused
			}
			a.until(linkIn(linkLive))
			a.until(linkIn(linkPaused))
			// Paused, it is written nothing more: once its close is in, all it got is.
			up.find(t, last-1, "session.close", ofType("session.close"))
			if n := cardAsks(up, last-1); n != 0 {
				t.Fatalf("asked %d times to the session on hold", n)
			}
			a.send(hold(false))
			a.speak(600*time.Millisecond, true) // as it wakes: the question waits for the user to stop
			a.until(linkIn(linkLive))
			waitFor(t, "the question in the woken session", func() bool { return cardAsks(up, last) > 0 })
			askedOnce(t, a, hub, up, logs, last)
		})
	}
}

// The realtime dialect: a hold that comes as a reply is under way (response.created, its response.done not yet come)
// or after a long reply that has not played out (the model's speech comes faster than it plays) ends that reply with
// the session. The woken session is asked for a response to what waited: its response.create is not held back for the
// old reply's end, which never comes, nor until the speech the app dropped would have played out.
func TestHoldEndsTheReplyUnderWay(t *testing.T) {
	for _, tt := range []struct {
		name  string
		done  bool          // the reply ended before the hold
		audio time.Duration // of the model's speech in it
	}{{"a reply under way", false, 0}, {"a long reply not played out", true, 10 * time.Second}} {
		t.Run(tt.name, func(t *testing.T) {
			setVar(t, &lookEvery, 100*time.Millisecond)
			setVar(t, &pauseAfter, time.Hour)
			hub := workingAgent(t)
			up := newSessionsUp(t, false, nil)
			pl := testPlugin(t, Config{Providers: []Provider{{ID: "rt", URL: up.url(), Dialect: DialectRealtime, Models: []string{"m1"},
				Tools: new(false)}}}, hub.server(t))
			logs := captureLog(t, pl)
			a := dialApp(t, pl, boundStart("a1", nil))
			a.until(ofType("ready"))
			hub.subscribed("a1")
			logs.followed()
			c := up.conn(0)
			up.send(c, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp_1"}})
			for range int(tt.audio / (100 * time.Millisecond)) { // 24 kHz
				up.send(c, map[string]any{"type": "response.output_audio.delta", "delta": b64(make([]byte, 4800))})
			}
			if tt.done {
				up.send(c, map[string]any{"type": "response.done", "response": map[string]any{"id": "resp_1"}})
			}
			up.send(c, map[string]any{"type": "response.output_audio_transcript.delta", "delta": "好的"})
			a.until(func(f map[string]any) bool { return f["type"] == "transcript" && f["role"] == "assistant" }) // all of it was read
			a.send(hold(true))
			a.until(linkIn(linkPaused))
			hub.add("a1", proto.Item{Role: "assistant", Kind: proto.KindText, Text: "集成测试全部通过。"})
			hub.end("a1")
			a.never(500*time.Millisecond, "woken on hold", linkIn(linkWaking))
			a.send(hold(false))
			a.until(linkIn(linkLive))
			live := time.Now()
			waitFor(t, "the result and its response.create in the woken session", func() bool {
				fs := up.frames(1)
				i := slices.IndexFunc(fs, func(f map[string]any) bool {
					return f["type"] == "conversation.item.create" && strings.Contains(fmt.Sprint(f["item"]), "集成测试全部通过")
				})
				return i >= 0 && slices.ContainsFunc(fs[i+1:], ofType("response.create"))
			})
			if took := time.Since(live); took > 3*time.Second {
				t.Fatalf("said %v after the session was up", took)
			}
		})
	}
}

// What nextToSay took goes back to the queue when the session goes before it is sent (the speaker is between the two
// as readApp takes a hold, or a write meets a pause): the next session says it. Nothing of it goes to the old one.
func TestHoldBetweenTakingAndSaying(t *testing.T) {
	result := func() *sentWords { return &sentWords{words: plainWords("集成测试全部通过。"), agent: true} }
	call := func(up *recLink) *conversation {
		c := &conversation{version: 2, pl: &Plugin{logger: log.New(io.Discard, "", 0)}, ctx: t.Context(), w: liveWire{},
			out: make(chan outFrame, 64), wake: make(chan struct{}, 1), unacked: map[string]*sentWords{}}
		c.up, c.ready = up, true
		return c
	}
	kept := func(c *conversation, s *sentWords) bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.queue) == 1 && c.queue[0] == s
	}
	saidTo := func(up *recLink) bool { return slices.Contains(up.sent(), "集成测试全部通过。") }

	for _, paused := range []bool{true, false} { // the hold paused the session, or it is on hold and not paused yet
		up := &recLink{left: -1}
		c := call(up)
		c.queue = []*sentWords{result()}
		s := c.nextToSay(time.Now())
		if s == nil {
			t.Fatal("nothing to say")
		}
		if paused {
			c.holdFrame(true)
		} else {
			c.mu.Lock()
			c.sus.onHold = true // as a reconnected session is, before the speaker's next look pauses it
			c.mu.Unlock()
		}
		c.sendNow(s)
		if saidTo(up) || !kept(c, s) {
			t.Fatalf("paused %v: said %v, queue %v", paused, up.sent(), c.queue)
		}
	}

	// The session goes as it is written to (a hold's pause closes it): the write fails, and it is kept.
	up := &recLink{left: 0}
	c := call(up)
	up.broke = func() {
		c.mu.Lock()
		c.gen++
		c.up, c.ready = nil, false
		c.mu.Unlock()
	}
	s := result()
	c.sendNow(s)
	if !kept(c, s) {
		t.Fatalf("a write the pause failed: queue %v", c.queue)
	}

	// A write that fails on a session still up is not kept (its reader sees it and lost reconnects): the speaker would
	// only try it again on the same session.
	up = &recLink{left: 0, broke: func() {}}
	c = call(up)
	c.sendNow(result())
	if len(c.queue) != 0 {
		t.Fatalf("a failed write on the same session: queue %v", c.queue)
	}
}

// typedIn is how often session n got the typed line text.
func typedIn(up *sessionsUp, n int, text string) int {
	return len(slices.DeleteFunc(up.frames(n), func(f map[string]any) bool {
		return f["type"] != "session.commentary.append" || f["delegation_id"] != nil || content(f) != text
	}))
}

// cardAsks is how often session n got the card's question (数据库用哪个).
func cardAsks(up *sessionsUp, n int) int {
	return len(slices.DeleteFunc(up.frames(n), func(f map[string]any) bool { return !strings.Contains(content(f), "数据库用哪个") }))
}

// followed waits until the call follows the turn a1 works on (workingAgent) as an observed run: what that turn brings
// is the call's to say.
func (l *logCapture) followed() {
	l.t.Helper()
	l.wait("call 1: run observed-1: the bound conversation works on a turn the call did not hand over: followed")
}

// trackerDone waits until the call's tracker has done every op it was given before now (a new session's reask): the
// app's resend of a run the call does not know (id) is one more op in its queue, done in turn, and logged.
func (a *app) trackerDone(logs *logCapture, id string) {
	a.t.Helper()
	a.send(map[string]any{"type": "resend", "id": id})
	logs.wait("call 1: resend " + id + ": no such refused run")
}

// wokeThrough waits until what the call kept for its woken session n has all come in: the wake is over (logged once
// all of it went out), and a line typed then reaches n after it.
func wokeThrough(t *testing.T, a *app, up *sessionsUp, logs *logCapture, n int) {
	t.Helper()
	logs.wait("call 1: woke the upstream in")
	a.send(map[string]any{"type": "text", "text": "还在吗"})
	waitFor(t, "the line typed once it woke", func() bool { return typedIn(up, n, "还在吗") > 0 })
}

// askedOnce checks that the woken session n asked the card once, however late the tracker came to it. The wake is
// over (logged after its connect gave the tracker the new session's reask), the tracker did all it was given by then
// (trackerDone), and then the agent's result, which goes after any copy of the question queued before it (a card goes
// first, and nothing queued later is let go sooner), reached n.
func askedOnce(t *testing.T, a *app, hub *fakeHub, up *sessionsUp, logs *logCapture, n int) {
	t.Helper()
	logs.wait("call 1: woke the upstream in")
	a.trackerDone(logs, "r-after-wake")
	logs.followed()
	hub.add("a1", proto.Item{Role: "assistant", Kind: proto.KindText, Text: "集成测试全部通过。"})
	hub.end("a1")
	up.find(t, n, "the result, said after the question", func(f map[string]any) bool {
		return said(f) && strings.Contains(content(f), "集成测试全部通过")
	})
	if got := cardAsks(up, n); got != 1 {
		t.Fatalf("asked %d times in session %d", got, n)
	}
}

// got: the app got a frame of type typ so far.
func (a *app) got(typ string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.types[typ]
}

// A hold is not for a call that takes none: on protocol 1 and on a meeting call it changes nothing, and the log says
// so once. A hold the app sends before it has its ready is read once the call is up (the extension reads no frame
// before its ready), and pauses it then.
func TestHoldIgnored(t *testing.T) {
	t.Run("protocol 1", func(t *testing.T) {
		setVar(t, &pauseAfter, 100*time.Millisecond)
		// The model is nudged for the typed line by the speaker's look 3 pauseAfter after it: a call that would pause
		// would have by then, and a paused one is not nudged.
		setVar(t, &nudgeAfter, 3*pauseAfter)
		setVar(t, &nudgeQuiet, pauseAfter)
		hub := newFakeHub(t)
		hub.status("main1", "working")
		up := newSessionsUp(t, false, nil)
		pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
		logs := captureLog(t, pl)
		a := dialApp(t, pl, map[string]any{"type": "start"})
		a.until(ofType("ready"))
		a.send(hold(true))
		a.send(hold(false))
		a.send(hold(true))
		a.send(map[string]any{"type": "text", "text": "数字是几"})
		up.find(t, 0, "the typed line", func(f map[string]any) bool { return content(f) == "数字是几" })
		logs.wait("call 1: hold frames on a protocol 1 call: ignored")
		waitFor(t, "the nudge for the typed line", func() bool {
			if strings.Contains(logs.String(), "paused the upstream") {
				t.Fatal("paused")
			}
			return strings.Contains(logs.String(), "call 1: nudge: answer owed for")
		})
		if n := strings.Count(logs.String(), "hold frames"); n != 1 {
			t.Fatalf("logged %d times", n)
		}
		if up.count() != 1 || strings.Contains(logs.String(), "paused the upstream") {
			t.Fatal("paused")
		}
		a.protocolOne()
	})

	t.Run("meeting call", func(t *testing.T) {
		setVar(t, &pauseAfter, 100*time.Millisecond)
		hub := roomHub(t)
		up := newSessionsUp(t, true, nil)
		pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
		logs := captureLog(t, pl)
		a := dialApp(t, pl, meetingStart("rec1", "room1", nil))
		a.until(ofType("ready"))
		a.send(hold(true))
		logs.wait("call 1: hold frames on a meeting call: ignored")
		a.never(500*time.Millisecond, "a meeting call on hold", func(f map[string]any) bool { return f["type"] == "link" })
		a.send(map[string]any{"type": "text", "text": "What did we decide on the vendor?"})
		hub.waitSent(1) // its lines go to the room as before
		if up.count() != 1 {
			t.Fatalf("%d sessions", up.count())
		}
	})

	t.Run("before ready", func(t *testing.T) {
		setVar(t, &pauseAfter, time.Hour)
		hub := agentHub(t)
		up := newSessionsUp(t, false, nil)
		pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
		a := dialApp(t, pl, nil)
		a.send(boundStart("a1", nil))
		a.send(hold(true))
		a.until(ofType("ready"))
		a.until(linkIn(linkPaused))
		up.find(t, 0, "its start", isStart)
	})
}
