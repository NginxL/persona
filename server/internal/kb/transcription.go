package kb

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// Hub transcription (#3862 phase 5, docs/assistant/meeting-minutes.md "Phase 5: hub transcription"): a meeting
// uploaded with transcription hub is transcribed on the hub from the audio the phone streams. What the knowledge base
// keeps of it: the mode, the state and the timeline in meeting.json (never meta.json, as the room: an older manager
// after a rollback rewrites meta.json without fields it does not know), the hub's own transcript in hub_segments.json
// (hub_segments.go), the audio in attachments/audio/ until the minutes from the final transcript are written
// (recording_audio.go). The transcript of record (TranscriptOfRecord) is what everything that reads the transcript
// reads: transcript.md, detail.segments, search, the minutes, the summary and the follow-up's seed. The manager owns
// the socket, the jobs and the final pass; it changes the state through TranscriptionUpdate.

// Modes, states, parts, limits.
const (
	// The transcription modes (meeting.json's transcription.mode, the upload's transcription).
	TranscriptionDevice, TranscriptionHub = "device", "hub"
	// The states of a hub-mode recording's transcription.
	TranscriptionLive, TranscriptionFinishing, TranscriptionFinal, TranscriptionFailed = "live", "finishing", "final", "failed"
	// The upload's transcription_problem: the hub was asked for and is not ready, or the recording is not a meeting.
	TranscriptionUnavailable, TranscriptionNotMeeting = "unavailable", "not_meeting"
	// The meeting parts the apps read again (GET …/live?parts=, the meeting_live event): PartTranscription's revision
	// is kept in meeting.json, PartInterim's is the caption's time in milliseconds (the manager's, never stored).
	PartTranscription, PartInterim = "transcription", "interim"
	// The timeline's limits: role changes and speaking spans.
	MaxTimelineRoles, MaxTimelineSpans = 100, 1000
	// AudioRate is the stored audio's sample rate (PCM16 little-endian, mono).
	AudioRate = 16000
	// MaxAudioSeconds is the most audio the hub keeps for one recording: none from this recording second on.
	MaxAudioSeconds = 12 * 3600
	// The passes that wrote the hub's transcript (HubTranscript.Pass).
	HubPassLive, HubPassFinal = "live", "final"
	// A segment's source in the transcript of record (RecordingSegment.Source) and a live line's (LiveSegment.Source).
	SourceHub, SourceDevice = "hub", "device"
)

// minutesHubWait is how long a hub-mode meeting's minutes wait for its final transcript after the complete upload.
const minutesHubWait = 45 * time.Minute

var (
	// ErrHubTranscribes: POST …/live on a recording the hub transcribes (its live log is the hub's).
	ErrHubTranscribes = errors.New("the hub transcribes this meeting")
	// ErrNotHub: the recording's mode is not hub (the audio socket, PUT …/audio).
	ErrNotHub = errors.New("this recording is not transcribed on the hub")
	// ErrTranscriptionFinal: the final pass has started; the hub takes no more audio.
	ErrTranscriptionFinal = errors.New("the final pass has started")
	// ErrAudioFull: the recording holds MaxAudioSeconds of audio.
	ErrAudioFull = errors.New("the hub holds the most audio a recording may have")
	// ErrBadAudio: audio that is not whole 16-bit samples, or a position that is not 0 or more seconds (400, or the
	// socket's bad_frame).
	ErrBadAudio = errors.New("audio must be whole 16-bit samples")
)

// ---- the timeline ----

// TimelineRole is one role change of the timeline: the role (AttendanceOff, AttendanceNotes, AttendanceListener,
// AttendanceParticipant) from recording second At on.
type TimelineRole struct {
	At         float64 `json:"at"`
	Attendance string  `json:"attendance"`
}

// Timeline is a recording's role changes and the spans Persona spoke in, in recording seconds, each with the answer
// said in it as an upload's spoken has them (SpokenSpan), as the phone keeps them ("The timeline"); meeting.json keeps
// the union of every one received. Roles are in time order, one per At; Speaking is a union (sorted, apart: spokenUnion).
type Timeline struct {
	Roles    []TimelineRole `json:"roles,omitempty"`
	Speaking []SpokenSpan   `json:"speaking,omitempty"`
}

// timelineRole is attendance as a timeline has it ("" when it is not one of the four roles).
func timelineRole(attendance string) string {
	switch a := strings.ToLower(strings.TrimSpace(attendance)); a {
	case AttendanceOff, AttendanceNotes, AttendanceListener, AttendanceParticipant:
		return a
	}
	return ""
}

// ParseTimeline reads a timeline as the upload and the socket's start carry it: an object with roles
// ([{"at", "attendance"}]) and speaking ([{"start", "end", "text"?}], as an upload's spoken; a text over maxSpokenText
// bytes is cut at a character boundary). ok is false when it is absent, malformed or over the limits: a timeline with
// any entry that is not one (an at that is not a number of 0 or more, a role that is not one of the four, a span that
// is not an object with numbers start and end, 0 <= start <= end, and a string text if any) is ignored whole, never an
// error.
func ParseTimeline(raw json.RawMessage) (t Timeline, ok bool) {
	if jsonNull(raw) {
		return Timeline{}, false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return Timeline{}, false
	}
	var roles, speaking []json.RawMessage
	if v := obj["roles"]; !jsonNull(v) && json.Unmarshal(v, &roles) != nil {
		return Timeline{}, false
	}
	if v := obj["speaking"]; !jsonNull(v) && json.Unmarshal(v, &speaking) != nil {
		return Timeline{}, false
	}
	if len(roles) > MaxTimelineRoles || len(speaking) > MaxTimelineSpans {
		return Timeline{}, false
	}
	var in Timeline
	for _, raw := range roles {
		var role struct {
			At         *float64 `json:"at"`
			Attendance *string  `json:"attendance"`
		}
		if json.Unmarshal(raw, &role) != nil || role.At == nil || role.Attendance == nil || *role.At < 0 {
			return Timeline{}, false
		}
		a := timelineRole(*role.Attendance)
		if a == "" {
			return Timeline{}, false
		}
		in.Roles = append(in.Roles, TimelineRole{At: *role.At, Attendance: a})
	}
	for _, raw := range speaking {
		var span struct {
			Start *float64 `json:"start"`
			End   *float64 `json:"end"`
			Text  *string  `json:"text"`
		}
		if json.Unmarshal(raw, &span) != nil || span.Start == nil || span.End == nil || *span.Start < 0 || *span.End < *span.Start {
			return Timeline{}, false
		}
		sp := SpokenSpan{Start: *span.Start, End: *span.End}
		if span.Text != nil {
			sp.Text = SpokenText(*span.Text)
		}
		in.Speaking = append(in.Speaking, sp)
	}
	t.Merge(in)
	return t, true
}

// Merge adds o to t: one role change per at (o's winning: it was received later), in time order; overlapping or
// touching spans made one, their texts joined in time order, a span received again counted once (spokenUnion). Past
// the limits t keeps the first MaxTimelineRoles role changes and MaxTimelineSpans spans. Entries that are not ones are
// left out. changed says whether t moved.
func (t *Timeline) Merge(o Timeline) (changed bool) {
	before, _ := json.Marshal(t)
	byAt := map[float64]string{}
	for _, list := range [][]TimelineRole{t.Roles, o.Roles} {
		for _, r := range list {
			if a := timelineRole(r.Attendance); a != "" && finite(r.At) && r.At >= 0 {
				byAt[r.At] = a
			}
		}
	}
	var roles []TimelineRole
	for at, a := range byAt {
		roles = append(roles, TimelineRole{At: at, Attendance: a})
	}
	slices.SortFunc(roles, func(a, b TimelineRole) int { return cmp.Compare(a.At, b.At) })
	if len(roles) > MaxTimelineRoles {
		roles = roles[:MaxTimelineRoles]
	}
	var spans []SpokenSpan
	for _, s := range spokenUnion(append(slices.Clone(t.Speaking), o.Speaking...)) {
		if s.Start >= 0 {
			spans = append(spans, s)
		}
	}
	if len(spans) > MaxTimelineSpans {
		spans = spans[:MaxTimelineSpans]
	}
	t.Roles, t.Speaking = roles, spans
	after, _ := json.Marshal(t)
	return !bytes.Equal(before, after)
}

// RoleAt is the role at recording second at: the timeline's last role change at or before it, else otherwise (the
// meeting's role, room.attendance).
func (t Timeline) RoleAt(at float64, otherwise string) string {
	role, found := otherwise, -1.0
	for _, r := range t.Roles {
		if r.At <= at && r.At >= found {
			role, found = r.Attendance, r.At
		}
	}
	return role
}

func (t *Timeline) clone() *Timeline {
	if t == nil {
		return nil
	}
	return &Timeline{Roles: slices.Clone(t.Roles), Speaking: slices.Clone(t.Speaking)}
}

// ---- meeting.json's transcription ----

// MeetingTranscription is meeting.json's "transcription" (never meta.json). The manager changes it through
// TranscriptionUpdate; uploads set the mode, the timeline's union, CompleteAt and End; the knowledge base keeps Gaps
// up to date while the audio comes, and AudioGoneAt.
type MeetingTranscription struct {
	Mode     string    `json:"mode"`
	State    string    `json:"state,omitempty"`    // hub only
	Problem  string    `json:"problem,omitempty"`  // the audio socket's standing problem
	Fallback bool      `json:"fallback,omitempty"` // the live log is filled from the phone
	Timeline *Timeline `json:"timeline,omitempty"`
	// End is the recording's end in recording seconds: the socket's end.at (the manager's), else the duration of the
	// complete upload or the end of the audio held, whichever is later.
	End        float64   `json:"end,omitempty"`
	CompleteAt time.Time `json:"complete_at,omitzero"` // the first complete:true upload (the gap and minutes waits)
	FinalFrom  time.Time `json:"final_from,omitzero"`  // the final pass started: transcription_final from here
	// FinalAt is when the state became final or failed (set then when the manager did not).
	FinalAt    time.Time `json:"final_at,omitzero"`
	FinalTries int       `json:"final_tries,omitempty"`
	// FinalUnreachable counts the final pass's tries that never reached the upstream (upstream_unavailable, nothing
	// billed): they are tried again on their own, longer schedule (the manager's finalUnreachableRetries).
	FinalUnreachable int `json:"final_unreachable,omitempty"`
	// FinalDone is how far the final pass has got, in recording seconds: the end of the last window it finished, set
	// by the manager as each window ends and when a try starts (0 after a restart, which starts over). The apps read it
	// as the pass's progress (#13461); none once the state is final or failed.
	FinalDone float64 `json:"final_done,omitempty"`
	// Gaps are the spans of the recording the hub holds no audio for (GapsIn), kept up to date by the audio writes
	// until the final pass starts and frozen from then on: the final pass's, which stay once the audio is gone.
	Gaps         [][2]float64 `json:"gaps,omitempty"`
	AudioGoneAt  time.Time    `json:"audio_gone_at,omitzero"`
	UsageSeconds float64      `json:"usage_seconds,omitempty"` // the upstream's billed seconds, diagnostics only
}

func (t *MeetingTranscription) clone() *MeetingTranscription {
	if t == nil {
		return nil
	}
	c := *t
	c.Timeline = t.Timeline.clone()
	c.Gaps = slices.Clone(t.Gaps)
	return &c
}

// hub: t is a hub-mode transcription.
func (t *MeetingTranscription) hub() bool { return t != nil && t.Mode == TranscriptionHub }

// done: the final pass has run (final), or the hub made nothing usable (failed).
func (t *MeetingTranscription) done() bool {
	return t.State == TranscriptionFinal || t.State == TranscriptionFailed
}

// audioClosed: the hub takes no more audio of it (the final pass started or ran, or the audio is gone).
func (t *MeetingTranscription) audioClosed() bool {
	return !t.FinalFrom.IsZero() || t.done() || !t.AudioGoneAt.IsZero()
}

// timeline is t's timeline (empty without one).
func (t *MeetingTranscription) timeline() Timeline {
	if t == nil || t.Timeline == nil {
		return Timeline{}
	}
	return *t.Timeline
}

// settle keeps t's own rules after a change: a hub-mode transcription has a state; once final or failed the final
// pass has started and ended; no empty gaps or timeline.
func (t *MeetingTranscription) settle(now time.Time) {
	if t.Mode != TranscriptionHub {
		*t = MeetingTranscription{Mode: t.Mode}
		return
	}
	if t.State == "" {
		t.State = TranscriptionLive
	}
	if t.done() {
		if t.FinalAt.IsZero() {
			t.FinalAt = now.UTC()
		}
		if t.FinalFrom.IsZero() {
			t.FinalFrom = t.FinalAt
		}
		t.FinalDone = 0
	}
	if len(t.Gaps) == 0 {
		t.Gaps = nil
	}
	if t.Timeline != nil && len(t.Timeline.Roles) == 0 && len(t.Timeline.Speaking) == 0 {
		t.Timeline = nil
	}
}

// transcriptionShown is what of t the apps read (TranscriptionView): PartTranscription moves when it changes, not
// when only the timeline grows or the bookkeeping does.
func transcriptionShown(t *MeetingTranscription) any {
	if t == nil {
		return nil
	}
	var gaps [][2]float64
	if len(t.Gaps) > 0 {
		gaps = t.Gaps
	}
	done, end, _ := t.finalProgress()
	return struct {
		Mode, State, Problem string
		Gaps                 [][2]float64
		Fallback             bool
		FinalDone, End       float64
	}{t.Mode, t.State, t.Problem, gaps, t.Fallback, done, end}
}

// finalProgress is how far the final pass is, for the apps (#13461): the recording seconds it has finished and the
// recording's end, while the state is finishing and the pass has started (it may be waiting for its next try), and
// the end is known. done is at most end.
func (t *MeetingTranscription) finalProgress() (done, end float64, ok bool) {
	if !t.hub() || t.State != TranscriptionFinishing || t.FinalFrom.IsZero() || t.End <= 0 {
		return 0, 0, false
	}
	return min(max(t.FinalDone, 0), t.End), t.End, true
}

// TranscriptionView is transcription on GET …/live and in detail.meta.
type TranscriptionView struct {
	Mode     string        `json:"mode"`
	State    string        `json:"state,omitempty"`
	Gaps     *[][2]float64 `json:"gaps,omitempty"` // hub: always present, [] when none
	Problem  string        `json:"problem,omitempty"`
	Fallback bool          `json:"fallback,omitempty"`
	// FinalDone and End: while the final pass runs (finishing, the pass started), how far it is and the recording's
	// end, both in recording seconds (#13461); absent otherwise. FinalDone is 0 as the pass starts.
	FinalDone *float64 `json:"final_done,omitempty"`
	End       *float64 `json:"end,omitempty"`
}

// TranscriptionView is m's transcription as the apps read it; nil while no mode was decided. gaps are the spans the
// hub holds no audio for now; nil takes those meeting.json keeps (the audio writes keep them up to date). Once the
// final pass has started, or the audio is gone, they are the ones meeting.json keeps whatever gaps says (a trimmed
// recording's: none).
func (m Meeting) TranscriptionView(gaps [][2]float64) *TranscriptionView {
	t := m.Transcription
	if t == nil || t.Mode == "" {
		return nil
	}
	if !t.hub() {
		return &TranscriptionView{Mode: t.Mode}
	}
	if gaps == nil || t.audioClosed() {
		gaps = t.Gaps
	}
	shown := append([][2]float64{}, gaps...)
	state := cmp.Or(t.State, TranscriptionLive)
	v := &TranscriptionView{Mode: t.Mode, State: state, Gaps: &shown, Problem: t.Problem, Fallback: t.Fallback}
	if done, end, ok := t.finalProgress(); ok {
		v.FinalDone, v.End = &done, &end
	}
	return v
}

// HubTranscribed: r's meeting.json has mode hub (an unreadable meeting.json: false).
func (r Recording) HubTranscribed() bool { return r.Conv != nil && r.Conv.Transcription.hub() }

// hubTranscription is r's hub-mode transcription, nil when r is not transcribed on the hub.
func (r *Recording) hubTranscription() *MeetingTranscription {
	if !r.HubTranscribed() {
		return nil
	}
	return r.Conv.Transcription
}

// transcriptionHolds: r's minutes wait for its final transcript: hub mode, the state not final nor failed, at most
// minutesHubWait after the complete upload.
func (r *Recording) transcriptionHolds(now time.Time) bool {
	t := r.hubTranscription()
	return t != nil && !t.done() && !t.CompleteAt.IsZero() && now.Before(t.CompleteAt.Add(minutesHubWait))
}

// transcriptionDone: r is transcribed on the hub and its final pass has run or failed.
func (r *Recording) transcriptionDone() bool {
	t := r.hubTranscription()
	return t != nil && t.done()
}

// ---- the upload ----

// readTranscription reads an upload's transcription and timeline (unmarshalRecordingUpload): a transcription that is
// not "device" or "hub" (another string, another type, null) names no mode; a timeline that ParseTimeline does not
// take is none. Neither fails the upload.
func (u *RecordingUpload) readTranscription(mode, timeline json.RawMessage) {
	u.transcription, u.timeline, u.transcriptionProblem = "", nil, ""
	var s string
	if !jsonNull(mode) && json.Unmarshal(mode, &s) == nil {
		u.transcription = transcriptionMode(s)
	}
	if t, ok := ParseTimeline(timeline); ok {
		u.timeline = &t
	}
}

// transcriptionMode is s as a mode ("" when it names none).
func transcriptionMode(s string) string {
	switch s = strings.ToLower(strings.TrimSpace(s)); s {
	case TranscriptionDevice, TranscriptionHub:
		return s
	}
	return ""
}

// SetTranscription gives an upload a transcription mode and a timeline (tests and the HTTP layer build uploads in Go).
func (u *RecordingUpload) SetTranscription(mode string, t *Timeline) {
	u.transcription, u.timeline = mode, t
}

// putTranscriptionFields applies what upload u (cleaned) says of the transcription to m, recording id's meeting.json
// under meetMu: the mode of the first meeting upload that names one (hub only while TranscriptionReady says so, else
// device with TranscriptionUnavailable), and on every hub-mode upload the timeline's union, CompleteAt and End (from
// the complete upload's duration, or the end of the audio held when that is later, while the socket said no end). A
// note or day upload's are ignored.
func (e *Engine) putTranscriptionFields(id string, m *Meeting, u *RecordingUpload, now time.Time) {
	asked := transcriptionMode(u.transcription)
	if u.Kind != RecordingMeeting {
		return
	}
	t := m.Transcription
	if t == nil && asked != "" {
		mode := asked
		if mode == TranscriptionHub && (e.TranscriptionReady == nil || !e.TranscriptionReady()) {
			mode, u.transcriptionProblem = TranscriptionDevice, TranscriptionUnavailable
		}
		t = &MeetingTranscription{Mode: mode}
		m.Transcription = t
	}
	if !t.hub() {
		return
	}
	if u.timeline != nil {
		tl := t.timeline()
		tl.Merge(*u.timeline)
		t.Timeline = &tl
	}
	if u.Complete {
		if t.CompleteAt.IsZero() {
			t.CompleteAt = now.UTC()
		}
		if t.End == 0 && finite(u.Duration) && u.Duration > 0 {
			// The longer of the upload's and the audio held (#9587): a phone killed mid-segment uploads less.
			t.End = max(u.Duration, e.audioHeldEnd(id))
		}
	}
	t.settle(now)
	if !t.audioClosed() {
		t.Gaps = e.audioGaps(id, t.End)
	}
}

// transcriptionOf is the transcription mode in effect after an upload that asked for asked (the stored one, device
// when none) and why it is not the one asked for: problem when the upload had one (TranscriptionUnavailable when it
// decided device for want of the hub), else TranscriptionNotMeeting for hub asked of a note or a day with no mode, and
// TranscriptionUnavailable for hub asked of a recording that has another (the mode never changes mid-recording).
func transcriptionOf(m *Meeting, asked, kind, problem string) (mode, why string) {
	mode = TranscriptionDevice
	if m != nil && m.Transcription != nil && m.Transcription.Mode != "" {
		mode = m.Transcription.Mode
	}
	asked = transcriptionMode(asked)
	switch {
	case problem != "":
		return mode, problem
	case asked != TranscriptionHub || mode == TranscriptionHub:
		return mode, ""
	case kind != RecordingMeeting && (m == nil || m.Transcription == nil):
		return mode, TranscriptionNotMeeting
	}
	return mode, TranscriptionUnavailable
}

// ---- the manager's changes ----

// TranscriptionUpdate is MeetingUpdate on the transcription part: fn runs under the meeting's lock on recording
// rawID's hub-mode transcription (ErrNotHub for a recording not transcribed on the hub), and MeetingChanged is told of
// PartTranscription after it when what the apps read changed. Its mode never changes. Once the state is final or
// failed FinalAt (and FinalFrom) are set when fn did not; once FinalFrom is set the recording takes no more audio
// (ErrTranscriptionFinal). Until then the gaps follow the audio held and End, unless fn set them: as the final pass
// starts they are frozen, those it has no audio for. The transcription after it.
func (e *Engine) TranscriptionUpdate(rawID string, fn func(t *MeetingTranscription, r Recording) error) (MeetingTranscription, error) {
	id, ok := RecordingID(rawID)
	if !ok {
		return MeetingTranscription{}, ErrNotFound
	}
	now := e.now()
	m, err := e.MeetingUpdate(id, func(m *Meeting, r Recording) error {
		t := m.Transcription
		if !t.hub() {
			return ErrNotHub
		}
		open, gaps := !t.audioClosed(), slices.Clone(t.Gaps)
		if err := fn(t, r); err != nil {
			return err
		}
		t.Mode = TranscriptionHub
		t.settle(now)
		if open && sameSpans(gaps, t.Gaps) {
			t.Gaps = e.audioGaps(id, t.End)
			t.settle(now)
		}
		return nil
	})
	if err != nil {
		return MeetingTranscription{}, err
	}
	t := m.Transcription
	if !t.FinalFrom.IsZero() {
		e.Store.audioFinal(id)
	}
	return *t.clone(), nil
}

// HubRecordings are the hub-mode recordings still needing the manager, oldest first: their state not final nor
// failed, or their audio not gone. Trimmed ones need nothing (their transcript and audio went with the trim).
func (e *Engine) HubRecordings() ([]string, error) {
	var rs []*Recording
	for _, r := range e.Store.Recordings() {
		if t := r.hubTranscription(); t != nil && !r.Trimmed() && (!t.done() || t.AudioGoneAt.IsZero()) {
			rs = append(rs, r)
		}
	}
	slices.SortFunc(rs, func(a, b *Recording) int {
		if c := a.StartedAt.Compare(b.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out, nil
}

// trimTranscription is the trim's part of recording id's transcription (PruneRecordings, under recMu): a trimmed
// recording keeps its mode and state, with no gaps, problem, fallback or timeline, and its audio is gone. The parts
// that changed, for MeetingChanged once the store's locks are let go.
func (e *Engine) trimTranscription(id string, now time.Time) []string {
	st := e.Store
	st.meetMu.Lock()
	defer st.meetMu.Unlock()
	_, parts, err := e.meetingUpdateLocked(id, func(m *Meeting, _ Recording) error {
		t := m.Transcription
		if t == nil {
			return nil
		}
		t.Gaps, t.Problem, t.Fallback, t.Timeline = nil, "", false, nil
		if t.hub() && t.AudioGoneAt.IsZero() {
			t.AudioGoneAt = now.UTC()
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrRecordingDeleted) {
		e.logf("recording %s: trim transcription: %v", id, err)
	}
	return parts
}
