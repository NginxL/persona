package recording

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Long meetings (docs/assistant/meeting-minutes.md "Long meetings: lean batches and compaction (#8253)"): a room this
// manager starts (kb.MeetingRoom.Batching, set when it first starts) gets a batch at most every two minutes, without the
// topics and highlights rules (they are in its CLAUDE.md) and with the lists so far only at the boundaries: a batch
// with the meeting so far (its first, or after its role was off), the first after its conversation was compacted (by the
// manager, the owner, or Claude Code on its own), and once the last batch that showed them is half an hour old. Once
// the batches given since it started or was last compacted cover 45 minutes of the meeting and 10,000 characters of
// lines, the manager compacts it in place (the node's assistant.compact, Claude Code's /compact in the same session) at
// an idle moment, inside the room's look and holding the room's one-ask slot while it starts it; until the session's
// compaction says it ended, nothing goes into the room. What it keeps for that is the room's record in meeting.json
// (kb.MeetingRoomLean), so a restart neither compacts twice nor loses a running one: the session's compaction says how
// it is going. An older room (Batching 0) keeps a batch a minute with the rules and the lists in each, and is never
// compacted.

const (
	roomLeanEvery       = 2 * time.Minute  // at most one batch this often to a lean room (the end batch does not wait)
	roomListsEvery      = 30 * time.Minute // the lists go again once the last batch that showed them is this old
	roomCompactSpan     = 45 * time.Minute // of the meeting the batches since the last compaction cover, and …
	roomCompactRunes    = 10000            // … characters of their lines: then the room is compacted
	roomCompactRetry    = 5 * time.Minute  // a compaction refused or failed is tried again at a look this much later
	roomCompactAttempts = 3                // for the same span, then the room waits for its batches to cover another
	roomCompactLost     = 10 * time.Minute // one the node does not report ended after this long has (the node's limit)
	roomCompactWait     = time.Minute      // for the node's answer to assistant.compact
)

// roomSpan is what the batches given to a lean room since feed position at cover: the earliest start and the latest
// end among the live log's lines from there to its feed position (l.Fed), and their characters. Read off the live log,
// it is what the batches gave but for two things, both bounded: lines a batch left out (over feedBatchRunes) count, and
// lines a batch with the meeting so far gives again count once, and only after at.
func roomSpan(l kb.Live, at int) (from, to float64, runes int) {
	end := min(l.Fed, len(l.Segments))
	seen := false
	for _, sg := range l.Segments[min(max(at, 0), end):end] {
		if sg.Text == "" {
			continue // never fed
		}
		if !seen {
			from, to, seen = sg.Start, sg.End, true
		}
		from, to, runes = min(from, sg.Start), max(to, sg.End), runes+utf8.RuneCountInString(sg.Text)
	}
	return from, to, runes
}

// roomCompactDue: the batches given since the room started or was last compacted cover roomCompactSpan of the meeting
// (the earliest line's start to the latest line's end among them) and roomCompactRunes characters of lines.
func roomCompactDue(l kb.Live, ln kb.MeetingRoomLean) bool {
	from, to, runes := roomSpan(l, ln.SpanAt)
	return runes >= roomCompactRunes && to-from >= roomCompactSpan.Seconds()
}

// roomListsDue: a lean room's next batch shows the topics and highlights so far: none showed them yet, its
// conversation was compacted since, or the last that did is roomListsEvery old.
func roomListsDue(ln kb.MeetingRoomLean, now time.Time) bool {
	return ln.ListsDue || ln.ListsAt.IsZero() || now.Sub(ln.ListsAt) >= roomListsEvery
}

// roomBatchNote notes in recording id's room record that batch b, showing the lists, went to the lean room at sent;
// ln is the record before it. The bookkeeping after it. (What the batch's lines add to the span is the live log's.)
func (s *Recordings) roomBatchNote(e *kb.Engine, id string, b feedBatch, sent time.Time, ln kb.MeetingRoomLean) kb.MeetingRoomLean {
	if !b.lean || !b.lists {
		return ln
	}
	if rm, ok := s.roomSet(e, id, func(rm *kb.MeetingRoom) { rm.Lean.ListsAt, rm.Lean.ListsDue = sent, false }); ok {
		return rm.Lean
	}
	return ln
}

// roomRead is a lean room's row as one look read it (attendeeRow), which the look's checks share.
type roomRead struct {
	row   proto.Session
	found bool
	err   error
}

// roomRowRead reads room's row, on its machine as attendeeCheckFor names it.
func (s *Recordings) roomRowRead(ctx context.Context, room *kb.MeetingRoom) roomRead {
	machine := room.MachineID
	machine = s.canonical(machine)
	row, found, err := s.attendeeRow(ctx, machine, room.SessionID)
	return roomRead{row, found, err}
}

// roomCompactPending: a compaction the manager asked of the node (ln's mark) has not ended as the room's row (found)
// says it at now: the row shows no compaction of the session's that began since and ended, and it was asked less than
// roomCompactLost ago. Asks, batches and photos wait for it, as they do for a compaction the row shows running
// (roomWorking).
func roomCompactPending(ln kb.MeetingRoomLean, row proto.Session, found bool, now time.Time) bool {
	if ln.CompactStartedAt.IsZero() || now.Sub(ln.CompactStartedAt) >= roomCompactLost {
		return false
	}
	c := row.Compaction
	return !found || c == nil || c.StartedAt.Equal(ln.CompactSeen) || c.State == proto.CompactionRunning
}

// roomCompactedAt notes in ln that the room's conversation was compacted with the batches up to feed position fed in
// it: the next batch shows the lists, and the span (and its attempts) starts again from there.
func roomCompactedAt(ln *kb.MeetingRoomLean, fed int) {
	ln.ListsDue, ln.SpanAt, ln.Attempts, ln.RetryAt = true, fed, 0, time.Time{}
}

// roomCompaction looks after a lean room's compactions at one of its looks, its row as rd: it settles one asked of the
// node (still running, done, failed, or not heard of for roomCompactLost), notes one the owner asked for and Claude Code
// compacting the room on its own, and with start (the role on, the meeting's end not due) starts one that is due at an
// idle moment (photos: a photo or withdrawal is still to go in). held: nothing goes into the room now, one runs or may;
// at: when to look again for it (zero: nothing to do before the next batch); the room's bookkeeping after it.
func (s *Recordings) roomCompaction(ctx context.Context, e *kb.Engine, id string, l kb.Live, r kb.Recording, now time.Time, start, photos bool, rd roomRead) (at time.Time, held bool, ln kb.MeetingRoomLean) {
	room := r.Conv.Room
	ln = room.Lean
	set := func(fn func(ln *kb.MeetingRoomLean)) bool {
		rm, ok := s.roomSet(e, id, func(rm *kb.MeetingRoom) { fn(&rm.Lean) })
		if ok {
			ln = rm.Lean
		}
		return ok
	}
	row, found := rd.row, rd.found
	if rd.err != nil {
		if !ln.CompactStartedAt.IsZero() {
			return now.Add(feedBusyPoll), true, ln // one may run: nothing goes before its row says how it is
		}
		return time.Time{}, false, ln
	}
	var c *proto.Compaction
	if found {
		c = row.Compaction
	}
	ended := c != nil && c.State != proto.CompactionRunning && !c.StartedAt.Equal(ln.CompactSeen) // since the last seen
	if !ln.CompactStartedAt.IsZero() {
		switch {
		case c != nil && c.State == proto.CompactionRunning && !c.StartedAt.Equal(ln.CompactSeen):
			return now.Add(feedBusyPoll), true, ln
		case ended && c.State == proto.CompactionDone:
			// The span starts again where it was asked for: what was given since (nothing, unless its row showed it
			// late) was not in what it compacted.
			set(func(ln *kb.MeetingRoomLean) {
				roomCompactedAt(ln, ln.CompactFed)
				ln.CompactStartedAt, ln.CompactFed, ln.CompactSeen = time.Time{}, 0, c.StartedAt
				ln.CompactedAt, ln.Compactions = now, ln.Compactions+1
				ln.Tokens, ln.TokensStale = 0, row.ContextTokens
			})
			ended = false
		case ended:
			s.logf("meeting %s: its room's compaction failed: %s", short(id), c.Error)
			set(func(ln *kb.MeetingRoomLean) { roomCompactFailed(ln, now, l.Fed); ln.CompactSeen = c.StartedAt })
			ended = false
		case now.Sub(ln.CompactStartedAt) < roomCompactLost:
			// Asked of the node, and not on its row (yet): it may run, so nothing goes into the room meanwhile, and no
			// second one starts.
			return now.Add(feedBusyPoll), true, ln
		default:
			// Never reported, or no longer (its node restarted): it has ended. It may have compacted the room, so the
			// lists go again.
			set(func(ln *kb.MeetingRoomLean) { roomCompactFailed(ln, now, l.Fed); ln.ListsDue = true })
		}
	}
	switch {
	case ended && c.State == proto.CompactionDone:
		// One the owner asked for (or one of the manager's it counted as ended before its row showed it): the room's
		// conversation was compacted with what was given to it.
		set(func(ln *kb.MeetingRoomLean) {
			roomCompactedAt(ln, l.Fed)
			ln.CompactSeen, ln.Tokens, ln.TokensStale = c.StartedAt, 0, row.ContextTokens
		})
	case ended:
		set(func(ln *kb.MeetingRoomLean) { ln.CompactSeen = c.StartedAt })
	}
	// Claude Code compacting the room on its own: the context its row reports while no turn runs there dropped below
	// half of the most it reported since the room was last compacted. The span counts from here.
	if t := row.ContextTokens; found && t > 0 && t != ln.TokensStale && !roomWorking(row) {
		switch {
		case ln.Tokens > 0 && t < ln.Tokens/2:
			set(func(ln *kb.MeetingRoomLean) { roomCompactedAt(ln, l.Fed); ln.Tokens, ln.TokensStale = t, 0 })
		case t > ln.Tokens:
			set(func(ln *kb.MeetingRoomLean) { ln.Tokens, ln.TokensStale = t, 0 })
		}
	}
	if !start || ln.Never || !roomCompactDue(l, ln) {
		return time.Time{}, false, ln
	}
	if now.Before(ln.RetryAt) {
		return ln.RetryAt, false, ln
	}
	// Only at an idle moment: no turn running and its process running (a node is never made to start the room's process
	// only to compact it), no photo or withdrawal to send, no compaction running, and (the caller's gate, then the slot)
	// no ask waiting for the room or running in it. What does not end by itself (a photo given up on for now, the
	// process not running, the computer away) is not polled for: the look after the next batch's turn tries again
	// (roomFeed), so a quiet room is not looked at every few seconds for hours.
	if photos {
		return time.Time{}, false, ln // the photos have their own looks (roomPhotos)
	}
	if !found || row.Status == proto.StatusOffline {
		return time.Time{}, false, ln // its process not running (reaped in a quiet spell): the next batch starts it
	}
	if roomWorking(row) {
		return now.Add(feedBusyPoll), false, ln // the turn ends
	}
	ref := kb.SessionRef{MachineID: room.MachineID, SessionID: room.SessionID}
	switch _, verdict := s.attendeeCheckRead(ctx, ref, now, id, &rd); verdict {
	case "":
	case attendeeBusy:
		return now.Add(feedBusyPoll), false, ln
	default:
		return time.Time{}, false, ln // its computer away: the batch waiting for it brings the look once it is back
	}
	release, _, ok := s.room.take(id)
	if !ok {
		return now.Add(feedBusyPoll), false, ln // an ask or a photo is on its way into the room
	}
	defer release()
	// Noted before the node is asked: after a restart the session's compaction says whether it runs. One that could not
	// be noted is not asked for.
	if !set(func(ln *kb.MeetingRoomLean) { ln.CompactStartedAt, ln.CompactFed = now, l.Fed }) {
		return now.Add(feedRetry), false, ln
	}
	cctx, cancel := context.WithTimeout(ctx, roomCompactWait)
	// only_running: a node with #8253 refuses not_running when the room's process exited since its row said it runs,
	// rather than start it only to compact it; an older node ignores it (the row's check above is all there is then).
	err := s.call(cctx, room.MachineID, DeviceAssistantCompact, map[string]any{"session_id": room.SessionID, "only_running": true}, nil)
	cancel()
	switch {
	case err == nil:
		return now.Add(feedBusyPoll), true, ln
	case roomCompactInDoubt(err):
		// The node may have taken it: it stays noted as asked for, and its row says how it goes (or, not showing it for
		// roomCompactLost, that it ended).
		s.logf("meeting %s: compact its room, no answer: %v", short(id), err)
		return now.Add(feedBusyPoll), true, ln
	case roomCompactNotRunning(err):
		// Its process exited since its row was read: as when the row says so, the look after the next batch tries again.
		set(func(ln *kb.MeetingRoomLean) { ln.CompactStartedAt, ln.CompactFed = time.Time{}, 0 })
		return time.Time{}, false, ln
	case unknownMethod(err), roomCompactRefusedForGood(err):
		// A node too old for the method, or one that would run the room's process without its restrictions: none.
		s.logf("meeting %s: its room is not compacted: %v", short(id), err)
		set(func(ln *kb.MeetingRoomLean) {
			ln.CompactStartedAt, ln.CompactFed, ln.Never = time.Time{}, 0, true
		})
		return time.Time{}, false, ln
	}
	s.logf("meeting %s: compact its room: %v", short(id), err) // busy, compacting, its computer away: tried again later
	set(func(ln *kb.MeetingRoomLean) { roomCompactFailed(ln, now, l.Fed) })
	return ln.RetryAt, false, ln
}

// roomCompactFailed notes a compaction that was refused, failed or has not been heard of: tried again roomCompactRetry
// later, at most roomCompactAttempts times for the same span; then the span starts again from feed position fed.
func roomCompactFailed(ln *kb.MeetingRoomLean, now time.Time, fed int) {
	ln.CompactStartedAt, ln.CompactFed = time.Time{}, 0
	ln.Attempts++
	ln.RetryAt = now.Add(roomCompactRetry)
	if ln.Attempts >= roomCompactAttempts {
		ln.Attempts, ln.SpanAt = 0, fed
	}
}

// roomCompactNotRunning: the node refused because the room's process is not running (only_running).
func roomCompactNotRunning(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && re.Code == "not_running"
}

// roomCompactInDoubt: assistant.compact may have reached the node though no answer said so: the time ran out, the
// manager stopped (its run's context cancelled), or a link on the way dropped with the call out (rpc.ErrDropped; the
// hub passes that on as a node link that closed, in rpc.ErrClosed's words). A call that provably never went out
// (rpc.ErrNotWritten, the node offline) is not.
func roomCompactInDoubt(err error) bool {
	if errors.Is(err, rpc.ErrNotWritten) {
		return false
	}
	var re *rpc.Error
	return startWaitEnded(err) || errors.Is(err, rpc.ErrClosed) || errors.As(err, &re) && re.Message == rpc.ErrClosed.Error()
}

// roomCompactRefusedForGood: the node will not compact the room, ever: it would have to start the room's process on a
// launcher that runs another agent by now (without the room's restrictions), or the room's record there lost its
// tools (node/assistant_guest.go errGuestAgent, errRoomRecord).
func roomCompactRefusedForGood(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "guest conversation runs on Claude Code only") || strings.Contains(msg, "lost its record on this computer")
}
