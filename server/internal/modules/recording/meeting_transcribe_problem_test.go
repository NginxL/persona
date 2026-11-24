package recording

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/nwverse/persona/server/internal/extwire"
)

// A job's fatal code becomes the phone's problem by the shared cases (#13446): the kind of the upstream's failure
// (refused, busy, out of reach, anything else) reaches the phone, which says why and what to do.
func TestTranscriptionProblemsFollowTheSharedCases(t *testing.T) {
	raw, err := os.ReadFile(transcriptionProblemFixtures)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Version int `json:"version"`
		Cases   []struct {
			Name          string  `json:"name"`
			ExtensionCode *string `json:"extension_code"`
			Problem       string  `json:"problem"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.Version != 1 {
		t.Fatalf("version %d", f.Version)
	}
	n := 0
	for _, c := range f.Cases {
		if c.ExtensionCode == nil {
			continue // the phone's own case
		}
		n++
		if got := problemOfJob(*c.ExtensionCode); got != c.Problem {
			t.Errorf("%s: problemOfJob(%q) = %q, want %q", c.Name, *c.ExtensionCode, got, c.Problem)
		}
	}
	if n < 7 {
		t.Fatalf("only %d cases with an extension code", n)
	}
}

// The phone gets the kind as the socket's problem, and the hub keeps it in meeting.json, which a restart reads back
// (#13446): a refused or busy service is not folded into upstream_unavailable any more.
func TestTranscriptionProblemKindReachesThePhone(t *testing.T) {
	for _, tc := range []struct{ code, problem string }{
		{extwire.TEUpstreamForbidden, problemUpstreamForbidden},
		{extwire.TEUpstreamBusy, problemUpstreamBusy},
	} {
		t.Run(tc.problem, func(t *testing.T) {
			h := newHubFixture(t)
			h.tr.set(func(f *fakeTranscriber) { f.live = jobScript{refuse: tc.code} })
			h.hubUpload(meetingRec, 1, nil)
			p, _ := h.open(meetingRec, 0, nil)
			p.audio(0.5)
			if f := p.wait("problem", 1); f["code"] != tc.problem {
				t.Fatalf("problem %v", f)
			}
			waitFor(t, "the problem kept", func() bool { return h.transcription(meetingRec)["problem"] == tc.problem })
			h.stopRuns()
			r := newHubRun(h.s.Recordings, h.engine(), meetingRec) // as after a restart: from meeting.json
			if r == nil {
				t.Fatal("no run")
			}
			defer r.end()
			if r.upstream != tc.problem {
				t.Fatalf("restored problem %q", r.upstream)
			}
		})
	}
}
