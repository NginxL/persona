package recording

import (
	"testing"

	"github.com/nwverse/persona/server/internal/kb"
)

// The push counts only meetings the hub knows of (#5051): a day with one recorded meeting, and a one-to-one, a hallway
// chat and lunch the model still called meetings, reads "1 meeting"; the same meeting in a second entry counts once.
func TestDayRecapCountsOnlyKnownMeetings(t *testing.T) {
	known := &kb.MeetingRef{Kind: kb.KindRecording, ID: "rec-standup", Name: "Standup"}
	d := kb.DayRecap{Timeline: []kb.DayRecapEntry{
		{Kind: kb.RecapMeeting, Title: "Standup", Meeting: known},
		{Kind: kb.RecapMeeting, Title: "One-to-one about the release"},
		{Kind: kb.RecapMeeting, Title: "Hallway chat"},
		{Kind: kb.RecapMeeting, Title: "Lunch", Meeting: &kb.MeetingRef{}},
		{Kind: kb.RecapOutside, Title: "Writing the report"},
		{Kind: kb.RecapMeeting, Title: "Standup, after the break", Meeting: known},
	}, Actions: []kb.MinutesAction{{What: "send the notes"}}}
	if meetings, actions := dayRecapCounts(nil, "2026-11-11", "r1", d); meetings != 1 || actions != 1 {
		t.Fatalf("meetings %d actions %d, want 1 and 1", meetings, actions)
	}
}
