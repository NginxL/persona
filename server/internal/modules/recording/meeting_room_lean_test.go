package recording

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func newLeanRoom(t *testing.T) *leanRoom {
	t.Helper()
	r := newRoomFixture(t)
	lr := &leanRoom{roomFixture: r, start: time.Now().Add(-time.Hour).Truncate(time.Minute).UTC()}
	lr.clock.Store(lr.start.Add(time.Minute).UnixNano())
	r.s.attend.feed.now = lr.now
	lr.sid = r.roomUpWith(meetingRec, "listener", map[string]any{"started_at": lr.start.Format(time.RFC3339)})
	r.f.answerNode(DeviceAssistantCompact, func(_ string, params json.RawMessage) (any, error) {
		var a map[string]any
		_ = json.Unmarshal(params, &a)
		lr.compacts, lr.compactParams = append(lr.compacts, lr.now()), append(lr.compactParams, a)
		if lr.compactErr != nil {
			return nil, lr.compactErr
		}
		if !lr.noMark {
			lr.rowLocked(func(c *proto.Session) {
				c.Compaction = &proto.Compaction{State: proto.CompactionRunning, StartedAt: lr.now()}
			})
		}
		return map[string]any{"ok": true}, nil
	})
	return lr
}

func (lr *leanRoom) look() time.Time { return lr.roomFixture.look(meetingRec) }

// span is what the batches given since the room's last compaction cover now.
func (lr *leanRoom) span() (from, to float64, runes int) {
	return roomSpan(lr.live(), lr.lean().SpanAt)
}

// spanDue: a compaction is due by the span now.
func (lr *leanRoom) spanDue() bool { return roomCompactDue(lr.live(), lr.lean()) }

// A compaction is due once the batches since the room started or was last compacted cover 45 minutes of the meeting
// and 10,000 characters of lines, both. What they cover is read off the live log: its lines from where the span starts
// (the feed position at the last compaction) to its feed position, so a restart between a batch and a note of it loses
// nothing.
func TestRoomCompactionDueRule(t *testing.T) {
	for _, c := range []struct {
		from, to float64
		runes    int
		want     bool
	}{
		{0, 45 * 60, 10000, true},
		{600, 600 + 45*60, 25000, true},
		{0, 45*60 - 1, 50000, false},
		{0, 120 * 60, 9998, false},
	} {
		l := kb.Live{Segments: []kb.LiveSegment{{Start: c.from, End: c.from + 1, Text: strings.Repeat("a", c.runes/2)},
			{Start: c.to - 1, End: c.to, Text: strings.Repeat("b", c.runes-c.runes/2)}, {Start: c.to + 600, End: c.to + 601, Text: "not fed yet"}}, Fed: 2}
		if got := roomCompactDue(l, kb.MeetingRoomLean{}); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
		if roomCompactDue(l, kb.MeetingRoomLean{SpanAt: 1}) { // the first line given before the last compaction
			t.Errorf("%+v: due without its first line", c)
		}
	}
	if roomCompactDue(kb.Live{}, kb.MeetingRoomLean{SpanAt: 3}) {
		t.Error("due with nothing given")
	}
	// The span batches give: the earliest start to the latest end among their lines, and their characters.
	lr := newLeanRoom(t)
	lr.say(10, "一二三四五", "六七八九十")
	lr.look()
	lr.advance(roomLeanEvery)
	lr.say(130, "abc")
	lr.look()
	if from, to, runes := lr.span(); from != 10 || to != 135.5 || runes != 13 {
		t.Fatalf("span %v–%v, %d characters", from, to, runes)
	}
}

// A compaction starts only at an idle moment: not while a turn runs, an ask waits, a photo is still to go in, the role
// is off, or the meeting's end is due. Then once, holding the room's slot; while it runs the room counts as working
// (no batch, an ask waits); once done the span starts again, it is counted, and the next batch shows the lists.
func TestRoomCompactionWaitsForAnIdleRoom(t *testing.T) {
	lr := newLeanRoom(t)
	lr.say(10, "Ada: we keep the cap.")
	lr.look()
	lr.due()
	lr.advance(30 * time.Second)

	lr.row(func(c *proto.Session) { c.Status, c.State = proto.StatusWorking, proto.StateWorking })
	lr.look()
	if n := len(lr.compactCalls()); n != 0 {
		t.Fatalf("compacted while a turn runs: %d", n)
	}
	lr.row(func(c *proto.Session) { c.Status, c.State = proto.StatusIdle, proto.StateIdle })

	if _, err := lr.engine().LiveUpdate(meetingRec, func(l *kb.Live, _ kb.Recording) error {
		l.Asks = append(l.Asks, kb.LiveAsk{ID: askOne, Question: "q", State: askPending, Via: kb.AskViaRoom, AskedAt: lr.now()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	lr.s.asks.mu.Lock() // as if it were being answered: not taken up again by the look
	if lr.s.asks.running == nil {
		lr.s.asks.running = map[string]map[string]chan struct{}{}
	}
	lr.s.asks.running[meetingRec] = map[string]chan struct{}{askOne: make(chan struct{})}
	lr.s.asks.mu.Unlock()
	lr.look()
	if n := len(lr.compactCalls()); n != 0 {
		t.Fatalf("compacted while an ask waits: %d", n)
	}
	lr.s.asks.mu.Lock()
	delete(lr.s.asks.running, meetingRec)
	lr.s.asks.mu.Unlock()
	if _, err := lr.engine().LiveUpdate(meetingRec, func(l *kb.Live, _ kb.Recording) error { l.Asks = nil; return nil }); err != nil {
		t.Fatal(err)
	}

	// A photo whose sends keep failing: the batches go on, the compaction waits for it.
	lr.photoNode(func(string) (bool, error) { return false, errors.New("the node did not take it") })
	lr.addPhoto(meetingRec, photoBody(t, photoOne, 20, nil))
	for range photoSendTries + 1 {
		lr.look()
	}
	if n := len(lr.compactCalls()); n != 0 {
		t.Fatalf("compacted with a photo to go in: %d", n)
	}
	// Deleted: its withdrawal goes in first (it may have reached the room), and the compaction waits for that too.
	if _, err := lr.engine().PhotoDelete(meetingRec, 1); err != nil {
		t.Fatal(err)
	}
	lr.look()
	if n := len(lr.compactCalls()); n != 0 {
		t.Fatalf("compacted with a withdrawal to go in: %d", n)
	}
	lr.photoNode(func(string) (bool, error) { return false, nil })
	lr.look()
	if sent := lr.photoSends(); len(sent) != 1 || sent[0]["client_msg_id"] != "photo-withdrawn-"+photoOne {
		t.Fatalf("withdrawal %v", sent)
	}

	lr.upload(meetingRec, 2, map[string]any{"room": map[string]any{"attendance": "off"}, "started_at": lr.start.Format(time.RFC3339)})
	lr.look()
	if n := len(lr.compactCalls()); n != 0 {
		t.Fatalf("compacted with the role off: %d", n)
	}
	lr.upload(meetingRec, 3, map[string]any{"room": map[string]any{"attendance": "listener"}, "started_at": lr.start.Format(time.RFC3339)})

	before := len(lr.batches())
	lr.look()
	calls := lr.compactCalls()
	if len(calls) != 1 || lr.lean().CompactStartedAt.IsZero() || !lr.s.room.free(meetingRec) {
		t.Fatalf("not compacted at an idle moment: %v %+v", calls, lr.lean())
	}
	// While it runs: no batch, no second one, the room counts as working.
	lr.advance(roomLeanEvery)
	lr.say(200, "Bo: later.")
	lr.look()
	if n := len(lr.compactCalls()); n != 1 || len(lr.batches()) != before {
		t.Fatalf("while it runs: %d compactions, %d batches", n, len(lr.batches())-before)
	}
	row, _, _ := lr.s.attendeeRow(context.Background(), "server", lr.sid)
	if !roomWorking(row) {
		t.Fatal("a compaction does not count as working")
	}
	if _, verdict := lr.s.attendeeCheckFor(context.Background(), kb.SessionRef{MachineID: "server", SessionID: lr.sid}, lr.now(), meetingRec); verdict != attendeeBusy {
		t.Fatalf("a batch would go during it: %q", verdict)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	if lr.s.roomWaitFree(ctx, lr.engine(), meetingRec, "server", lr.sid) {
		t.Fatal("an ask did not wait for the compaction")
	}
	cancel()

	// Done: counted, the span starts again from where it was asked for (the lines given since: this batch's new one,
	// the meeting so far going again since its role was off), and the batch shows the lists.
	lr.row(func(c *proto.Session) { c.Compaction.State = proto.CompactionDone })
	lr.look()
	ln := lr.lean()
	b := lr.batches()
	if from, to, runes := lr.span(); ln.Compactions != 1 || !ln.CompactedAt.Equal(lr.now()) || !ln.CompactStartedAt.IsZero() || ln.ListsDue ||
		len(b) != before+1 || !hasLists(b[len(b)-1]) || from != 200 || to != 205.5 || runes != utf8.RuneCountInString("Bo: later.") {
		t.Fatalf("after it: %+v, %d batches, span %v–%v %d", ln, len(b)-before, from, to, runes)
	}
	if !lr.s.roomWaitFree(context.Background(), lr.engine(), meetingRec, "server", lr.sid) {
		t.Fatal("an ask still waits")
	}

	// The meeting's end due: none.
	lr.due()
	lr.upload(meetingRec, 4, map[string]any{"room": map[string]any{"attendance": "listener"}, "started_at": lr.start.Format(time.RFC3339),
		"ended_at": lr.now().Format(time.RFC3339), "complete": true})
	lr.advance(time.Minute)
	lr.look()
	if n := len(lr.compactCalls()); n != 1 {
		t.Fatalf("compacted at the meeting's end: %d", n)
	}
}

// free: no ask or photo holds recording's room.
func (r *roomState) free(recording string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.turn[recording] == nil
}

// An assistant.compact whose answer did not come (a link on the way dropped with it out, the manager stopped, the time
// ran out) may have reached the node: it stays noted as asked for, so no second one starts at the retry and nothing goes
// into the room meanwhile, and the session's compaction settles it once its row shows it. One that could not be noted
// first is not asked for.
func TestRoomCompactionInDoubt(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
	}{
		{"dropped", rpc.ErrDropped},
		{"stopped", context.Canceled},
		{"timed out", context.DeadlineExceeded},
		{"relayed", &rpc.Error{Code: CodeNodeClosed, Message: rpc.ErrClosed.Error()}},
	} {
		t.Run(c.name, func(t *testing.T) {
			lr := newLeanRoom(t)
			lr.say(10, "Ada: we keep the cap.")
			lr.look()
			lr.due()
			lr.f.mu.Lock()
			lr.compactErr = c.err
			lr.f.mu.Unlock()
			lr.advance(30 * time.Second)
			before := len(lr.batches())
			lr.say(lr.now().Sub(lr.start).Seconds(), "Bo: more.")
			lr.advance(roomLeanEvery)
			lr.look()
			asked := lr.lean().CompactStartedAt
			if ln := lr.lean(); len(lr.compactCalls()) != 1 || asked.IsZero() || ln.Attempts != 0 || !ln.RetryAt.IsZero() ||
				len(lr.batches()) != before {
				t.Fatalf("no answer: %d calls, %+v, %d batches", len(lr.compactCalls()), ln, len(lr.batches())-before)
			}
			// Where a failure would be tried again (5 minutes): none, it may run; nothing went into the room meanwhile.
			lr.advance(roomCompactRetry + time.Minute)
			lr.look()
			if n := len(lr.compactCalls()); n != 1 || len(lr.batches()) != before {
				t.Fatalf("asked again: %d, %d batches", n, len(lr.batches())-before)
			}
			// The node did take it: done, counted, the span starts again where it was asked for, the batch (all that
			// waited for it) shows the lists.
			lr.row(func(c *proto.Session) {
				c.Compaction = &proto.Compaction{State: proto.CompactionDone, StartedAt: asked.Add(time.Second)}
			})
			lr.advance(roomLeanEvery)
			lr.say(lr.now().Sub(lr.start).Seconds(), "Ada: and then.")
			lr.look()
			if ln := lr.lean(); ln.Compactions != 1 || !ln.CompactStartedAt.IsZero() || ln.Attempts != 0 || lr.spanDue() {
				t.Fatalf("settled: %+v", ln)
			}
			if b := lr.batches(); len(b) != before+1 || !hasLists(b[len(b)-1]) || !strings.Contains(b[len(b)-1], "Bo: more.") {
				t.Fatalf("after it: %d batches", len(b)-before)
			}
		})
	}

	// Not in doubt: refused, or never out.
	for _, err := range []error{&rpc.Error{Code: "busy", Message: "answering"}, &rpc.Error{Code: CodeMachineOffline, Message: "offline"},
		fmt.Errorf("wrapped: %w", rpc.ErrNotWritten)} {
		if roomCompactInDoubt(err) {
			t.Errorf("in doubt: %v", err)
		}
	}

	t.Run("not noted", func(t *testing.T) {
		lr := newLeanRoom(t)
		lr.say(10, "Ada: we keep the cap.")
		lr.look()
		lr.due()
		lr.advance(30 * time.Second)
		l, r, err := lr.engine().LiveRead(meetingRec)
		if err != nil {
			t.Fatal(err)
		}
		// Its record gone from meeting.json under the look: the mark cannot be written.
		if _, err := lr.engine().MeetingUpdate(meetingRec, func(m *kb.Meeting, _ kb.Recording) error { m.Room = nil; return nil }); err != nil {
			t.Fatal(err)
		}
		if _, held, _ := lr.s.roomCompaction(context.Background(), lr.engine(), meetingRec, l, r, lr.now(), true, false, lr.s.roomRowRead(context.Background(), r.Conv.Room)); held ||
			len(lr.compactCalls()) != 0 || !lr.s.room.free(meetingRec) {
			t.Fatalf("asked without the mark: held %v, %d calls", held, len(lr.compactCalls()))
		}
	})
}

// An ask waits for a compaction its room's row shows running for as long as one may run (the node's own limit), not
// only as long as it waits for a turn: it never goes into the compaction, where its own answer time would run out. A
// turn alone holds it askRoomWait.
func TestRoomAskWaitsForACompaction(t *testing.T) {
	lr := newLeanRoom(t)
	recTimings(lr.s, map[string]time.Duration{"askRoomWait": 20 * time.Millisecond}, nil)
	lr.row(func(c *proto.Session) {
		c.Compaction = &proto.Compaction{State: proto.CompactionRunning, StartedAt: lr.now()}
	})
	done := make(chan bool, 1)
	go func() { done <- lr.s.roomWaitFree(context.Background(), lr.engine(), meetingRec, "server", lr.sid) }()
	select {
	case <-done:
		t.Fatal("the ask went into a compaction past its wait for a turn")
	case <-time.After(10 * lr.s.tmNow().askRoomWait):
	}
	lr.row(func(c *proto.Session) { c.Compaction.State = proto.CompactionDone })
	select {
	case free := <-done:
		if !free {
			t.Fatal("not free once it ended")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still waits once it ended")
	}
	lr.row(func(c *proto.Session) { c.Status, c.State = proto.StatusWorking, proto.StateWorking })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !lr.s.roomWaitFree(ctx, lr.engine(), meetingRec, "server", lr.sid) || ctx.Err() != nil {
		t.Fatal("a turn held the ask past its wait")
	}
}

// A compaction asked of the node that its row does not show yet (a row slow to say so, an answer that did not come) is
// one place everything waits at: no ask, photo or withdrawal goes into the room until the row shows it ended or it
// counts as ended (10 minutes).
func TestRoomCompactionAskedHoldsAsksAndPhotos(t *testing.T) {
	lr := newLeanRoom(t)
	lr.photoNode(func(string) (bool, error) { return false, nil })
	lr.say(10, "Ada: we keep the cap.")
	lr.look()
	lr.addPhoto(meetingRec, photoBody(t, photoOne, 20, nil)) // forwarded, so its delete is withdrawn in the room
	lr.look()
	if sent := lr.photoSends(); len(sent) != 1 {
		t.Fatalf("photo %v", sent)
	}
	lr.due()
	lr.f.mu.Lock()
	lr.noMark = true
	lr.f.mu.Unlock()
	lr.advance(30 * time.Second)
	lr.look()
	asked := lr.lean().CompactStartedAt
	if len(lr.compactCalls()) != 1 || asked.IsZero() {
		t.Fatalf("not asked: %+v", lr.lean())
	}
	// An ask waits for it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	if lr.s.roomWaitFree(ctx, lr.engine(), meetingRec, "server", lr.sid) {
		t.Fatal("an ask went in while the compaction may run")
	}
	cancel()
	// A photo waits for it, and so does a withdrawal, also while the role is off; minutes after it was asked for too, its
	// row still showing nothing.
	lr.addPhoto(meetingRec, photoBody(t, photoTwo, 40, nil))
	lr.advance(2 * time.Minute)
	lr.look()
	if _, err := lr.engine().PhotoDelete(meetingRec, 1); err != nil {
		t.Fatal(err)
	}
	lr.upload(meetingRec, 2, map[string]any{"room": map[string]any{"attendance": "off"}, "started_at": lr.start.Format(time.RFC3339)})
	lr.advance(2 * time.Minute)
	lr.look()
	if sent := lr.photoSends(); len(sent) != 1 || len(lr.batches()) != 1 {
		t.Fatalf("sent while the compaction may run: %v, %d batches", sent[1:], len(lr.batches()))
	}
	// Its row shows it ended: the ask goes, and the withdrawal (the role still off).
	lr.row(func(c *proto.Session) {
		c.Compaction = &proto.Compaction{State: proto.CompactionDone, StartedAt: asked.Add(time.Second)}
	})
	if !lr.s.roomWaitFree(context.Background(), lr.engine(), meetingRec, "server", lr.sid) {
		t.Fatal("an ask still waits once it ended")
	}
	lr.advance(feedBusyPoll)
	lr.look()
	if sent := lr.photoSends(); len(sent) != 2 || sent[1]["client_msg_id"] != "photo-withdrawn-"+photoOne {
		t.Fatalf("after it: %v", sent)
	}
	// One never shown counts as ended after 10 minutes: an ask waits no longer.
	lr.setLean(func(ln *kb.MeetingRoomLean) { ln.CompactStartedAt, ln.CompactSeen = lr.now(), asked.Add(time.Second) })
	lr.advance(roomCompactLost)
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !lr.s.roomWaitFree(ctx, lr.engine(), meetingRec, "server", lr.sid) || ctx.Err() != nil {
		t.Fatal("an ask waits past the compaction's limit")
	}
}

// A lean batch without the lists has none of them to render: the topics and highlights are not made up for nothing.
func TestFeedLeanBatchLeavesOutTheListsItDoesNotShow(t *testing.T) {
	r := kb.Recording{ID: meetingRec, StartedAt: time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC)}
	at := 60.0
	b := feedBatch{room: true, topics: true, highlights: true, lean: true, topicsSoFar: []kb.LiveTopic{{Title: "Pricing", State: kb.TopicCurrent, At: &at}},
		highlightsSoFar: []kb.LiveHighlight{{Text: "We keep the cap.", At: 60}}, lines: []kb.LiveSegment{{Start: 60, End: 65, Text: "Ada: we keep the cap."}}}
	if d := feedData(r, b, "en", time.UTC); d.TopicsSoFar != nil || d.HighlightsSoFar != nil {
		t.Fatalf("a lean batch without the lists: %+v", d)
	}
	b.lists = true
	if d := feedData(r, b, "en", time.UTC); len(d.TopicsSoFar) != 1 || len(d.HighlightsSoFar) != 1 {
		t.Fatalf("a lean batch with the lists: %+v", d)
	}
	b.lean, b.lists = false, true // an older room's
	if d := feedData(r, b, "en", time.UTC); len(d.TopicsSoFar) != 1 || len(d.HighlightsSoFar) != 1 {
		t.Fatalf("an older room's batch: %+v", d)
	}
}

// runRoomTwoHours drives a two-hour meeting (10 lines of 28 Chinese characters a minute, 280 characters a minute) through
// the real roomLook and roomFeed on the test's clock, as the feeder would look (at the time each look asks for, and a
// moment after new lines): a batch's turn takes 20 seconds, in which the room sets its topics (one more every 6 minutes,
// up to 20) and its highlights (up to 4); a compaction takes a minute. older: the room an older manager started.
func runRoomTwoHours(t *testing.T, older bool) roomTwoHourRun {
	t.Helper()
	lr := newLeanRoom(t)
	if older {
		lr.olderRoom(meetingRec)
	}
	var run roomTwoHourRun
	var turnEnd, compactEnd time.Time
	since := 0
	lr.f.mu.Lock()
	lr.f.onAssistantSend = func(f *fakeHub, p map[string]any) {
		if p["session_id"] != lr.sid || p["client_msg_id"] != nil {
			return
		}
		text, _ := p["text"].(string)
		n := utf8.RuneCountInString(text)
		minute := lr.now().Sub(lr.start).Minutes()
		run.batches++
		run.total += n
		run.largest = max(run.largest, n)
		since += n
		run.sinceMost = max(run.sinceMost, since)
		run.at, run.minutes = append(run.at, lr.now().Sub(lr.start)), append(run.minutes, minute)
		run.sizes, run.lists = append(run.sizes, n), append(run.lists, hasLists(text))
		lr.rowLocked(func(c *proto.Session) {
			c.Status, c.State, c.UpdatedAt = proto.StatusWorking, proto.StateWorking, lr.now()
		})
		turnEnd = lr.now().Add(20 * time.Second)
	}
	lr.f.mu.Unlock()
	topicsAt := func(minute float64) []kb.LiveTopic {
		var out []kb.LiveTopic
		k := min(kb.MaxTopics, 1+int(minute)/6)
		for i := range k {
			at := float64(i * 360)
			state := kb.TopicDone
			if i == k-1 {
				state = kb.TopicCurrent
			}
			out = append(out, kb.LiveTopic{Title: fmt.Sprintf("议题%02d：发布计划与测试安排", i+1), State: state, At: &at})
		}
		return out
	}
	highlightsAt := func(minute float64) []kb.LiveHighlight {
		var out []kb.LiveHighlight
		for i, m := range []float64{10, 30, 60, 90} {
			if minute >= m {
				out = append(out, kb.LiveHighlight{Text: fmt.Sprintf("要点%d：发布推迟一周，测试先补齐回归用例", i+1), At: m * 60, End: m*60 + 5.5})
			}
		}
		return out
	}
	const step = 5 * time.Second
	var next, floor time.Time
	line := 0
	end := lr.at(121)
	for now := lr.at(1); now.Before(end); now = now.Add(step) {
		lr.clock.Store(now.UnixNano())
		minute := now.Sub(lr.start).Minutes()
		if now.Sub(lr.start)%(30*time.Second) == 0 && minute <= 120 { // the phone's lines of the last half minute
			from := minute*60 - 30
			var texts []string
			for range 5 {
				texts = append(texts, roomTwoHourLine(line))
				line++
			}
			lr.say(from, texts...)
			next = feedEarliest(next, feedLatest(now.Add(feedKickDelay), floor))
		}
		lr.f.mu.Lock()
		if !turnEnd.IsZero() && !now.Before(turnEnd) {
			lr.rowLocked(func(c *proto.Session) { c.Status, c.State, c.UpdatedAt = proto.StatusIdle, proto.StateIdle, turnEnd })
			turnEnd = time.Time{}
			lr.f.mu.Unlock()
			if _, err := lr.engine().LiveUpdate(meetingRec, func(l *kb.Live, _ kb.Recording) error {
				l.Topics, l.Highlights = topicsAt(minute), highlightsAt(minute)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			lr.f.mu.Lock()
		}
		if calls := len(lr.compacts); calls > len(run.compactions) {
			run.compactions = append(run.compactions, minute)
			compactEnd = now.Add(time.Minute)
		}
		if !compactEnd.IsZero() && !now.Before(compactEnd) {
			lr.rowLocked(func(c *proto.Session) {
				done := now
				c.Compaction.State, c.Compaction.FinishedAt = proto.CompactionDone, &done
			})
			compactEnd = time.Time{}
			run.compactd = append(run.compactd, now.Sub(lr.start))
			since = 0
		}
		lr.f.mu.Unlock()
		if !next.IsZero() && !now.Before(next) {
			next = lr.look()
			floor = now.Add(feedBusyPoll)
		}
	}
	return run
}

// leanRoom is a room fixture on a clock the test moves, with its room up (lean, unless made older) and a node that
// takes assistant.compact: it marks the room's compaction running, as a node does, unless compactErr refuses it.
type leanRoom struct {
	*roomFixture
	sid   string
	start time.Time
	clock atomic.Int64
	pos   int // the live log's next position
	// Under the fake's lock: assistant.compact asked of the node (when, and its params), and its answer.
	compacts      []time.Time
	compactParams []map[string]any
	compactErr    error
	noMark        bool // the node takes it but its row does not say so (yet)
}

func (lr *leanRoom) now() time.Time { return time.Unix(0, lr.clock.Load()).UTC() }

func (lr *leanRoom) advance(d time.Duration) { lr.clock.Add(int64(d)) }

// at is the clock at minute m of the meeting.
func (lr *leanRoom) at(m float64) time.Time {
	return lr.start.Add(time.Duration(m * float64(time.Minute)))
}

// rowLocked changes the room's row; the caller holds the fake's lock.
func (lr *leanRoom) rowLocked(fn func(c *proto.Session)) {
	for i := range lr.f.sessions["server"] {
		if lr.f.sessions["server"][i].SessionID == lr.sid {
			fn(&lr.f.sessions["server"][i])
		}
	}
}

func (lr *leanRoom) row(fn func(c *proto.Session)) {
	lr.f.mu.Lock()
	defer lr.f.mu.Unlock()
	lr.rowLocked(fn)
}

// compactCalls are the times assistant.compact was asked of the node.
func (lr *leanRoom) compactCalls() []time.Time {
	lr.f.mu.Lock()
	defer lr.f.mu.Unlock()
	return append([]time.Time(nil), lr.compacts...)
}

// say appends the lines said from second from on, each 6 seconds after the last.
func (lr *leanRoom) say(from float64, texts ...string) {
	lr.t.Helper()
	var segs []kb.LiveSegment
	for i, text := range texts {
		start := from + float64(6*i)
		segs = append(segs, kb.LiveSegment{Start: start, End: start + 5.5, Text: text})
	}
	if _, _, err := lr.engine().LiveAppend(meetingRec, lr.pos, segs); err != nil {
		lr.t.Fatal(err)
	}
	lr.pos += len(segs)
}

// batches are the texts of the batches the room got (the header and asks excluded).
func (lr *leanRoom) batches() []string {
	var out []string
	for _, s := range lr.sends() {
		if s["session_id"] == lr.sid && s["resume_only"] == true && s["client_msg_id"] == nil {
			text, _ := s["text"].(string)
			out = append(out, text)
		}
	}
	return out
}

func (lr *leanRoom) lean() kb.MeetingRoomLean { return lr.meeting(meetingRec).Room.Lean }

// setLean changes the room's bookkeeping (a span that is due, say).
func (lr *leanRoom) setLean(fn func(ln *kb.MeetingRoomLean)) {
	lr.t.Helper()
	if _, err := lr.engine().MeetingUpdate(meetingRec, func(m *kb.Meeting, _ kb.Recording) error { fn(&m.Room.Lean); return nil }); err != nil {
		lr.t.Fatal(err)
	}
}

// due makes the span of the batches since the last compaction one that is due: lines covering 46 minutes and 12,000
// characters, given to the room (the live log's feed position past them) as batches would have.
func (lr *leanRoom) due() {
	lr.t.Helper()
	var segs []kb.LiveSegment
	for i := range 12 {
		start := float64(i) * 250 // the last ends at 2755.5: 46 minutes from the first's start
		segs = append(segs, kb.LiveSegment{Start: start, End: start + 5.5, Text: strings.Repeat("字", 1000)})
	}
	if _, _, err := lr.engine().LiveAppend(meetingRec, lr.pos, segs); err != nil {
		lr.t.Fatal(err)
	}
	lr.pos += len(segs)
	if _, err := lr.engine().LiveUpdate(meetingRec, func(l *kb.Live, _ kb.Recording) error { l.Fed = len(l.Segments); return nil }); err != nil {
		lr.t.Fatal(err)
	}
}

// live is the recording's live log now.
func (lr *leanRoom) live() kb.Live {
	lr.t.Helper()
	l, _, err := lr.engine().LiveRead(meetingRec)
	if err != nil {
		lr.t.Fatal(err)
	}
	return l
}

func hasLists(text string) bool {
	return strings.Contains(text, "Topics so far:") || strings.Contains(text, "Highlights so far:")
}

// A room this manager starts is lean: its CLAUDE.md says batches come every two minutes, and it gets one at most every
// two minutes (the first showing the lists, "none yet", then none), each with one short line and no rules. A room an
// older manager started keeps a batch a minute with the rules and the lists in each; #2981 attending sessions keep a
// batch a minute too.
func TestRoomLeanBatchesEveryTwoMinutes(t *testing.T) {
	lr := newLeanRoom(t)
	if m := lr.meeting(meetingRec); m.Room.Batching != kb.RoomBatchingLean {
		t.Fatalf("room %+v", m.Room)
	}
	if md, _ := lr.prepared[0]["claude_md"].(string); !strings.Contains(md, "Batches of lines come every two minutes") ||
		!strings.Contains(md, "first read the newest lines with `meeting_lines`") || !strings.Contains(md, "write nothing") {
		t.Fatalf("CLAUDE.md %s", md)
	}
	lr.say(10, "Ada: we keep the cap.")
	lr.look()
	b := lr.batches()
	if len(b) != 1 || !strings.Contains(b[0], "Topics so far: none yet.") || !strings.Contains(b[0], "Highlights so far: none yet.") ||
		!strings.Contains(b[0], "Update the topics and the highlights if the meeting moved on, as your rules say, then write nothing.") ||
		strings.Contains(b[0], "meeting_topics_set") {
		t.Fatalf("first batch %v", b)
	}
	fedAt := lr.now()
	lr.advance(time.Minute)
	lr.say(70, "Bo: and the tiers.")
	if next := lr.look(); len(lr.batches()) != 1 || !next.Equal(fedAt.Add(roomLeanEvery)) {
		t.Fatalf("a batch a minute later: %d batches, next %v", len(lr.batches()), next)
	}
	lr.advance(time.Minute)
	lr.look()
	b = lr.batches()
	if len(b) != 2 || hasLists(b[1]) || strings.Contains(b[1], "meeting_highlights_set") || !strings.Contains(b[1], "Bo: and the tiers.") ||
		!strings.Contains(b[1], "then write nothing.") {
		t.Fatalf("second batch %v", b)
	}

	// An older room: a batch a minute, the rules and the lists in each.
	lr.olderRoom(meetingRec)
	lr.advance(roomLeanEvery)
	lr.say(130, "Ada: next.")
	lr.look()
	lr.advance(time.Minute)
	lr.say(190, "Bo: pricing.")
	lr.look()
	b = lr.batches()
	if len(b) != 4 || !strings.Contains(b[3], "meeting_topics_set") || !strings.Contains(b[3], "Topics so far:") {
		t.Fatalf("an older room's batches %v", b[2:])
	}
	if lr.meeting(meetingRec).Room.Lean != (kb.MeetingRoomLean{}) {
		t.Fatalf("an older room keeps lean bookkeeping: %+v", lr.meeting(meetingRec).Room.Lean)
	}
	if feedBatchEvery != time.Minute {
		t.Fatalf("#2981's batches come every %v", feedBatchEvery)
	}
}

// The lists go only at the boundaries: the first batch, the first after the room's conversation was compacted, the
// first once the last that showed them is 30 minutes old, and the meeting so far after the room was fed afresh. When
// they last went is kept in meeting.json, so a restart behaves; it raises no revision the apps read.
func TestRoomLeanListsAtBoundaries(t *testing.T) {
	lr := newLeanRoom(t)
	revs := lr.meeting(meetingRec).Revs[kb.PartRoom]
	var shown []bool
	for i := range 17 { // a batch every two minutes for 32 minutes
		lr.say(float64(i*120+10), fmt.Sprintf("Ada: point %d.", i))
		lr.look()
		b := lr.batches()
		if len(b) != i+1 {
			t.Fatalf("minute %d: %d batches", i*2, len(b))
		}
		shown = append(shown, hasLists(b[i]))
		lr.advance(roomLeanEvery)
	}
	for i, s := range shown {
		if want := i == 0 || i == 15; s != want { // minute 0, and minute 30
			t.Fatalf("batch %d (minute %d) shows the lists: %v; all %v", i, i*2, s, shown)
		}
	}
	if _, _, runes := lr.span(); !lr.lean().ListsAt.Equal(lr.start.Add(time.Minute+30*time.Minute)) || runes == 0 {
		t.Fatalf("bookkeeping %+v, %d characters", lr.lean(), runes)
	}
	if got := lr.meeting(meetingRec).Revs[kb.PartRoom]; got != revs {
		t.Fatalf("the room's revision went from %d to %d", revs, got)
	}
	// Fed afresh (the role turned off and on again): the meeting so far, with the lists.
	lr.attendNoteFresh()
	lr.say(17*120+10, "Bo: later.")
	lr.look()
	if b := lr.batches(); !strings.Contains(b[len(b)-1], "Lines so far") || !hasLists(b[len(b)-1]) {
		t.Fatalf("the meeting so far %s", b[len(b)-1])
	}
	// The next one does not; after a compaction ended, the one after it does.
	lr.advance(roomLeanEvery)
	lr.say(18*120+10, "Ada: then.")
	lr.look()
	if b := lr.batches(); hasLists(b[len(b)-1]) {
		t.Fatalf("after the meeting so far %s", b[len(b)-1])
	}
	lr.setLean(func(ln *kb.MeetingRoomLean) { ln.ListsDue = true })
	lr.advance(roomLeanEvery)
	lr.say(19*120+10, "Bo: done.")
	lr.look()
	if b := lr.batches(); !hasLists(b[len(b)-1]) || lr.lean().ListsDue {
		t.Fatalf("after a compaction %s %+v", b[len(b)-1], lr.lean())
	}
}

// attendNoteFresh has the room fed afresh, as after its role turned off and on again.
func (lr *leanRoom) attendNoteFresh() {
	lr.t.Helper()
	if _, err := lr.engine().LiveUpdate(meetingRec, func(l *kb.Live, _ kb.Recording) error { l.FedTo = nil; return nil }); err != nil {
		lr.t.Fatal(err)
	}
}

// A restart between a batch's two writes (the live log's feed position, then the room's record) loses none of the
// span: it is read off the live log. Here the room's record is as it was before the batch that makes the span due.
func TestRoomCompactionSpanSurvivesALostNote(t *testing.T) {
	lr := newLeanRoom(t)
	lr.say(10, "Ada: we keep the cap.")
	lr.look()
	var segs []kb.LiveSegment
	for i := range 11 { // 40 minutes and 11,000 characters given: not due yet
		segs = append(segs, kb.LiveSegment{Start: float64(i * 240), End: float64(i*240) + 5.5, Text: strings.Repeat("字", 1000)})
	}
	if _, _, err := lr.engine().LiveAppend(meetingRec, lr.pos, segs); err != nil {
		t.Fatal(err)
	}
	lr.pos += len(segs)
	if _, err := lr.engine().LiveUpdate(meetingRec, func(l *kb.Live, _ kb.Recording) error { l.Fed = len(l.Segments); return nil }); err != nil {
		t.Fatal(err)
	}
	if lr.spanDue() {
		t.Fatal("due before the batch")
	}
	before := lr.lean()
	lr.advance(roomLeanEvery)
	lr.say(46*60, "Bo: and the tiers, at minute 46.")
	lr.look()
	lr.setLean(func(ln *kb.MeetingRoomLean) { *ln = before }) // the note after the batch lost
	lr.advance(feedBusyPoll)
	lr.look()
	if n := len(lr.compactCalls()); n != 1 {
		t.Fatalf("the batch's span lost with its note: %d compactions", n)
	}
}

// A compaction refused (busy, already compacting, the computer away) or failed is tried again at a look at least 5
// minutes later, at most 3 times for the same span; then the room waits for its batches to cover another 45 minutes. A
// node without assistant.compact, or one that would start the room's process without its restrictions, gets none.
func TestRoomCompactionRetries(t *testing.T) {
	lr := newLeanRoom(t)
	lr.say(10, "Ada: we keep the cap.")
	lr.look()
	lr.due()
	lr.f.mu.Lock()
	lr.compactErr = &rpc.Error{Code: "busy", Message: "answering"}
	lr.f.mu.Unlock()
	lr.advance(30 * time.Second)
	next := lr.look()
	if n, ln := len(lr.compactCalls()), lr.lean(); n != 1 || ln.Attempts != 1 || !ln.RetryAt.Equal(lr.now().Add(roomCompactRetry)) ||
		!ln.CompactStartedAt.IsZero() || next.After(ln.RetryAt) {
		t.Fatalf("refused: %d %+v next %v", n, ln, next)
	}
	lr.advance(time.Minute)
	lr.look()
	if n := len(lr.compactCalls()); n != 1 {
		t.Fatalf("tried again after a minute: %d", n)
	}
	lr.advance(4 * time.Minute)
	lr.look()
	lr.advance(roomCompactRetry)
	lr.look()
	if _, _, runes := lr.span(); len(lr.compactCalls()) != 3 || lr.lean().Attempts != 0 || runes != 0 || lr.spanDue() {
		t.Fatalf("three times: %d %+v", len(lr.compactCalls()), lr.lean())
	}
	lr.advance(roomCompactRetry)
	lr.look()
	if n := len(lr.compactCalls()); n != 3 {
		t.Fatalf("a fourth time for the same span: %d", n)
	}

	// The next span is due: tried again. A node that does not know the method: never again.
	lr.due()
	lr.f.mu.Lock()
	lr.compactErr = errors.New("unknown method assistant.compact")
	lr.f.mu.Unlock()
	lr.look()
	if n, ln := len(lr.compactCalls()), lr.lean(); n != 4 || !ln.Never {
		t.Fatalf("an older node: %d %+v", n, ln)
	}
	lr.due()
	lr.advance(time.Hour)
	lr.look()
	if n := len(lr.compactCalls()); n != 4 {
		t.Fatalf("an older node asked again: %d", n)
	}

	// A node that would have to start the room's process on another agent: never either.
	lr.setLean(func(ln *kb.MeetingRoomLean) { ln.Never = false })
	lr.f.mu.Lock()
	lr.compactErr = errors.New("a guest conversation runs on Claude Code only (its built-in tools can be taken away there)")
	lr.f.mu.Unlock()
	lr.look()
	lr.advance(time.Hour)
	lr.look()
	if n, ln := len(lr.compactCalls()), lr.lean(); n != 5 || !ln.Never {
		t.Fatalf("a room it would start without its restrictions: %d %+v", n, ln)
	}
}

// A compaction asked of the node is noted first, so neither a restart nor a row slow to say so starts a second one,
// and nothing goes into the room while it may run; one the node never reports (or no longer: it restarted) has ended
// after 10 minutes: tried again later, and the lists go again, since it may have compacted the room. One that failed is
// tried again 5 minutes later. An earlier compaction of the session's, ended before, is not taken for this one.
func TestRoomCompactionRestartSafe(t *testing.T) {
	lr := newLeanRoom(t)
	lr.say(10, "Ada: we keep the cap.")
	lr.look()
	earlier := lr.now().Add(-time.Hour)
	lr.row(func(c *proto.Session) { // an earlier compaction of the session's, done (the owner's): seen once
		c.Compaction = &proto.Compaction{State: proto.CompactionDone, StartedAt: earlier}
	})
	lr.advance(roomLeanEvery)
	lr.look()
	lr.due()
	lr.f.mu.Lock()
	lr.noMark = true
	lr.f.mu.Unlock()
	lr.advance(30 * time.Second)
	lr.look()
	if ln := lr.lean(); len(lr.compactCalls()) != 1 || ln.CompactStartedAt.IsZero() || !ln.CompactSeen.Equal(earlier) {
		t.Fatalf("started: %+v", ln)
	}
	// Looks after it (as after a restart: all it knows is meeting.json) never start a second one, and no batch goes
	// while it may run: its row shows nothing yet.
	before := len(lr.batches())
	for range 4 { // 8.5 minutes after it was asked for
		lr.advance(roomLeanEvery)
		lr.say(lr.now().Sub(lr.start).Seconds(), "Bo: more.")
		lr.look()
	}
	if n, ln := len(lr.compactCalls()), lr.lean(); n != 1 || len(lr.batches()) != before || ln.CompactStartedAt.IsZero() {
		t.Fatalf("while it may run: %d compactions, %d batches, %+v", n, len(lr.batches())-before, ln)
	}
	lr.advance(roomLeanEvery) // 10.5 minutes
	lr.say(lr.now().Sub(lr.start).Seconds(), "Ada: and then.")
	lr.look()
	ln := lr.lean()
	b := lr.batches()
	if ln.Attempts != 1 || !ln.CompactStartedAt.IsZero() || ln.Compactions != 0 || len(b) != before+1 || !hasLists(b[len(b)-1]) ||
		!strings.Contains(b[len(b)-1], "Bo: more.") {
		t.Fatalf("never reported: %+v, %d batches", ln, len(b)-before)
	}

	// Tried again 5 minutes later; the node restarts under it and reports it failed.
	lr.f.mu.Lock()
	lr.noMark = false
	lr.f.mu.Unlock()
	lr.advance(roomCompactRetry)
	lr.row(func(c *proto.Session) { c.Status = proto.StatusIdle; c.UpdatedAt = lr.now() })
	lr.look()
	if n := len(lr.compactCalls()); n != 2 {
		t.Fatalf("not tried again: %d", n)
	}
	lr.row(func(c *proto.Session) {
		c.Compaction.State, c.Compaction.Error = proto.CompactionFailed, "the process exited"
	})
	lr.look()
	if ln := lr.lean(); ln.Attempts != 2 || !ln.RetryAt.Equal(lr.now().Add(roomCompactRetry)) || ln.Compactions != 0 ||
		!ln.CompactSeen.Equal(lr.now()) { // the failed one's start, asked at this minute
		t.Fatalf("failed: %+v", ln)
	}
}

// A compaction that is due but cannot start (the room's process not running, its computer away, a photo given up on
// for now) is not looked at again for itself: a quiet meeting is not looked at every few seconds for hours. The next
// batch is, once its turn ended, and that look compacts it.
func TestRoomCompactionNotPolledWhileItCannotStart(t *testing.T) {
	lr := newLeanRoom(t)
	lr.say(10, "Ada: we keep the cap.")
	lr.look()
	lr.due()
	far := func(next time.Time) bool { return next.IsZero() || next.Sub(lr.now()) > time.Hour }

	// Its process reaped while the meeting was quiet.
	lr.row(func(c *proto.Session) { c.Status, c.State = proto.StatusOffline, proto.StateIdle })
	lr.advance(roomLeanEvery)
	if next := lr.look(); !far(next) || len(lr.compactCalls()) != 0 {
		t.Fatalf("offline: next %v, %d calls", next, len(lr.compactCalls()))
	}
	// Its computer away.
	lr.row(func(c *proto.Session) { c.Status = proto.StatusIdle })
	lr.f.mu.Lock()
	devices := lr.f.devices
	lr.f.devices = []Device{{ID: "server", Name: "server", Online: false}}
	lr.f.mu.Unlock()
	lr.advance(time.Minute)
	if next := lr.look(); !far(next) || len(lr.compactCalls()) != 0 {
		t.Fatalf("away: next %v, %d calls", next, len(lr.compactCalls()))
	}
	lr.f.mu.Lock()
	lr.f.devices = devices
	lr.f.mu.Unlock()

	// Someone speaks: the batch goes, and the look after its turn compacts the room.
	lr.row(func(c *proto.Session) { c.Status = proto.StatusOffline })
	lr.say(lr.now().Sub(lr.start).Seconds(), "Bo: back to it.")
	before := len(lr.batches())
	next := lr.look()
	if len(lr.batches()) != before+1 || !next.Equal(lr.now().Add(feedBusyPoll)) {
		t.Fatalf("the batch: %d, next %v", len(lr.batches())-before, next.Sub(lr.now()))
	}
	lr.row(func(c *proto.Session) { c.Status = proto.StatusIdle })
	lr.advance(feedBusyPoll)
	lr.look()
	if n := len(lr.compactCalls()); n != 1 {
		t.Fatalf("not compacted after the batch: %d", n)
	}
}

// Claude Code compacting the room on its own: its context, read while no turn runs there, dropped below half of the
// most it reported since the room was last compacted; the next batch shows the lists.
func TestRoomLeanNoticesItsOwnCompaction(t *testing.T) {
	lr := newLeanRoom(t)
	lr.say(10, "Ada: we keep the cap.")
	lr.look()
	lr.row(func(c *proto.Session) { c.ContextTokens, c.UpdatedAt = 120000, lr.now().Add(10*time.Second) })
	lr.advance(30 * time.Second)
	lr.look()
	if ln := lr.lean(); ln.Tokens != 120000 || ln.ListsDue {
		t.Fatalf("read: %+v", ln)
	}
	lr.advance(roomLeanEvery)
	lr.say(130, "Bo: and the tiers.")
	lr.look()
	if b := lr.batches(); hasLists(b[len(b)-1]) {
		t.Fatal("lists without a boundary")
	}
	lr.row(func(c *proto.Session) { c.ContextTokens, c.UpdatedAt = 30000, lr.now().Add(10*time.Second) })
	lr.advance(30 * time.Second)
	lr.look()
	if ln := lr.lean(); !ln.ListsDue {
		t.Fatalf("not noticed: %+v", ln)
	}
	lr.advance(roomLeanEvery)
	lr.say(250, "Ada: next.")
	lr.look()
	if b := lr.batches(); !hasLists(b[len(b)-1]) {
		t.Fatalf("after its own compaction %s", b[len(b)-1])
	}
}

// The hub asks the node for a compaction only_running; a node that refuses not_running (the room's process exited since
// its row said it runs) is as a row that says so: nothing is counted against the span, nothing is polled for, and the
// look after the next batch's turn tries again.
func TestRoomCompactionOnlyWhileItsProcessRuns(t *testing.T) {
	lr := newLeanRoom(t)
	lr.say(10, "Ada: we keep the cap.")
	lr.look()
	lr.due()
	lr.f.mu.Lock()
	lr.compactErr = &rpc.Error{Code: "not_running", Message: "its process is not running"}
	lr.f.mu.Unlock()
	lr.advance(30 * time.Second)
	next := lr.look()
	lr.f.mu.Lock()
	params := lr.compactParams
	lr.compactErr = nil
	lr.f.mu.Unlock()
	if ln := lr.lean(); len(params) != 1 || params[0]["only_running"] != true || params[0]["session_id"] != lr.sid ||
		!ln.CompactStartedAt.IsZero() || ln.Attempts != 0 || !ln.RetryAt.IsZero() {
		t.Fatalf("refused: %v %+v", params, ln)
	}
	if fedAt := lr.live().FedAt; !next.Equal(fedAt.Add(roomLeanEvery)) {
		t.Fatalf("looked at again at %v, the next batch at %v", next, fedAt.Add(roomLeanEvery))
	}
	lr.advance(roomLeanEvery)
	lr.say(lr.now().Sub(lr.start).Seconds(), "Bo: back to it.")
	lr.look()
	lr.advance(feedBusyPoll)
	lr.look()
	if n := len(lr.compactCalls()); n != 2 {
		t.Fatalf("not tried after the next batch: %d", n)
	}
}

// The most context the room reported is kept when a batch shows the lists: Claude Code compacting the room in that
// batch's own turn is still noticed, and the lists go again.
func TestRoomLeanNoticesACompactionInAListsBatch(t *testing.T) {
	lr := newLeanRoom(t)
	lr.say(10, "Ada: we keep the cap.")
	lr.look()
	lr.row(func(c *proto.Session) { c.ContextTokens = 120000 })
	lr.advance(30 * time.Second)
	lr.look()
	lr.setLean(func(ln *kb.MeetingRoomLean) { ln.ListsDue = true }) // the half hour, say
	lr.advance(roomLeanEvery)
	lr.say(130, "Bo: and the tiers.")
	lr.look()
	if b := lr.batches(); !hasLists(b[len(b)-1]) || lr.lean().Tokens != 120000 {
		t.Fatalf("the lists batch: %v %+v", hasLists(b[len(b)-1]), lr.lean())
	}
	lr.row(func(c *proto.Session) { c.ContextTokens = 30000 }) // its turn compacted the room
	lr.advance(30 * time.Second)
	lr.look()
	if ln := lr.lean(); !ln.ListsDue || ln.Tokens != 30000 {
		t.Fatalf("not noticed: %+v", ln)
	}
}

// Claude Code compacting the room on its own starts the span again: no scheduled compaction follows it at once.
func TestRoomLeanOwnCompactionStartsTheSpanAgain(t *testing.T) {
	lr := newLeanRoom(t)
	lr.say(10, "Ada: we keep the cap.")
	lr.look()
	lr.row(func(c *proto.Session) { c.ContextTokens = 160000 })
	lr.advance(30 * time.Second)
	lr.look()
	lr.due()
	lr.row(func(c *proto.Session) { c.ContextTokens = 40000 })
	lr.advance(30 * time.Second)
	lr.look()
	if _, _, runes := lr.span(); len(lr.compactCalls()) != 0 || runes != 0 || !lr.lean().ListsDue {
		t.Fatalf("after its own compaction: %d compactions, %d characters, %+v", len(lr.compactCalls()), runes, lr.lean())
	}
}

// A compaction the owner asked for (the session's compaction done, none asked by the manager) is a boundary too: the
// lists go again and the span starts again, once; it does not count as a scheduled one, nor is the manager's own taken
// for a second boundary.
func TestRoomLeanOwnersCompactionIsABoundary(t *testing.T) {
	lr := newLeanRoom(t)
	lr.say(10, "Ada: we keep the cap.")
	lr.look()
	lr.due()
	owners := lr.now().Add(-time.Minute)
	lr.row(func(c *proto.Session) {
		c.Compaction = &proto.Compaction{State: proto.CompactionDone, StartedAt: owners}
	})
	lr.advance(30 * time.Second)
	lr.look()
	if ln := lr.lean(); len(lr.compactCalls()) != 0 || lr.spanDue() || !ln.ListsDue || ln.Compactions != 0 || !ln.CompactSeen.Equal(owners) {
		t.Fatalf("the owner's compaction: %d compactions, %+v", len(lr.compactCalls()), ln)
	}
	lists := func() int {
		n := 0
		for _, b := range lr.batches() {
			if hasLists(b) {
				n++
			}
		}
		return n
	}
	for i := range 3 { // the lists once after it
		lr.advance(roomLeanEvery)
		lr.say(lr.now().Sub(lr.start).Seconds(), fmt.Sprintf("Bo: point %d.", i))
		lr.look()
	}
	if n := lists(); n != 2 || lr.lean().ListsDue {
		t.Fatalf("%d batches showed the lists, %+v", n, lr.lean())
	}
	// The manager's own, done: one boundary.
	lr.due()
	lr.advance(30 * time.Second)
	lr.look()
	lr.row(func(c *proto.Session) { c.Compaction.State = proto.CompactionDone })
	for i := range 3 {
		lr.advance(roomLeanEvery)
		lr.say(lr.now().Sub(lr.start).Seconds(), fmt.Sprintf("Ada: point %d.", i))
		lr.look()
	}
	if ln := lr.lean(); len(lr.compactCalls()) != 1 || ln.Compactions != 1 || lists() != 3 || ln.ListsDue {
		t.Fatalf("the manager's own: %d compactions, %d lists batches, %+v", len(lr.compactCalls()), lists(), ln)
	}
}

// The context the room's row reports is read whenever no turn runs there, whatever the row's time (the transcript's
// last exchange may be the batch's own line, from before its send answered); but not a value it still reports from
// before a compaction the manager saw end (a compaction writes no reply), which would pass for Claude Code compacting
// the room on its own once the next reply came.
func TestRoomLeanContextReadSoundly(t *testing.T) {
	lr := newLeanRoom(t)
	lr.say(10, "Ada: we keep the cap.")
	lr.look()
	lr.row(func(c *proto.Session) { c.ContextTokens, c.UpdatedAt = 150000, lr.now().Add(-2*time.Second) })
	lr.advance(30 * time.Second)
	lr.look()
	if ln := lr.lean(); ln.Tokens != 150000 {
		t.Fatalf("not read: %+v", ln)
	}
	lr.due()
	lr.look() // compacted: running, then done, the row still reporting the context from before it
	lr.row(func(c *proto.Session) { c.Compaction.State = proto.CompactionDone })
	lr.advance(30 * time.Second)
	lr.look()
	if ln := lr.lean(); ln.Compactions != 1 || ln.Tokens != 0 || ln.TokensStale != 150000 {
		t.Fatalf("after it: %+v", ln)
	}
	lr.advance(roomLeanEvery)
	lr.say(lr.now().Sub(lr.start).Seconds(), "Bo: after it.")
	lr.look()
	lr.row(func(c *proto.Session) { c.ContextTokens = 20000 }) // the first reply after it
	lr.advance(30 * time.Second)
	lr.look()
	if ln := lr.lean(); ln.ListsDue || ln.Tokens != 20000 || ln.TokensStale != 0 {
		t.Fatalf("the reply after it taken for another compaction: %+v", ln)
	}
}

// A compaction whose end its row shows late keeps in the span the batches given after it was asked for: the span
// starts again where it was asked for, not where the look that saw it end is.
func TestRoomCompactionLateEndKeepsLaterBatches(t *testing.T) {
	lr := newLeanRoom(t)
	lr.say(10, "Ada: we keep the cap.")
	lr.look()
	lr.due()
	lr.f.mu.Lock()
	lr.noMark = true
	lr.f.mu.Unlock()
	lr.advance(30 * time.Second)
	lr.look()
	asked := lr.lean().CompactStartedAt
	// Lines given after it was asked for (as by a batch that went before its row showed it).
	lr.say(lr.now().Sub(lr.start).Seconds(), "Bo: after it was asked for.")
	if _, err := lr.engine().LiveUpdate(meetingRec, func(l *kb.Live, _ kb.Recording) error { l.Fed = len(l.Segments); return nil }); err != nil {
		t.Fatal(err)
	}
	lr.row(func(c *proto.Session) {
		c.Compaction = &proto.Compaction{State: proto.CompactionDone, StartedAt: asked.Add(time.Second)}
	})
	lr.advance(30 * time.Second)
	lr.look()
	if _, _, runes := lr.span(); lr.lean().Compactions != 1 || runes != utf8.RuneCountInString("Bo: after it was asked for.") {
		t.Fatalf("the span after it: %d characters, %+v", runes, lr.lean())
	}
}

// A lean room's look reads the room's row once, for the compaction and the batch alike.
func TestRoomLeanLookReadsItsRowOnce(t *testing.T) {
	lr := newLeanRoom(t)
	lr.say(10, "Ada: we keep the cap.")
	lr.look()
	lr.advance(roomLeanEvery)
	lr.say(130, "Bo: and the tiers.")
	from := lr.f.callCount()
	lr.look()
	reads := 0
	lr.f.mu.Lock()
	for _, c := range lr.f.calls[from:] {
		if c.Method == DeviceSessionsList && c.Device == "server" {
			reads++
		}
	}
	lr.f.mu.Unlock()
	if len(lr.batches()) != 2 || reads != 1 {
		t.Fatalf("%d batches, the row read %d times", len(lr.batches()), reads)
	}
}

// roomTwoHourLine is line i of the synthetic meeting: 28 Chinese characters (a number of four digits among them).
func roomTwoHourLine(i int) string {
	return fmt.Sprintf("第%04d句我们继续讨论发布计划和测试安排的具体细节问题", i)
}

// roomTwoHourRun is what a synthetic two-hour meeting gave its room.
type roomTwoHourRun struct {
	batches     int
	total       int // characters of all batches
	largest     int // characters of the largest batch
	sinceMost   int // the most characters given since the last compaction (the whole meeting without one)
	compactions []float64
	// Each batch's time into the meeting (and as minutes), characters and whether it showed the lists; when
	// compactions ended.
	at       []time.Duration
	minutes  []float64
	sizes    []int
	lists    []bool
	compactd []time.Duration
}

// TestRoomTwoHourMeeting: a synthetic two-hour meeting, once for a room an older manager started and once for a lean
// one. The lean room gets at most 61 batches (the older one about 120), each lean batch within twice a minute's lines
// and a fixed overhead and not growing with the topics, the lists only at the boundaries, compactions at about minutes
// 45 and 90, and at most about 45 minutes of batches given to it since its last compaction. The table goes in the PR.
func TestRoomTwoHourMeeting(t *testing.T) {
	if testing.Short() {
		t.Skip("a two-hour meeting")
	}
	if n := utf8.RuneCountInString(roomTwoHourLine(0)); n != 28 {
		t.Fatalf("a line has %d characters", n)
	}
	older := runRoomTwoHours(t, true)
	lean := runRoomTwoHours(t, false)
	t.Logf("two-hour meeting, 280 characters a minute (characters of the texts given to the room):")
	t.Logf("%-8s %8s %12s %8s %18s %14s %12s", "room", "batches", "characters", "largest", "largest w/o lists", "since compact", "compactions")
	for _, row := range []struct {
		name string
		run  roomTwoHourRun
	}{{"older", older}, {"lean", lean}} {
		without := 0
		for i, n := range row.run.sizes {
			if !row.run.lists[i] {
				without = max(without, n)
			}
		}
		withoutText := "-" // every batch of an older room shows them
		if without > 0 {
			withoutText = fmt.Sprint(without)
		}
		t.Logf("%-8s %8d %12d %8d %18s %14d %12d", row.name, row.run.batches, row.run.total, row.run.largest, withoutText, row.run.sinceMost,
			len(row.run.compactions))
	}
	t.Logf("lean compactions started at minutes %.1f; boundary batches at minutes %v", lean.compactions, boundaryMinutes(lean))

	if older.batches < 110 || len(older.compactions) != 0 {
		t.Errorf("older room: %d batches, %d compactions", older.batches, len(older.compactions))
	}
	if lean.batches > 61 {
		t.Errorf("lean room: %d batches in two hours", lean.batches)
	}
	// Each lean batch: twice a minute's lines (with 15% to spare), their clock stamps, and a fixed overhead (the short
	// line, the context block's wrapper and title: about 460 characters here).
	bound := int(2*280*1.15) + int(2*10*1.15)*len("[HH:MM] \n") + 500
	early, late := 0, 0
	for i, n := range lean.sizes {
		if lean.lists[i] {
			continue
		}
		if n > bound {
			t.Errorf("lean batch %d (minute %.1f) has %d characters, over %d", i, lean.minutes[i], n, bound)
		}
		switch m := lean.minutes[i]; {
		case m < 40:
			early = max(early, n)
		case m >= 80:
			late = max(late, n)
		}
	}
	if late > early+2*len("[HH:MM] \n") {
		t.Errorf("lean batches grew with the topics: %d characters before minute 40, %d after minute 80", early, late)
	}
	// The lists only at the boundaries: the first batch, the first after a compaction, the first 30 minutes after the
	// last that showed them.
	var listsAt time.Duration
	ci := 0
	for i, at := range lean.at {
		afterCompaction := false
		for ci < len(lean.compactd) && lean.compactd[ci] <= at {
			afterCompaction, ci = true, ci+1
		}
		want := i == 0 || afterCompaction || at-listsAt >= roomListsEvery
		if lean.lists[i] != want {
			t.Errorf("batch %d (minute %.1f) shows the lists: %v, want %v", i, lean.minutes[i], lean.lists[i], want)
		}
		if lean.lists[i] {
			listsAt = at
		}
	}
	if len(lean.compactions) != 2 || lean.compactions[0] < 45 || lean.compactions[0] > 50 || lean.compactions[1] < 90 || lean.compactions[1] > 96 {
		t.Errorf("compactions at minutes %v", lean.compactions)
	}
	if most := (int(roomCompactSpan/roomLeanEvery) + 3) * bound; lean.sinceMost > most {
		t.Errorf("%d characters given since the last compaction, over %d", lean.sinceMost, most)
	}
}

// boundaryMinutes are the minutes of the batches that showed the lists.
func boundaryMinutes(run roomTwoHourRun) []string {
	var out []string
	for i, l := range run.lists {
		if l {
			out = append(out, fmt.Sprintf("%.1f", run.minutes[i]))
		}
	}
	return out
}
