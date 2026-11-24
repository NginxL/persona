package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Pausing the upstream while the bound conversation works (docs/assistant/voice.md 4.9 "Suspension", suspend.go).

// sessionsUp is a live service that keeps each session's frames apart, in the order the sessions began; with hand, its
// model hands every line the user types over as a client delegation (dlg1, dlg2…). refuse says whether session n
// (from 0) is refused: closed before it starts; drops (set before the call) whether it is closed as soon as it started;
// slow (set before the call, slowStart) what session n's start waits for: it starts once that is closed (nil: at once).
// It starts a realtime dialect's session too (its session.update), as an otherwise silent service.
type sessionsUp struct {
	*fakeUpstream
	mu       sync.Mutex
	sessions [][]map[string]any
	conns    []*websocket.Conn
	ids      map[*websocket.Conn]int
	drops    func(n int) bool
	slow     func(n int) <-chan struct{}
}

func newSessionsUp(t *testing.T, hand bool, refuse func(n int) bool) *sessionsUp {
	s := &sessionsUp{ids: map[*websocket.Conn]int{}}
	var dlg atomic.Int32
	s.fakeUpstream = newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		s.mu.Lock()
		n, ok := s.ids[c]
		if !ok {
			n = len(s.sessions)
			s.ids[c] = n
			s.sessions, s.conns = append(s.sessions, nil), append(s.conns, c)
		}
		s.sessions[n] = append(s.sessions[n], f)
		s.mu.Unlock()
		switch f["type"] {
		case "session.start", "session.update":
			if refuse != nil && refuse(n) {
				_ = c.Close(websocket.StatusGoingAway, "not now")
				return
			}
			if s.slow != nil {
				if wait := s.slow(n); wait != nil {
					<-wait
				}
			}
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
			if s.drops != nil && s.drops(n) {
				_ = c.Close(websocket.StatusGoingAway, "upstream restarts")
			}
		case "session.commentary.append":
			if hand && f["delegation_id"] == nil && f["event_id"] == nil {
				delegate(u, c, fmt.Sprintf("dlg%d", dlg.Add(1)), content(f))
			}
		}
	})
	return s
}

// slowStart makes session n slow to start (set before the call): its start waits until release, which the test calls
// once it has done what it does meanwhile, and which the test's end calls at the latest (a failed assertion leaves no
// start waiting).
func (s *sessionsUp) slowStart(t *testing.T, n int) (release func()) {
	started := make(chan struct{})
	release = sync.OnceFunc(func() { close(started) })
	t.Cleanup(release)
	s.slow = func(i int) <-chan struct{} {
		if i == n {
			return started
		}
		return nil
	}
	return release
}

// count is how many sessions began.
func (s *sessionsUp) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// frames is what session n got so far.
func (s *sessionsUp) frames(n int) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n >= len(s.sessions) {
		return nil
	}
	return slices.Clone(s.sessions[n])
}

// find waits for a frame of session n that matches.
func (s *sessionsUp) find(t *testing.T, n int, what string, match func(map[string]any) bool) map[string]any {
	t.Helper()
	var got map[string]any
	waitFor(t, fmt.Sprintf("session %d: %s", n, what), func() bool {
		i := slices.IndexFunc(s.frames(n), match)
		if i >= 0 {
			got = s.frames(n)[i]
		}
		return i >= 0
	})
	return got
}

// audio is the amplitude of the first sample of each frame of the app's audio session n got, in order.
func (s *sessionsUp) audio(n int) (amps []int16, bytes []int) {
	for _, f := range s.frames(n) {
		if f["type"] != "session.input_audio.append" {
			continue
		}
		b, _ := base64.StdEncoding.DecodeString(f["audio"].(string))
		if len(b) < 2 {
			continue
		}
		amps, bytes = append(amps, int16(uint16(b[0])|uint16(b[1])<<8)), append(bytes, len(b))
	}
	return amps, bytes
}

// conn is session n's socket, for the test to send its events on.
func (s *sessionsUp) conn(n int) *websocket.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[n]
}

// drop closes session n's socket, as a service that restarts does.
func (s *sessionsUp) drop(n int) {
	s.mu.Lock()
	c := s.conns[n]
	s.mu.Unlock()
	_ = c.Close(websocket.StatusGoingAway, "upstream restarts")
}

func linkIn(state string) func(map[string]any) bool {
	return func(f map[string]any) bool { return f["type"] == "link" && f["state"] == state }
}

func isStart(f map[string]any) bool { return f["type"] == "session.start" }

func instructionsOf(f map[string]any) string {
	return f["session"].(map[string]any)["instructions"].(string)
}

// workingAgent is agentHub with a1 working on a turn begun at the computer (the call follows it as an observed run),
// left to the test to answer.
func workingAgent(t *testing.T) *fakeHub {
	hub := agentHub(t)
	hub.manual = true
	hub.add("a1", proto.Item{Role: "user", Kind: proto.KindText, Text: "跑一下集成测试"})
	hub.status("a1", "working")
	return hub
}

// quick makes the call look and pause in a test's time.
func quick(t *testing.T) {
	setVar(t, &lookEvery, 100*time.Millisecond)
	setVar(t, &pauseAfter, 500*time.Millisecond)
}

// The call is quiet for pauseAfter while the agent works on what it handed over: the upstream session is closed and
// the app told link{paused}; no heartbeat wakes it. The user asking how far it is does (link{waking}, a new session
// told what was said before the pause and what is still awaited, link{live}); quiet again, it pauses again, and the
// answer wakes it: said in the new session, its delegation gone with the first one.
func TestPauseWhileTheAgentWorks(t *testing.T) {
	quick(t)
	setVar(t, &heartbeatAfter, time.Second)
	hub := agentHub(t)
	hub.manual = true
	up := newSessionsUp(t, true, nil)
	pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	spoke := time.Now()
	a.send(map[string]any{"type": "text", "text": "跑一下全部测试"})
	m := hub.waitSent(1)
	hub.edit("a1", func(s *fakeSession) { s.status, s.activity = "working", "在跑 npm test" })
	a.until(linkIn(linkPaused))
	if took := time.Since(spoke); took < pauseAfter {
		t.Fatalf("paused %v after the user spoke", took)
	}
	up.find(t, 0, "session.close", ofType("session.close"))
	time.Sleep(1500 * time.Millisecond) // past heartbeatAfter: no heartbeat, no session for one
	if n := up.count(); n != 1 {
		t.Fatalf("%d sessions while paused", n)
	}

	a.send(map[string]any{"type": "text", "text": "状态：测试跑到哪了"})
	a.until(linkIn(linkWaking))
	a.until(linkIn(linkLive))
	instructions := instructionsOf(up.find(t, 1, "its start", isStart))
	for _, want := range []string{"语音暂停了一下", "暂停前说过的话", "用户：跑一下全部测试", "## 还在等的事", "「跑一下全部测试」还没有结果"} {
		if !strings.Contains(instructions, want) {
			t.Errorf("instructions lack %q: %s", want, instructions)
		}
	}
	if strings.Contains(instructions, "语音连接刚断过") || strings.Contains(instructions, "状态：测试跑到哪了") {
		t.Errorf("instructions: %s", instructions)
	}
	if got := content(up.find(t, 1, "the status", func(f map[string]any) bool { return f["delegation_id"] == "dlg2" })); !strings.Contains(got, "还在做") {
		t.Fatalf("status: %s", got)
	}

	a.until(linkIn(linkPaused))
	hub.add("a1", proto.Item{Role: "assistant", Kind: proto.KindText, Text: "全部通过，改了 `web/src/Login.tsx`。"})
	hub.end("a1")
	a.untilAll(linkIn(linkWaking), linkIn(linkLive), func(f map[string]any) bool {
		return f["type"] == "run" && f["id"] == m.ID && f["state"] == runDone
	})
	if got := content(up.find(t, 2, "the answer, said", said)); !strings.Contains(got, "Claude Code 这一轮做完了") || !strings.Contains(got, "全部通过") {
		t.Fatalf("said: %s", got)
	}
	if slices.ContainsFunc(up.frames(0), func(f map[string]any) bool { return f["delegation_id"] == "dlg1" }) {
		t.Fatal("the answer went on the paused session's delegation")
	}
	if hub.lookCount() == 0 || len(hub.messages()) != 1 {
		t.Fatalf("sent %+v", hub.messages())
	}
}

// untilAll reads the app's frames until each of match has matched one, in any order.
func (a *app) untilAll(match ...func(map[string]any) bool) {
	a.t.Helper()
	left := slices.Clone(match)
	a.until(func(f map[string]any) bool {
		left = slices.DeleteFunc(left, func(m func(map[string]any) bool) bool { return m(f) })
		return len(left) == 0
	})
}

// A paused upstream wakes on the user's voice for wakeVoice: the zero frames of a muted app never wake it, nor a
// shorter sound. The app's audio of the last ringSpan goes to the new session first, in order, before what comes once
// it is up; none of it went to the paused one.
func TestWakeOnTheUsersVoice(t *testing.T) {
	quick(t)
	hub := workingAgent(t)
	up := newSessionsUp(t, false, nil)
	pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	a.until(linkIn(linkPaused))
	up.find(t, 0, "session.close", ofType("session.close"))

	a.speak(4*time.Second, false) // muted
	a.speak(100*time.Millisecond, true)
	a.speak(300*time.Millisecond, false)
	time.Sleep(300 * time.Millisecond)
	if n := up.count(); n != 1 {
		t.Fatal("woken by silence or a click")
	}
	for range 15 { // 300 ms of the user's voice
		_ = a.c.Write(t.Context(), websocket.MessageBinary, tone(320, 3000))
	}
	a.until(linkIn(linkWaking))
	a.until(linkIn(linkLive))
	for range 10 {
		_ = a.c.Write(t.Context(), websocket.MessageBinary, tone(320, 2000))
	}
	var amps []int16
	var sizes []int
	waitFor(t, "the new session's audio", func() bool {
		amps, sizes = up.audio(1)
		return slices.Index(amps, 2000) >= 0 && len(amps)-slices.Index(amps, 2000) == 10
	})
	live := slices.Index(amps, 2000)
	if n := len(slices.DeleteFunc(slices.Clone(amps[:live]), func(v int16) bool { return v != 3000 })); n != 20 {
		t.Errorf("%d frames of the voice replayed before the live audio, want 20 (the click and the words)", n)
	}
	held := 0
	for _, n := range sizes[:live] {
		held += n
	}
	// At most ringSpan was kept when the new session came up (4.7 s of audio came in before); the words may still come
	// in as that goes out (a loaded machine reads the app's frames late), and go out after it.
	if most := int(ringSpan.Seconds())*16000*2 + 15*640; held > most {
		t.Errorf("%d bytes replayed, more than %v of audio and the words", held, ringSpan)
	}
	if old, _ := up.audio(0); slices.Contains(old, 3000) || slices.Contains(old, 2000) {
		t.Error("audio went to the paused session")
	}
}

// A line the user types while paused wakes the upstream and goes to the new session once it is up; the session is
// told what was said before the pause, not that line.
func TestTypedLineWakesTheUpstream(t *testing.T) {
	quick(t)
	hub := workingAgent(t)
	up := newSessionsUp(t, false, nil)
	pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", map[string]any{"history": []map[string]string{
		{"role": "user", "text": "记住数字七"}, {"role": "assistant", "text": "好的，记住了。"}}}))
	a.until(ofType("ready"))
	a.until(linkIn(linkPaused))
	a.send(map[string]any{"type": "text", "text": "数字是几"})
	a.until(finalLine("user", "数字是几"))
	a.until(linkIn(linkWaking))
	a.until(linkIn(linkLive))
	instructions := instructionsOf(up.find(t, 1, "its start", isStart))
	if !strings.Contains(instructions, "暂停前说过的话") || !strings.Contains(instructions, "用户：记住数字七") || strings.Contains(instructions, "数字是几") {
		t.Fatalf("instructions: %s", instructions)
	}
	up.find(t, 1, "the typed line", func(f map[string]any) bool {
		return f["type"] == "session.commentary.append" && f["delegation_id"] == nil && content(f) == "数字是几"
	})
}

// A line the user types while paused counts as the user speaking once it reaches the new session: after the agent's
// words that session's instructions took in (the agent wrote while paused, which closes the user-speech gate as the
// session comes up), an instruction typed then is sent and a "Stop:" stops the turn, neither refused as unprompted.
func TestTypedLineAfterTheAgentWrote(t *testing.T) {
	for _, line := range []string{"顺便把 README 也改一下", "停止：先别做了"} {
		t.Run(line, func(t *testing.T) {
			quick(t)
			setVar(t, &gateWait, 300*time.Millisecond)
			hub := workingAgent(t)
			up := newSessionsUp(t, true, nil)
			pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
			a := dialApp(t, pl, boundStart("a1", nil))
			a.until(ofType("ready"))
			a.until(linkIn(linkPaused))
			hub.add("a1", proto.Item{Role: "assistant", Kind: proto.KindText, Text: "集成测试跑完一半了。"})
			a.send(map[string]any{"type": "text", "text": line})
			a.until(linkIn(linkLive))
			if in := instructionsOf(up.find(t, 1, "its start", isStart)); !strings.Contains(in, "集成测试跑完一半了") {
				t.Fatalf("the new session was not told what the agent wrote: %s", in)
			}
			if stop := strings.HasPrefix(line, "停止："); stop {
				got := content(up.find(t, 1, "the answer on dlg1", func(f map[string]any) bool { return f["delegation_id"] == "dlg1" }))
				hub.mu.Lock()
				interrupts := slices.Clone(hub.interrupts)
				hub.mu.Unlock()
				if !strings.Contains(got, "已经叫停了 Claude Code") || !slices.Equal(interrupts, []string{"a1"}) {
					t.Fatalf("stop: %s (interrupts %v)", got, interrupts)
				}
				return
			}
			a.until(func(f map[string]any) bool {
				if f["type"] == "run" && f["state"] == runRefused {
					t.Fatalf("refused: %v", f)
				}
				return f["type"] == "run" && f["state"] == runSent
			})
			if m := hub.waitSent(1); !strings.Contains(m.Text, "README") {
				t.Fatalf("sent %+v", m)
			}
		})
	}
}

// The user's voice kept while paused counts once it reaches the new session (#5672), as a typed line does: the agent
// wrote while paused (the gate closes as the session comes up), the user's voice wakes it, and the woken session's
// model hands what they said over without a caption of it (a reply that only calls a tool has it come after the
// result): sent, not refused as unprompted.
func TestVoiceAfterTheAgentWrote(t *testing.T) {
	quick(t)
	setVar(t, &gateWait, 300*time.Millisecond)
	setVar(t, &spokeVoice, 100*time.Millisecond) // spoken before it wakes: all of the voice is kept
	hub := workingAgent(t)
	var mu sync.Mutex
	sessions, voiced := map[*websocket.Conn]int{}, map[*websocket.Conn]int{}
	up := newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		switch f["type"] {
		case "session.start":
			mu.Lock()
			sessions[c] = len(sessions)
			mu.Unlock()
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
		case "session.input_audio.append":
			b, _ := base64.StdEncoding.DecodeString(f["audio"].(string))
			loud := len(b) >= 2 && int16(uint16(b[0])|uint16(b[1])<<8) == 3000
			mu.Lock()
			n := voiced[c]
			if loud {
				voiced[c]++
			} else if n >= 10 && sessions[c] > 0 { // the woken session heard the user out: its model hands it over
				voiced[c] = -1 << 20
				delegate(u, c, "dlg1", "顺便把 README 也改一下")
			}
			mu.Unlock()
		}
	})
	pl := testPlugin(t, liveProvider(up), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	a.until(linkIn(linkPaused))
	hub.add("a1", proto.Item{Role: "assistant", Kind: proto.KindText, Text: "集成测试跑完一半了。"})
	a.speak(wakeVoice, true) // kept until the session is up: nothing of it goes live
	a.until(linkIn(linkWaking))
	a.until(linkIn(linkLive))
	a.speak(200*time.Millisecond, false)
	a.until(func(f map[string]any) bool {
		if f["type"] == "run" && f["state"] == runRefused {
			t.Fatalf("refused: %v", f)
		}
		return f["type"] == "run" && f["state"] == runSent
	})
	if m := hub.waitSent(1); !strings.Contains(m.Text, "README") {
		t.Fatalf("sent %+v", m)
	}
}

// recLink is an upstream session that keeps the frames written to it; once broken it refuses the next write, after
// broke (as the session's reader noticing the drop).
type recLink struct {
	mu     sync.Mutex
	frames []map[string]any
	left   int // writes it takes before it breaks; < 0: it never does
	broke  func()
}

func (l *recLink) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	<-ctx.Done()
	return 0, nil, ctx.Err()
}

func (l *recLink) Write(_ context.Context, _ websocket.MessageType, p []byte) error {
	l.mu.Lock()
	if l.left == 0 {
		l.mu.Unlock()
		l.broke()
		return net.ErrClosed
	}
	l.left--
	var f map[string]any
	_ = json.Unmarshal(p, &f)
	l.frames = append(l.frames, f)
	l.mu.Unlock()
	return nil
}

func (l *recLink) Close(websocket.StatusCode, string) error { return nil }
func (l *recLink) CloseNow() error                          { return nil }

// sent is what it got: an audio frame as its first sample, a typed line as itself.
func (l *recLink) sent() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, f := range l.frames {
		if f["type"] == "session.input_audio.append" {
			b, _ := base64.StdEncoding.DecodeString(f["audio"].(string))
			out = append(out, fmt.Sprint(b[0]))
		} else {
			out = append(out, content(f))
		}
	}
	return out
}

// A woken session that drops before what was kept for it has gone out leaves the rest kept, in order: the session lost
// reconnects to gets it (lost flushes once it is up), the lines typed still counting as the user speaking then.
func TestFlushAfterTheWokenSessionDropped(t *testing.T) {
	c := &conversation{version: 2, pl: &Plugin{logger: log.New(io.Discard, "", 0)}, ctx: t.Context(), w: liveWire{},
		wake: make(chan struct{}, 1)}
	c.sus.state = linkWaking
	for i := range 3 {
		c.sus.ring.add([]byte{byte(i + 1), 0}, 1<<20)
	}
	c.sus.texts = []string{"数字是几", "顺便看看日志"}
	want := []string{"1", "2", "3", "数字是几", "顺便看看日志"}

	c.flushRing() // it dropped before this began: lost took it off
	if c.sus.state != linkWaking || c.sus.flush || len(c.sus.ring.frames) != 3 || len(c.sus.texts) != 2 {
		t.Fatalf("not kept: %+v", c.sus)
	}

	// The next one drops after two frames: its reader sees it and lost takes it off.
	first := &recLink{left: 2}
	first.broke = func() {
		c.mu.Lock()
		if c.up == first {
			c.gen++
			c.up, c.ready = nil, false
		}
		c.mu.Unlock()
	}
	c.up, c.ready = first, true
	c.flushRing()
	if got := first.sent(); !slices.Equal(got, want[:2]) {
		t.Fatalf("the dropped session got %v", got)
	}
	if c.sus.state != linkWaking || c.sus.flush || len(c.sus.ring.frames) != 1 || len(c.sus.texts) != 2 || len(c.history) != 0 {
		t.Fatalf("not kept: %+v, history %v", c.sus, c.history)
	}

	// Its line breaks it too: the line is kept, not taken as said.
	second := &recLink{left: 1}
	second.broke = func() {
		c.mu.Lock()
		if c.up == second {
			c.gen++
			c.up, c.ready = nil, false
		}
		c.mu.Unlock()
	}
	c.up, c.ready = second, true
	c.flushRing()
	if got := second.sent(); !slices.Equal(got, want[2:3]) || len(c.sus.texts) != 2 || len(c.history) != 0 {
		t.Fatalf("the second session got %v; kept %v, history %v", got, c.sus.texts, c.history)
	}

	c.mu.Lock()
	c.gate.told(time.Now(), agentReconnect) // the reconnected session's snapshot has the agent's new words
	at := c.gate.agent
	c.mu.Unlock()
	third := &recLink{left: -1}
	c.up, c.ready = third, true
	c.flushRing()
	if got := third.sent(); !slices.Equal(got, want[3:]) {
		t.Fatalf("the reconnected session got %v", got)
	}
	if c.sus.state != "" || c.sus.flush || len(c.history) != 2 || c.history[1].Text != "顺便看看日志" {
		t.Fatalf("after: %+v, history %v", c.sus, c.history)
	}
	if !c.gate.openSince(at, time.Now()) {
		t.Fatal("the lines typed do not count as the user speaking")
	}
	select {
	case <-c.wake:
	default:
		t.Fatal("what waits to be said was not let go")
	}
}

// A woken session that drops as soon as it is up is a drop like any other: reconnecting, and the session after it is
// told the connection dropped (not that the voice was paused). The line typed to wake it is not lost: written to the
// woken session before the drop was seen, it is the last line of what was said, which the next one is told to answer;
// else that one gets it.
func TestWokenSessionDrops(t *testing.T) {
	quick(t)
	hub := workingAgent(t)
	up := newSessionsUp(t, false, nil)
	up.drops = func(n int) bool { return n == 1 }
	pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	a.until(linkIn(linkPaused))
	a.send(map[string]any{"type": "text", "text": "数字是几"})
	a.until(linkIn(linkWaking))
	a.until(linkIn(linkReconnecting))
	a.until(linkIn(linkLive))
	in := instructionsOf(up.find(t, 2, "its start", isStart))
	if !strings.Contains(in, "语音连接刚断过") || strings.Contains(in, "语音暂停了一下") {
		t.Fatalf("instructions: %s", in)
	}
	if strings.Contains(in, "用户：数字是几") {
		return
	}
	up.find(t, 2, "the typed line", func(f map[string]any) bool {
		return f["type"] == "session.commentary.append" && content(f) == "数字是几"
	})
}

// A card asked by voice is asked again to the session after a drop, once; a card that wakes a paused upstream is asked
// to the new session once (reconnected, which the wake calls too, does not ask it again).
func TestCardAfterANewSession(t *testing.T) {
	card := proto.Interaction{ID: "q1", Kind: proto.IxQuestion, MachineID: "m", SessionID: "a1",
		Questions: []proto.Question{{Question: "数据库用哪个？", Options: []proto.QuestionOption{{Label: "Postgres"}, {Label: "SQLite"}}}}}
	asked := func(up *sessionsUp, n int) int {
		return len(slices.DeleteFunc(up.frames(n), func(f map[string]any) bool { return !strings.Contains(content(f), "数据库用哪个") }))
	}
	for _, wake := range []bool{false, true} {
		t.Run(fmt.Sprint("wake=", wake), func(t *testing.T) {
			quick(t)
			hub := workingAgent(t)
			up := newSessionsUp(t, false, nil)
			pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
			a := dialApp(t, pl, boundStart("a1", nil))
			a.until(ofType("ready"))
			if wake {
				a.until(linkIn(linkPaused))
				hub.raise(card)
				a.untilAll(linkIn(linkWaking), linkIn(linkLive), func(f map[string]any) bool { return f["type"] == "ask" && f["state"] == askAsking })
			} else {
				hub.raise(card)
				a.until(func(f map[string]any) bool { return f["type"] == "ask" && f["state"] == askAsking })
				up.drop(0)
				a.until(linkIn(linkLive))
			}
			waitFor(t, "the question in the new session", func() bool { return asked(up, 1) > 0 })
			time.Sleep(time.Second) // the tracker's look after the session came up
			if n := asked(up, 1); n != 1 {
				t.Fatalf("asked %d times in the new session", n)
			}
		})
	}
}

// A card told to this upstream session is not asked again to it, however late the tracker comes to the new session
// (reask); one told to the session before is.
func TestToldBefore(t *testing.T) {
	c := &conversation{}
	c.startAsk("q1")
	if c.toldBefore("q1") {
		t.Fatal("not told yet")
	}
	c.gen++ // a wake: the question was queued, and is told to the new session
	c.told("q1")
	if c.toldBefore("q1") || c.toldBefore("q2") {
		t.Fatal("told to this session: asked again")
	}
	c.gen++ // it dropped
	if !c.toldBefore("q1") {
		t.Fatal("told to the last session: not asked again")
	}
	c.told("q1") // asked again
	if c.toldBefore("q1") {
		t.Fatal("asked again to this session: asked once more")
	}
}

// A wake is tried wakeMost times in a row: two refusals and it is up (the count starts over: two more on the next wake
// are not the end); three and the call ends (upstream_failed).
func TestWakeTriesThreeTimesInARow(t *testing.T) {
	for _, fails := range []int{2, 3} {
		t.Run(fmt.Sprint("fails=", fails), func(t *testing.T) {
			quick(t)
			setVar(t, &wakeBackoff, 20*time.Millisecond)
			hub := workingAgent(t)
			up := newSessionsUp(t, false, func(n int) bool { return n >= 1 && n <= fails || n == 4 || n == 5 })
			pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
			a := dialApp(t, pl, boundStart("a1", nil))
			a.until(ofType("ready"))
			a.until(linkIn(linkPaused))
			a.send(map[string]any{"type": "text", "text": "到哪了"})
			a.until(linkIn(linkWaking))
			if fails == 3 {
				if f := a.until(ofType("error")); f["code"] != codeUpstreamFailed || f["fatal"] != true {
					t.Fatalf("error: %v", f)
				}
				if n := up.count(); n != 4 {
					t.Fatalf("%d sessions", n)
				}
				return
			}
			a.until(linkIn(linkLive))
			a.until(linkIn(linkPaused))
			a.send(map[string]any{"type": "text", "text": "现在呢"})
			a.until(linkIn(linkLive))
			if n := up.count(); n != 7 {
				t.Fatalf("%d sessions", n)
			}
		})
	}
}

// Pausing and waking are not drops: more of them than maxReconnects leave the reconnects as they were, and a drop
// after is reconnected.
func TestPausingIsNotADrop(t *testing.T) {
	quick(t)
	setVar(t, &stableAfter, time.Hour)
	hub := workingAgent(t)
	up := newSessionsUp(t, false, nil)
	pl := testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t))
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	for i := range maxReconnects + 1 {
		a.until(linkIn(linkPaused))
		a.send(map[string]any{"type": "text", "text": fmt.Sprint("第 ", i, " 句")})
		a.until(linkIn(linkLive))
	}
	up.drop(up.count() - 1)
	a.until(linkIn(linkReconnecting))
	a.until(linkIn(linkLive))
	a.mu.Lock()
	failed := a.types["error"]
	a.mu.Unlock()
	if failed {
		t.Fatal("the call ended")
	}
}

// No pause when the bound conversation does not work, while a card is asked by voice, nor on protocol 1.
func TestNoPause(t *testing.T) {
	quick(t)
	quiet := func(a *app) {
		t.Helper()
		time.Sleep(3 * pauseAfter)
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.types["link"] {
			t.Error("a link frame")
		}
	}

	t.Run("idle", func(t *testing.T) {
		hub := agentHub(t)
		up := newSessionsUp(t, false, nil)
		a := dialApp(t, testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t)), boundStart("a1", nil))
		a.until(ofType("ready"))
		quiet(a)
	})

	t.Run("asking", func(t *testing.T) {
		hub := workingAgent(t)
		hub.raise(proto.Interaction{ID: "q1", Kind: proto.IxQuestion, MachineID: "m", SessionID: "a1",
			Questions: []proto.Question{{Question: "数据库用哪个？", Options: []proto.QuestionOption{{Label: "Postgres"}, {Label: "SQLite"}}}}})
		up := newSessionsUp(t, false, nil)
		a := dialApp(t, testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t)), boundStart("a1", nil))
		a.until(ofType("ready"))
		a.until(func(f map[string]any) bool { return f["type"] == "ask" && f["state"] == askAsking })
		quiet(a)
		hub.answerOnScreen("q1")
		a.until(func(f map[string]any) bool { return f["type"] == "ask" && f["state"] == askReleased })
		a.until(linkIn(linkPaused))
	})

	t.Run("protocol 1", func(t *testing.T) {
		hub := newFakeHub(t)
		hub.status("main1", "working")
		up := newSessionsUp(t, false, nil)
		a := dialApp(t, testPlugin(t, liveProvider(up.fakeUpstream), hub.server(t)), map[string]any{"type": "start"})
		a.until(ofType("ready"))
		quiet(a)
		a.protocolOne()
	})
}

// What holds a pause off: each of these, set alone on a call that may pause, does.
func TestPauseDue(t *testing.T) {
	now := time.Now()
	recent, long := now.Add(-pauseAfter/2), now.Add(-2*pauseAfter)
	may := func() *conversation {
		c := &conversation{version: 2, ready: true, up: newFakeLink(), pending: map[string]*time.Timer{}}
		c.sus.works, c.connectedAt = true, long
		return c
	}
	if !may().pauseDue(now) {
		t.Fatal("a quiet call whose conversation works does not pause")
	}
	// What still holds a pause off on hold (#9290): no session to pause, or a meeting call. Everything else does not.
	hard := map[string]bool{"protocol 1": true, "paused": true, "reconnecting": true, "over": true, "a meeting call": true}
	for what, set := range map[string]func(c *conversation){
		"protocol 1":             func(c *conversation) { c.version = 1 },
		"it does not work":       func(c *conversation) { c.sus.works = false },
		"paused":                 func(c *conversation) { c.sus.state = linkPaused },
		"reconnecting":           func(c *conversation) { c.ready, c.up = false, nil },
		"over":                   func(c *conversation) { c.closed = true },
		"a meeting call":         func(c *conversation) { c.meeting = &meetingCall{} },
		"the model speaks":       func(c *conversation) { c.pace.playEnd = now.Add(time.Second) },
		"the model spoke lately": func(c *conversation) { c.pace.playEnd = recent },
		"the user's voice":       func(c *conversation) { c.pace.voice = recent },
		"the user's caption":     func(c *conversation) { c.gate.user = recent },
		"the user's open line":   func(c *conversation) { c.user = &line{id: "u1", text: "等一下"} },
		"a handover":             func(c *conversation) { c.pace.delegated = recent },
		"told the model":         func(c *conversation) { c.sus.said = recent },
		"a new session":          func(c *conversation) { c.connectedAt = recent },
		"something to say":       func(c *conversation) { c.queue = []*sentWords{{}} },
		"a call to come":         func(c *conversation) { c.pending["d1"] = nil },
		"a card asked":           func(c *conversation) { c.askCur = "q1" },
	} {
		c := may()
		set(c)
		if c.pauseDue(now) {
			t.Errorf("%s: it pauses", what)
		}
		c = may()
		set(c)
		c.sus.onHold = true
		if c.pauseDue(now) != !hard[what] {
			t.Errorf("%s, on hold: it pauses %v", what, c.pauseDue(now))
		}
	}
	idle := may()
	idle.sus.works, idle.connectedAt, idle.sus.onHold = false, now, true
	if !idle.pauseDue(now) {
		t.Error("on hold: an idle call that just connected does not pause")
	}
}

// The ring keeps the last frames up to its size, oldest first, and empties as it is taken.
func TestRing(t *testing.T) {
	var r ring
	for i := range 10 {
		r.add([]byte{byte(i), 0}, 7)
	}
	r.add(nil, 7)
	got := r.take()
	if len(got) != 3 || got[0][0] != 7 || got[2][0] != 9 {
		t.Fatalf("kept %v", got)
	}
	if len(r.take()) != 0 || r.size != 0 {
		t.Fatal("not emptied")
	}
	r.add(make([]byte, 20), 7) // a frame larger than it all is kept alone
	if got := r.take(); len(got) != 1 {
		t.Fatalf("kept %d frames", len(got))
	}
}

// A long call's history with a full snapshot stays within instructionsMost: its oldest lines go first.
func TestInstructionsFit(t *testing.T) {
	var history []prompts.VoiceLine
	for i := range historyLines {
		history = append(history, prompts.VoiceLine{Role: "user", Text: fmt.Sprintf("line %d %s", i, strings.Repeat("word ", 40))})
	}
	snap := &prompts.VoiceTarget{Kind: "main", State: "working", Title: strings.Repeat("t", 120), Goal: strings.Repeat("g", 200),
		Work: strings.Repeat("w", 200)}
	for range recentMost {
		snap.Recent = append(snap.Recent, prompts.VoiceLine{Role: "assistant", Text: strings.Repeat("r", recentRunes)})
	}
	for range agentsMost {
		snap.Agents = append(snap.Agents, prompts.VoiceAgent{Title: strings.Repeat("a", 80), State: "working"})
	}
	in := prompts.VoiceInstructions{Lang: "en", Tools: "bound", History: clipHistory(history), Target: snap, Paused: true}
	if utf8.RuneCountInString(prompts.RenderVoiceInstructions(in)) <= instructionsMost {
		t.Fatal("the test's instructions fit as they are")
	}
	got := fitInstructions(in)
	if n := utf8.RuneCountInString(got); n > instructionsMost || !strings.Contains(got, fmt.Sprintf("line %d ", historyLines-1)) ||
		strings.Contains(got, "line 0 ") {
		t.Fatalf("%d runes: %s", n, got)
	}
}
