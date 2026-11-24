package recording

import (
	"context"
	"strings"
	"testing"
)

// The follow-up's seed is the meeting's transcript without Persona's echo (#5905): a line someone said while Persona
// answered stays in it, Persona's own answer heard by the microphone does not.
func TestFollowupSeedLeavesPersonasEchoOut(t *testing.T) {
	m := newMeetingFixture(t)
	m.upload(meetingRec, 1, map[string]any{"complete": true, "segments": []map[string]any{
		{"start": 2, "end": 9, "text": "Who owns the regression test?"},
		{"start": 10.2, "end": 17.8, "text": "Bo owns the regression test."},
		{"start": 18, "end": 24, "text": "You kept the cap for this release."},
		{"start": 25, "end": 30, "text": "The phone heard this one as Persona.", "echo": true},
	}, "spoken": []map[string]any{{"start": 10, "end": 17.5, "text": "You kept the cap."},
		{"start": 18, "end": 24.5, "text": "You kept the cap for this release."}}})
	e := m.engine()
	meeting, rec, err := e.MeetingRead(meetingRec)
	if err != nil {
		t.Fatal(err)
	}
	seed := m.s.followupSeed(context.Background(), e, rec, meeting, "Draft the email.")
	if !strings.Contains(seed, "Who owns the regression test?") || !strings.Contains(seed, "Bo owns the regression test.") {
		t.Fatalf("the meeting's lines are missing:\n%s", seed)
	}
	if strings.Contains(seed, "You kept the cap") || strings.Contains(seed, "The phone heard this one") {
		t.Fatalf("Persona's echo is in the seed:\n%s", seed)
	}
}
