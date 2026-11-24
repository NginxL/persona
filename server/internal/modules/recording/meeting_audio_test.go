package recording

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/kb"
)

// The upload's 200 says the mode in effect: hub when asked and ready, device with unavailable when the contribution
// is not ready, device on a meeting naming none, not_meeting for hub asked of a note; the mode never changes after.
func TestTranscriptionModeOnUpload(t *testing.T) {
	h := newHubFixture(t)
	if out := h.hubUpload(meetingRec, 1, nil); out["transcription"] != "hub" || out["transcription_problem"] != nil {
		t.Fatalf("hub: %v", out)
	}
	if out := h.upload(meetingRec, 2, map[string]any{"transcription": "device"}); out["transcription"] != "hub" {
		t.Fatalf("a later upload changed the mode: %v", out)
	}
	if out := h.upload(meetingOther, 1, nil); out["transcription"] != "device" || out["transcription_problem"] != nil {
		t.Fatalf("a meeting naming none: %v", out)
	}
	note := "66666666-6666-4666-8666-666666666666"
	if out := h.upload(note, 1, map[string]any{"kind": "note", "transcription": "hub"}); out["transcription"] != "device" ||
		out["transcription_problem"] != "not_meeting" {
		t.Fatalf("a note: %v", out)
	}
	if out := h.upload("77777777-7777-4777-8777-777777777777", 1, map[string]any{"kind": "note"}); out["transcription"] != nil {
		t.Fatalf("a note naming none: %v", out)
	}
	h.tr.set(func(f *fakeTranscriber) { f.infoErr = errDial })
	h.s.transcriptionReadinessChanged()
	notReady := "88888888-8888-4888-8888-888888888888"
	if out := h.hubUpload(notReady, 1, nil); out["transcription"] != "device" || out["transcription_problem"] != "unavailable" {
		t.Fatalf("not ready: %v", out)
	}
	if tr := h.transcription(notReady); tr["mode"] != "device" || tr["state"] != nil {
		t.Fatalf("device mode's transcription: %v", tr)
	}
	if tr := h.transcription(meetingRec); tr["mode"] != "hub" || tr["state"] != "live" || tr["gaps"] == nil {
		t.Fatalf("hub mode's transcription: %v", tr)
	}
	// POST …/live of a device-mode meeting says its transcription too (once a mode was named); detail.meta has it.
	device := "14141414-1414-4414-8414-141414141414"
	h.upload(device, 1, map[string]any{"attendance": "listener", "transcription": "device"})
	out := h.do("POST", livePath(device), "", `{"from":0,"segments":[{"start":1,"end":2,"text":"Hello"}]}`, http.StatusOK)
	if tr, _ := out["transcription"].(map[string]any); tr["mode"] != "device" || out["revs"] != nil {
		t.Fatalf("POST …/live: %v", out)
	}
	assertHubDetail := func() kb.TranscriptionView {
		t.Helper()
		d, err := h.engine().Get(kb.KindRecording, meetingRec, 0)
		if err != nil {
			t.Fatal(err)
		}
		var meta struct {
			Transcription *kb.TranscriptionView `json:"transcription"`
		}
		if err := json.Unmarshal(d.Meta, &meta); err != nil {
			t.Fatalf("detail.meta: %s %v", d.Meta, err)
		}
		tr := meta.Transcription
		if tr == nil || tr.Mode != "hub" || tr.State != "live" || tr.Gaps == nil || len(*tr.Gaps) != 0 {
			t.Fatalf("detail.meta: %s", d.Meta)
		}
		return *tr
	}
	assertHubDetail()
	// No audio socket connects in this test. Its normal fallback clock may have
	// elapsed already; fallback adds metadata without changing the chosen mode.
	run := h.s.hubRunFor(h.engine(), meetingRec)
	if run == nil {
		t.Fatal("missing hub transcription run")
	}
	run.mu.Lock()
	fallbackDue := run.downSince.Add(h.s.tmNow().fallbackAfter)
	run.mu.Unlock()
	run.tick(fallbackDue)
	if tr := assertHubDetail(); !tr.Fallback {
		t.Fatalf("detail.meta did not expose the legitimate fallback: %+v", tr)
	}
}
