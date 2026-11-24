package recording

import (
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
)

// A job that ends with a fatal error: upstream_failed at once and the fallback; once the hub's sentences come again
// problem null, the fallback ends, and a hub sentence the phone's text filled already stays out of the live log.
func TestFallbackUpstreamFailed(t *testing.T) {
	h := newHubFixture(t)
	h.tr.set(func(f *fakeTranscriber) { f.live = jobScript{failAt: 1} })
	h.hubUpload(meetingRec, 1, nil)
	p, _ := h.open(meetingRec, 0, nil)
	p.audio(1.2)
	if f := p.wait("problem", 1); f["code"] != problemUpstreamFailed {
		t.Fatalf("problem %v", f)
	}
	waitFor(t, "the fallback", func() bool { return h.transcription(meetingRec)["fallback"] == true })
	h.hubUpload(meetingRec, 2, map[string]any{"segments": phoneSegs(fakeLine{0, 1.8, "Phone says one."}, fakeLine{2, 3.4, "Phone says two."})})
	if got := lineTexts(h.lines(meetingRec)); len(got) != 2 {
		t.Fatalf("live log %q", got)
	}
	if tr := h.transcription(meetingRec); tr["problem"] != problemUpstreamFailed {
		t.Fatalf("transcription %v", tr)
	}
	h.tr.set(func(f *fakeTranscriber) {
		f.live = jobScript{lines: []fakeLine{{3.0, 3.4, "Filled already."}, {3.6, 4.5, "Hub is back."}}}
	})
	r := h.s.hubRunOf(meetingRec)
	r.mu.Lock()
	retryAt := r.jobRetryAt
	r.mu.Unlock()
	p.audio(1.8)
	waitFor(t, "the retry's time", func() bool { return time.Now().After(retryAt) })
	p.audio(2)
	if f := p.wait("line", 1); f["text"] != "Hub is back." || f["n"] != 2.0 {
		t.Fatalf("line %v", f)
	}
	if f := p.wait("problem", 2); f["code"] != nil {
		t.Fatalf("problem %v", f)
	}
	waitFor(t, "the fallback ended", func() bool { return h.transcription(meetingRec)["fallback"] == nil })
	if got := lineTexts(h.lines(meetingRec)); !slices.Equal(got, []string{"Phone says one.", "Phone says two.", "Hub is back."}) {
		t.Fatalf("live log %q", got)
	}
	waitFor(t, "the hub's transcript", func() bool { return slices.Contains(h.hubTexts(meetingRec), "Filled already.") })
	h.hubUpload(meetingRec, 3, map[string]any{"segments": phoneSegs(fakeLine{0, 1.8, "Phone says one."}, fakeLine{2, 3.4, "Phone says two."},
		fakeLine{4.6, 6, "Phone says three."})})
	if got := lineTexts(h.lines(meetingRec)); len(got) != 3 {
		t.Fatalf("the phone's text after the fallback ended: %q", got)
	}
}

// A job that stays connected but acknowledges nothing (its upstream hangs) is closed once it has been behind for
// jobStall: upstream_failed, then the fallback, so the live log is not left empty.
func TestFallbackJobStalled(t *testing.T) {
	h := newHubFixture(t)
	h.tr.set(func(f *fakeTranscriber) { f.live = jobScript{lines: mixedLines, noAck: true, held: true} })
	recTimings(h.s, map[string]time.Duration{"liveAhead": time.Second, "jobStall": 300 * time.Millisecond}, nil)
	h.hubUpload(meetingRec, 1, map[string]any{"segments": phoneSegs(fakeLine{0, 3, "Phone text."})})
	p, _ := h.open(meetingRec, 0, nil)
	p.audio(0.5)
	waitFor(t, "the job ready", func() bool {
		r := h.s.hubRunOf(meetingRec)
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.job != nil && r.job.ready
	})
	p.audio(3)
	if f := p.wait("problem", 1); f["code"] != problemUpstreamFailed {
		t.Fatalf("problem %v", f)
	}
	if !h.tr.jobsOf(extwire.TranscriptionLive)[0].over() {
		t.Fatal("the stalled job is still open")
	}
	waitFor(t, "the fallback", func() bool { return h.transcription(meetingRec)["fallback"] == true })
	waitFor(t, "the phone's line", func() bool { return slices.Contains(lineTexts(h.lines(meetingRec)), "Phone text.") })
}
