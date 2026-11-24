package recording

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
)

// Feeding the meetings Persona attends (docs/assistant/meeting-minutes.md "Feeding the attending session"): the
// phone appends the sentences it hears to the recording's live log (kb/live.go), and the attending session gets them
// as one message a batch, at most one a minute, with the questions answered on the phone since the last one, and an
// end batch once the meeting is over. A batch goes only to a session that is free (meeting_attendee.go); one that
// cannot take it keeps it for later, and the live log says why (its problem). What was given to which session is the
// live log's too (fed, fed_to, end_fed), so a restart goes on from there (a batch may be given twice then).
//
// Each recording is looked at on a timer: when the knowledge base says it changed (Engine.LiveChanged, an upload or
// new lines: meetingKick), when the minute between batches is up, while the session is busy, and at the end's
// deadline. One look at a time per recording.

const (
	feedBatchEvery = time.Minute // at most one batch a minute per recording (the end batch does not wait for it)
	feedBatchRunes = 12000       // of live text in a batch: older lines are noted as left out
	feedEndWait    = 3 * time.Minute
	feedBusyPoll   = 20 * time.Second // a session that is busy is looked at again; kicks come no faster either
	feedRetry      = time.Minute      // a session that cannot take batches, or a failure
	feedKickDelay  = time.Second      // an upload and its first lines make one look
	feedTitleRunes = 100
	feedSendWait   = 2 * time.Minute // a stopped Persona chat is resumed first
	// feedStale: a meeting nothing came of for this long (no upload, its end long past) is not looked at again until
	// it changes, so a session that never became free is not asked about every minute for the recording's days.
	feedStale = 12 * time.Hour
)

// attendState is the Service's part for attended meetings.
type attendState struct {
	feed  attendFeeder
	turns meetingTurnBook // meeting_turns.go
}

type attendFeeder struct {
	mu      sync.Mutex
	timers  map[string]attendTimer // by recording id: its next look
	looking map[string]bool
	again   map[string]bool      // a kick came during the look
	floor   map[string]time.Time // no look from a kick before this (feedBusyPoll after the last one)
	// now and after are the clock and time.AfterFunc (nil: the real ones; tests set them).
	now   func() time.Time
	after func(time.Duration, func()) func() bool
}

type attendTimer struct {
	at   time.Time
	stop func() bool
}

// hostNow is the Host's clock (Host.FeedClock), else the real one.
func (s *Recordings) hostNow() time.Time {
	if s.h != nil {
		if now, _ := s.h.FeedClock(); now != nil {
			return now()
		}
	}
	return time.Now()
}

func (s *Recordings) attendNow() time.Time {
	if f := s.attend.feed.now; f != nil {
		return f()
	}
	return s.hostNow()
}

// meetingKick: recording id changed in the knowledge base (an upload, new live lines): it is looked at soon.
func (s *Recordings) meetingKick(id string) {
	f := &s.attend.feed
	f.mu.Lock()
	at := s.attendNow().Add(feedKickDelay)
	if floor := f.floor[id]; at.Before(floor) {
		at = floor
	}
	if f.looking[id] {
		if f.again == nil {
			f.again = map[string]bool{}
		}
		f.again[id] = true
		f.mu.Unlock()
		return
	}
	f.mu.Unlock()
	s.attendLookAt(id, at)
}

// attendResume picks up the meetings attended when the knowledge base opened (after a restart): their live logs say
// how far each was fed.
func (s *Recordings) attendResume(e *kb.Engine) {
	if e == nil || e.Store.ReadOnly() {
		return
	}
	for _, r := range e.AttendingRecordings() {
		s.meetingKick(r.ID)
	}
	for _, r := range e.RoomMeetings() { // a room to start, settle, end or feed (meeting_room.go)
		switch r.Conv.Room.State {
		case kb.RoomStarting, kb.RoomReady, kb.RoomUnavailable:
			s.meetingKick(r.ID)
		}
	}
}

// attendLookAt has recording id looked at at at, unless a look is due as early already.
func (s *Recordings) attendLookAt(id string, at time.Time) {
	f := &s.attend.feed
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.timers[id]; ok {
		if !t.at.After(at) {
			return
		}
		t.stop()
	}
	if f.timers == nil {
		f.timers = map[string]attendTimer{}
	}
	after := f.after
	if after == nil && s.h != nil {
		_, after = s.h.FeedClock()
	}
	if after == nil {
		after = func(d time.Duration, fn func()) func() bool { return time.AfterFunc(d, fn).Stop }
	}
	f.timers[id] = attendTimer{at: at, stop: after(max(at.Sub(s.attendNow()), 0), func() { s.attendRun(id) })}
}

// attendRun is one look at recording id, and the next one it asks for.
func (s *Recordings) attendRun(id string) {
	f := &s.attend.feed
	f.mu.Lock()
	delete(f.timers, id)
	if f.looking == nil {
		f.looking = map[string]bool{}
	}
	if f.looking[id] {
		f.mu.Unlock()
		return
	}
	f.looking[id] = true
	f.mu.Unlock()
	next := s.attendLook(s.runCtx(), id)
	f.mu.Lock()
	delete(f.looking, id)
	again := f.again[id]
	delete(f.again, id)
	if next.IsZero() && !again {
		delete(f.floor, id) // nothing pending: the next change is looked at at once
	} else {
		if f.floor == nil {
			f.floor = map[string]time.Time{}
		}
		floor := s.attendNow().Add(feedBusyPoll)
		f.floor[id] = floor
		if again {
			next = feedEarliest(next, floor)
		}
	}
	f.mu.Unlock()
	if !next.IsZero() {
		s.attendLookAt(id, next)
	}
}

// attendLook looks at recording id once: feeds what is due when it can, notes the problem when it cannot; when to look
// again (zero: when it next changes).
func (s *Recordings) attendLook(ctx context.Context, id string) time.Time {
	now := s.attendNow()
	if s.hello(ctx).Assistant.Standby { // a mesh member's: the center's knowledge base is the group's
		return now.Add(feedRetry)
	}
	e, err := s.kbGet(ctx)
	switch {
	case s.h.KBOff(err):
		return time.Time{}
	case err != nil:
		return now.Add(feedRetry)
	case e.Store.ReadOnly():
		return time.Time{} // nothing could be noted as fed: the phone's lines are refused there anyway
	}
	l, r, err := e.LiveRead(id)
	switch {
	case errors.Is(err, kb.ErrNotFound), errors.Is(err, kb.ErrRecordingDeleted):
		return time.Time{}
	case err != nil:
		s.logf("meeting %s: %v", short(id), err)
		return now.Add(feedRetry)
	case r.RoomMeeting():
		return s.roomLook(ctx, e, id, l, r, now) // its own room attends it (meeting_room.go)
	case l.EndFed:
		return time.Time{} // later changes are stored but feed nothing
	}
	if !r.Attending() {
		// Off: nothing is fed while it is, and turned on again the session gets the meeting so far.
		s.attendNote(e, id, func(l *kb.Live) { l.FedTo, l.Problem = nil, "" })
		return time.Time{}
	}
	if last := feedLatest(r.Updated, l.EndedAt); !last.IsZero() && now.Sub(last) > feedStale {
		return time.Time{}
	}
	end := r.EndedAt != nil && r.Complete || !l.EndedAt.IsZero() && !now.Before(l.EndedAt.Add(feedEndWait))
	var deadline time.Time // the end batch's, while it is not due
	if !end && !l.EndedAt.IsZero() {
		deadline = l.EndedAt.Add(feedEndWait)
	}
	if !end && !l.FedAt.IsZero() && now.Before(l.FedAt.Add(feedBatchEvery)) {
		return feedEarliest(l.FedAt.Add(feedBatchEvery), deadline)
	}
	ref, ok := s.attendeeRef(ctx, r)
	if !ok { // no main chat
		s.attendNote(e, id, func(l *kb.Live) { l.Problem = attendeeUnavailable })
		return now.Add(feedRetry)
	}
	fresh := l.FedTo == nil || !s.sameMachine(l.FedTo.MachineID, ref.MachineID) || l.FedTo.SessionID != ref.SessionID
	b := feedBatchOf(l, fresh, end)
	if b.empty() {
		if end && l.FedTo == nil { // nothing was ever said to anyone: no end to tell
			s.attendNote(e, id, func(l *kb.Live) { l.EndFed = true })
			return time.Time{}
		}
		if !end {
			// Only empty positions came: they count as given, so the phone does not show it catching up.
			if !fresh && b.upto > l.Fed {
				s.attendNote(e, id, func(l *kb.Live) { l.Fed = max(l.Fed, b.upto) })
			}
			return deadline
		}
	}
	target, verdict := s.attendeeCheck(ctx, ref, now)
	switch verdict {
	case attendeeBusy:
		s.attendNote(e, id, func(l *kb.Live) { l.Problem = "" })
		return feedEarliest(now.Add(feedBusyPoll), deadline)
	case attendeeUnavailable, attendeeNotAllowed:
		s.attendNote(e, id, func(l *kb.Live) { l.Problem = verdict })
		return feedEarliest(now.Add(feedRetry), deadline)
	}
	sent, err := s.feedSend(ctx, target, r, b)
	switch {
	case feedRefused(err): // a dialog or a draft at the computer: nothing pressed or cleared, the batch waits
		s.attendNote(e, id, func(l *kb.Live) { l.Problem = "" })
		return feedEarliest(now.Add(feedBusyPoll), deadline)
	case err != nil:
		s.logf("meeting %s: batch to %s: %v", short(id), short(ref.SessionID), err)
		s.attendNote(e, id, func(l *kb.Live) { l.Problem = attendeeUnavailable })
		return feedEarliest(now.Add(feedRetry), deadline)
	}
	fedTo := target.ref
	s.attendNote(e, id, func(l *kb.Live) {
		if fresh {
			l.Fed = b.upto
		} else {
			l.Fed = max(l.Fed, b.upto)
		}
		l.FedTo, l.FedAt, l.Problem = &fedTo, sent, ""
		for i := range l.Asks {
			if b.asks[l.Asks[i].ID] {
				l.Asks[i].Fed = true
			}
		}
		if end {
			l.EndFed = true
		}
	})
	if end {
		return time.Time{}
	}
	return feedEarliest(sent.Add(feedBatchEvery), deadline)
}

// attendNote changes recording id's live log with fn (live.go LiveUpdate: written only when it changed).
func (s *Recordings) attendNote(e *kb.Engine, id string, fn func(l *kb.Live)) {
	if _, err := e.LiveUpdate(id, func(l *kb.Live, _ kb.Recording) error { fn(l); return nil }); err != nil &&
		!errors.Is(err, kb.ErrNotFound) {
		s.logf("meeting %s: live log: %v", short(id), err)
	}
}

// feedEarliest is the earlier of a and b, a zero one counting as none.
func feedEarliest(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return b
	case b.IsZero() || a.Before(b):
		return a
	}
	return b
}

// feedLatest is the later of a and b.
func feedLatest(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// feedRefused: the node refused to type into the session (a dialog has its keyboard, its input box holds a draft).
func feedRefused(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && (re.Code == screen.DialogOpen || re.Code == screen.InputDraft)
}

// feedAskAnswered is an answered ask's state (kb.LiveAsk.State).
const feedAskAnswered = "answered"

// feedBatch is what goes to the session now: the live lines up to upto that fit (leftOut earlier ones did not),
// the questions answered, and whether it is the meeting so far (a session not fed before) or the end.
type feedBatch struct {
	upto    int
	lines   []kb.LiveSegment
	leftOut int
	answers []kb.LiveAsk
	asks    map[string]bool // their ids, to note as fed
	soFar   bool
	end     bool
	room    bool // to the meeting's own room: no meeting-turn mark, and it writes nothing after it
	// topics: the room has meeting_topics_set, so the batch asks it to update its topics so far (topicsSoFar) and
	// shows them (for a lean room only with lists).
	topics      bool
	topicsSoFar []kb.LiveTopic
	// highlights: the room has meeting_highlights_set, so the batch asks it to update its highlights so far
	// (highlightsSoFar) and shows them (for a lean room only with lists).
	highlights      bool
	highlightsSoFar []kb.LiveHighlight
	// lean: a room started with lean batches that has both tools (#8253, meeting_room_lean.go): one short line instead
	// of the rules, and the lists so far only at the boundaries (lists).
	lean, lists bool
}

func (b feedBatch) empty() bool { return len(b.lines) == 0 && len(b.answers) == 0 }

// feedBatchOf is l's batch: the lines from the last one fed (from the start when fresh, the session not being the
// one fed before), the latest that fit feedBatchRunes, and the questions answered and not given yet (all of them
// when fresh). Empty positions are never fed.
func feedBatchOf(l kb.Live, fresh, end bool) feedBatch {
	b := feedBatch{upto: len(l.Segments), soFar: fresh, end: end, asks: map[string]bool{}}
	from := min(l.Fed, b.upto)
	if fresh {
		from = 0
	}
	runes := 0
	for i := b.upto - 1; i >= from; i-- { // the latest first; once one does not fit, every older one is left out
		sg := l.Segments[i]
		if sg.Text == "" {
			continue
		}
		if n := utf8.RuneCountInString(sg.Text); b.leftOut == 0 && runes+n <= feedBatchRunes {
			runes += n
			b.lines = append(b.lines, sg)
			continue
		}
		b.leftOut++
	}
	for i, j := 0, len(b.lines)-1; i < j; i, j = i+1, j-1 {
		b.lines[i], b.lines[j] = b.lines[j], b.lines[i]
	}
	for _, a := range l.Asks {
		if a.State == feedAskAnswered && a.Answer != "" && (fresh || !a.Fed) {
			b.answers = append(b.answers, a)
			b.asks[a.ID] = true
		}
	}
	return b
}

// feedMeetingName is how the batches and their source name meeting r: the upload's title, else its calendar hint's
// (never the knowledge base's calendar match), else when it started; one line, clipped.
func feedMeetingName(r kb.Recording, loc *time.Location) string {
	name := strings.TrimSpace(r.Title)
	if name == "" && r.Calendar != nil {
		name = strings.TrimSpace(r.Calendar.Title)
	}
	if name == "" {
		name = r.StartedAt.In(loc).Format("2006-01-02 15:04")
	}
	return cutRunes(strings.Join(strings.Fields(name), " "), feedTitleRunes)
}

// feedText is batch b of meeting r as the session reads it (prompts/meeting/feed.md), in the user's language and
// time zone.
func feedText(r kb.Recording, b feedBatch, lang string, loc *time.Location) string {
	return prompts.RenderMeetingFeed(feedData(r, b, lang, loc))
}

// feedData is what prompts/meeting/feed.md renders batch b from; a lean batch without the lists has none of them.
func feedData(r kb.Recording, b feedBatch, lang string, loc *time.Location) prompts.MeetingFeed {
	at := func(sec float64) string { return kb.LiveClock(r, sec, loc) } // what a highlight's said_at names
	d := prompts.MeetingFeed{Lang: lang, Title: feedMeetingName(r, loc), Role: r.AttendanceRole(), SoFar: b.soFar, LeftOut: b.leftOut,
		Ended: b.end, Room: b.room, Topics: b.room && b.topics, Highlights: b.room && b.highlights, Lean: b.room && b.lean, Lists: b.lists}
	if !d.Lean || d.Lists { // the lists only where the text shows them
		d.TopicsSoFar = meetingTopics(b.topicsSoFar)
		for _, h := range b.highlightsSoFar {
			d.HighlightsSoFar = append(d.HighlightsSoFar, prompts.MeetingHighlight{At: at(h.At), Text: oneLine(h.Text)})
		}
	}
	for _, sg := range b.lines {
		d.Lines = append(d.Lines, prompts.MeetingFeedLine{At: at(sg.Start), Text: sg.Text})
	}
	if n := len(b.lines); n > 0 {
		d.From, d.To = at(b.lines[0].Start), at(b.lines[n-1].End)
		if d.To == d.From {
			d.To = ""
		}
	}
	for _, a := range b.answers {
		d.Asks = append(d.Asks, prompts.MeetingFeedAsk{Question: a.Question, Answer: a.Answer})
	}
	return d
}

// meetingTopics are a meeting's topics as the prompts take them: each on one line, when it began as a time from the
// meeting's start.
func meetingTopics(topics []kb.LiveTopic) []prompts.MeetingTopic {
	var out []prompts.MeetingTopic
	for _, t := range topics {
		mt := prompts.MeetingTopic{State: t.State, Title: oneLine(t.Title), Note: oneLine(t.Note)}
		if t.At != nil {
			mt.At = meetingClock(*t.At)
		}
		out = append(out, mt)
	}
	return out
}

// feedSend gives target batch b of meeting r, its turn marked a meeting turn first; when it was sent. Message source
// meeting, never with a client_msg_id: it is not the user's, nor matched to a send of theirs, and does not take back a
// question to the user (UserSpoke is not called).
func (s *Recordings) feedSend(ctx context.Context, t attendeeTarget, r kb.Recording, b feedBatch) (time.Time, error) {
	loc := s.location(ctx)
	text := feedText(r, b, string(s.lang(ctx)), loc)
	source := &proto.MessageSource{Kind: proto.SourceMeeting, Title: feedMeetingName(r, loc)}
	var marked time.Time
	if !b.room { // a room's every turn is a side exchange already (meeting_turns.go): no mark to lift
		var err error
		if marked, err = s.meetingTurnSent(t.ref.MachineID, t.ref.SessionID, r.ID, t.persona); err != nil {
			return time.Time{}, err
		}
	}
	method, params := DeviceSessionSend, map[string]any{"session_id": t.ref.SessionID, "text": text, "message_source": source}
	if t.persona {
		method, params["resume_only"] = DeviceAssistantSend, true // a Persona chat that stopped is resumed, as relays are
	}
	sctx, cancel := context.WithTimeout(ctx, feedSendWait)
	defer cancel()
	if err := s.call(sctx, t.ref.MachineID, method, params, nil); err != nil {
		if !b.room && (feedRefused(err) || !errors.Is(err, context.DeadlineExceeded)) {
			s.meetingTurnUnsent(t.ref.MachineID, t.ref.SessionID, marked) // no turn comes of it (one that timed out may have gone)
		}
		return time.Time{}, err
	}
	return s.attendNow(), nil
}
