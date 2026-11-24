package recording

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Handing a meeting to an Agent after it (#8852, docs/assistant/meeting-minutes.md "Handing a meeting to an Agent"):
// POST /api/v1/recordings/{id}/handoff sends the owner's line to an Agent session they picked, after the meeting's
// name, time and minutes (its transcript too when they ask) in a context block, as their own message to it. The Agent
// is held to attending's rules (attendeeVerdict): one of this manager's computers, not a work computer, reachable,
// listed there, taking messages, not bypassPermissions, not closed; a Persona conversation is not an Agent (the
// follow-up is its way). A busy Agent takes it as it takes any message of the owner's. Each try is kept in
// meeting.json's handoffs (sent, or failed and why) for the meeting's page to show and retry.

const (
	// maxHandoffBody is the most a hand-off's body may be: ids, a title and the owner's line.
	maxHandoffBody = 64 << 10
	// maxHandoffTitleRunes is the most of the Agent's title the page's row keeps.
	maxHandoffTitleRunes = 120
	// handoffTranscriptRunes is the most of the transcript a hand-off carries when asked: its start and its end.
	handoffTranscriptRunes = 40000
	// handoffSendWait is how long the send to the Agent's computer may take.
	handoffSendWait = 20 * time.Second
)

// A hand-off's problems, kept with a failed one (the page says them in the owner's words).
const (
	handoffNotAgent    = "not_agent"   // a Persona conversation: the follow-up is its way
	handoffNotAllowed  = "not_allowed" // attending's "never" (work computer, bypassPermissions, another manager's…)
	handoffUnavailable = "unavailable" // its computer can't be reached, it isn't listed there, takes no messages or is closed
	handoffSendFailed  = "send_failed" // the computer didn't take the message
)

// handoffRequest is a hand-off's body: the Agent (machine and session), the title the app showed for it, the owner's
// line (may be empty) and whether the transcript goes too.
type handoffRequest struct {
	MachineID  string `json:"machine_id"`
	SessionID  string `json:"session_id"`
	Title      string `json:"title"`
	Text       string `json:"text"`
	Transcript bool   `json:"transcript"`
}

// parseHandoff reads a hand-off's body: 400 for bad JSON, a missing machine or session, or a line over
// maxMeetingTextRunes.
func parseHandoff(raw []byte) (handoffRequest, error) {
	var in handoffRequest
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, i18n.Err("assistant-meeting.bad_json", i18n.Args{"error": err.Error()})
	}
	in.MachineID, in.SessionID = strings.TrimSpace(in.MachineID), strings.TrimSpace(in.SessionID)
	if in.MachineID == "" || in.SessionID == "" {
		return in, i18n.Err("assistant-meeting.bad_ask_field", i18n.Args{"field": "session"})
	}
	in.Text = strings.TrimSpace(in.Text)
	if utf8.RuneCountInString(in.Text) > maxMeetingTextRunes {
		return in, i18n.Err("assistant-meeting.text_too_long", i18n.Args{"max": maxMeetingTextRunes})
	}
	in.Title = cutRunes(oneLine(in.Title), maxHandoffTitleRunes)
	return in, nil
}

func (s *Recordings) handoffRoutes() {
	s.handle("POST /api/v1/recordings/{id}/handoff", s.handoffRoute)
}

// handoffRoute hands recording id's meeting to an Agent: 409 meeting_running while it goes on, 409 no_material when it
// has neither minutes nor (asked for) a transcript; otherwise the hand-off as kept, sent or failed, with 200.
func (s *Recordings) handoffRoute(w http.ResponseWriter, r *http.Request) {
	if !meetingCaller(w, r) {
		return
	}
	e := s.meetingEngine(w, r)
	if e == nil {
		return
	}
	raw, ok := meetingBody(w, r, maxHandoffBody, "64 KiB")
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, _, ok := meetingRecording(w, e, id); !ok {
		return
	}
	in, err := parseHandoff(raw)
	if err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	m, rec, err := e.MeetingRead(id)
	if err != nil {
		writeMeetingError(w, err)
		return
	}
	minutes := e.Store.HasMinutes(rec.ID)
	transcript := in.Transcript && rec.Chars > 0 && !rec.Trimmed()
	switch {
	case meetingRunning(rec, m, time.Now()):
		apierr.WriteCode(w, http.StatusConflict, codeMeetingRunning, i18n.Err("assistant-meeting.meeting_running"))
		return
	case !minutes && !transcript:
		apierr.WriteCode(w, http.StatusConflict, codeNoMaterial, i18n.Err("assistant-meeting.no_material"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handoffSendWait)
	defer cancel()
	h := kb.MeetingHandoff{ID: rand.Text(), At: time.Now().UTC(), MachineID: in.MachineID, SessionID: in.SessionID,
		Title: in.Title, Transcript: transcript, State: kb.HandoffSent}
	h.Problem = s.handoffSend(ctx, e, rec, kb.SessionRef{MachineID: in.MachineID, SessionID: in.SessionID}, in.Text, transcript)
	if h.Problem != "" {
		h.State = kb.HandoffFailed
	}
	if _, err := e.MeetingUpdate(rec.ID, func(m *kb.Meeting, _ kb.Recording) error {
		m.AddHandoff(h)
		return nil
	}); err != nil {
		writeMeetingError(w, err)
		return
	}
	apierr.WriteJSON(w, map[string]any{"handoff": h})
}

// handoffSend checks ref as an Agent that may take the meeting and sends it the hand-off's message; "" when it went,
// else the problem.
func (s *Recordings) handoffSend(ctx context.Context, e *kb.Engine, rec kb.Recording, ref kb.SessionRef, text string, transcript bool) string {
	t, verdict := s.attendeeCheck(ctx, ref, time.Now())
	switch {
	case verdict == attendeeNotAllowed:
		return handoffNotAllowed
	case verdict == attendeeUnavailable:
		return handoffUnavailable
	case t.persona:
		return handoffNotAgent
	}
	// attendeeBusy is a batch's word: a message of the owner's goes to a busy Agent as any other does.
	msg := s.handoffText(ctx, e, rec, text, transcript)
	if err := s.call(ctx, t.ref.MachineID, DeviceSessionSend, map[string]any{"session_id": t.ref.SessionID, "text": msg}, nil); err != nil {
		s.logf("meeting %s: hand off to %s: %v", short(rec.ID), short(t.ref.SessionID), err)
		return handoffSendFailed
	}
	return ""
}

// handoffText is the hand-off's message: the meeting's name, calendar title, start and minutes (its transcript too when
// asked, within handoffTranscriptRunes) in a context block, then the owner's line.
func (s *Recordings) handoffText(ctx context.Context, e *kb.Engine, rec kb.Recording, text string, transcript bool) string {
	loc := s.location(ctx)
	d := prompts.MeetingHandoff{Title: feedMeetingName(rec, loc), Started: rec.StartedAt.In(loc).Format("2006-01-02 15:04 MST"), Text: text}
	if rec.Calendar != nil {
		d.Calendar = oneLine(rec.Calendar.Title)
	}
	if mins, err := e.Store.ReadMinutes(rec.ID); err == nil {
		d.Minutes = strings.TrimSpace(mins.Markdown)
	}
	if transcript {
		var lines []string
		for _, sg := range kb.MeetingLines(e.Store.RecordingSegments(rec.ID)) { // Persona's echo is not the meeting's
			lines = append(lines, "["+meetingClock(sg.Start)+"] "+oneLine(sg.Text))
		}
		d.Transcript, d.Cut = cutMiddle(strings.Join(lines, "\n"), handoffTranscriptRunes)
	}
	return prompts.RenderMeetingHandoff(d)
}
