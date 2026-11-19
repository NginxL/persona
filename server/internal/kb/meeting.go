package kb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"
)

// The meeting conversation (docs/assistant/meeting-minutes.md "Meeting conversation (#3862)"): a meeting recording
// whose upload carries room is a room meeting, attended by a conversation of its own (the room) in the role room says.
// What the manager keeps of it (the room's role, state and ids, the private mark, the follow-up and the creations
// asked of a node that are not settled yet) is meeting.json beside meta.json, never meta.json: an older manager after a
// rollback rewrites meta.json without the fields it does not know and, when it starts, resumes feeding every recording
// whose meta.json has an attendance, so a room meeting's meta.json never carries one (fail closed). The trim keeps
// meeting.json (an older manager's trim removes it, which leaves the meeting unmarked: private); search skips it.

const meetingFile = "meeting.json"

// Room states (MeetingRoom.State, an open set for the apps).
const (
	RoomNone        = "none"
	RoomStarting    = "starting"
	RoomReady       = "ready"
	RoomUnavailable = "unavailable"
	RoomEnded       = "ended"
	RoomDeleted     = "deleted"
)

// Why a room is unavailable (MeetingRoom.Problem, an open set).
const (
	RoomRuntimeUnsupported = "runtime_unsupported"
	RoomNodeTooOld         = "node_too_old"
	RoomMachineOffline     = "machine_offline"
	RoomStartFailed        = "start_failed"
)

// The parts of a meeting the apps read again when they change (GET …/live?parts=, the meeting_live event). PartPhotos'
// revision is kept in photos.json (recording_photos.go); PartAsks', PartTopics' and PartHighlights' in live.json, and
// PartLines' is the live log's length (live.go); the others' in meeting.json.
const (
	PartRoom     = "room"
	PartAsks     = "asks"
	PartPrivate  = "private"
	PartFollowup = "followup"
	PartPhotos   = "photos"
	PartLines    = "lines"
	PartTopics   = "topics"
	// PartHighlights is the room's highlights of the meeting so far (phase 2b, live.go HighlightsSet).
	PartHighlights = "highlights"
)

// Meeting is meeting.json.
type Meeting struct {
	// Room is the room meeting's: nil when no upload ever carried room (a #2981 meeting, or not attended).
	Room *MeetingRoom `json:"room,omitempty"`
	// Private is the private mark: nil until chosen.
	Private  *bool            `json:"private,omitempty"`
	Followup *MeetingFollowup `json:"followup,omitempty"`
	// Transcription is the meeting's transcription mode and, in hub mode, its state and timeline (#3862 phase 5,
	// transcription.go): nil until an upload named a mode.
	Transcription *MeetingTranscription `json:"transcription,omitempty"`
	// Handoffs are the meeting's hand-offs to Agents after it (#8852, POST …/handoff), oldest first, the last
	// MeetingHandoffsKept: whether each went, and why not. No part of their own: the apps read them with the detail and
	// from the hand-off's answer.
	Handoffs []MeetingHandoff `json:"handoffs,omitempty"`
	// Revs are the parts' revisions (PartRoom, PartPrivate, PartFollowup, PartTranscription): each only goes up.
	Revs map[string]int64 `json:"revs,omitempty"`
}

// MeetingHandoff is one hand-off of a meeting to an Agent (#8852): when, to which session (and the title the app
// showed for it), whether the transcript went too, and its state: HandoffSent, or HandoffFailed with Problem.
type MeetingHandoff struct {
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	MachineID  string    `json:"machine_id"`
	SessionID  string    `json:"session_id"`
	Title      string    `json:"title,omitempty"`
	Transcript bool      `json:"transcript,omitempty"`
	State      string    `json:"state"`
	Problem    string    `json:"problem,omitempty"`
}

// Hand-off states and how many a meeting keeps.
const (
	HandoffSent         = "sent"
	HandoffFailed       = "failed"
	MeetingHandoffsKept = 20
)

// AddHandoff appends h, keeping the last MeetingHandoffsKept.
func (m *Meeting) AddHandoff(h MeetingHandoff) {
	m.Handoffs = append(m.Handoffs, h)
	if n := len(m.Handoffs); n > MeetingHandoffsKept {
		m.Handoffs = slices.Clone(m.Handoffs[n-MeetingHandoffsKept:])
	}
}

// MeetingRoom is a room meeting's room conversation.
type MeetingRoom struct {
	// Attendance is the role the room attends in: AttendanceNotes, AttendanceListener, AttendanceParticipant, or ""
	// (off).
	Attendance string `json:"attendance,omitempty"`
	State      string `json:"state"`
	Problem    string `json:"problem,omitempty"`
	MachineID  string `json:"machine_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	// Pending: the room's creation is (or is about to be) asked of the node under SessionID and is not settled yet
	// ("Made once"): after a restart the manager looks there first.
	Pending bool `json:"pending,omitempty"`
	// StartingAt is when the room last turned starting (a start not ready within 2 minutes failed), RetryAt when an
	// unavailable room is tried again.
	StartingAt time.Time `json:"starting_at,omitzero"`
	RetryAt    time.Time `json:"retry_at,omitzero"`
	// EndingAt: the meeting's end batch went to the room then; it turns ended once that turn ends, at the latest a
	// little later.
	EndingAt time.Time `json:"ending_at,omitzero"`
	// Started: the room came up (it was ready once): its ids are shown from then on. A start that never got there
	// keeps them here only, for the deletion.
	Started bool `json:"started,omitempty"`
	// Batching is how the room is fed (docs/assistant/meeting-minutes.md "Long meetings: lean batches and compaction
	// (#8253)"): RoomBatchingLean for a room a manager with lean batches started; 0 for an older room, fed a batch a
	// minute with the rules and the lists in each until it is deleted. An older manager after a rollback drops it when
	// it rewrites meeting.json, which makes the room an older one from then on.
	Batching int `json:"batching,omitempty"`
	// Lean is a lean room's bookkeeping: the lists' boundaries and its scheduled compaction. The apps never see it, so
	// its changes raise no revision.
	Lean MeetingRoomLean `json:"lean,omitzero"`
}

// RoomBatchingLean marks a room started with lean batches (MeetingRoom.Batching).
const RoomBatchingLean = 2

// MeetingRoomLean is what the manager keeps of a lean room across a restart (#8253).
type MeetingRoomLean struct {
	// ListsAt is when the last batch that showed the topics and highlights so far went; ListsDue: the next batch
	// shows them (the room's conversation was compacted since).
	ListsAt  time.Time `json:"lists_at,omitzero"`
	ListsDue bool      `json:"lists_due,omitempty"`
	// Tokens is the most context the room's row reported since its conversation was last compacted, as far as the
	// manager knows: a drop below half of it is Claude Code compacting the room on its own. TokensStale is what the row
	// reported when the manager saw a compaction end: the context of the reply before it (a compaction writes none),
	// not read as the room's until the row reports another.
	Tokens      int32 `json:"tokens,omitempty"`
	TokensStale int32 `json:"tokens_stale,omitempty"`
	// SpanAt is the live log's feed position (live.json's fed) when the room started or was last compacted: what the
	// batches given since cover is read off the live log's lines from there to its feed position now, so a restart
	// between a batch's two writes loses none of it.
	SpanAt int `json:"span_at,omitempty"`
	// CompactedAt is when the last scheduled compaction ended done, Compactions how many did.
	CompactedAt time.Time `json:"compacted_at,omitzero"`
	Compactions int       `json:"compactions,omitempty"`
	// CompactStartedAt: a compaction was asked of the node then and has not ended as far as the manager knows;
	// CompactFed is the feed position then (the lines given before it are what it compacts).
	CompactStartedAt time.Time `json:"compact_started_at,omitzero"`
	CompactFed       int       `json:"compact_fed,omitempty"`
	// CompactSeen is the start of the latest compaction of the room's session the manager has taken into account (its
	// own, or one the owner asked for), to tell one that ended since from it.
	CompactSeen time.Time `json:"compact_seen,omitzero"`
	// Attempts are the compactions refused or failed for the current span, RetryAt when the next may start; Never:
	// the node cannot compact the room (too old for it, or it would start the room's process without its restrictions).
	Attempts int       `json:"attempts,omitempty"`
	RetryAt  time.Time `json:"retry_at,omitzero"`
	Never    bool      `json:"never,omitempty"`
}

// MeetingFollowup is the meeting's follow-up conversation.
type MeetingFollowup struct {
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// Pending: its creation was asked of the node by the call OpID and is not settled yet. OpID stays once it is made:
	// that call repeated is answered as made, with nothing sent again.
	Pending bool   `json:"pending,omitempty"`
	OpID    string `json:"op_id,omitempty"`
}

var (
	// ErrNotMeeting: the recording is not a meeting as #2981 counts them (neither kind meeting nor minutes asked).
	ErrNotMeeting = errors.New("this recording is not a meeting")
)

// Clone is a deep copy of m.
func (m Meeting) Clone() Meeting {
	out := Meeting{Private: m.Private, Revs: maps.Clone(m.Revs)}
	if m.Room != nil {
		r := *m.Room
		out.Room = &r
	}
	if m.Private != nil {
		p := *m.Private
		out.Private = &p
	}
	if m.Followup != nil {
		f := *m.Followup
		out.Followup = &f
	}
	out.Transcription = m.Transcription.clone()
	out.Handoffs = slices.Clone(m.Handoffs)
	return out
}

// RoomMeeting: an upload of r carried room once: its room conversation attends it, never another session.
func (r Recording) RoomMeeting() bool { return r.Conv != nil && r.Conv.Room != nil }

// Attending: r is attended now (its role is not off): the phone sends its live sentences and a room meeting's room is
// fed them, notes included.
func (r Recording) Attending() bool { return r.AttendanceRole() != AttendanceOff }

// Answering: Persona answers in r's meeting now (listener or participant): not while it is off, nor while its room only
// takes notes (#8851), when …/ask answers not_attending.
func (r Recording) Answering() bool {
	role := r.AttendanceRole()
	return role == AttendanceListener || role == AttendanceParticipant
}

// IsMeeting: r is a meeting as #2981 counts them: uploaded as one, or its minutes asked for.
func (r Recording) IsMeeting() bool { return r.Kind == RecordingMeeting || r.MinutesAsked }

// MarkedNotPrivate: r's private mark is false, chosen. A meeting with no mark counts as private.
func (r Recording) MarkedNotPrivate() bool {
	return r.Conv != nil && r.Conv.Private != nil && !*r.Conv.Private
}

// readMeeting is recording id's meeting.json, read from disk (an upload's undo journal never holds it); false when
// there is none.
func (s *Store) readMeeting(id string) (*Meeting, error) {
	raw, err := os.ReadFile(s.path(KindRecording, id, meetingFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m Meeting
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("meeting of %s: %w", id, err)
	}
	return &m, nil
}

// putMeeting writes recording id's meeting.json, not when the recording was deleted meanwhile (false then); a trimmed
// recording keeps it (its private mark and its room's end). The caller holds meetMu.
func (s *Store) putMeeting(id string, m *Meeting) (bool, error) {
	if s.readOnly {
		return false, ErrReadOnly
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return false, err
	}
	s.recFilesMu.Lock()
	defer s.recFilesMu.Unlock()
	if _, gone := s.tombstones()[id]; gone {
		return false, nil
	}
	if _, err := os.Stat(s.path(KindRecording, id)); err != nil {
		return false, nil
	}
	return true, writeRecordingFile(s.path(KindRecording, id, meetingFile), append(raw, '\n'))
}

// meetingParts are the parts of m that differ from before, in a fixed order.
func meetingParts(before, after Meeting) []string {
	var out []string
	same := func(a, b any) bool {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		return bytes.Equal(x, y)
	}
	if !same(roomShown(before.Room), roomShown(after.Room)) {
		out = append(out, PartRoom)
	}
	if !same(before.Private, after.Private) {
		out = append(out, PartPrivate)
	}
	if !same(before.Followup, after.Followup) {
		out = append(out, PartFollowup)
	}
	if !same(transcriptionShown(before.Transcription), transcriptionShown(after.Transcription)) {
		out = append(out, PartTranscription)
	}
	return out
}

// roomShown is room without what only the manager reads (its batching and a lean room's bookkeeping, #8253), whose
// changes raise no revision: the apps see nothing new of it.
func roomShown(room *MeetingRoom) *MeetingRoom {
	if room == nil {
		return nil
	}
	r := *room
	r.Batching, r.Lean = 0, MeetingRoomLean{}
	return &r
}

// meetingSame: a and b are the same meeting.json (also where no part the apps read differs: the timeline, the final
// pass's bookkeeping, a lean room's bookkeeping).
func meetingSame(a, b Meeting) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// bumpRevs raises the revisions of parts in m.
func bumpRevs(m *Meeting, parts []string) {
	if len(parts) == 0 {
		return
	}
	if m.Revs == nil {
		m.Revs = map[string]int64{}
	}
	for _, p := range parts {
		m.Revs[p]++
	}
}

// meetingChanged tells the engine's MeetingChanged of recording id's changed parts.
func (e *Engine) meetingChanged(id string, parts []string) {
	if len(parts) > 0 && e.MeetingChanged != nil {
		e.MeetingChanged(id, parts)
	}
}

// MeetingRead is recording id's meeting.json (empty without one) and its stored upload, also on a read-only knowledge
// base: ErrRecordingDeleted after a delete, ErrNotFound with no upload of it.
func (e *Engine) MeetingRead(rawID string) (Meeting, Recording, error) {
	id, ok := RecordingID(rawID)
	if !ok {
		return Meeting{}, Recording{}, ErrNotFound
	}
	if _, gone := e.Store.tombstones()[id]; gone {
		return Meeting{}, Recording{}, ErrRecordingDeleted
	}
	r, ok := e.Store.Recording(id)
	if !ok {
		return Meeting{}, Recording{}, ErrNotFound
	}
	var m Meeting
	if r.Conv != nil {
		m = r.Conv.Clone()
	}
	return m, *r, nil
}

// MeetingUpdate changes recording id's meeting.json with fn (given it and the stored upload) and writes the result
// unless fn fails or changed nothing; the revisions of the parts that changed go up and MeetingChanged hears of them.
// ErrRecordingDeleted after a delete, ErrNotFound with no upload (or deleted meanwhile), ErrReadOnly on a read-only
// knowledge base. fn runs with meeting.json locked: it must be quick and call no Engine method of the recordings.
func (e *Engine) MeetingUpdate(rawID string, fn func(m *Meeting, r Recording) error) (Meeting, error) {
	id, ok := RecordingID(rawID)
	if !ok {
		return Meeting{}, ErrNotFound
	}
	st := e.Store
	st.meetMu.Lock()
	m, parts, err := e.meetingUpdateLocked(id, fn)
	st.meetMu.Unlock()
	if err == nil {
		e.meetingChanged(id, parts)
	}
	return m, err
}

func (e *Engine) meetingUpdateLocked(id string, fn func(m *Meeting, r Recording) error) (Meeting, []string, error) {
	st := e.Store
	if _, gone := st.tombstones()[id]; gone {
		return Meeting{}, nil, ErrRecordingDeleted
	}
	r, ok := st.Recording(id)
	if !ok {
		return Meeting{}, nil, ErrNotFound
	}
	if st.ReadOnly() {
		return Meeting{}, nil, ErrReadOnly
	}
	var before Meeting
	if r.Conv != nil {
		before = r.Conv.Clone()
	}
	m := before.Clone()
	if err := fn(&m, *r); err != nil {
		return Meeting{}, nil, err
	}
	if meetingSame(before, m) {
		return m, nil, nil
	}
	parts := meetingParts(before, m)
	bumpRevs(&m, parts)
	if ok, err := st.putMeeting(id, &m); err != nil {
		return Meeting{}, nil, err
	} else if !ok {
		return Meeting{}, nil, ErrNotFound
	}
	return m, parts, nil
}

// SetPrivate sets recording id's private mark (PUT /api/v1/recordings/{id}/private): any time, a trimmed recording's
// too. Errors in the contract's order: ErrRecordingDeleted, ErrNotFound, ErrReadOnly, ErrNotMeeting.
func (e *Engine) SetPrivate(rawID string, private bool) (Meeting, error) {
	return e.MeetingUpdate(rawID, func(m *Meeting, r Recording) error {
		if !r.IsMeeting() {
			return ErrNotMeeting
		}
		m.Private = &private
		return nil
	})
}

// roomUpload is an upload's room: the role its room conversation attends in.
type roomUpload struct {
	Attendance string
	bad        bool // attendance was not a string
}

// readRoomAndPrivate reads an upload's room and private (unmarshalRecordingUpload): a room that is not an object
// counts as absent, a private that is not a boolean is ignored.
func (u *RecordingUpload) readRoomAndPrivate(room, private json.RawMessage) {
	u.room, u.Private = nil, nil
	if !jsonNull(room) {
		var keys map[string]json.RawMessage
		if json.Unmarshal(room, &keys) == nil && keys != nil {
			ru := &roomUpload{}
			if v := keys["attendance"]; !jsonNull(v) && json.Unmarshal(v, &ru.Attendance) != nil {
				ru.bad = true
			}
			u.room = ru
		}
	}
	if !jsonNull(private) {
		var b bool
		if json.Unmarshal(private, &b) == nil {
			u.Private = &b
		}
	}
}

// SetRoom gives an upload a room in role (tests and the HTTP layer build uploads in Go).
func (u *RecordingUpload) SetRoom(role string) { u.room = &roomUpload{Attendance: role} }

// HasRoom: the upload carries room.
func (u *RecordingUpload) HasRoom() bool { return u.room != nil }

// roomRole is the role an upload's room asks for, normalized as #2981's attendance is (normalizeAttendance), with
// notes besides (#8851): a value that is not one of the four is off with AttendanceUnknownRole, a role on a recording
// that is not a meeting off with AttendanceNotMeeting. The upload is cleaned (its Kind set).
func (u *RecordingUpload) roomRole() (role, problem string) {
	role = strings.ToLower(strings.TrimSpace(u.room.Attendance))
	switch {
	case u.room.bad:
		return "", AttendanceUnknownRole
	case role == "" || role == AttendanceOff:
		return "", ""
	case role != AttendanceNotes && role != AttendanceListener && role != AttendanceParticipant:
		return "", AttendanceUnknownRole
	case u.Kind != RecordingMeeting:
		return "", AttendanceNotMeeting
	}
	return role, ""
}

// putMeetingFields stores what upload u says of recording id's meeting: its room (an upload with room makes it a room
// meeting for good; one without turns the role of a room meeting off and changes nothing else), its first private
// mark (only a meeting's, only while it has none) and its transcription (putTranscriptionFields: the first mode named,
// then in hub mode the timeline and the end). The caller holds recMu; meeting.json is written before meta.json, so a
// crash between leaves a role no older manager reads. The upload's top-level attendance fields are cleared for a room
// meeting (they are ignored beside room, and never written to its meta.json). The meeting after it, and the parts
// that changed.
func (e *Engine) putMeetingFields(id string, u *RecordingUpload, now time.Time) (*Meeting, []string, error) {
	st := e.Store
	st.meetMu.Lock()
	defer st.meetMu.Unlock()
	// From the file, under meetMu: r was read before it, and a MeetingUpdate since (the private mark, the room, the
	// follow-up) would be written back over.
	var before Meeting
	if m, err := st.readMeeting(id); err != nil {
		return nil, nil, err
	} else if m != nil {
		before = m.Clone()
	}
	m := before.Clone()
	switch {
	case u.room != nil:
		role, problem := u.roomRole()
		u.roomProblem = problem
		if m.Room == nil {
			m.Room = &MeetingRoom{State: RoomNone}
		}
		m.Room.Attendance = role
	case m.Room != nil:
		m.Room.Attendance = "" // once a room meeting, always one: without room it is off
	}
	if m.Room != nil {
		u.Attendance, u.AttendingSession = "", nil
	}
	if u.Private != nil && m.Private == nil && u.Kind == RecordingMeeting {
		p := *u.Private
		m.Private = &p
	}
	e.putTranscriptionFields(id, &m, u, now)
	if meetingSame(before, m) {
		if m.Room == nil && m.Private == nil && m.Followup == nil && m.Transcription == nil {
			return nil, nil, nil
		}
		return &m, nil, nil
	}
	parts := meetingParts(before, m)
	bumpRevs(&m, parts)
	raw, err := json.MarshalIndent(&m, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(st.path(KindRecording, id), 0o700); err != nil {
		return nil, nil, err
	}
	st.recFilesMu.Lock()
	err = writeRecordingFile(st.path(KindRecording, id, meetingFile), append(raw, '\n'))
	st.recFilesMu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	return &m, parts, nil
}

// RoomMeetings are the recordings that are room meetings, oldest first: what the room keeper looks at after a
// restart.
func (e *Engine) RoomMeetings() []Recording {
	var out []Recording
	for _, r := range e.Store.Recordings() {
		if r.RoomMeeting() {
			out = append(out, *r)
		}
	}
	slices.SortFunc(out, func(a, b Recording) int {
		if c := a.StartedAt.Compare(b.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// RoomView is a room as the apps read it (GET …/live, detail.meta, the upload's 200): the ids once it has started,
// never with none or deleted; the problem only while unavailable.
type RoomView struct {
	State     string `json:"state"`
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Problem   string `json:"problem,omitempty"`
}

// ConvView is a conversation of the meeting (the follow-up) as the apps read it.
type ConvView struct {
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
}

// RoomView is m's room as the apps read it; nil when the recording is not a room meeting.
func (m Meeting) RoomView() *RoomView {
	if m.Room == nil {
		return nil
	}
	rm := m.Room
	v := &RoomView{State: rm.State}
	if v.State == "" {
		v.State = RoomNone
	}
	if v.State == RoomUnavailable {
		v.Problem = rm.Problem
	}
	if v.State != RoomNone && v.State != RoomDeleted && rm.Started && rm.SessionID != "" {
		v.MachineID, v.SessionID = rm.MachineID, rm.SessionID
	}
	return v
}

// FollowupView is m's follow-up as the apps read it; nil before it is made.
func (m Meeting) FollowupView() *ConvView {
	f := m.Followup
	if f == nil || f.Pending || f.SessionID == "" {
		return nil
	}
	return &ConvView{MachineID: f.MachineID, SessionID: f.SessionID}
}

// metaWithMeeting is a recording's meta.json as detail.meta shows it: with its meeting.json's private mark, room,
// follow-up and transcription (its mode, and in hub mode its state and gaps); a room meeting's attendance is its
// room's role (absent when off), never an attending_session.
func metaWithMeeting(raw []byte, m *Meeting) []byte {
	if m == nil {
		return raw
	}
	var meta map[string]json.RawMessage
	if json.Unmarshal(raw, &meta) != nil || meta == nil {
		return raw
	}
	set := func(k string, v any) {
		if b, err := json.Marshal(v); err == nil {
			meta[k] = b
		}
	}
	if m.Private != nil {
		set("private", *m.Private)
	}
	if v := m.RoomView(); v != nil {
		set("room", v)
		delete(meta, "attending_session")
		delete(meta, "attendance")
		if m.Room.Attendance != "" {
			if trimmed := meta["trimmed_at"]; jsonNull(trimmed) {
				set("attendance", m.Room.Attendance)
			}
		}
	}
	if v := m.FollowupView(); v != nil {
		set("followup", v)
	}
	if v := m.TranscriptionView(nil); v != nil {
		set("transcription", v)
	}
	if len(m.Handoffs) > 0 {
		set("handoffs", m.Handoffs)
	}
	out, err := json.Marshal(meta)
	if err != nil {
		return raw
	}
	return out
}

// HasMinutes: recording id has minutes that can be read.
func (s *Store) HasMinutes(id string) bool { return s.hasMinutes(id) }
