package recording

import (
	"cmp"
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
)

// Hub transcription (#3862 phase 5, docs/assistant/meeting-minutes.md "Phase 5: hub transcription"): a meeting uploaded
// with transcription hub streams its audio to the hub (meeting_audio.go), which stores it (kb.AudioWriter) and hands
// it to a transcription contribution's live job (meeting_transcribe_job.go). Each recording being transcribed has a
// hubRun: it holds a sentence back while Persona speaks and then marks it Persona's echo or not by the echo rule
// (kb.EchoHeld, kb.EchoOf: kb/echo.go, against the speaking spans and the answers said in them), puts every sentence in
// the hub's transcript (hub_segments.json, kb.HubTranscriptPut; Persona's echo with its mark) and the meeting's own,
// while the role at their end is not off, in the live log (kb.LiveAppendHub), sending the phone its line frames.
// Persona's echo never reaches the live log, a line frame or the room. When the hub has no
// sentences the phone's text fills the live log (meeting_transcribe_fallback.go). After the meeting the final pass
// transcribes the stored audio again (meeting_transcribe_final.go). Nothing here logs what was said.

// liveInterim is the latest caption of a meeting the hub transcribes: interim on GET …/live, while it is at most
// interimShown old (At, the manager's clock).
type liveInterim struct {
	Start float64   `json:"start"`
	Text  string    `json:"text"`
	At    time.Time `json:"-"`
}

// hubRuns are the recordings being transcribed live in this process, and their final passes.
type hubRuns struct {
	mu     sync.Mutex
	by     map[string]*hubRun
	passes map[string]*finalPassState
	kick   chan struct{}
	// wg counts the goroutines of hub transcription: the sockets, the runs' clocks and jobs, the final passes.
	wg sync.WaitGroup
}

// hubRun is one recording's live transcription. mu is held while its state changes; the knowledge base is written
// under it (never the other way round: the knowledge base's callbacks never reach a run).
type hubRun struct {
	s    *Recordings
	id   string
	quit chan struct{}
	ctx  context.Context
	end  context.CancelFunc

	mu       sync.Mutex
	e        *kb.Engine
	stopped  bool
	stopCode string // what the run ended with (a socket's fatal error code, "" for none)

	// The phone's socket (nil while down); phoneReady: it sent start and was answered ready (frames go to it only from
	// then on); started: one sent start; downSince: since when it is down (from the first hub-mode upload while none
	// ever started; a socket that never sent start counts as down); ended: the phone sent end.
	phone      *phoneSocket
	phoneReady bool
	started    bool
	downSince  time.Time
	ended      bool
	// finishing: the phone sent end and its last sentences are still coming (finishLive): the final pass waits.
	finishing bool

	// The live job (nil while none), when one may be opened again, whether the last one dropped.
	job        *liveJob
	jobRetryAt time.Time
	dropped    bool
	// edge is the live edge, in samples: the end of the latest audio given to the live job; audioAt when audio came.
	edge    int64
	audioAt time.Time

	// The timeline: meeting.json's union and what the socket said since; role is the meeting's own (before the
	// timeline's first change). openFrom is a speaking span open now (since openAt), openText the answer being said in
	// it (its speaking on's text) and openOn the socket that sent its speaking on; held the sentences waiting for it to
	// close, in order. openAssumed: it was held echoHoldMost without closing, and is assumed open since: sentences are
	// no longer held for it but judged with it (assumedSpanLocked) until a speaking off or the timeline closes it.
	timeline    kb.Timeline
	role        string
	openFrom    *float64
	openText    string
	openAt      time.Time
	openOn      *phoneSocket
	openAssumed bool
	held        []hubSentence

	// The hub's live sentences (hub_segments.json's and those since), whether hub_segments.json lags behind them,
	// when it was last written, the end of the latest one.
	hub        []kb.RecordingSegment
	hubDirty   bool
	hubPutAt   time.Time
	lastHubEnd float64

	interim    *liveInterim
	interimRev int64

	// Problems: the upstream's (upstream_unavailable, upstream_failed) since upstreamAt, no captions, a job ended by
	// a fatal error; shown is the problem the phone and meeting.json were told last.
	upstream   string
	upstreamAt time.Time
	noInterim  bool
	fatal      bool
	shown      string

	// The fallback (the phone's text fills the live log): on, the spans it filled and the end of the latest; device
	// the latest upload's segments and deviceTo the furthest end of any.
	fallback bool
	filled   [][2]float64
	filledTo float64
	device   []kb.RecordingSegment
	deviceTo float64

	// flushedTo: the job's sentences up to here are in (flushed); flushWant: the earliest at a heard ask waits for the
	// live edge to reach before it flushes (0: none); wake is closed on every change a waiter reads.
	flushedTo float64
	flushWant float64
	wake      chan struct{}
}

// hubSentence is a sentence of a transcription job, in recording seconds.
type hubSentence struct {
	Start, End float64
	Text       string
}

// The problems of hub transcription (the socket's problem frame, transcription.problem; an open set). The upstream's
// kinds (#13446): refused (a key or model the service won't take, a quota it says is gone), busy or rate-limited, out
// of reach (or no job opens), anything else.
const (
	problemUpstreamForbidden   = "upstream_forbidden"
	problemUpstreamBusy        = "upstream_busy"
	problemUpstreamUnavailable = "upstream_unavailable"
	problemUpstreamFailed      = "upstream_failed"
	problemNoInterim           = "no_interim"
)

// hubRunOf is recording id's run, nil without one.
func (s *Recordings) hubRunOf(id string) *hubRun {
	id, ok := kb.RecordingID(id)
	if !ok {
		return nil
	}
	s.hubTr.mu.Lock()
	defer s.hubTr.mu.Unlock()
	return s.hubTr.by[id]
}

// hubRunFor is recording id's run, made when there is none; nil when the recording is not transcribed on the hub or
// its live pass is over (the final pass started).
func (s *Recordings) hubRunFor(e *kb.Engine, id string) *hubRun {
	id, ok := kb.RecordingID(id)
	if !ok {
		return nil
	}
	s.hubTr.mu.Lock()
	if r := s.hubTr.by[id]; r != nil {
		s.hubTr.mu.Unlock()
		r.mu.Lock()
		r.e = e
		r.mu.Unlock()
		return r
	}
	s.hubTr.mu.Unlock()
	r := newHubRun(s, e, id)
	if r == nil {
		return nil
	}
	s.hubTr.mu.Lock()
	if old := s.hubTr.by[id]; old != nil { // made meanwhile
		s.hubTr.mu.Unlock()
		r.end()
		return old
	}
	if s.hubTr.by == nil {
		s.hubTr.by = map[string]*hubRun{}
	}
	s.hubTr.by[id] = r
	s.hubTr.mu.Unlock()
	s.hubTr.wg.Add(1)
	go func() {
		defer s.hubTr.wg.Done()
		r.loop()
	}()
	return r
}

// newHubRun is recording id's run as the knowledge base has it: its timeline, the hub's live sentences, the lines
// the phone's text filled, the standing problem. Nil when it has no live pass.
func newHubRun(s *Recordings, e *kb.Engine, id string) *hubRun {
	l, rec, err := e.LiveRead(id)
	if err != nil || !rec.HubTranscribed() || rec.Trimmed() {
		return nil
	}
	t := rec.Conv.Transcription
	if !t.FinalFrom.IsZero() || !t.AudioGoneAt.IsZero() || t.State == kb.TranscriptionFinal || t.State == kb.TranscriptionFailed {
		return nil
	}
	h, err := e.HubTranscript(id)
	if err != nil || h.Pass == kb.HubPassFinal {
		return nil
	}
	ctx, cancel := context.WithCancel(s.runCtx())
	r := &hubRun{s: s, id: id, e: e, quit: make(chan struct{}), ctx: ctx, end: cancel, role: rec.AttendanceRole(),
		hub: slices.Clone(h.Segments), wake: make(chan struct{}), shown: t.Problem, fallback: t.Fallback}
	if t.Timeline != nil {
		r.timeline.Merge(*t.Timeline)
	}
	for _, sg := range r.hub {
		r.lastHubEnd = max(r.lastHubEnd, sg.End)
	}
	switch t.Problem {
	case problemNoInterim:
		r.noInterim = true
	case problemUpstreamForbidden, problemUpstreamBusy, problemUpstreamUnavailable, problemUpstreamFailed:
		r.upstream, r.upstreamAt = t.Problem, time.Now()
	}
	for _, sg := range l.Segments {
		if sg.Source == kb.SourceDevice {
			r.filled = append(r.filled, [2]float64{sg.Start, sg.End})
			r.filledTo = max(r.filledTo, sg.End)
		}
	}
	if segs, err := e.RecordOf(id); err == nil {
		for _, sg := range segs {
			if sg.Source == kb.SourceDevice {
				r.device = append(r.device, sg)
				r.deviceTo = max(r.deviceTo, sg.End)
			}
		}
	}
	return r
}

// loop is the run's clock: spans open too long, the hub's transcript, idle jobs, the fallback.
func (r *hubRun) loop() {
	t := time.NewTicker(r.s.tm().hubTick)
	defer t.Stop()
	for {
		select {
		case <-r.quit:
			return
		case now := <-t.C:
			r.tick(now)
		}
	}
}

func (r *hubRun) tick(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	if r.openFrom != nil && !r.openAssumed && now.Sub(r.openAt) >= r.s.tm().echoHoldMost {
		r.releaseHoldLocked()
	}
	if r.hubDirty && now.Sub(r.hubPutAt) >= r.s.tm().hubSegmentsEvery {
		r.putHubLocked()
	}
	if j := r.job; j != nil && !j.ending && !r.audioAt.IsZero() && now.Sub(r.audioAt) >= r.idleLimitLocked() {
		r.s.logf("recording %s: transcription job idle (socket up: %t), ended", r.id, r.phone != nil)
		r.endJobLocked(j)
	}
	if j := r.job; j != nil && j.ready && !j.ending && j.behind && now.Sub(j.behindAt) >= r.s.tm().jobStall && now.Sub(j.ackAt) >= r.s.tm().jobStall {
		r.s.logf("recording %s: transcription job stalled (nothing acknowledged), closed", r.id)
		r.jobStalledLocked(j)
	}
	r.fallbackCheckLocked(now)
}

// idleLimitLocked is how long the live job may go without audio before it is ended: jobIdle while the phone's socket
// is up (a slow link), jobAwayIdle once it is gone without end (dropped, paused, the app ended by the system). The job's
// upstream sessions bill silence by the second, three at once in the voice extension, and a phone that died mid-meeting
// kept them billed for the whole minute (#9198); one back within jobAwayIdle keeps its job, and one back later opens a
// new job with its next audio, as after a dropped job, resending what the hub lacks from ready.from.
func (r *hubRun) idleLimitLocked() time.Duration {
	if r.phone == nil {
		return r.s.tm().jobAwayIdle
	}
	return r.s.tm().jobIdle
}

// notifyLocked wakes the waiters (hubFlush, awaitHeard).
func (r *hubRun) notifyLocked() {
	close(r.wake)
	r.wake = make(chan struct{})
}

// phoneSendLocked sends the phone's socket frame v, when one is up and was answered ready (a socket waiting for its
// start gets nothing before ready, and the standing problem right after it).
func (r *hubRun) phoneSendLocked(v any) {
	if r.phone != nil && r.phoneReady {
		r.phone.send(v)
	}
}

// roleNowLocked is the role at the live edge, for what only goes out while the meeting is attended.
func (r *hubRun) roleNowLocked() string {
	return r.timeline.RoleAt(float64(r.edge)/kb.AudioRate, r.role)
}

// ---- the phone's socket ----

// attach makes p the recording's socket; the one before it is replaced (the fatal error replaced, closed). A run
// ended meanwhile takes none: the code it ended with.
func (r *hubRun) attach(p *phoneSocket) (refused string) {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return cmp.Or(r.stopCode, codeTranscriptionFinal)
	}
	old := r.phone
	r.phone, r.phoneReady = p, false
	r.mu.Unlock()
	if old != nil && old != p {
		old.fail(codeReplaced)
	}
	return ""
}

// begin is the phone's start on socket p: it is answered ready (from, the end of the latest audio held; gaps, the
// spans missing before it; next, the live log's length, read under the run's lock so that every line appended later
// goes out after ready), its timeline joins the union, the socket is up from now (the fallback's "down" ends) and the
// standing problem goes out.
func (r *hubRun) begin(p *phoneSocket, t *kb.Timeline, from float64, gaps [][2]float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phone != p || r.stopped {
		return
	}
	l, _, _ := r.e.LiveRead(r.id)
	p.send(map[string]any{"type": "ready", "from": from, "gaps": gaps, "next": len(l.Segments)})
	r.phoneReady = true
	r.started, r.downSince, r.ended = true, time.Time{}, false
	if t != nil {
		r.mergeTimelineLocked(*t)
	}
	r.reconcileOpenLocked() // a speaking off the socket before lost
	if r.shown != "" {
		p.send(map[string]any{"type": "problem", "code": r.shown})
	}
}

// detach: socket p is over; ended says the phone sent end (else it is down from now).
func (r *hubRun) detach(p *phoneSocket, ended bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.phone != p {
		return
	}
	wasReady := r.phoneReady
	r.phone, r.phoneReady = nil, false
	r.clearInterimLocked()
	if ended {
		r.ended = true
		return
	}
	if wasReady || r.downSince.IsZero() {
		r.downSince = time.Now() // a socket that never started leaves the time it is down from as it was
	}
}

// socketOpen: a socket is up that has not sent end, or one that sent it still waits for its last sentences (the final
// pass would close that job before they come).
func (r *hubRun) socketOpen() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.phone != nil && !r.ended || r.finishing
}

// audio is audio the phone's socket stored, from recording second at: what lies past the live edge goes to the live
// job (audio behind it, a hole filled late, is only stored, for the final pass).
func (r *hubRun) audio(at float64, pcm []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	from := int64(math.Round(at * kb.AudioRate))
	to := from + int64(len(pcm)/2)
	if to <= r.edge {
		return
	}
	if from < r.edge {
		pcm, from = pcm[(r.edge-from)*2:], r.edge
	}
	r.edge, r.audioAt = to, time.Now()
	if j := r.ensureJobLocked(from); j != nil {
		j.feedLocked(from, pcm)
		if r.flushWant > 0 && float64(j.sentS)/kb.AudioRate >= r.flushWant {
			r.flushWant = 0
			r.notifyLocked() // a heard ask's audio reached the job: it flushes now (hubFlush)
		}
	}
}

// roleFrame is the socket's role frame: the role from at on.
func (r *hubRun) roleFrame(at float64, attendance string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mergeTimelineLocked(kb.Timeline{Roles: []kb.TimelineRole{{At: at, Attendance: attendance}}})
}

// speaking is socket p's speaking frame: a span opens at at, Persona saying text (sentences that end after it began
// wait), or closes. A speaking on for the span open already (a socket that started while it was open sends it again)
// makes p the socket that saw it open; one at a later time means the open span ended unseen (its speaking off lost):
// it is closed there as assumed (expireSpanLocked) and the new one opens. A speaking off closes the open span also
// after it was assumed open (releaseHoldLocked): the phone said where it ended.
func (r *hubRun) speaking(p *phoneSocket, on bool, at float64, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	if on {
		const slack = 1.0 / kb.AudioRate // times are rounded to a sample
		if r.openFrom != nil && at > *r.openFrom+slack {
			r.expireSpanLocked(*r.openFrom, at)
		}
		if r.openFrom == nil {
			r.openFrom, r.openText, r.openAt = &at, kb.SpokenText(text), time.Now()
			r.clearInterimLocked() // no caption goes out while Persona speaks
		}
		r.openOn = p
		return
	}
	if r.openFrom == nil {
		return // a span the socket never saw open: the uploads' timeline has it
	}
	from := *r.openFrom
	r.closeSpanLocked(from, max(at, from))
}

// clearOpenLocked: no speaking span is open.
func (r *hubRun) clearOpenLocked() {
	r.openFrom, r.openText, r.openOn, r.openAssumed = nil, "", nil, false
}

// closeSpanLocked: the open span is [from, to], with the answer said in it; the sentences held for it are judged now.
func (r *hubRun) closeSpanLocked(from, to float64) {
	span := kb.SpokenSpan{Start: from, End: to, Text: r.openText}
	r.clearOpenLocked()
	r.mergeTimelineLocked(kb.Timeline{Speaking: []kb.SpokenSpan{span}})
	held := r.held
	r.held = nil
	for _, s := range held {
		r.judgeLocked(s)
	}
	r.notifyLocked()
}

// expireSpanLocked: the open span counts as closed at to without the phone saying so (the phone's end, a later
// speaking on, the run ending): the sentences held for it are judged with [from, to] and the answer it carried as a
// speaking span, which is never written to the timeline. The timeline has only what the phone said, so a span whose
// speaking off a dropped socket lost (and that the next start or upload repairs) never becomes an assumed span that
// marks real speech in the final pass.
func (r *hubRun) expireSpanLocked(from, to float64) {
	span := kb.SpokenSpan{Start: from, End: max(from, to), Text: r.openText}
	r.clearOpenLocked()
	held := r.held
	r.held = nil
	for _, s := range held {
		r.judgeLocked(s, span)
	}
	r.notifyLocked()
}

// releaseHoldLocked: the open span was held echoHoldMost, longer than any answer the phone says (its speaking off is
// late or lost), so the sentences held for it go now. The span is not forgotten: it stays open, assumed open to the end
// of each sentence judged (assumedSpanLocked), the held ones and every one after them, until a speaking off (which
// still writes [from, at] to the timeline: the phone sent it) or the timeline closes it; no caption goes out
// meanwhile. Like expireSpanLocked's, the assumed span is never written to the timeline.
func (r *hubRun) releaseHoldLocked() {
	r.openAssumed = true
	held := r.held
	r.held = nil
	for _, s := range held {
		r.judgeLocked(s)
	}
	r.notifyLocked()
}

// assumedSpanLocked is the open span assumed open (releaseHoldLocked) as sentence s is judged: from its start to the
// sentence's end, with the answer it carried; false when no span is assumed open.
func (r *hubRun) assumedSpanLocked(s hubSentence) (kb.SpokenSpan, bool) {
	if r.openFrom == nil || !r.openAssumed {
		return kb.SpokenSpan{}, false
	}
	from := *r.openFrom
	return kb.SpokenSpan{Start: from, End: max(from, s.End), Text: r.openText}, true
}

// reconcileOpenLocked closes the open speaking span once the timeline has it closed: the speaking off a dropped
// socket lost comes back in the next socket's start.timeline or an upload's timeline (a timeline has only spans that
// ended; a span still open when a socket starts is not in its start, the phone sends speaking on after it):
// kb.EchoSpanClosed.
func (r *hubRun) reconcileOpenLocked() {
	if r.openFrom == nil {
		return
	}
	from := *r.openFrom
	if to, ok := kb.EchoSpanClosed(r.timeline, from); ok {
		r.closeSpanLocked(from, to)
	}
}

// openSeenLocked: the socket that saw the open speaking span open (sent its speaking on) is still up, so its own
// speaking off will come: an upload's timeline does not close the span before it (hubUploaded).
func (r *hubRun) openSeenLocked() bool {
	return r.openFrom != nil && r.openOn != nil && r.phone == r.openOn
}

// mergeTimelineLocked adds t to the run's timeline and meeting.json's.
func (r *hubRun) mergeTimelineLocked(t kb.Timeline) {
	if !r.timeline.Merge(t) {
		return
	}
	if _, err := r.e.TranscriptionUpdate(r.id, func(m *kb.MeetingTranscription, _ kb.Recording) error {
		var tl kb.Timeline
		if m.Timeline != nil {
			tl = *m.Timeline
		}
		tl.Merge(t)
		m.Timeline = &tl
		return nil
	}); err != nil {
		r.s.logf("recording %s: timeline: %v", r.id, err)
	}
}

// ---- the sentences ----

// sentenceLocked is a sentence of the live job: held while a speaking span that it may be Persona's echo of is open
// and not yet assumed open (and behind one held, so the live log stays in order), else judged at once.
func (r *hubRun) sentenceLocked(s hubSentence) {
	if len(r.held) > 0 || r.openFrom != nil && !r.openAssumed && kb.EchoHeld(s.End, *r.openFrom) {
		r.held = append(r.held, s)
		return
	}
	r.judgeLocked(s)
}

// judgeLocked takes sentence s, marked Persona's echo when the echo rule says so against the timeline's speaking spans,
// extra (a span assumed closed, expireSpanLocked) and the span assumed open, if any (assumedSpanLocked).
func (r *hubRun) judgeLocked(s hubSentence, extra ...kb.SpokenSpan) {
	if sp, ok := r.assumedSpanLocked(s); ok {
		extra = append(slices.Clone(extra), sp)
	}
	r.acceptLocked(s, kb.EchoOf(r.timeline, s.Start, max(s.End, s.Start), s.Text, extra...))
}

// acceptLocked takes sentence s into the hub's transcript (echo: marked Persona's echo) and, unless it is Persona's
// echo, while the role at its end is not off and the phone's text did not fill its time, into the live log, with its
// line frame. The hub's sentences coming again (its echo too: the upstream is back) end a problem and the fallback.
func (r *hubRun) acceptLocked(s hubSentence, echo bool) {
	text := strings.TrimSpace(s.Text)
	if text == "" {
		return
	}
	if s.End < s.Start {
		s.End = s.Start
	}
	r.hub = append(r.hub, kb.RecordingSegment{Start: s.Start, End: s.End, Text: text, Source: kb.SourceHub, Echo: echo})
	r.hubDirty = true
	r.lastHubEnd = max(r.lastHubEnd, s.End)
	if r.upstream != "" || r.fatal {
		r.upstream, r.fatal = "", false
		r.tellProblemLocked()
	}
	if r.fallback {
		r.setFallbackLocked(false)
	}
	defer r.notifyLocked()
	if echo || r.timeline.RoleAt(s.End, r.role) == kb.AttendanceOff {
		return // in the hub's transcript only (Persona's echo marked there)
	}
	if kb.CoveredShare(s.Start, s.End, r.filled) > 0.5 {
		return // the phone's text said it already
	}
	n, err := r.e.LiveAppendHub(r.id, []kb.LiveSegment{{Start: s.Start, End: s.End, Text: text}})
	if err != nil {
		if !errors.Is(err, kb.ErrLiveFull) {
			r.s.logf("recording %s: live line: %v", r.id, err)
		}
		return
	}
	r.phoneSendLocked(map[string]any{"type": "line", "n": n, "start": s.Start, "end": s.End, "text": text})
}

// interimLocked is a caption of the live job: sent to the phone whatever the role, none while Persona speaks.
func (r *hubRun) interimLocked(start float64, text string) {
	if r.noInterim {
		r.noInterim = false // captions came
		r.tellProblemLocked()
	}
	if r.openFrom != nil {
		return
	}
	now := time.Now()
	r.interim = &liveInterim{Start: start, Text: text, At: now}
	r.interimRev = max(now.UnixMilli(), r.interimRev+1)
	r.phoneSendLocked(map[string]any{"type": "interim", "start": start, "text": text})
	if r.roleNowLocked() != kb.AttendanceOff {
		r.s.meetingLiveChanged(r.id, []string{kb.PartInterim})
	}
}

// clearInterimLocked: no caption stands (a span opened, the socket went).
func (r *hubRun) clearInterimLocked() {
	if r.interim == nil {
		return
	}
	r.interim = nil
	r.interimRev++
	r.s.meetingLiveChanged(r.id, []string{kb.PartInterim})
}

// putHubLocked writes the hub's live sentences to hub_segments.json (with transcript.md).
func (r *hubRun) putHubLocked() {
	if !r.hubDirty {
		return
	}
	r.hubPutAt = time.Now()
	if _, err := r.e.HubTranscriptPut(r.id, kb.HubPassLive, r.hub); err != nil {
		switch {
		case errors.Is(err, kb.ErrReadOnly):
			return // kept until it can be written
		case errors.Is(err, kb.ErrTranscriptionFinal), errors.Is(err, kb.ErrNotFound), errors.Is(err, kb.ErrNotHub):
		default:
			r.s.logf("recording %s: hub transcript: %v", r.id, err)
			return
		}
	}
	r.hubDirty = false
}

// ---- problems ----

// problemLocked is the problem that stands: the upstream's first, else no captions.
func (r *hubRun) problemLocked() string {
	switch {
	case r.upstream != "":
		return r.upstream
	case r.noInterim:
		return problemNoInterim
	}
	return ""
}

// setUpstreamLocked: the upstream's problem is p.
func (r *hubRun) setUpstreamLocked(p string) {
	if r.upstream != p {
		r.upstream, r.upstreamAt = p, time.Now()
	}
	r.tellProblemLocked()
}

// tellProblemLocked tells the phone and meeting.json the problem that stands, when it changed.
func (r *hubRun) tellProblemLocked() {
	p := r.problemLocked()
	if p == r.shown {
		return
	}
	r.shown = p
	var code any
	if p != "" {
		code = p
	}
	r.phoneSendLocked(map[string]any{"type": "problem", "code": code})
	if _, err := r.e.TranscriptionUpdate(r.id, func(t *kb.MeetingTranscription, _ kb.Recording) error {
		t.Problem = p
		return nil
	}); err != nil && !errors.Is(err, kb.ErrNotFound) && !errors.Is(err, kb.ErrRecordingDeleted) {
		r.s.logf("recording %s: transcription problem: %v", r.id, err)
	}
}

// ---- the end of a run ----

// stop ends the run: the phone's socket gets code (none: closed as at its end), the job is closed, the sentences held
// are judged and the hub's transcript is written (not after a delete or trim).
func (r *hubRun) stop(code string) {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	gone := code == codeRecordingDeleted || code == codeTranscriptExpired
	if r.openFrom != nil && !gone {
		from := *r.openFrom
		r.expireSpanLocked(from, float64(r.edge)/kb.AudioRate)
	}
	if !gone {
		r.putHubLocked()
	}
	r.stopped, r.stopCode = true, code
	phone, job := r.phone, r.job
	r.phone, r.phoneReady, r.job = nil, false, nil
	r.notifyLocked()
	r.mu.Unlock()
	close(r.quit)
	if job != nil {
		job.close()
	}
	if phone != nil {
		if code == "" {
			phone.closeWith(1000)
		} else {
			phone.fail(code)
		}
	}
	r.end()
	r.s.hubTr.mu.Lock()
	if r.s.hubTr.by[r.id] == r {
		delete(r.s.hubTr.by, r.id)
	}
	r.s.hubTr.mu.Unlock()
}

// hubStop ends recording id's run, when it has one, telling its socket code; a recording deleted or trimmed ends its
// final pass too.
func (s *Recordings) hubStop(id, code string) {
	if r := s.hubRunOf(id); r != nil {
		r.stop(code)
	}
	if code == codeRecordingDeleted || code == codeTranscriptExpired {
		s.finalCancel(id)
	}
}

// hubSweep ends the runs of recordings deleted, trimmed or past their live pass, and the final passes of recordings
// deleted or trimmed (each retention pass, each final round).
func (s *Recordings) hubSweep(e *kb.Engine) {
	s.hubTr.mu.Lock()
	runs := make([]*hubRun, 0, len(s.hubTr.by))
	for _, r := range s.hubTr.by {
		runs = append(runs, r)
	}
	var passes []string
	for id, st := range s.hubTr.passes {
		if st.running {
			passes = append(passes, id)
		}
	}
	s.hubTr.mu.Unlock()
	for _, id := range passes {
		if _, rec, err := e.MeetingRead(id); errors.Is(err, kb.ErrRecordingDeleted) || errors.Is(err, kb.ErrNotFound) || err == nil && rec.Trimmed() {
			s.finalCancel(id)
		}
	}
	for _, r := range runs {
		_, rec, err := e.MeetingRead(r.id)
		switch {
		case errors.Is(err, kb.ErrRecordingDeleted), errors.Is(err, kb.ErrNotFound):
			r.stop(codeRecordingDeleted)
		case err != nil:
		case rec.Trimmed():
			r.stop(codeTranscriptExpired)
		case !rec.HubTranscribed():
			r.stop(codeNotHub)
		case !rec.Conv.Transcription.FinalFrom.IsZero():
			r.stop(codeTranscriptionFinal)
		}
	}
}

// ---- what the asks and the live view read ----

// hubInterim is recording id's latest caption for GET …/live: while the socket is up, the role at it is not off and
// it is at most interimShown old; nil otherwise.
func (s *Recordings) hubInterim(id string) *liveInterim {
	r := s.hubRunOf(id)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	in := r.interim
	if in == nil || !r.phoneReady || r.ended || time.Since(in.At) > s.tm().interimShown ||
		r.timeline.RoleAt(in.Start, r.role) == kb.AttendanceOff {
		return nil
	}
	c := *in
	return &c
}

// hubInterimRev is the revision of recording id's caption (its time in milliseconds), 0 without one.
func (s *Recordings) hubInterimRev(id string) int64 {
	r := s.hubRunOf(id)
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.interimRev
}

// hubFlush asks the live job of recording id for its sentences up to recording second at; true once every sentence
// that ends before at is in the live log (none held back for a speaking span), false when ctx ended first or no job
// is transcribing it.
func (s *Recordings) hubFlush(ctx context.Context, id string, at float64) bool {
	r := s.hubRunOf(id)
	if r == nil {
		return false
	}
	for {
		r.mu.Lock()
		if r.flushedTo >= at && !slices.ContainsFunc(r.held, func(h hubSentence) bool { return h.End <= at }) {
			r.mu.Unlock()
			return true
		}
		j := r.job
		if j == nil || r.stopped {
			r.mu.Unlock()
			return false
		}
		switch {
		case !j.ready:
		case float64(j.sentS)/kb.AudioRate < at: // the ask came before its audio: woken once the audio reaches it
			if r.flushWant == 0 || at < r.flushWant {
				r.flushWant = at
			}
		case !j.flushAsked:
			j.flushLocked()
		}
		wake := r.wake
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-wake:
		}
	}
}

// awaitHeard is recording id's live log for heard ask a: in device mode once it holds a.LiveNext positions
// (awaitLive); in hub mode, whatever live_next says, once the hub's sentences up to a.At are in (hubFlush), or in the
// fallback once an upload's segments reach it, waiting at most askLiveWait either way.
func (s *Recordings) awaitHeard(ctx context.Context, e *kb.Engine, id string, a kb.LiveAsk) (kb.Live, error) {
	l, rec, err := e.LiveRead(id)
	if err != nil {
		return l, err
	}
	if !rec.HubTranscribed() || a.At == nil {
		return awaitLive(ctx, s.tm(), e, id, a.LiveNext)
	}
	if r := s.hubRunOf(id); r != nil {
		wctx, cancel := context.WithTimeout(ctx, s.tm().askLiveWait)
		if r.inFallback() {
			r.awaitDevice(wctx, *a.At)
		} else {
			s.hubFlush(wctx, id, *a.At)
		}
		cancel()
	}
	if err := ctx.Err(); err != nil {
		return l, err
	}
	l, _, err = e.LiveRead(id)
	return l, err
}

// kickFinal has the final passes looked at now.
func (s *Recordings) kickFinal() {
	s.hubTr.mu.Lock()
	if s.hubTr.kick == nil {
		s.hubTr.kick = make(chan struct{}, 1)
	}
	k := s.hubTr.kick
	s.hubTr.mu.Unlock()
	select {
	case k <- struct{}{}:
	default:
	}
}

// transcriptionGlossary is what a job's start carries as its glossary: the meeting's title, its calendar title and
// attendees, then meeting_glossary; one line each, repeats (case-insensitive) and blanks dropped, terms over
// glossaryTermMost characters left out, at most glossaryMost. Data: a contribution uses it for spelling only.
func (s *Recordings) transcriptionGlossary(ctx context.Context, rec kb.Recording) []string {
	terms := []string{rec.Title}
	if c := rec.Calendar; c != nil {
		terms = append(terms, c.Title)
		terms = append(terms, c.Attendees...)
	}
	terms = append(terms, s.h.MeetingGlossary(ctx)...)
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		t = strings.Join(strings.Fields(t), " ")
		if t == "" || len([]rune(t)) > glossaryTermMost ||
			slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, t) }) {
			continue
		}
		out = append(out, t)
		if len(out) == glossaryMost {
			break
		}
	}
	return out
}

// The glossary a job's start carries at most.
const (
	glossaryMost     = 256
	glossaryTermMost = 80
)

// transcriptionJobsCheck ends the live jobs of a contribution no longer chosen (meeting_transcription_extension
// changed, or the extension named was uninstalled): no more of the meeting's audio goes to it, and the next audio opens
// a job of the contribution chosen now, or none when it is not ready (upstream_unavailable, the phone's text fills).
func (s *Recordings) transcriptionJobsCheck(ctx context.Context) {
	_, choice, err := s.transcriptionTarget(ctx)
	s.hubTr.mu.Lock()
	runs := make([]*hubRun, 0, len(s.hubTr.by))
	for _, r := range s.hubTr.by {
		runs = append(runs, r)
	}
	s.hubTr.mu.Unlock()
	for _, r := range runs {
		r.mu.Lock()
		if j := r.job; j != nil && !j.ending && j.choice != "" && (j.choice != choice || err != nil) {
			r.s.logf("recording %s: transcription job of a contribution no longer chosen, ended", r.id)
			r.endJobLocked(j)
		}
		r.mu.Unlock()
	}
}

// recordingLanguage is the recording's language tag: the job's hint, never a lock (every word is written in the
// language it was said in).
func recordingLanguage(rec kb.Recording) string { return strings.TrimSpace(rec.Language) }
