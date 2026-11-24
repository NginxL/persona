package recording

import (
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
)

// The session a recorded meeting's batches go to (docs/assistant/meeting-minutes.md "Feeding the attending session"):
// the one the upload names, else the main chat as it is at each batch. Checked at every batch, since trust and
// sessions change mid-meeting: whether it may be given other people's words at all (attendee_not_allowed), whether it
// can take a message now (attendee_unavailable), and whether it is free for one (idle or done, nothing waiting on it).

// Problems the feeder notes in the live log for GET and POST …/live to report.
const (
	attendeeUnavailable = "attendee_unavailable"
	attendeeNotAllowed  = "attendee_not_allowed"
)

// attendeeBusy is attendeeVerdict's word for a session that may attend but not take a batch now (no problem: the
// phone shows it catching up).
const attendeeBusy = "busy"

// attendeeNeverOrigins are the origins of assistant conversations whose replies leave the apps: they never attend
// (the core names the same origins so: its origins.go).
var attendeeNeverOrigins = []string{"lark_agent", "channel", "extension", "inbox"}

// attendeeTarget is the session a batch goes to: ref by its real ids (the main chat's too), persona when it is one of
// the assistant's conversations (assistant.send resumes one that stopped) rather than an Agent (session.send).
type attendeeTarget struct {
	ref     kb.SessionRef
	persona bool
}

// attendeeFacts are what attendeeVerdict needs beyond the session's row.
type attendeeFacts struct {
	ours    bool // its computer is one this manager carries
	work    bool // that computer's trust is work
	online  bool // and its node can be reached now
	found   bool // the session is listed there
	guest   bool // a guest conversation (guest.go)
	unknown bool // whether it is a guest, or where it comes from, cannot be told now
	// settling: its last turn ended moments ago and that turn's push and hooks are not decided yet; a batch now would
	// pass for that turn's end. running: a batch's turn is under way there.
	settling, running bool
}

// attendeeVerdict says what becomes of a batch for sess: "" it goes now, attendeeBusy it waits, or the problem.
func attendeeVerdict(sess proto.Session, f attendeeFacts) string {
	switch {
	case !f.ours || f.work:
		return attendeeNotAllowed
	case !f.online, !f.found:
		return attendeeUnavailable
	}
	if sess.Assistant {
		switch {
		case spaceOf(sess) != "", slices.Contains(attendeeNeverOrigins, sess.Origin), sess.OpenedBy != "", f.guest:
			return attendeeNotAllowed
		case f.unknown:
			return attendeeUnavailable
		}
	}
	if sess.PermissionMode == proto.ModeBypass { // other people's words reach it and nothing would ever ask first
		return attendeeNotAllowed
	}
	if !sess.Assistant {
		switch {
		case sess.IsSubagent, sess.SourceKind != "" && (sess.Capabilities == nil || !sess.Capabilities.Send):
			return attendeeUnavailable // it takes no messages
		case sess.Status == proto.StatusOffline, sess.Status == proto.StatusInterrupted:
			return attendeeUnavailable // closed: an Agent is not resumed for a meeting
		}
	}
	switch {
	case sess.Status == proto.StatusWorking, sess.Status == proto.StatusBlocked, sess.Status == proto.StatusUnknown, sess.Status == "",
		sess.Starting, sess.PendingInteractions > 0, sess.State == proto.StateWorking, sess.State == proto.StateNeedsYou,
		sess.BackgroundWorking, sess.TurnErrorResumes, f.settling, f.running:
		return attendeeBusy
	}
	return ""
}

// attendeeRef is the session recording r is attended by: the one its upload names, else the main chat as it is now;
// false when there is none (no main chat).
func (s *Recordings) attendeeRef(ctx context.Context, r kb.Recording) (kb.SessionRef, bool) {
	if a := r.AttendingSession; a != nil && a.MachineID != "" && a.SessionID != "" {
		return *a, true
	}
	cfg := s.settings(ctx)
	if cfg.Main == "" || cfg.MachineID == "" {
		return kb.SessionRef{}, false
	}
	return kb.SessionRef{MachineID: cfg.MachineID, SessionID: cfg.Main}, true
}

// attendeeCheck checks ref for a batch now: the target, and attendeeVerdict's word.
func (s *Recordings) attendeeCheck(ctx context.Context, ref kb.SessionRef, now time.Time) (attendeeTarget, string) {
	return s.attendeeCheckFor(ctx, ref, now, "")
}

// attendeeCheckFor is attendeeCheck for recording's meeting: its own room is exempt from "Sessions that never
// attend", which refuses every guest conversation; any other meeting's room is refused all the same.
func (s *Recordings) attendeeCheckFor(ctx context.Context, ref kb.SessionRef, now time.Time, recording string) (attendeeTarget, string) {
	return s.attendeeCheckRead(ctx, ref, now, recording, nil)
}

// attendeeCheckRead is attendeeCheckFor with the session's row as a look read it already (a lean room's, #8253), nil:
// read now.
func (s *Recordings) attendeeCheckRead(ctx context.Context, ref kb.SessionRef, now time.Time, recording string, rd *roomRead) (attendeeTarget, string) {
	ref.MachineID = s.canonical(ref.MachineID) // an id another one absorbed: that one
	t := attendeeTarget{ref: ref}
	var f attendeeFacts
	machine := ref.MachineID
	f.ours, f.online = s.attendeeMachine(ctx, machine)
	f.work = s.isWorkMachine(machine)
	if !f.ours || f.work || !f.online {
		return t, attendeeVerdict(proto.Session{}, f)
	}
	var sess proto.Session
	var found bool
	var err error
	if rd != nil {
		sess, found, err = rd.row, rd.found, rd.err
	} else {
		sess, found, err = s.attendeeRow(ctx, machine, ref.SessionID)
	}
	if err != nil {
		s.logf("meeting: sessions of %s: %v", machine, err)
		return t, attendeeUnavailable
	}
	f.found = found
	t.persona = sess.Assistant
	if found && sess.Assistant {
		_, guest, err := s.guestOf(ctx, machine, ref.SessionID)
		f.guest, f.unknown = guest, err != nil
		if rec, room, rerr := s.roomOf(ctx, machine, ref.SessionID); guest && rerr == nil && room && recording != "" && rec == recording {
			f.guest = false // the meeting's own room: it has no tool that acts
			// Its conversation being compacted (#8253): batches and photos wait for that as for a turn.
			f.running = roomCompacting(sess)
		}
		if origins, ok := s.conversationOrigins(); !ok {
			f.unknown = true
		} else if o, ok := origins[ref.SessionID]; ok {
			sess.Origin, sess.OpenedBy = o.origin, o.ext // the book is where the lists' fields come from
		}
		if sess.Origin == OriginMeeting && !f.guest && recording == "" {
			f.guest = true // a room, whatever the book says: never the attendee of another meeting
		}
	}
	if found {
		s.meetingTurnStopped(machine, ref.SessionID, sess, false) // stopped since its batch: whatever its end looked like
		running, err := s.meetingTurnRunning(ctx, machine, ref.SessionID)
		f.running = f.running || running || err != nil
		f.settling = s.turnSettling(machine, ref.SessionID, now)
	}
	return t, attendeeVerdict(sess, f)
}
