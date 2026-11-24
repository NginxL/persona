package recording

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Asking the room (docs/assistant/meeting-minutes.md "Asking the room"): in a room meeting POST …/ask is answered by
// the room conversation. A typed or spoken question goes to the room as the owner's own message, the question's text
// only; a heard one as a message with source meeting, the lines before the tap in a context block. Each attempt's
// message has client_msg_id ask-<id>-<k> and is kept with the ask (kb.AskAttempt), so a restart looks for it and its
// turn in the room before it decides anything, and asking a failed ask again reaches the room again. The answer is the
// room's reply to that message, Markdown removed and cut at a sentence end. Which way an ask goes (via) is fixed when
// its first attempt goes out: the room while it is ready; meeting.answer while it is unavailable or once it ended;
// while it is starting the ask waits for it.

// The asks' extra states (the contract's).
const (
	askSpoken         = "spoken"
	askNoAnswer       = "no_answer"
	askRoomUnavailble = "room_unavailable"
)

// How often an ask in the room looks at it, and how long it waits for a turn running there before it goes; tests
// shorten them.
// askMsgID is the client_msg_id of attempt k of ask id.
func askMsgID(id string, k int) string { return fmt.Sprintf("ask-%s-%d", id, k) }

// answerInRoom answers ask a of recording id, a room meeting: it waits for a starting room, fixes its way (via) at
// its first attempt and answers in the room, or from meeting material. resumed: a restart left it pending (its
// attempt, if it has one, may be in the room already).
func (s *Recordings) answerInRoom(ctx context.Context, e *kb.Engine, id string, a kb.LiveAsk) kb.LiveAsk {
	if a.Via == "" {
		via, ok := s.roomVia(ctx, e, id)
		if !ok {
			return failAsk(a, askInterrupted) // the service is stopping: asked again after the restart
		}
		if via == kb.AskViaMaterial {
			a.Via = via
			s.askNote(e, id, a.ID, func(x *kb.LiveAsk) { x.Via = via })
			return s.answerMaterial(ctx, e, id, a)
		}
		a.Via = via
	}
	if a.Via == kb.AskViaMaterial {
		return s.answerMaterial(ctx, e, id, a)
	}
	return s.answerRoomTurn(ctx, e, id, a)
}

// roomVia is the way an ask of recording id goes now: the room once it is ready, meeting.answer once it is
// unavailable or over; while it is starting (or about to) it waits. False when ctx ended first.
func (s *Recordings) roomVia(ctx context.Context, e *kb.Engine, id string) (string, bool) {
	for {
		changed := s.room.wait(id)
		m, r, err := e.MeetingRead(id)
		if err != nil || m.Room == nil {
			return kb.AskViaMaterial, true
		}
		switch m.Room.State {
		case kb.RoomReady:
			return kb.AskViaRoom, true
		case kb.RoomUnavailable, kb.RoomEnded, kb.RoomDeleted:
			return kb.AskViaMaterial, true
		case "", kb.RoomNone:
			if !r.Attending() {
				return kb.AskViaMaterial, true
			}
			s.meetingKick(id) // its room is about to start
		}
		t := time.NewTimer(s.tm().askRoomPoll * 5)
		select {
		case <-ctx.Done():
			t.Stop()
			return "", false
		case <-changed:
		case <-t.C:
		}
		t.Stop()
	}
}

// answerMaterial is a's answer from meeting material (meeting.answer, as in #2981), within askTimeout.
func (s *Recordings) answerMaterial(ctx context.Context, e *kb.Engine, id string, a kb.LiveAsk) kb.LiveAsk {
	ctx, cancel := context.WithTimeout(ctx, s.tm().askTimeout)
	defer cancel()
	out := s.answerMeetingAsk(ctx, e, id, a)
	out.Via = kb.AskViaMaterial
	return out
}

// askNote changes ask askID of recording id's live log with fn.
func (s *Recordings) askNote(e *kb.Engine, id, askID string, fn func(a *kb.LiveAsk)) {
	if _, err := e.LiveUpdate(id, func(l *kb.Live, _ kb.Recording) error {
		i := slices.IndexFunc(l.Asks, func(x kb.LiveAsk) bool { return x.ID == askID })
		if i < 0 || l.Asks[i].State != askPending {
			return errAskGone
		}
		fn(&l.Asks[i])
		return nil
	}); err != nil && !errors.Is(err, errAskGone) {
		s.logf("recording %s: question %s: %v", short(id), askID, err)
	}
}

// answerRoomTurn answers a in recording id's room: one ask at a time per room, after a batch turn running there;
// its latest attempt is sent (again, under the same id, when the room does not have it) and its turn read.
func (s *Recordings) answerRoomTurn(ctx context.Context, e *kb.Engine, id string, a kb.LiveAsk) kb.LiveAsk {
	var release func()
	for {
		rel, busy, ok := s.room.take(id)
		if ok {
			release = rel
			break
		}
		select {
		case <-ctx.Done():
			return failAsk(a, askInterrupted)
		case <-busy:
		}
	}
	defer release()
	m, r, err := e.MeetingRead(id)
	if err != nil || m.Room == nil || m.Room.SessionID == "" {
		return failAsk(a, askRoomUnavailble)
	}
	machine, session := m.Room.MachineID, m.Room.SessionID
	if m.Room.State != kb.RoomReady && len(a.Attempts) == 0 {
		return failAsk(a, askRoomUnavailble)
	}
	var attempt kb.AskAttempt
	if n := len(a.Attempts); n > 0 {
		attempt = a.Attempts[n-1]
	}
	transcript := busTranscript{s: s, machine: machine, session: session}
	if attempt.K == 0 || attempt.SentAt.IsZero() {
		if attempt.K == 0 {
			attempt = kb.AskAttempt{K: 1, MsgID: askMsgID(a.ID, 1)}
		}
		if !s.roomWaitFree(ctx, e, id, machine, session) {
			return failAsk(a, askInterrupted)
		}
		attempt.Base = s.lastSeq(ctx, machine, session)
		text, src, failed := s.roomAskMessage(ctx, e, id, r, &a)
		if failed != "" {
			return failAsk(a, failed)
		}
		attempt.SentAt = time.Now().UTC()
		s.askNote(e, id, a.ID, func(x *kb.LiveAsk) {
			x.Via, x.Heard, x.Question = kb.AskViaRoom, a.Heard, a.Question
			x.Attempts = append(slices.DeleteFunc(x.Attempts, func(y kb.AskAttempt) bool { return y.K == attempt.K }), attempt)
		})
		if err := s.roomSendAsk(ctx, machine, session, attempt.MsgID, text, src); err != nil {
			s.logf("recording %s: question %s to its room: %v", short(id), a.ID, err)
			if errors.Is(ctx.Err(), context.Canceled) {
				return failAsk(a, askInterrupted)
			}
			return failAsk(a, askModelError)
		}
	}
	deadline := attempt.SentAt.Add(s.tm().askTimeout)
	run := &channel.Run{MsgIDs: []string{attempt.MsgID}, Base: attempt.Base}
	resent := false
	for {
		row, found, rowErr := s.attendeeRow(ctx, machine, session)
		reading, err := channel.ReadTurn(ctx, transcript, run)
		working := rowErr != nil || found && roomWorking(row)
		switch {
		case err != nil:
		case reading.Found && !working && strings.TrimSpace(reading.Answer) != "" &&
			(!found || !row.UpdatedAt.Before(attempt.SentAt) || time.Since(attempt.SentAt) > 2*s.tm().askRoomPoll):
			// Its row says how the turn ended once it is from after the message (a moment later at most): Claude Code's
			// own words for a failed request ("Not logged in · Please run /login") are no answer.
			if failed := roomTurnError(row, found, attempt.SentAt, reading.Answer); failed != "" {
				s.logf("recording %s: question %s: its room's turn failed: %s", short(id), a.ID, failed)
				if te, _ := latestTurnError(row); te != "" && !s.h.TurnErrorTransient(row) {
					s.roomCannotAnswer(e, id)
				}
				return failAsk(a, askModelError)
			}
			return roomAnswered(a, reading.Answer, s.room.takeSources(id))
		case reading.Found && !working && reading.Stopped != "":
			return failAsk(a, askNoAnswer)
		case reading.Found && !working && found && !row.UpdatedAt.Before(attempt.SentAt) && time.Since(attempt.SentAt) > 2*s.tm().askRoomPoll:
			return failAsk(a, askNoAnswer) // its turn ended with no text
		case !reading.Found && !working && !resent && time.Since(attempt.SentAt) > 10*s.tm().askRoomPoll:
			// Its message is not in the room (a restart cut its send off): sent again under the same id.
			resent = true
			text, src, failed := s.roomAskMessage(ctx, e, id, r, &a)
			if failed != "" {
				return failAsk(a, failed)
			}
			if err := s.roomSendAsk(ctx, machine, session, attempt.MsgID, text, src); err != nil {
				s.logf("recording %s: question %s to its room again: %v", short(id), a.ID, err)
			}
		}
		if !time.Now().Before(deadline) {
			return failAsk(a, askModelTimeout)
		}
		t := time.NewTimer(s.tm().askRoomPoll)
		select {
		case <-ctx.Done():
			t.Stop()
			return failAsk(a, askInterrupted)
		case <-t.C:
		}
	}
}

// roomTurnError is the error the room's turn that answered with answer ended in, as its row says it (found: there is
// one), "" for none: the row's turn error when the row is from after sentAt or the error is the answer itself.
func roomTurnError(row proto.Session, found bool, sentAt time.Time, answer string) string {
	if !found {
		return ""
	}
	te, _ := latestTurnError(row)
	if te == "" || row.UpdatedAt.Before(sentAt) && te != strings.TrimSpace(answer) {
		return ""
	}
	return te
}

// roomCannotAnswer makes recording id's ready room unavailable after a turn that failed on what the owner must fix
// (the sign-in, a key, the model, a usage limit): the asks after it are answered from meeting material, and the room
// is started again after roomRetry.
func (s *Recordings) roomCannotAnswer(e *kb.Engine, id string) {
	now := time.Now().UTC()
	s.roomSet(e, id, func(rm *kb.MeetingRoom) {
		if rm.State == kb.RoomReady {
			rm.State, rm.Problem, rm.RetryAt = kb.RoomUnavailable, kb.RoomStartFailed, now.Add(roomRetry)
		}
	})
	s.roomAsksFallBack(id)
}

// roomWaitFree waits while recording id's room on machine runs a turn (a batch), at most askRoomWait; a room started
// with lean batches also while its conversation is being compacted (#8253): its row shows it running, or the manager
// asked for one that has not ended (roomCompactPending), as long as a compaction may run (roomCompactLost, the node's
// own limit). An ask goes after it, never into a compaction. False when ctx ended.
func (s *Recordings) roomWaitFree(ctx context.Context, e *kb.Engine, id, machine, session string) bool {
	began := time.Now()
	lean := false
	for first := true; ; first = false {
		row, found, err := s.attendeeRow(ctx, machine, session)
		var ln kb.MeetingRoomLean
		if first || lean { // an older room's record is not read again
			if m, _, merr := e.MeetingRead(id); merr == nil && m.Room != nil && m.Room.Batching >= kb.RoomBatchingLean {
				lean, ln = true, m.Room.Lean
			}
		}
		switch {
		case lean && (found && roomCompacting(row) || roomCompactPending(ln, row, found && err == nil, s.attendNow())):
			if time.Since(began) >= roomCompactLost {
				return true
			}
		case err == nil && (!found || !roomWorking(row)):
			return true
		case time.Since(began) >= s.tm().askRoomWait:
			return true
		}
		t := time.NewTimer(s.tm().askRoomPoll)
		select {
		case <-ctx.Done():
			t.Stop()
			return false
		case <-t.C:
		}
	}
}

// roomAskMessage is the message ask a sends recording r's room: a typed or spoken question as it is (the owner's
// own, no message source), a heard one the lines before the tap (message source meeting); a.Heard is set for it. A
// failure (nothing_heard, model_error) when there is nothing to send.
func (s *Recordings) roomAskMessage(ctx context.Context, e *kb.Engine, id string, r kb.Recording, a *kb.LiveAsk) (string, *proto.MessageSource, string) {
	if a.QuestionSource != askHeard {
		return a.Question, nil, ""
	}
	l, err := s.awaitHeard(ctx, e, id, *a)
	if err != nil {
		return "", nil, askModelError
	}
	heard := heardLines(l.Segments, *a.At)
	if len(heard) == 0 {
		return "", nil, askNothingHeard
	}
	a.Heard = heard
	d := prompts.MeetingRoomHeard{Owner: s.owner(ctx), At: meetingClock(*a.At)}
	for _, sg := range heard {
		d.Lines = append(d.Lines, prompts.MeetingLine{At: meetingClock(sg.Start), Text: oneLine(sg.Text)})
	}
	return prompts.RenderMeetingRoomHeard(d), &proto.MessageSource{Kind: proto.SourceMeeting, Title: feedMeetingName(r, s.location(ctx))}, ""
}

// roomSendAsk sends an ask's message into the room under the hub's once-only guard: a message id already delivered is
// not sent again.
func (s *Recordings) roomSendAsk(ctx context.Context, machine, session, msgID, text string, src *proto.MessageSource) error {
	p := map[string]any{"session_id": session, "text": text, "client_msg_id": msgID, "resume_only": true}
	if src != nil {
		p["message_source"] = src
		p["text"] = prompts.MessageSourceText(text, src)
	}
	sctx, cancel := context.WithTimeout(ctx, feedSendWait)
	defer cancel()
	return s.sendOnce(sctx, machine, p)
}

// roomAnswered is a, answered with the room's reply: Markdown removed, cut at a sentence end to maxAnswerRunes.
func roomAnswered(a kb.LiveAsk, reply string, sources []kb.AskSource) kb.LiveAsk {
	answer := plainAnswer(stripMarkdown(reply))
	if answer == "" {
		return failAsk(a, askNoAnswer)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if sources == nil {
		sources = []kb.AskSource{}
	}
	a.State, a.Answer, a.Error, a.Sources, a.AnsweredAt, a.Via = askAnswered, answer, "", sources, &now, kb.AskViaRoom
	return a
}

var (
	mdLink    = lazyre.New(`\[([^\]]*)\]\([^)]*\)`)
	mdHeading = lazyre.New(`(?m)^\s{0,3}(#{1,6}\s+|[-*+]\s+|\d+[.)]\s+|>\s?)`)
)

// stripMarkdown is a reply without Markdown: links as their text, no headings, list marks or quotes.
func stripMarkdown(s string) string {
	s = mdLink.ReplaceAllString(s, "$1")
	s = mdHeading.ReplaceAllString(s, "")
	return s
}
