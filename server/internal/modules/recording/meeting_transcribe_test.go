package recording

import (
	"slices"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/kb"
)

// While Persona speaks a sentence that may be its echo waits (and those after it, in order); when the span closes
// Persona's echo (its time and its words: the answer said) is marked in the hub's transcript and goes nowhere else, and
// the rest is kept; no caption goes out meanwhile.
func TestHubEchoHoldBack(t *testing.T) {
	h := newHubFixture(t)
	h.tr.set(func(f *fakeTranscriber) { f.live = jobScript{lines: mixedLines, interim: true} })
	h.hubUpload(meetingRec, 1, nil)
	p, _ := h.open(meetingRec, 0, nil)
	p.audio(1.6)
	p.wait("line", 1)
	p.send(map[string]any{"type": "speaking", "on": true, "at": 1.8, "text": "Sure. " + mixedLines[1].text})
	p.audio(3)
	waitFor(t, "both sentences held", func() bool {
		r := h.s.hubRunOf(meetingRec)
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.held) == 2
	})
	if len(p.of("line")) != 1 || len(p.of("interim")) != 1 {
		t.Fatalf("frames while Persona speaks: %v", p.frames)
	}
	p.send(map[string]any{"type": "speaking", "on": false, "at": 3.2})
	p.wait("line", 2)
	if got := p.texts(); !slices.Equal(got, []string{mixedLines[0].text, mixedLines[2].text}) {
		t.Fatalf("lines %q", got)
	}
	if got := lineTexts(h.lines(meetingRec)); !slices.Equal(got, []string{mixedLines[0].text, mixedLines[2].text}) {
		t.Fatalf("live log %q", got)
	}
	waitFor(t, "the hub's transcript", func() bool { return len(h.hubTexts(meetingRec)) == 3 })
	if got := h.hubEcho(meetingRec); !slices.Equal(got, []bool{false, true, false}) {
		t.Fatalf("the hub's transcript's echo marks %v (%q)", got, h.hubTexts(meetingRec))
	}
	m, _, _ := h.engine().MeetingRead(meetingRec)
	if tl := m.Transcription.Timeline; tl == nil || len(tl.Speaking) != 1 ||
		tl.Speaking[0] != (kb.SpokenSpan{Start: 1.8, End: 3.2, Text: "Sure. " + mixedLines[1].text}) {
		t.Fatalf("the timeline: %+v", m.Transcription.Timeline)
	}
}

// runHas says whether recording id's run, read under its lock, holds ok.
func (h *hubFixture) runHas(id string, ok func(r *hubRun) bool) bool {
	r := h.s.hubRunOf(id)
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return ok(r)
}

// A span the phone does not close within echoHoldMost is assumed open from then on: the sentences held for it go,
// judged with the answer its speaking on carried.
func TestHubEchoSpanTimesOut(t *testing.T) {
	h := newHubFixture(t)
	h.tr.set(func(f *fakeTranscriber) { f.live = jobScript{lines: mixedLines[:2]} })
	h.hubUpload(meetingRec, 1, nil)
	p, _ := h.open(meetingRec, 0, nil)
	recTimings(h.s, map[string]time.Duration{"echoHoldMost": 200 * time.Millisecond}, nil)
	p.send(map[string]any{"type": "speaking", "on": true, "at": 1.9, "text": mixedLines[1].text})
	p.audio(3.2)
	p.wait("line", 1)
	waitFor(t, "the hold released", func() bool {
		return h.runHas(meetingRec, func(r *hubRun) bool { return r.openAssumed && len(r.held) == 0 && len(r.hub) == 2 })
	})
	if got := p.texts(); len(got) != 1 {
		t.Fatalf("lines %q", got) // [2, 3] overlaps the span open since 1.9 and says the answer: echo
	}
	waitFor(t, "the hub's transcript", func() bool { return len(h.hubTexts(meetingRec)) == 2 })
	if got := h.hubEcho(meetingRec); !slices.Equal(got, []bool{false, true}) {
		t.Fatalf("echo marks %v", got)
	}
	// The span assumed for the hold is not the phone's: meeting.json's timeline (the final pass's) never has it.
	m, _, _ := h.engine().MeetingRead(meetingRec)
	if tl := m.Transcription.Timeline; tl != nil && len(tl.Speaking) > 0 {
		t.Fatalf("an assumed span in the timeline: %+v", tl.Speaking)
	}
}

// An answer longer than echoHoldMost: once the hold is released the span stays assumed open, so a sentence of the
// answer after it is still Persona's echo (no line, no caption), and the late speaking off closes it, written to the
// timeline as the phone sent it.
func TestHubEchoSpanLongerThanTheHold(t *testing.T) {
	h := newHubFixture(t)
	h.tr.set(func(f *fakeTranscriber) { f.live = jobScript{lines: longAnswer, interim: true} })
	h.hubUpload(meetingRec, 1, nil)
	p, _ := h.open(meetingRec, 0, nil)
	p.audio(1.6)
	p.wait("line", 1)
	recTimings(h.s, map[string]time.Duration{"echoHoldMost": 200 * time.Millisecond}, nil)
	answer := longAnswer[1].text + " " + longAnswer[2].text
	p.send(map[string]any{"type": "speaking", "on": true, "at": 1.9, "text": answer})
	p.audio(1.6)
	waitFor(t, "the hold released", func() bool {
		return h.runHas(meetingRec, func(r *hubRun) bool { return len(r.held) == 0 && len(r.hub) == 2 })
	})
	p.audio(1.5) // the rest of the answer, said after the hold
	waitFor(t, "the answer's second sentence", func() bool {
		return h.runHas(meetingRec, func(r *hubRun) bool { return len(r.hub) == 3 })
	})
	p.send(map[string]any{"type": "speaking", "on": false, "at": 4.8})
	p.audio(2)
	waitFor(t, "the last sentence's line", func() bool {
		lines := p.of("line")
		return len(lines) > 0 && lines[len(lines)-1]["text"] == longAnswer[3].text
	})
	if got := p.texts(); !slices.Equal(got, []string{longAnswer[0].text, longAnswer[3].text}) {
		t.Fatalf("lines %q", got)
	}
	if got := lineTexts(h.lines(meetingRec)); !slices.Equal(got, []string{longAnswer[0].text, longAnswer[3].text}) {
		t.Fatalf("live log %q", got)
	}
	if n := len(p.of("interim")); n != 2 { // the first and the last sentence's: none while Persona speaks
		t.Fatalf("%d interim frames: %v", n, p.of("interim"))
	}
	waitFor(t, "the hub's transcript", func() bool { return len(h.hubTexts(meetingRec)) == 4 })
	if got := h.hubEcho(meetingRec); !slices.Equal(got, []bool{false, true, true, false}) {
		t.Fatalf("echo marks %v", got)
	}
	m, _, _ := h.engine().MeetingRead(meetingRec)
	if tl := m.Transcription.Timeline; tl == nil || !slices.Equal(tl.Speaking, []kb.SpokenSpan{{Start: 1.9, End: 4.8, Text: answer}}) {
		t.Fatalf("the timeline: %+v", m.Transcription.Timeline)
	}
	if h.runHas(meetingRec, func(r *hubRun) bool { return r.openFrom != nil }) {
		t.Fatal("the span is still open after its speaking off")
	}
}

// A speaking on at a later time while a span is assumed open: the span before ended unseen (its speaking off lost) and
// the new answer is what the sentences after it are judged against; neither assumed end is written to the timeline.
func TestHubEchoNextAnswerEndsAnAssumedSpan(t *testing.T) {
	h := newHubFixture(t)
	lines := []fakeLine{longAnswer[0], longAnswer[1], {4.0, 5.0, longAnswer[2].text}, {6.0, 7.0, longAnswer[3].text}}
	h.tr.set(func(f *fakeTranscriber) { f.live = jobScript{lines: lines} })
	h.hubUpload(meetingRec, 1, nil)
	p, _ := h.open(meetingRec, 0, nil)
	p.audio(1.6)
	p.wait("line", 1)
	recTimings(h.s, map[string]time.Duration{"echoHoldMost": 200 * time.Millisecond}, nil)
	p.send(map[string]any{"type": "speaking", "on": true, "at": 1.9, "text": lines[1].text})
	p.audio(1.6)
	waitFor(t, "the hold released", func() bool {
		return h.runHas(meetingRec, func(r *hubRun) bool { return len(r.held) == 0 && len(r.hub) == 2 })
	})
	p.send(map[string]any{"type": "speaking", "on": true, "at": 3.8, "text": lines[2].text}) // the off at 3.2 lost
	p.audio(2)
	p.send(map[string]any{"type": "speaking", "on": false, "at": 5.1})
	p.audio(2)
	waitFor(t, "the last sentence's line", func() bool {
		lines := p.of("line")
		return len(lines) > 0 && lines[len(lines)-1]["text"] == longAnswer[3].text
	})
	if got := p.texts(); !slices.Equal(got, []string{lines[0].text, lines[3].text}) {
		t.Fatalf("lines %q", got)
	}
	waitFor(t, "the hub's transcript", func() bool { return len(h.hubTexts(meetingRec)) == 4 })
	if got := h.hubEcho(meetingRec); !slices.Equal(got, []bool{false, true, true, false}) {
		t.Fatalf("echo marks %v", got)
	}
	m, _, _ := h.engine().MeetingRead(meetingRec)
	if tl := m.Transcription.Timeline; tl == nil || !slices.Equal(tl.Speaking, []kb.SpokenSpan{{Start: 3.8, End: 5.1, Text: lines[2].text}}) {
		t.Fatalf("the timeline: %+v", m.Transcription.Timeline)
	}
}

// A speaking off lost with a dropped socket: the next socket's start.timeline (or an upload's timeline while no socket
// that saw the span open is up) closes the open span, and the sentences after it go out at once instead of waiting
// echoHoldMost; the timeline keeps the phone's span.
func TestHubEchoSpanRepairedByTheTimeline(t *testing.T) {
	h := newHubFixture(t)
	h.tr.set(func(f *fakeTranscriber) { f.live = jobScript{lines: mixedLines} })
	h.hubUpload(meetingRec, 1, nil)
	p, _ := h.open(meetingRec, 0, nil)
	p.audio(1.6)
	p.wait("line", 1)
	p.send(map[string]any{"type": "speaking", "on": true, "at": 1.65, "text": "Noted."})
	waitFor(t, "the span open", func() bool {
		r := h.s.hubRunOf(meetingRec)
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.openFrom != nil
	})
	_ = p.c.CloseNow() // the speaking off at 1.7 is lost with it
	waitFor(t, "the socket gone", func() bool { return !h.s.hubRunOf(meetingRec).socketOpen() })
	back, ready := h.open(meetingRec, 0, map[string]any{"speaking": []map[string]any{{"start": 1.65, "end": 1.7, "text": "Noted."}}})
	if ready["from"] != 1.6 {
		t.Fatalf("ready %v", ready)
	}
	back.audio(3)
	back.wait("line", 2)
	if got := back.texts(); !slices.Equal(got, []string{mixedLines[1].text, mixedLines[2].text}) {
		t.Fatalf("lines %q", got)
	}
	m, _, _ := h.engine().MeetingRead(meetingRec)
	if tl := m.Transcription.Timeline; tl == nil || len(tl.Speaking) != 1 || tl.Speaking[0] != (kb.SpokenSpan{Start: 1.65, End: 1.7, Text: "Noted."}) {
		t.Fatalf("the timeline: %+v", m.Transcription.Timeline)
	}

	// An upload's timeline repairs it too, once the socket that saw it open is gone.
	back.send(map[string]any{"type": "speaking", "on": true, "at": 4.7, "text": "Again."})
	waitFor(t, "the span open", func() bool {
		r := h.s.hubRunOf(meetingRec)
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.openFrom != nil
	})
	_ = back.c.CloseNow() // the speaking off at 4.8 is lost with it
	waitFor(t, "the socket gone", func() bool { return !h.s.hubRunOf(meetingRec).socketOpen() })
	h.hubUpload(meetingRec, 2, map[string]any{"timeline": map[string]any{"speaking": []map[string]any{
		{"start": 1.65, "end": 1.7, "text": "Noted."}, {"start": 4.7, "end": 4.8, "text": "Again."}}}})
	r := h.s.hubRunOf(meetingRec)
	r.mu.Lock()
	open := r.openFrom
	r.mu.Unlock()
	if open != nil {
		t.Fatalf("still open from %v after the upload closed it", *open)
	}
}

// An upload while the socket that saw the span open is up does not close it, whatever its timeline says (a timeline
// has only spans that ended; a phone that put the span being said there, ended at the upload's time, would cut it
// short): the sentences of the answer after that time stay Persona's echo, and the socket's own speaking off closes it.
func TestHubEchoSpanNotClosedByAnUploadWhileItsSocketIsUp(t *testing.T) {
	h := newHubFixture(t)
	h.tr.set(func(f *fakeTranscriber) { f.live = jobScript{lines: longAnswer} })
	h.hubUpload(meetingRec, 1, nil)
	p, _ := h.open(meetingRec, 0, nil)
	p.audio(1.6)
	p.wait("line", 1)
	answer := longAnswer[1].text + " " + longAnswer[2].text
	p.send(map[string]any{"type": "speaking", "on": true, "at": 1.8, "text": answer})
	p.audio(1.6)
	waitFor(t, "the first sentence of the answer held", func() bool {
		return h.runHas(meetingRec, func(r *hubRun) bool { return len(r.held) == 1 })
	})
	h.hubUpload(meetingRec, 2, map[string]any{"timeline": map[string]any{"speaking": []map[string]any{
		{"start": 1.8, "end": 2.5, "text": answer}}}})
	if !h.runHas(meetingRec, func(r *hubRun) bool { return r.openFrom != nil }) {
		t.Fatal("the upload closed the span its socket still has open")
	}
	p.audio(1.5)
	p.send(map[string]any{"type": "speaking", "on": false, "at": 4.8})
	p.audio(2)
	waitFor(t, "the last sentence's line", func() bool {
		lines := p.of("line")
		return len(lines) > 0 && lines[len(lines)-1]["text"] == longAnswer[3].text
	})
	if got := p.texts(); !slices.Equal(got, []string{longAnswer[0].text, longAnswer[3].text}) {
		t.Fatalf("lines %q", got)
	}
	waitFor(t, "the hub's transcript", func() bool { return len(h.hubTexts(meetingRec)) == 4 })
	if got := h.hubEcho(meetingRec); !slices.Equal(got, []bool{false, true, true, false}) {
		t.Fatalf("echo marks %v", got)
	}
	m, _, _ := h.engine().MeetingRead(meetingRec)
	if tl := m.Transcription.Timeline; tl == nil || !slices.Equal(tl.Speaking, []kb.SpokenSpan{{Start: 1.8, End: 4.8, Text: answer}}) {
		t.Fatalf("the timeline: %+v", m.Transcription.Timeline)
	}
}

// A span still open when the phone sends end counts as closed at the end: the sentences held for it are judged with
// [from, end] and the ones that are not Persona's echo go out as line frames before the socket closes.
func TestHubEchoSpanOpenAtTheEnd(t *testing.T) {
	h := newHubFixture(t)
	lines := []fakeLine{longAnswer[0], longAnswer[1], longAnswer[3]}
	h.tr.set(func(f *fakeTranscriber) { f.live = jobScript{lines: lines} })
	h.hubUpload(meetingRec, 1, nil)
	p, _ := h.open(meetingRec, 0, nil)
	p.audio(1.6)
	p.wait("line", 1)
	p.send(map[string]any{"type": "speaking", "on": true, "at": 1.9, "text": lines[1].text})
	p.audio(5.4)
	waitFor(t, "both sentences held", func() bool {
		return h.runHas(meetingRec, func(r *hubRun) bool { return len(r.held) == 2 })
	})
	p.send(map[string]any{"type": "end", "at": 7.0})
	if got := p.waitClosed(); got != websocket.StatusNormalClosure {
		t.Fatalf("after end: %v", got)
	}
	if got := p.texts(); !slices.Equal(got, []string{lines[0].text, lines[2].text}) {
		t.Fatalf("lines before the close %q", got)
	}
	if !h.runHas(meetingRec, func(r *hubRun) bool { return len(r.held) == 0 && r.openFrom == nil }) {
		t.Fatal("sentences still held after the end")
	}
	if got := h.hubEcho(meetingRec); !slices.Equal(got, []bool{false, true, false}) {
		t.Fatalf("echo marks %v (%q)", got, h.hubTexts(meetingRec))
	}
	// The end assumed for the span is not the phone's: the timeline never has it.
	m, _, _ := h.engine().MeetingRead(meetingRec)
	if tl := m.Transcription.Timeline; tl != nil && len(tl.Speaking) > 0 {
		t.Fatalf("an assumed span in the timeline: %+v", tl.Speaking)
	}
}

// A dropped job socket is opened again at once from the live edge; a job with no audio for jobIdle is ended.
func TestHubJobReopenedAndIdle(t *testing.T) {
	h := newHubFixture(t)
	h.tr.set(func(f *fakeTranscriber) { f.live = jobScript{lines: mixedLines, dropAt: 2.5} })
	h.hubUpload(meetingRec, 1, nil)
	p, _ := h.open(meetingRec, 0, nil)
	p.audio(2.5)
	p.wait("line", 1)
	waitFor(t, "the job dropped", func() bool {
		r := h.s.hubRunOf(meetingRec)
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.job == nil && r.dropped
	})
	h.tr.set(func(f *fakeTranscriber) { f.live = jobScript{lines: []fakeLine{{3.0, 3.5, "Back again."}}} })
	recTimings(h.s, map[string]time.Duration{"jobIdle": 300 * time.Millisecond}, nil)
	p.audio(1)
	p.wait("line", 2)
	jobs := h.tr.jobsOf(extwire.TranscriptionLive)
	if len(jobs) != 2 || *jobs[1].start.At < 2.5 {
		t.Fatalf("jobs: %d, the second from %v", len(jobs), jobs[len(jobs)-1].start.At)
	}
	waitFor(t, "the idle job ended", func() bool {
		_, _, _, ended := jobs[1].snapshot()
		return ended
	})
	p.audio(0.5) // audio again: a job again
	waitFor(t, "a third job", func() bool { return len(h.tr.jobsOf(extwire.TranscriptionLive)) == 3 })
	if f := p.of("problem"); len(f) != 0 {
		t.Fatalf("a drop reopened at once is no problem: %v", f)
	}
}

// The phone's socket gone without end (the app ended by the system, a pause, a drop): the live job, whose upstream
// sessions bill silence, is ended once it has had no audio for jobAwayIdle, not the minute a socket that is up gets
// (#9198). A socket that stays up keeps its job past jobAwayIdle; the phone back on a new socket opens a new job with
// its next audio.
func TestHubJobEndedSoonOnceThePhoneIsGone(t *testing.T) {
	h := newHubFixture(t)
	h.tr.set(func(f *fakeTranscriber) { f.live = jobScript{lines: mixedLines[:1]} })
	h.hubUpload(meetingRec, 1, nil)
	recTimings(h.s, map[string]time.Duration{"jobAwayIdle": 300 * time.Millisecond}, nil) // jobIdle stays a minute
	p, _ := h.open(meetingRec, 0, nil)
	p.audio(2)
	p.wait("line", 1)
	jobs := h.tr.jobsOf(extwire.TranscriptionLive)
	if len(jobs) != 1 {
		t.Fatalf("live jobs: %d", len(jobs))
	}
	// Up and quiet, past jobAwayIdle (the run's clock moved on by hand): the job stays.
	r := h.s.hubRunOf(meetingRec)
	r.mu.Lock()
	quiet := r.audioAt.Add(4 * h.s.tmNow().jobAwayIdle)
	r.mu.Unlock()
	r.tick(quiet)
	r.mu.Lock()
	kept := r.job != nil && !r.job.ending
	r.mu.Unlock()
	if !kept {
		t.Fatal("the job ended while the phone's socket was up")
	}
	// Gone without end: ended within jobAwayIdle (and its tick), long before jobIdle.
	_ = p.c.CloseNow()
	gone := time.Now()
	waitFor(t, "the job ended", func() bool {
		_, _, _, ended := jobs[0].snapshot()
		return ended
	})
	if took := time.Since(gone); took >= 3*time.Second {
		t.Fatalf("the job ended %v after the phone's socket went", took)
	}
	// Back: the new socket's audio opens a new job, from the live edge.
	h.tr.set(func(f *fakeTranscriber) { f.live = jobScript{lines: []fakeLine{{2.5, 3.0, "Back again."}}} })
	p2, ready := h.open(meetingRec, 2, nil)
	if from, _ := ready["from"].(float64); from != 2 {
		t.Fatalf("ready from %v, want 2", ready["from"])
	}
	p2.audio(1.5)
	p2.wait("line", 1)
	jobs = h.tr.jobsOf(extwire.TranscriptionLive)
	if len(jobs) != 2 || *jobs[1].start.At < 2 {
		t.Fatalf("jobs: %d, the second from %v", len(jobs), jobs[len(jobs)-1].start.At)
	}
}

// attachedPhone waits until recording id's run has a socket other than before (a new one attached, not started).
func (h *hubFixture) attachedPhone(id string, before *phoneSocket) *phoneSocket {
	h.t.Helper()
	var p *phoneSocket
	waitFor(h.t, "the socket attached", func() bool {
		r := h.s.hubRunOf(id)
		if r == nil {
			return false
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		p = r.phone
		return p != nil && p != before
	})
	return p
}

// A socket gets nothing before ready: a problem that changes while it waits for its start goes out once, after ready.
func TestHubNothingBeforeReady(t *testing.T) {
	h := newHubFixture(t)
	h.hubUpload(meetingRec, 1, nil)
	p := dialPhone(t, h.sockets, meetingRec)
	h.attachedPhone(meetingRec, nil)
	r := h.s.hubRunOf(meetingRec)
	r.mu.Lock()
	r.setUpstreamLocked(problemUpstreamFailed)
	r.mu.Unlock()
	p.send(map[string]any{"type": "start", "at": 0})
	p.wait("problem", 1)
	p.mu.Lock()
	frames := slices.Clone(p.frames)
	p.mu.Unlock()
	if len(frames) != 2 || frames[0]["type"] != "ready" || frames[1]["type"] != "problem" || frames[1]["code"] != problemUpstreamFailed {
		t.Fatalf("frames %v", frames)
	}
}

// A socket that never sends start counts as down: the fallback comes fallbackAfter after the first hub-mode upload
// while it waits for its start, and its end leaves the time the socket is down from as it was.
func TestHubSocketNeverStartedCountsAsDown(t *testing.T) {
	h := newHubFixture(t)
	h.hubUpload(meetingRec, 1, map[string]any{"segments": phoneSegs(fakeLine{0, 3, "Phone line one."})})
	r := h.s.hubRunOf(meetingRec)
	r.mu.Lock()
	since := r.downSince
	r.mu.Unlock()
	p := dialPhone(t, h.sockets, meetingRec)
	sock := h.attachedPhone(meetingRec, nil)
	waitFor(t, "the fallback", func() bool { return h.transcription(meetingRec)["fallback"] == true })
	r.mu.Lock()
	still := r.phone == sock
	r.mu.Unlock()
	if !still {
		t.Fatal("the fallback waited for the socket that never started to end")
	}
	_ = p.c.CloseNow()
	waitFor(t, "the socket gone", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.phone == nil
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.downSince.Equal(since) {
		t.Fatalf("down since %v, was %v", r.downSince, since)
	}
}
