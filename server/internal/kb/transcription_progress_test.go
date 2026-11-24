package kb

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"
)

// minutesWaitFixtures are the cases the hub, the iPhone and the web share (spec/fixtures/minutes-wait, #13461).
const minutesWaitFixtures = "../../../spec/fixtures/minutes-wait/cases.json"

// The view carries final_done and end exactly when the shared cases say, with their values: while the final pass runs
// (finishing, started) and the end is known, final_done at most the end.
func TestMinutesWaitFixture(t *testing.T) {
	raw, err := os.ReadFile(minutesWaitFixtures)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Version int `json:"version"`
		Cases   []struct {
			Name string `json:"name"`
			Hub  *struct {
				State       string  `json:"state"`
				PassStarted bool    `json:"pass_started"`
				FinalDone   float64 `json:"final_done"`
				End         float64 `json:"end"`
			} `json:"hub"`
			Transcription map[string]json.RawMessage `json:"transcription"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if file.Version != 1 {
		t.Fatalf("version %d", file.Version)
	}
	checked := 0
	for _, c := range file.Cases {
		if c.Hub == nil {
			continue
		}
		checked++
		t.Run(c.Name, func(t *testing.T) {
			tr := &MeetingTranscription{Mode: TranscriptionHub, State: c.Hub.State, FinalDone: c.Hub.FinalDone, End: c.Hub.End}
			if c.Hub.PassStarted {
				tr.FinalFrom = time.Date(2026, 11, 10, 15, 0, 0, 0, time.UTC)
			}
			got, err := json.Marshal(Meeting{Transcription: tr}.TranscriptionView(nil))
			if err != nil {
				t.Fatal(err)
			}
			var view map[string]json.RawMessage
			if err := json.Unmarshal(got, &view); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"mode", "state", "final_done", "end"} {
				want, wok := c.Transcription[key]
				have, hok := view[key]
				if wok != hok || (wok && !sameNumberOrText(t, want, have)) {
					t.Errorf("%s: view %s, want %s", key, got, mustJSON(t, c.Transcription))
				}
			}
		})
	}
	if checked < 8 {
		t.Fatalf("only %d hub cases", checked)
	}
}

// sameNumberOrText: two JSON values are the same number, or the same text.
func sameNumberOrText(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return x == y
}

// final_done is kept in meeting.json and moves the transcription's revision (the apps hear of it in meeting_live); it
// is dropped once the state is final.
func TestFinalDoneMovesTheRevision(t *testing.T) {
	now := time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone)
	e, changed := hubEngine(t, nil)
	e.clock = func() time.Time { return now }
	putRecording(t, e, recID, hubUpload(1, now.Add(-time.Hour), false, nil), now)
	parts := func() []string {
		var all []string
		for _, p := range *changed {
			all = append(all, p...)
		}
		*changed = nil
		return all
	}
	upd := func(fn func(t *MeetingTranscription)) MeetingTranscription {
		t.Helper()
		tr, err := e.TranscriptionUpdate(recID, func(t *MeetingTranscription, _ Recording) error { fn(t); return nil })
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	upd(func(t *MeetingTranscription) { t.State, t.End = TranscriptionFinishing, 600 })
	parts()
	upd(func(t *MeetingTranscription) { t.FinalDone = 300 })
	if p := parts(); len(p) != 0 {
		t.Fatalf("progress before the pass started moved %v", p)
	}
	upd(func(t *MeetingTranscription) { t.FinalFrom = now })
	if p := parts(); !slices.Equal(p, []string{PartTranscription}) {
		t.Fatalf("the pass's start told %v", p)
	}
	upd(func(t *MeetingTranscription) { t.FinalDone = 450 })
	if p := parts(); !slices.Equal(p, []string{PartTranscription}) {
		t.Fatalf("a window told %v", p)
	}
	if tr := transcriptionNow(t, e, recID); tr.FinalDone != 450 {
		t.Fatalf("kept %+v", tr)
	}
	m, _, _ := e.MeetingRead(recID)
	if v := m.TranscriptionView(nil); v.FinalDone == nil || *v.FinalDone != 450 || v.End == nil || *v.End != 600 {
		t.Fatalf("view %+v", v)
	}
	tr := upd(func(t *MeetingTranscription) { t.State = TranscriptionFinal })
	if tr.FinalDone != 0 {
		t.Fatalf("final keeps its progress: %+v", tr)
	}
	m, _, _ = e.MeetingRead(recID)
	if v := m.TranscriptionView(nil); v.FinalDone != nil || v.End != nil {
		t.Fatalf("final view %+v", v)
	}
}
