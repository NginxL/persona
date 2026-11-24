package recording

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/kb"
)

// finalState waits for recording id's state to be want.
func (h *hubFixture) finalState(id, want string) {
	h.t.Helper()
	waitFor(h.t, "state "+want, func() bool {
		h.s.finalRound(context.Background())
		return h.transcription(id)["state"] == want
	})
}

// The pass waits for the gap uploads; from its start the hub takes no more audio; its windows replace the live
// sentences where it did not keep them; the transcript of record and detail.segments are the hub's; the minutes are
// made from it and the audio goes once they are written.
func TestFinalPass(t *testing.T) {
	h := newHubFixture(t)
	h.meetingAudio(meetingRec)
	h.tr.set(func(f *fakeTranscriber) {
		f.replay = jobScript{windows: []fakeWindow{{0, 3, false, []fakeLine{{0.5, 1.5, "Replay one."}, {2.2, 2.8, "Replay in the gap."}}},
			{3, 6, true, nil}}, usage: 7}
	})
	h.s.finalRound(context.Background())
	if tr := h.transcription(meetingRec); tr["state"] != "finishing" || len(tr["gaps"].([]any)) != 1 {
		t.Fatalf("waiting for the gap: %v", tr)
	}
	if len(h.tr.jobsOf(extwire.TranscriptionReplay)) != 0 {
		t.Fatal("the pass started with a gap left")
	}
	path := h.srv.URL + "/api/v1/recordings/" + meetingRec + "/audio?at=2"
	req, _ := http.NewRequest("PUT", path, strings.NewReader(string(make([]byte, 2*2*kb.AudioRate))))
	resp, err := h.srv.Client().Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("gap upload: %v %v", err, resp)
	}
	resp.Body.Close()
	h.finalState(meetingRec, "final")
	jobs := h.tr.jobsOf(extwire.TranscriptionReplay)
	if len(jobs) != 1 || jobs[0].start.Speed != 4 || *jobs[0].start.At != 0 || jobs[0].start.Language != "en-US" {
		t.Fatalf("replay jobs: %+v", jobs)
	}
	frames, audio, _, _ := jobs[0].snapshot()
	var refs []string
	for _, f := range frames {
		if f.Type == extwire.TFReference {
			refs = append(refs, *f.Text)
		}
	}
	if !slices.Equal(refs, []string{"Live one.", "Phone in the gap.", "Live two."}) || audio != 6*kb.AudioRate {
		t.Fatalf("references %q, %d samples", refs, audio)
	}
	if got := h.hubTexts(meetingRec); !slices.Equal(got, []string{"Replay one.", "Replay in the gap.", "Live two."}) {
		t.Fatalf("the final transcript %q", got)
	}
	d, err := h.engine().Get(kb.KindRecording, meetingRec, 0)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, sg := range d.Segments {
		texts = append(texts, sg.Text+"/"+sg.Source)
	}
	if !slices.Equal(texts, []string{"Replay one./hub", "Replay in the gap./hub", "Live two./hub"}) {
		t.Fatalf("detail.segments %q", texts)
	}
	m, _, _ := h.engine().MeetingRead(meetingRec)
	if tr := m.Transcription; tr.UsageSeconds < 7 || tr.FinalAt.IsZero() || len(tr.Gaps) != 0 {
		t.Fatalf("transcription %+v", tr)
	}
	put, _ := http.NewRequest("PUT", path, strings.NewReader("ab"))
	if resp, err := h.srv.Client().Do(put); err != nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("audio after the pass: %v %v", err, resp)
	}
	audioDir := filepath.Join(h.s.kbDir, kb.KindRecording, meetingRec, "attachments", "audio")
	if _, err := os.Stat(audioDir); err != nil {
		t.Fatalf("the audio went before the minutes: %v", err)
	}
	h.minutesFor(meetingRec, `{"title":"Design review","decisions":[{"text":"Keep the cap","line":1}]}`)
	if _, err := os.Stat(audioDir); !os.IsNotExist(err) {
		t.Fatalf("the audio after the final minutes: %v", err)
	}
}

// While the pass runs the apps read how far it is (#13461): final_done, the end of the last window it finished, beside
// the recording's end, kept in meeting.json; neither once the transcript is final.
func TestFinalPassProgress(t *testing.T) {
	h := newHubFixture(t)
	h.meetingAudio(meetingRec)
	gate := make(chan struct{})
	h.tr.set(func(f *fakeTranscriber) {
		f.replay = jobScript{windows: []fakeWindow{{0, 3, false, []fakeLine{{0.5, 1.5, "Replay one."}}}, {3, 9, false, nil}},
			endGate: gate}
	})
	recTimings(h.s, map[string]time.Duration{"finalGapWait": 0}, nil)
	h.s.finalRound(context.Background())
	waitFor(t, "the first window's progress", func() bool {
		tr := h.transcription(meetingRec)
		return tr["state"] == "finishing" && tr["final_done"] == 3.0 && tr["end"] == 6.0
	})
	if m, _, _ := h.engine().MeetingRead(meetingRec); m.Transcription.FinalDone != 3 {
		t.Fatalf("meeting.json %+v", m.Transcription)
	}
	close(gate)
	h.finalState(meetingRec, "final")
	if tr := h.transcription(meetingRec); tr["final_done"] != nil || tr["end"] != nil {
		t.Fatalf("final %v", tr)
	}
}

// From the pass's start the phone's socket and gap uploads are refused transcription_final; a pass that waited
// finalGapWait starts with gaps left.
func TestFinalPassAfterTheWait(t *testing.T) {
	h := newHubFixture(t)
	h.meetingAudio(meetingRec)
	h.tr.set(func(f *fakeTranscriber) {
		f.replay = jobScript{windows: []fakeWindow{{0, 6, false, []fakeLine{{0.5, 1.5, "Replay one."}}}}}
	})
	e := h.engine()
	h.s.finalLook(context.Background(), e, meetingRec, time.Now().Add(2*h.s.tmNow().finalGapWait))
	waitFor(t, "final", func() bool { return h.transcription(meetingRec)["state"] == "final" })
	tr := h.transcription(meetingRec)
	if gaps := tr["gaps"].([]any); len(gaps) != 1 {
		t.Fatalf("the gaps the pass had no audio for: %v", tr)
	}
	p := dialPhone(t, h.sockets, meetingRec)
	if f := p.wait("error", 1); f["code"] != codeTranscriptionFinal {
		t.Fatalf("socket after the pass: %v", f)
	}
}

// finalRetryAfter keeps the two budgets apart: unreachable tries use theirs whatever failed before, and a failed try
// counts only the tries that were not unreachable.
func TestFinalRetryAfter(t *testing.T) {
	for _, c := range []struct {
		tries, unreachable int
		now                bool
		wait               time.Duration
		ok                 bool
	}{
		{1, 0, false, time.Minute, true},
		{2, 0, false, 5 * time.Minute, true},
		{3, 0, false, 0, false},
		{1, 1, true, time.Minute, true},
		{5, 5, true, time.Hour, true},
		{6, 6, true, 0, false},
		{4, 2, true, 5 * time.Minute, true},  // two failed tries before: the unreachable budget is untouched
		{4, 2, false, 5 * time.Minute, true}, // the second failed try, after two unreachable ones
		{5, 2, false, 0, false},              // the third: unreachable tries do not lengthen the failed budget
		{7, 5, false, 5 * time.Minute, true}, // a failed try after a long outage: its second
		{0, 0, false, 0, false},
	} {
		wait, ok := finalRetryAfter(defaultTimings(), kb.MeetingTranscription{FinalTries: c.tries, FinalUnreachable: c.unreachable}, c.now)
		if wait != c.wait || ok != c.ok {
			t.Errorf("tries %d, unreachable %d, this one unreachable %v: %v %v, want %v %v", c.tries, c.unreachable, c.now, wait, ok, c.wait, c.ok)
		}
	}
}

// A recording no complete upload ended is complete for the pass feedStale after its last upload.
func TestFinalPassNeverComplete(t *testing.T) {
	h := newHubFixture(t)
	h.tr.set(func(f *fakeTranscriber) {
		f.live = jobScript{lines: liveTwo[:1]}
		f.replay = jobScript{windows: []fakeWindow{{0, 2, false, []fakeLine{{0.5, 1.5, "Replay one."}}}}}
	})
	h.hubUpload(meetingRec, 1, nil)
	p, _ := h.open(meetingRec, 0, nil)
	p.audio(2)
	p.wait("line", 1)
	_ = p.c.CloseNow()
	e := h.engine()
	h.s.finalLook(context.Background(), e, meetingRec, time.Now())
	if tr := h.transcription(meetingRec); tr["state"] != "live" {
		t.Fatalf("before its end: %v", tr)
	}
	h.s.finalLook(context.Background(), e, meetingRec, time.Now().Add(feedStale+time.Minute))
	waitFor(t, "final", func() bool { return h.transcription(meetingRec)["state"] == "final" })
	if got := h.hubTexts(meetingRec); !slices.Equal(got, []string{"Replay one."}) {
		t.Fatalf("the final transcript %q", got)
	}
}

// A recording deleted, or gone with the retention, ends its socket and job.
func TestHubRunStoppedByDeleteAndRetention(t *testing.T) {
	h := newHubFixture(t)
	h.hubUpload(meetingRec, 1, nil)
	p, _ := h.open(meetingRec, 0, nil)
	p.audio(1)
	h.do("DELETE", "/api/v1/recordings/"+meetingRec, "", nil, http.StatusOK)
	if f := p.wait("error", 1); f["code"] != codeRecordingDeleted {
		t.Fatalf("deleted: %v", f)
	}
	if h.s.hubRunOf(meetingRec) != nil {
		t.Fatal("the run outlived the recording")
	}

	h.hubUpload(meetingOther, 1, nil)
	other, _ := h.open(meetingOther, 0, nil)
	other.audio(1)
	e := h.engine()
	e.PruneRecordings(time.Now().AddDate(0, 0, 40))
	h.s.hubSweep(e)
	if f := other.wait("error", 1); f["code"] != codeRecordingDeleted {
		t.Fatalf("gone with the retention: %v", f)
	}
}

// The recording's delete ends its final pass at once: the replay job is closed, not left transcribing a meeting that
// is gone; the trim (through the sweep) does too.
func TestFinalPassEndsWithTheRecording(t *testing.T) {
	h := newHubFixture(t)
	h.meetingAudio(meetingRec)
	h.tr.set(func(f *fakeTranscriber) { f.replay = jobScript{noAck: true} })
	recTimings(h.s, map[string]time.Duration{"finalGapWait": 0, "replayAhead": time.Second, "jobDoneWait": time.Minute}, nil)
	h.s.finalRound(context.Background())
	waitFor(t, "the replay waiting for its acks", func() bool {
		jobs := h.tr.jobsOf(extwire.TranscriptionReplay)
		if len(jobs) != 1 {
			return false
		}
		_, audio, _, _ := jobs[0].snapshot()
		return audio > kb.AudioRate
	})
	h.do("DELETE", "/api/v1/recordings/"+meetingRec, "", nil, http.StatusOK)
	waitFor(t, "the replay job closed", func() bool { return h.tr.jobsOf(extwire.TranscriptionReplay)[0].over() })

	h.meetingAudio(meetingOther)
	h.s.finalRound(context.Background())
	waitFor(t, "the replay waiting for its acks", func() bool { return len(h.tr.jobsOf(extwire.TranscriptionReplay)) == 2 })
	e := h.engine()
	e.PruneRecordings(time.Now().AddDate(0, 0, 40))
	h.s.hubSweep(e)
	waitFor(t, "the replay job closed", func() bool { return h.tr.jobsOf(extwire.TranscriptionReplay)[1].over() })
}

// The final pass waits for the live job's last sentences after the phone's end: a complete upload right after the
// end starts no pass before the socket got its last lines and was closed 1000.
func TestFinalPassWaitsForTheLastLines(t *testing.T) {
	h := newHubFixture(t)
	gate := make(chan struct{})
	h.tr.set(func(f *fakeTranscriber) {
		f.live = jobScript{lines: liveTwo[:1], held: true, endGate: gate}
		f.replay = jobScript{windows: []fakeWindow{{0, 2, false, []fakeLine{{0.5, 1.5, "Replay one."}}}}}
	})
	h.hubUpload(meetingRec, 1, nil)
	p, _ := h.open(meetingRec, 0, nil)
	p.audio(2)
	p.send(map[string]any{"type": "end", "at": 2.0})
	waitFor(t, "the job told of the end", func() bool {
		jobs := h.tr.jobsOf(extwire.TranscriptionLive)
		if len(jobs) != 1 {
			return false
		}
		_, _, _, ended := jobs[0].snapshot()
		return ended
	})
	h.hubUpload(meetingRec, 2, map[string]any{"complete": true, "duration": 2.0})
	h.s.finalRound(context.Background())
	h.s.hubTr.mu.Lock()
	st := h.s.hubTr.passes[meetingRec]
	running := st != nil && st.running
	h.s.hubTr.mu.Unlock()
	m, _, _ := h.engine().MeetingRead(meetingRec)
	if running || !m.Transcription.FinalFrom.IsZero() {
		t.Fatal("the pass started before the last lines")
	}
	close(gate)
	if f := p.wait("line", 1); f["text"] != liveTwo[0].text {
		t.Fatalf("line %v", f)
	}
	if status := p.waitClosed(); status != websocket.StatusNormalClosure || len(p.of("error")) != 0 {
		t.Fatalf("closed %d, errors %v", status, p.of("error"))
	}
	h.finalState(meetingRec, "final")
}

// The final pass marks Persona's echo among its sentences by the timeline's spans and their answers (kb/echo.go),
// never drops it: the final transcript and detail.segments keep it marked, transcript.md marks it, and chars leaves it
// out.
func TestFinalPassMarksEcho(t *testing.T) {
	h := newHubFixture(t)
	h.meetingAudio(meetingRec)
	h.hubUpload(meetingRec, 3, map[string]any{"complete": true, "duration": 6.0, "ended_at": time.Now().UTC().Format(time.RFC3339),
		"segments": phoneSegs(fakeLine{0.4, 1.6, "Phone one."}, fakeLine{2.1, 2.9, "Phone in the gap."}),
		"timeline": map[string]any{"speaking": []map[string]any{{"start": 4.0, "end": 5.2, "text": "Bo owns the regression test."}}}})
	h.tr.set(func(f *fakeTranscriber) {
		f.replay = jobScript{windows: []fakeWindow{{0, 6, false, []fakeLine{{0.5, 1.5, "Replay one."}, {4.2, 5.0, "Bo owns the regression test."},
			{4.4, 4.9, "Wait, who owns the docs?"}}}}}
	})
	h.s.finalLook(context.Background(), h.engine(), meetingRec, time.Now().Add(2*h.s.tmNow().finalGapWait))
	waitFor(t, "final", func() bool { return h.transcription(meetingRec)["state"] == "final" })
	if got, echo := h.hubTexts(meetingRec), h.hubEcho(meetingRec); len(got) != 3 || !slices.Equal(echo, []bool{false, true, false}) {
		t.Fatalf("the final transcript %q, echo %v", got, echo)
	}
	d, err := h.engine().Get(kb.KindRecording, meetingRec, 0)
	if err != nil {
		t.Fatal(err)
	}
	var marked []string
	for _, sg := range d.Segments {
		if sg.Echo {
			marked = append(marked, sg.Text)
		}
	}
	if !slices.Equal(marked, []string{"Bo owns the regression test."}) {
		t.Fatalf("detail.segments %+v", d.Segments)
	}
	md, err := os.ReadFile(filepath.Join(h.s.kbDir, kb.KindRecording, meetingRec, "transcript.md"))
	if err != nil || !strings.Contains(string(md), "[Persona 回声] Bo owns the regression test.") {
		t.Fatalf("transcript.md %s %v", md, err)
	}
	r, ok := h.engine().Store.Recording(meetingRec)
	if want := len("Replay one.") + len("Wait, who owns the docs?") + len("Phone in the gap."); !ok || r.Chars != want {
		t.Fatalf("chars %d, want %d (Persona's echo left out)", r.Chars, want)
	}
}
