package recording

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func newFeedTest(t *testing.T) *feedTest {
	t.Helper()
	f := newFakeHub().withMachine()
	f.hello.Locale = "en"
	f.settings = &Settings{MachineID: "server", Main: "main1", Timezone: "UTC"}
	f.known["main1"] = true
	f.sessions["server"] = []proto.Session{
		{SessionID: "main1", Assistant: true, Status: proto.StatusIdle, State: proto.StateIdle},
		{SessionID: "agent1", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone, State: proto.StateUnread, PermissionMode: "acceptEdits"},
	}
	ft := &feedTest{t: t, f: f, now: time.Now().UTC().Truncate(time.Second)}
	ft.start = ft.now.Add(-10 * time.Minute)
	ft.s = newCore(t, f)
	ft.s.attend.feed.now = func() time.Time { return ft.now }
	ft.s.attend.turns.now = ft.s.attend.feed.now
	ft.s.attend.feed.after = func(d time.Duration, _ func()) func() bool {
		ft.timed = append(ft.timed, d)
		return func() bool { return true }
	}
	ft.e = ft.s.engine()
	return ft
}

func (ft *feedTest) look() time.Time { return ft.s.attendLook(context.Background(), feedRec) }

// A batch holds at most feedBatchRunes of live text, the latest; the older lines are counted as left out. Empty
// positions are never fed.
func TestMeetingFeedBatchOf(t *testing.T) {
	long := strings.Repeat("a", feedBatchRunes/2)
	l := kb.Live{Segments: []kb.LiveSegment{{Text: "first"}, {Text: long}, {Text: ""}, {Text: long}, {Text: "last"}}, Fed: 1}
	b := feedBatchOf(l, false, false)
	if b.upto != 5 || b.leftOut != 1 || len(b.lines) != 2 || b.lines[0].Text != long || b.lines[1].Text != "last" {
		t.Fatalf("batch %+v", b)
	}
	if b := feedBatchOf(l, true, false); b.leftOut != 2 || !b.soFar {
		t.Fatalf("so far %+v", b)
	}
	if b := feedBatchOf(kb.Live{Segments: []kb.LiveSegment{{Text: ""}}}, false, false); !b.empty() || b.upto != 1 {
		t.Fatalf("only an empty position: %+v", b)
	}
}

// After a restart the meetings still attended are picked up; a kick is looked at once, and the look asks for the next.
func TestMeetingFeedSchedules(t *testing.T) {
	ft := newFeedTest(t)
	ft.upload(kb.AttendanceListener, nil, false, false)
	ft.timed = nil
	ft.s.attend.feed.mu.Lock()
	ft.s.attend.feed.timers = nil
	ft.s.attend.feed.mu.Unlock()
	ft.s.attendResume(ft.e)
	if len(ft.timed) != 1 || ft.timed[0] != feedKickDelay {
		t.Fatalf("timers %v", ft.timed)
	}
	ft.say(0, "Ada: hello.") // a kick while one is due as early already: no second timer
	if len(ft.timed) != 1 {
		t.Fatalf("timers %v", ft.timed)
	}
	ft.s.attendRun(feedRec)
	if chats, _ := ft.sent(); len(chats) != 1 {
		t.Fatalf("the look sent %d", len(chats))
	}
	if len(ft.timed) != 2 || ft.timed[1] != feedBatchEvery {
		t.Fatalf("timers %v", ft.timed)
	}
}

const feedRec = "a0000000-0000-4000-8000-0000000000a1"

// feedTest is an assistant with a knowledge base whose main chat main1 (on "server") attends recordings, a clock the
// test moves, and no timer of the feeder's ever firing: the test looks itself (attendLook).
type feedTest struct {
	t     *testing.T
	s     *Service
	f     *fakeHub
	e     *kb.Engine
	now   time.Time
	start time.Time
	rev   int64
	timed []time.Duration // what the feeder asked its timers for
}

// upload stores the phone's whole state of feedRec: a meeting attended in role by session (nil: the main chat).
func (ft *feedTest) upload(role string, session *kb.SessionRef, ended, complete bool) {
	ft.t.Helper()
	ft.rev++
	u := kb.RecordingUpload{Revision: ft.rev, StartedAt: ft.start, Device: "phone", Title: "Design review", Kind: kb.RecordingMeeting,
		Attendance: role, AttendingSession: session, Complete: complete, Segments: []kb.RecordingSegment{}}
	if ended {
		at := ft.now
		u.EndedAt = &at
	}
	if _, err := ft.e.PutRecording(feedRec, u, ft.now); err != nil {
		ft.t.Fatal(err)
	}
}

// say appends live lines from position from.
func (ft *feedTest) say(from int, texts ...string) {
	ft.t.Helper()
	var segs []kb.LiveSegment
	for i, text := range texts {
		segs = append(segs, kb.LiveSegment{Start: float64(60 * (from + i)), End: float64(60*(from+i) + 5), Text: text})
	}
	if _, _, err := ft.e.LiveAppend(feedRec, from, segs); err != nil {
		ft.t.Fatal(err)
	}
}

func (ft *feedTest) live() kb.Live {
	ft.t.Helper()
	l, _, err := ft.e.LiveRead(feedRec)
	if err != nil {
		ft.t.Fatal(err)
	}
	return l
}

// sent are the batches gone to Persona chats (assistant.send) and to Agents (session.send), in order.
func (ft *feedTest) sent() (chats, agents []map[string]any) {
	ft.f.mu.Lock()
	defer ft.f.mu.Unlock()
	return append([]map[string]any(nil), ft.f.sends...), append([]map[string]any(nil), ft.f.agentSent...)
}

// turnDone plays the batch's turn in session running and ending, as the session lists would show it.
func (ft *feedTest) turnDone(session string) {
	ft.s.meetingTurnStarted("server", session, false)
	ft.s.meetingTurnEnded("server", session)
}

func (ft *feedTest) setSession(id string, fn func(*proto.Session)) {
	ft.f.mu.Lock()
	defer ft.f.mu.Unlock()
	for i := range ft.f.sessions["server"] {
		if ft.f.sessions["server"][i].SessionID == id {
			fn(&ft.f.sessions["server"][i])
		}
	}
}

// The main chat gets the meeting in batches: the first lines at once, as message source meeting through assistant.send
// (resumed when it stopped); nothing more within the minute; then only the new lines, with the answers since.
func TestMeetingFeedBatches(t *testing.T) {
	ft := newFeedTest(t)
	ft.upload(kb.AttendanceParticipant, nil, false, false)
	if next := ft.look(); !next.IsZero() {
		t.Fatalf("nothing said yet: look again at %v", next)
	}
	ft.say(0, "Ada: let's keep the cap for this release.", "")
	if next := ft.look(); !next.Equal(ft.now.Add(feedBatchEvery)) {
		t.Fatalf("next look %v", next)
	}
	chats, _ := ft.sent()
	if len(chats) != 1 {
		t.Fatalf("sent %+v", chats)
	}
	first := chats[0]
	text, _ := first["text"].(string)
	src, _ := first["message_source"].(map[string]any)
	if first["session_id"] != "main1" || first["resume_only"] != true || first["client_msg_id"] != nil || src["kind"] != "meeting" ||
		src["title"] != "Design review" || !strings.Contains(text, "Ada: let's keep the cap") || !strings.Contains(text, `"kind":"meeting"`) ||
		!strings.Contains(text, "Lines so far") || !strings.Contains(text, "as a participant") {
		t.Fatalf("first batch %+v", first)
	}
	l := ft.live()
	if l.Fed != 2 || l.FedTo == nil || *l.FedTo != (kb.SessionRef{MachineID: "server", SessionID: "main1"}) || !l.FedAt.Equal(ft.now) || l.Problem != "" {
		t.Fatalf("live log after the first batch %+v", l)
	}
	if on, _ := ft.s.meetingTurnRunning(context.Background(), "server", "main1"); !on {
		t.Fatal("the batch's turn is not marked a meeting turn")
	}

	// Within the minute: nothing goes, the look comes back when it is up.
	ft.say(2, "Bo: I'll write the regression test by Friday.")
	ft.now = ft.now.Add(10 * time.Second)
	if next := ft.look(); !next.Equal(l.FedAt.Add(feedBatchEvery)) {
		t.Fatalf("within the minute: next %v", next)
	}
	// The minute is up, but the batch's turn is still running: it waits.
	ft.now = l.FedAt.Add(feedBatchEvery)
	if next := ft.look(); !next.Equal(ft.now.Add(feedBusyPoll)) {
		t.Fatalf("busy: next %v", next)
	}
	ft.turnDone("main1")
	_, _ = ft.e.LiveUpdate(feedRec, func(l *kb.Live, _ kb.Recording) error {
		l.Asks = append(l.Asks, kb.LiveAsk{ID: "q1", Question: "What did we decide last time?", State: feedAskAnswered, Answer: "You kept the 4 MB cap."},
			kb.LiveAsk{ID: "q2", Question: "Who owns it?", State: "pending"})
		return nil
	})
	ft.look()
	chats, _ = ft.sent()
	text, _ = chats[len(chats)-1]["text"].(string)
	if len(chats) != 2 || !strings.Contains(text, "New lines") || strings.Contains(text, "Ada:") || !strings.Contains(text, "Bo: I'll write") ||
		!strings.Contains(text, "You kept the 4 MB cap.") || strings.Contains(text, "Who owns it?") {
		t.Fatalf("second batch %q", text)
	}
	if l := ft.live(); l.Fed != 3 || !l.Asks[0].Fed || l.Asks[1].Fed {
		t.Fatalf("after the second batch %+v", l)
	}
}

// A busy session keeps the batch with no problem; one that may not attend, or cannot take messages, says why; a
// dialog or a draft at the computer keeps the batch, nothing pressed.
func TestMeetingFeedProblems(t *testing.T) {
	ft := newFeedTest(t)
	ft.upload(kb.AttendanceListener, nil, false, false)
	ft.say(0, "Ada: hello.")

	ft.setSession("main1", func(s *proto.Session) { s.Status, s.State = proto.StatusWorking, proto.StateWorking })
	if next := ft.look(); !next.Equal(ft.now.Add(feedBusyPoll)) || ft.live().Problem != "" || ft.live().Fed != 0 {
		t.Fatalf("busy: next %v, %+v", next, ft.live())
	}
	ft.setSession("main1", func(s *proto.Session) {
		s.Status, s.State, s.PermissionMode = proto.StatusIdle, proto.StateIdle, proto.ModeBypass
	})
	if next := ft.look(); !next.Equal(ft.now.Add(feedRetry)) || ft.live().Problem != attendeeNotAllowed {
		t.Fatalf("bypass: next %v, %+v", next, ft.live())
	}
	ft.setSession("main1", func(s *proto.Session) { s.PermissionMode = "default" })

	// Another session named: an Agent, closed.
	agent := &kb.SessionRef{MachineID: "server", SessionID: "agent1"}
	ft.upload(kb.AttendanceListener, agent, false, false)
	ft.setSession("agent1", func(s *proto.Session) { s.Status, s.State = proto.StatusOffline, proto.StateOffline })
	ft.look()
	if p := ft.live().Problem; p != attendeeUnavailable {
		t.Fatalf("a closed Agent: %q", p)
	}
	if _, agents := ft.sent(); len(agents) != 0 {
		t.Fatalf("a closed Agent was sent to (resumed): %+v", agents)
	}
	// Running again, but a draft in its input box: the batch waits, and its turn is not marked.
	ft.setSession("agent1", func(s *proto.Session) { s.Status, s.State = proto.StatusDone, proto.StateUnread })
	ft.f.mu.Lock()
	ft.f.failSend = &rpc.Error{Code: "input_draft", Message: "the input box holds a draft"}
	ft.f.mu.Unlock()
	if next := ft.look(); !next.Equal(ft.now.Add(feedBusyPoll)) || ft.live().Problem != "" || ft.live().Fed != 0 {
		t.Fatalf("a draft: next %v, %+v", next, ft.live())
	}
	if on, _ := ft.s.meetingTurnRunning(context.Background(), "server", "agent1"); on {
		t.Fatal("a refused batch marked a meeting turn")
	}
	ft.f.mu.Lock()
	ft.f.failSend = nil
	ft.f.mu.Unlock()
	ft.look()
	_, agents := ft.sent()
	if len(agents) != 1 || agents[0]["session_id"] != "agent1" || !strings.Contains(agents[0]["text"].(string), "Ada: hello.") {
		t.Fatalf("the Agent's batch %+v", agents)
	}
	if l := ft.live(); l.Fed != 1 || l.FedTo.SessionID != "agent1" || l.Problem != "" {
		t.Fatalf("after the Agent's batch %+v", l)
	}

	// No main chat: unavailable.
	ft.upload(kb.AttendanceListener, nil, false, false)
	setSettings(t, ft.s, func(v *Settings) { v.Main = "" })
	ft.now = ft.now.Add(feedBatchEvery)
	ft.look()
	if p := ft.live().Problem; p != attendeeUnavailable {
		t.Fatalf("no main chat: %q", p)
	}
}

// Another session to feed (here the main chat again after an Agent, or attending turned on again) first gets the
// meeting so far; turned off, nothing is fed and the live lines stay.
func TestMeetingFeedSessionChanges(t *testing.T) {
	ft := newFeedTest(t)
	ft.upload(kb.AttendanceListener, &kb.SessionRef{MachineID: "server", SessionID: "agent1"}, false, false)
	ft.say(0, "Ada: one.")
	ft.look()
	ft.turnDone("agent1")
	ft.say(1, "Bo: two.")
	ft.upload(kb.AttendanceListener, nil, false, false) // the main chat now
	ft.now = ft.now.Add(feedBatchEvery)
	ft.look()
	chats, agents := ft.sent()
	if len(agents) != 1 || len(chats) != 1 {
		t.Fatalf("sent %d to Agents, %d to chats", len(agents), len(chats))
	}
	if text := chats[0]["text"].(string); !strings.Contains(text, "Lines so far") || !strings.Contains(text, "Ada: one.") || !strings.Contains(text, "Bo: two.") {
		t.Fatalf("the new session's batch %q", text)
	}
	ft.turnDone("main1")

	ft.upload(kb.AttendanceOff, nil, false, false)
	if _, _, err := ft.e.LiveAppend(feedRec, 2, []kb.LiveSegment{{Text: "Ada: three."}}); !errors.Is(err, kb.ErrNotAttending) {
		t.Fatalf("a line while off: %v", err)
	}
	ft.now = ft.now.Add(feedBatchEvery)
	if next := ft.look(); !next.IsZero() || ft.live().FedTo != nil || len(ft.live().Segments) != 2 {
		t.Fatalf("off: next %v, %+v", next, ft.live())
	}
	ft.upload(kb.AttendanceListener, nil, false, false)
	ft.say(2, "Ada: three.")
	ft.look()
	chats, _ = ft.sent()
	if text := chats[len(chats)-1]["text"].(string); len(chats) != 2 || !strings.Contains(text, "Lines so far") || !strings.Contains(text, "Ada: one.") {
		t.Fatalf("on again: %d batches, %q", len(chats), text)
	}
}

// The end batch goes once: at the first upload with both ended_at and complete, or three minutes after the first with
// ended_at; nothing is fed after it.
func TestMeetingFeedEnd(t *testing.T) {
	ft := newFeedTest(t)
	ft.upload(kb.AttendanceParticipant, nil, false, false)
	ft.say(0, "Ada: one.")
	ft.look()
	ft.turnDone("main1")
	ended := ft.now
	ft.upload(kb.AttendanceParticipant, nil, true, false)
	ft.say(1, "Bo: last words.")
	ft.now = ft.now.Add(feedBatchEvery)
	if next := ft.look(); !next.Equal(ft.now.Add(feedBatchEvery)) {
		t.Fatalf("a batch before the end: next %v", next)
	}
	ft.turnDone("main1")
	ft.now = ft.now.Add(feedBatchEvery)
	if next := ft.look(); !next.Equal(ended.Add(feedEndWait)) {
		t.Fatalf("nothing new, ended: next %v (the end's deadline %v)", next, ended.Add(feedEndWait))
	}
	ft.now = ended.Add(feedEndWait)
	if next := ft.look(); !next.IsZero() {
		t.Fatalf("after the end batch: next %v", next)
	}
	chats, _ := ft.sent()
	if text := chats[len(chats)-1]["text"].(string); len(chats) != 3 || !strings.Contains(text, "has ended") {
		t.Fatalf("%d batches, the last %q", len(chats), text)
	}
	if !ft.live().EndFed {
		t.Fatal("the end batch is not noted")
	}
	ft.turnDone("main1")
	ft.upload(kb.AttendanceParticipant, &kb.SessionRef{MachineID: "server", SessionID: "agent1"}, true, true)
	ft.now = ft.now.Add(time.Hour)
	if next := ft.look(); !next.IsZero() {
		t.Fatalf("after the end: next %v", next)
	}
	if chats, agents := ft.sent(); len(chats) != 3 || len(agents) != 0 {
		t.Fatal("fed after the end batch")
	}

	// A meeting whose session never became free is not asked about forever: once nothing came of it for half a day,
	// only a change looks at it again.
	st := newFeedTest(t)
	st.upload(kb.AttendanceListener, nil, true, false)
	st.say(0, "Ada: one.")
	st.setSession("main1", func(s *proto.Session) { s.Status, s.State = proto.StatusWorking, proto.StateWorking })
	if next := st.look(); next.IsZero() {
		t.Fatal("a busy session is not looked at again")
	}
	st.now = st.now.Add(feedStale + time.Minute)
	if next := st.look(); !next.IsZero() {
		t.Fatalf("stale: next %v", next)
	}

	// Ended and complete at once: the end batch, with what is left, does not wait for the minute or the deadline.
	at := newFeedTest(t)
	at.upload(kb.AttendanceListener, nil, false, false)
	at.say(0, "Ada: one.")
	at.look()
	at.turnDone("main1")
	at.say(1, "Bo: two.")
	at.now = at.now.Add(5 * time.Second)
	at.upload(kb.AttendanceListener, nil, true, true)
	if next := at.look(); !next.IsZero() {
		t.Fatalf("complete: next %v", next)
	}
	chats, _ = at.sent()
	if text := chats[len(chats)-1]["text"].(string); len(chats) != 2 || !strings.Contains(text, "Bo: two.") || !strings.Contains(text, "has ended") {
		t.Fatalf("%d batches, the last %q", len(chats), text)
	}
}
