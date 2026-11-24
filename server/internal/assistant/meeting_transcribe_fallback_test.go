package assistant

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/kb"
)

// When the hub has no sentences the phone's text fills the live log (docs/assistant/meeting-minutes.md "Audio from
// the phone"). Names and texts are placeholders.

// A socket that never connects counts as down from the first hub-mode upload: after fallbackAfter the uploads'
// segments fill the live log (source device), and a heard ask waits for an upload whose segments reach it.
func TestFallbackNeverConnected(t *testing.T) {
	h := newHubFixture(t)
	// The ask below must wait for upload 3: the give-up after askLiveWait is not what this test is about, and a short one
	// races the upload on a loaded machine (the ask then answers from line two).
	pendingRec(t, map[string]time.Duration{"askLiveWait": time.Minute}, nil)
	h.hubUpload(meetingRec, 1, map[string]any{"segments": phoneSegs(fakeLine{0, 3, "Phone line one."})})
	waitFor(t, "the fallback", func() bool {
		tr := h.transcription(meetingRec)
		return tr["fallback"] == true
	})
	waitFor(t, "the phone's line", func() bool { return len(h.lines(meetingRec)) == 1 })
	h.hubUpload(meetingRec, 2, map[string]any{"segments": phoneSegs(fakeLine{0, 3, "Phone line one."}, fakeLine{3.5, 6, "Phone line two."})})
	segs := h.lines(meetingRec)
	if got := lineTexts(segs); !slices.Equal(got, []string{"Phone line one.", "Phone line two."}) || segs[1].Source != kb.SourceDevice {
		t.Fatalf("live log %q %+v", got, segs)
	}
	out := h.do("GET", livePath(meetingRec)+"?from=0", "", nil, http.StatusOK)
	if l := out["lines"].([]any); l[0].(map[string]any)["source"] != "device" {
		t.Fatalf("lines: %v", out)
	}

	asked := make(chan map[string]any, 1)
	go func() {
		asked <- h.do("POST", askPath(meetingRec), "", map[string]any{"id": askOne, "at": 9.0}, http.StatusOK)
	}()
	waitFor(t, "the ask waiting", func() bool { return len(h.asksOf(meetingRec)) == 1 })
	h.hubUpload(meetingRec, 3, map[string]any{"segments": phoneSegs(fakeLine{0, 3, "Phone line one."}, fakeLine{3.5, 6, "Phone line two."},
		fakeLine{6.5, 9.2, "What about pricing?"})})
	ask := meetingAskOf(t, <-asked)
	calls := h.model.called()
	if ask["state"] != askAnswered || len(calls) == 0 || !strings.Contains(calls[len(calls)-1].User, "What about pricing?") {
		t.Fatalf("the heard ask: %v %v", ask, calls)
	}
}

// no_interim never starts the fallback: the lines go on.
func TestNoInterimNeverFallsBack(t *testing.T) {
	h := newHubFixture(t)
	h.tr.set(func(f *fakeTranscriber) {
		f.live = jobScript{lines: mixedLines, features: []string{extwire.TranscriptionLive, extwire.TranscriptionReplay}}
	})
	h.hubUpload(meetingRec, 1, map[string]any{"segments": phoneSegs(fakeLine{0, 6, "Phone text."})})
	p, _ := h.open(meetingRec, 0, nil)
	p.audio(1.6)
	if f := p.wait("problem", 1); f["code"] != problemNoInterim {
		t.Fatalf("problem %v", f)
	}
	since := time.Now()
	waitFor(t, "fallbackAfter passed", func() bool { return time.Since(since) > 2*recTiming(h.s, "fallbackAfter") })
	p.audio(3)
	p.wait("line", 3)
	if tr := h.transcription(meetingRec); tr["fallback"] != nil || tr["problem"] != problemNoInterim {
		t.Fatalf("transcription %v", tr)
	}
	if got := lineTexts(h.lines(meetingRec)); slices.Contains(got, "Phone text.") {
		t.Fatalf("live log %q", got)
	}
}

// No job can be opened: upstream_unavailable, and the fallback once it stood fallbackAfter.
func TestFallbackUpstreamUnavailable(t *testing.T) {
	h := newHubFixture(t)
	h.tr.set(func(f *fakeTranscriber) { f.dialErr = errDial })
	h.hubUpload(meetingRec, 1, map[string]any{"segments": phoneSegs(fakeLine{0, 2, "Phone text."})})
	p, _ := h.open(meetingRec, 0, nil)
	p.audio(0.5)
	if f := p.wait("problem", 1); f["code"] != problemUpstreamUnavailable {
		t.Fatalf("problem %v", f)
	}
	waitFor(t, "the fallback", func() bool { return h.transcription(meetingRec)["fallback"] == true })
	waitFor(t, "the phone's line", func() bool { return len(h.lines(meetingRec)) == 1 })
}

// A socket down for fallbackAfter: the phone's segments after the hub's last sentence fill the live log, those the
// hub said already never; back, the hub's sentences go on from the next position and the fallback ends.
func TestFallbackSocketDown(t *testing.T) {
	h := newHubFixture(t)
	h.tr.set(func(f *fakeTranscriber) {
		f.live = jobScript{lines: []fakeLine{{0.2, 0.8, "Hub before."}, {3.5, 4.0, "Hub after."}}}
	})
	h.hubUpload(meetingRec, 1, nil)
	p, _ := h.open(meetingRec, 0, nil)
	p.audio(1)
	p.wait("line", 1)
	_ = p.c.CloseNow()
	waitFor(t, "the fallback", func() bool { return h.transcription(meetingRec)["fallback"] == true })
	h.hubUpload(meetingRec, 2, map[string]any{"segments": phoneSegs(fakeLine{0.2, 0.8, "Phone before."}, fakeLine{1.5, 3, "Phone after."})})
	if got := lineTexts(h.lines(meetingRec)); !slices.Equal(got, []string{"Hub before.", "Phone after."}) {
		t.Fatalf("live log %q", got)
	}
	back, ready := h.open(meetingRec, 0, nil)
	if ready["next"] != 2.0 || ready["from"] != 1.0 {
		t.Fatalf("ready %v", ready)
	}
	back.send(map[string]any{"type": "seek", "at": 3.0})
	back.audio(1.2)
	if f := back.wait("line", 1); f["n"] != 2.0 || f["text"] != "Hub after." {
		t.Fatalf("line %v", f)
	}
	waitFor(t, "the fallback ended", func() bool { return h.transcription(meetingRec)["fallback"] == nil })
}

// An upload with empty and blank segments fills the live log with its segments as stored: each once, none empty
// (the knowledge base never rewrites the handler's upload in place).
func TestFallbackUploadWithBlankSegments(t *testing.T) {
	h := newHubFixture(t)
	h.hubUpload(meetingRec, 1, map[string]any{"segments": phoneSegs(fakeLine{0, 3, "One."}, fakeLine{3, 6, ""}, fakeLine{6, 9, "Two."},
		fakeLine{9, 10, "   "}, fakeLine{10, 12, "Three."})})
	waitFor(t, "the fallback", func() bool { return h.transcription(meetingRec)["fallback"] == true })
	waitFor(t, "the phone's lines", func() bool { return len(h.lines(meetingRec)) >= 3 })
	if got := lineTexts(h.lines(meetingRec)); !slices.Equal(got, []string{"One.", "Two.", "Three."}) {
		t.Fatalf("live log %q", got)
	}
}

// The fallback never puts Persona's echo into the live log: neither a segment the phone marked nor one the echo rule
// marks against the timeline (its time and the answer's words); someone else speaking during an answer goes in. The
// stored segments keep the phone's mark.
func TestFallbackLeavesPersonasEchoOut(t *testing.T) {
	h := newHubFixture(t)
	segs := []map[string]any{
		{"start": 0, "end": 3, "text": "Phone line one."},
		{"start": 3.5, "end": 6, "text": "We keep the cap for this release.", "echo": true},
		{"start": 6.5, "end": 9, "text": "Bo owns the regression test."},
		{"start": 9.5, "end": 11, "text": "Hold on, what about the docs?"},
	}
	h.hubUpload(meetingRec, 1, map[string]any{"segments": segs, "timeline": map[string]any{"speaking": []map[string]any{
		{"start": 3.4, "end": 6.1, "text": "We keep the cap for this release."},
		{"start": 6.4, "end": 11.2, "text": "Bo owns the regression test, and the release note is Ada's."}}}})
	waitFor(t, "the fallback", func() bool { return h.transcription(meetingRec)["fallback"] == true })
	waitFor(t, "the phone's lines", func() bool { return len(h.lines(meetingRec)) == 2 })
	if got := lineTexts(h.lines(meetingRec)); !slices.Equal(got, []string{"Phone line one.", "Hold on, what about the docs?"}) {
		t.Fatalf("live log %q", got)
	}
	d, err := h.engine().Get(kb.KindRecording, meetingRec, 0)
	if err != nil {
		t.Fatal(err)
	}
	var marks []bool
	for _, sg := range d.Segments {
		marks = append(marks, sg.Echo)
	}
	if !slices.Equal(marks, []bool{false, true, false, false}) {
		t.Fatalf("stored marks %v", marks)
	}
}
