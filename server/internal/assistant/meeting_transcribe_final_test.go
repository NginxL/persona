package assistant

import (
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/kb"
)

// The final pass (docs/assistant/meeting-minutes.md "Gaps and the final pass"). Names and texts are placeholders.

// liveTwo are the live job's sentences of the final pass tests.
var liveTwo = []fakeLine{{0.5, 1.5, "Live one."}, {4.2, 5.0, "Live two."}}

// meetingAudio records recording id in hub mode: audio from 0 to 2 and from 4 to 6 (a gap between), liveTwo as its
// live sentences, the socket's end at 6, then the complete upload.
func (h *hubFixture) meetingAudio(id string) {
	h.t.Helper()
	h.tr.set(func(f *fakeTranscriber) { f.live = jobScript{lines: liveTwo} })
	h.hubUpload(id, 1, nil)
	p, _ := h.open(id, 0, nil)
	p.audio(2)
	p.send(map[string]any{"type": "seek", "at": 4.0})
	p.audio(2)
	p.send(map[string]any{"type": "end", "at": 6.0})
	p.waitClosed()
	waitFor(h.t, "the live sentences", func() bool { return len(h.hubTexts(id)) == 2 })
	h.hubUpload(id, 2, map[string]any{"complete": true, "duration": 6.0, "ended_at": time.Now().UTC().Format(time.RFC3339),
		"segments": phoneSegs(fakeLine{0.4, 1.6, "Phone one."}, fakeLine{2.1, 2.9, "Phone in the gap."})})
}

// A try that never reached the upstream (upstream_unavailable, nothing billed) is tried again after each of
// finalUnreachableRetries before the live sentences are final; one that billed first counts as failed, two retries
// (#9535).
func TestFinalPassRetriesWhileUnreachable(t *testing.T) {
	h := newHubFixture(t)
	h.meetingAudio(meetingRec)
	h.tr.set(func(f *fakeTranscriber) { f.replay = jobScript{refuse: extwire.TEUpstreamUnavailable} })
	recTimings(t, h.s, map[string]time.Duration{"finalGapWait": 0}, nil)
	h.finalState(meetingRec, "final")
	if n := len(h.tr.jobsOf(extwire.TranscriptionReplay)); n != 1+len(shortRec.lists["finalUnreachableRetries"]) {
		t.Fatalf("%d tries", n)
	}
	m, _, _ := h.engine().MeetingRead(meetingRec)
	if tr := m.Transcription; tr.FinalTries != 6 || tr.FinalUnreachable != 6 {
		t.Fatalf("tries %d, unreachable %d", tr.FinalTries, tr.FinalUnreachable)
	}
	if got := h.hubTexts(meetingRec); !slices.Equal(got, []string{"Live one.", "Live two."}) {
		t.Fatalf("the final transcript %q", got)
	}

	h.meetingAudio(meetingOther)
	h.tr.set(func(f *fakeTranscriber) {
		f.replay = jobScript{failAt: 1, failCode: extwire.TEUpstreamUnavailable, usage: 1}
	})
	h.finalState(meetingOther, "final")
	if n := len(h.tr.jobsOf(extwire.TranscriptionReplay)) - 6; n != 1+len(shortRec.lists["finalRetries"]) {
		t.Fatalf("billed, then unavailable: %d tries", n)
	}
	m, _, _ = h.engine().MeetingRead(meetingOther)
	if tr := m.Transcription; tr.FinalTries != 3 || tr.FinalUnreachable != 0 || tr.UsageSeconds < 3 {
		t.Fatalf("billed, then unavailable: %+v", tr)
	}
}

// A pass that fails is tried again twice, then the live sentences are final; a contribution without replay makes
// them final at once; with no audio at all the state is failed.
func TestFinalPassRetriesAndFallbacks(t *testing.T) {
	h := newHubFixture(t)
	h.meetingAudio(meetingRec)
	h.tr.set(func(f *fakeTranscriber) { f.replay = jobScript{refuse: extwire.TEUpstreamFailed} })
	recTimings(t, h.s, map[string]time.Duration{"finalGapWait": 0}, nil)
	h.finalState(meetingRec, "final")
	if n := len(h.tr.jobsOf(extwire.TranscriptionReplay)); n != 3 {
		t.Fatalf("%d tries", n)
	}
	if got := h.hubTexts(meetingRec); !slices.Equal(got, []string{"Live one.", "Live two."}) {
		t.Fatalf("the final transcript %q", got)
	}
	m, _, _ := h.engine().MeetingRead(meetingRec)
	if m.Transcription.FinalTries != 3 {
		t.Fatalf("tries %d", m.Transcription.FinalTries)
	}

	h.meetingAudio(meetingOther)
	h.tr.set(func(f *fakeTranscriber) { f.info.Features = []string{extwire.TranscriptionLive} })
	h.finalState(meetingOther, "final")
	if got := h.hubTexts(meetingOther); !slices.Equal(got, []string{"Live one.", "Live two."}) || len(h.tr.jobsOf(extwire.TranscriptionReplay)) != 3 {
		t.Fatalf("no replay: %q", got)
	}

	silent := "13131313-1313-4313-8313-131313131313"
	h.hubUpload(silent, 1, map[string]any{"complete": true, "duration": 60.0, "segments": phoneSegs(fakeLine{0, 5, "Phone only."})})
	h.finalState(silent, "failed")
	d, err := h.engine().Get(kb.KindRecording, silent, 0)
	if err != nil || len(d.Segments) != 1 || d.Segments[0].Source != kb.SourceDevice {
		t.Fatalf("a failed recording's segments: %+v %v", d, err)
	}
}

// A replay job that keeps its socket open but stops answering is given up after jobDoneWait without a frame, and
// tried again: after the retries the live sentences are final, the audio no longer held by a pass that never ends.
func TestFinalPassReplayThatStopsAnswering(t *testing.T) {
	h := newHubFixture(t)
	h.meetingAudio(meetingRec)
	h.tr.set(func(f *fakeTranscriber) { f.replay = jobScript{noAck: true} })
	recTimings(t, h.s, map[string]time.Duration{"finalGapWait": 0, "replayAhead": time.Second, "jobDoneWait": 300 * time.Millisecond}, nil)
	h.finalState(meetingRec, "final")
	jobs := h.tr.jobsOf(extwire.TranscriptionReplay)
	if len(jobs) != 3 {
		t.Fatalf("%d tries", len(jobs))
	}
	for _, j := range jobs {
		if !j.over() {
			t.Fatal("a replay job given up is still open")
		}
	}
	if got := h.hubTexts(meetingRec); !slices.Equal(got, []string{"Live one.", "Live two."}) {
		t.Fatalf("the final transcript %q", got)
	}
}

// Without a replay the live sentences are final, and the complete upload's timeline marks Persona's echo among them
// too (a span the live pass never saw closed, repaired late), never dropping it.
func TestFinalLiveSentencesMarkedByTheLateTimeline(t *testing.T) {
	h := newHubFixture(t)
	h.meetingAudio(meetingRec)
	h.hubUpload(meetingRec, 3, map[string]any{"complete": true, "duration": 6.0, "ended_at": time.Now().UTC().Format(time.RFC3339),
		"timeline": map[string]any{"speaking": []map[string]any{{"start": 4.0, "end": 5.2, "text": "Live two."}}}})
	h.tr.set(func(f *fakeTranscriber) { f.info.Features = []string{extwire.TranscriptionLive} })
	recTimings(t, h.s, map[string]time.Duration{"finalGapWait": 0}, nil)
	h.finalState(meetingRec, "final")
	if got, echo := h.hubTexts(meetingRec), h.hubEcho(meetingRec); !slices.Equal(got, []string{"Live one.", "Live two."}) ||
		!slices.Equal(echo, []bool{false, true}) {
		t.Fatalf("the final transcript %q, echo %v", got, echo)
	}
}
