package assistant

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Just me (docs/assistant/meeting-minutes.md "Just me"): the owner's private question during a meeting goes to the
// main chat (one is started when there is none) as their own message, with the meeting named in a collapsed context
// block. Nothing of it is stored with the recording. The turn it starts carries a mark (justMeBook), kept with the
// main chat until that turn ends and for at most justMeMax: while it stands nothing of the turn reaches a screen
// other than the meeting page (no completion push, nothing mirrored to the Lark bot or the channels, notify_user and
// phone_notify push nothing, a card it raises is pushed without its content). A turn that stops with background work
// still running has not ended: the turn that work's report wakes is the same work, and keeps the mark (its end is the
// held work's one completion push, fleet_push.go).

const justMeKey = "assistant.just_me_turns"

const justMeMsgPrefix = "just-me-"

const (
	// justMeMax: a mark lasts at most this long after its message went.
	justMeMax = 3 * time.Hour
	// justMeAfter: an ended mark stays this long for what is decided after a turn ends (its push, its mirror).
	justMeAfter = 2 * time.Minute
)

// justMeTurn is one main chat's mark.
type justMeTurn struct {
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	SentAt    time.Time `json:"sent_at"`
	Started   bool      `json:"started,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	// Held: the turn stopped with background work running. The mark stays (not only justMeAfter) for the turn the
	// work's report wakes.
	Held bool `json:"held,omitempty"`
}

type justMeBook struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]*justMeTurn // by meetingTurnKey
	now    func() time.Time       // nil: time.Now
}

func (b *justMeBook) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// justMeLocked reads the book once and drops what ran out; b.mu is held. False while it cannot be read.
func (s *Service) justMeLocked() bool {
	b := &s.justMe
	if !b.loaded {
		m := map[string]*justMeTurn{}
		if st := s.store(); st != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := st.Setting(ctx, justMeKey, &m)
			cancel()
			if err != nil {
				return false
			}
		}
		if m == nil {
			m = map[string]*justMeTurn{}
		}
		b.m, b.loaded = m, true
	}
	now := b.clock()
	for k, t := range b.m {
		if now.Sub(t.SentAt) > justMeMax+justMeAfter || !t.Held && !t.EndedAt.IsZero() && now.Sub(t.EndedAt) > justMeAfter {
			delete(b.m, k)
		}
	}
	return true
}

func (s *Service) saveJustMeLocked() {
	st := s.store()
	if st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := st.PutSetting(ctx, justMeKey, s.justMe.m); err != nil {
		s.logf("save just me turns: %v", err)
	}
}

// justMeSent marks the turn the Just me message about to go to machine's main chat session starts.
func (s *Service) justMeSent(machine, session string) {
	b := &s.justMe
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.justMeLocked() {
		return
	}
	b.m[s.meetingTurnKey(machine, session)] = &justMeTurn{MachineID: machine, SessionID: session, SentAt: b.clock()}
	s.saveJustMeLocked()
}

// justMeStarted: machine's session is seen working, a new turn when newTurn, one a background task's report started
// when woken. A turn after the Just me one drops its mark, unless it goes on with the Just me turn's work: one its
// background work's report woke, or any after a stop that left that work running (its woken flag can be read later
// than the start).
func (s *Service) justMeStarted(machine, session string, newTurn, woken bool) {
	b := &s.justMe
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.justMeLocked() {
		return
	}
	k := s.meetingTurnKey(machine, session)
	t := b.m[k]
	switch {
	case t == nil:
		return
	case !t.Started:
		t.Started = true
	case !t.EndedAt.IsZero() && newTurn && (woken || t.Held):
		t.EndedAt, t.Held = time.Time{}, false // the same work: its end decides again
	case !t.EndedAt.IsZero() && newTurn:
		delete(b.m, k)
	default:
		return
	}
	s.saveJustMeLocked()
}

// justMeEnded: machine's session stopped: a Just me turn seen running ended, with its background work still running
// when held.
func (s *Service) justMeEnded(machine, session string, held bool) {
	b := &s.justMe
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.justMeLocked() {
		return
	}
	if t := b.m[s.meetingTurnKey(machine, session)]; t != nil && t.Started && t.EndedAt.IsZero() {
		t.EndedAt, t.Held = b.clock(), held
		s.saveJustMeLocked()
	}
}

// justMeNow: machine's session's turn running or just ended is a Just me turn (or its message is on its way), for
// what would show it elsewhere. While the book cannot be read it is taken for one: the turn may be private.
func (s *Service) justMeNow(machine, session string) bool {
	if session == "" {
		return false
	}
	b := &s.justMe
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.justMeLocked() {
		return true
	}
	t := b.m[s.meetingTurnKey(machine, session)]
	return t != nil && b.clock().Sub(t.SentAt) <= justMeMax
}

// justMeRoute is POST /api/v1/recordings/{id}/just-me.
func (s *Service) justMeRoute(w http.ResponseWriter, r *http.Request) {
	if !meetingCaller(w, r) {
		return
	}
	e := s.meetingEngine(w, r)
	if e == nil {
		return
	}
	raw, ok := meetingBody(w, r, maxMeetingSendBody, "32 MiB")
	if !ok {
		return
	}
	id := r.PathValue("id")
	_, rec, ok := meetingRecording(w, e, id)
	if !ok {
		return
	}
	msg, err := parseMeetingMessage(raw)
	if err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	switch {
	case rec.Kind != kb.RecordingMeeting:
		apierr.WriteCode(w, http.StatusConflict, codeNotMeeting, i18n.Err("assistant-meeting.not_meeting"))
		return
	case rec.EndedAt != nil:
		apierr.WriteCode(w, http.StatusConflict, codeMeetingEnded, i18n.Err("assistant-meeting.meeting_ended"))
		return
	}
	ctx, cancel := meetingSendCtx(r)
	defer cancel()
	loc := s.location(ctx)
	cal := ""
	if rec.Calendar != nil {
		cal = oneLine(rec.Calendar.Title)
	}
	text := prompts.RenderMeetingJustMe(prompts.MeetingJustMe{Title: feedMeetingName(rec, loc), Calendar: cal, RecordingID: rec.ID,
		At: timeInMeeting(rec, time.Now()), Text: msg.Text})
	msgID := justMeMsgPrefix + msg.ID
	machine, session, err := s.justMeSend(ctx, text, msg.Images, msgID)
	if err != nil {
		writeSendError(w, err)
		return
	}
	apierr.WriteJSON(w, map[string]any{"conversation": map[string]string{"machine_id": machine, "session_id": session}, "client_msg_id": msgID})
}

// justMeSend sends text to the main chat as the owner's own message under msgID, its turn marked first; a main chat
// is started with it when there is none. The main chat it went to.
func (s *Service) justMeSend(ctx context.Context, text string, images []proto.Image, msgID string) (string, string, error) {
	cfg := s.settings(ctx)
	if cfg.Main == "" || cfg.MachineID == "" {
		// Marked under the id it is started with, before it starts: its turn may end before the start answers.
		want := meetingSessionID(msgID+":"+randomHex(8), "main")
		if cfg.MachineID != "" {
			s.justMeSent(cfg.MachineID, want)
		}
		sess, err := s.start(ctx, StartParams{Main: true, Text: text, Images: images, NewSessionID: want, ClientMsgID: msgID})
		if err != nil {
			return "", "", err
		}
		if sess.SessionID != want || !s.sameMachine(sess.MachineID, cfg.MachineID) {
			s.justMeSent(sess.MachineID, sess.SessionID) // a node that picked its own id
		}
		return sess.MachineID, sess.SessionID, nil
	}
	machine, session := cfg.MachineID, cfg.Main
	s.justMeSent(machine, session)
	s.UserSpoke(ctx, SessionRef{MachineID: machine, SessionID: session})
	p := map[string]any{"session_id": session, "text": text, "client_msg_id": msgID}
	if len(images) > 0 {
		p["images"] = images
	}
	params, err := s.SendParams(ctx, SendParams{MachineID: machine, SessionID: session, Params: p})
	if err != nil {
		return "", "", err
	}
	if err := s.sendOnce(ctx, machine, params); err != nil {
		return "", "", err
	}
	return machine, session, nil
}

// errJustMe is how a Just me turn's notify_user answers: the text stays in the conversation, which the meeting page
// shows; nothing is pushed.
var errJustMe = errors.New("just me")

// justMeNotice is what notify_user says in a Just me turn.
func justMeNotice(l i18n.Lang) string { return i18n.T(l, "assistant-meeting.just_me_notify") }
