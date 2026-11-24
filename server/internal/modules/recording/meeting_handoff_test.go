package recording

import (
	"testing"

	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
)

// A meeting keeps its last MeetingHandoffsKept hand-offs.
func TestMeetingHandoffsKept(t *testing.T) {
	var m kb.Meeting
	for i := range kb.MeetingHandoffsKept + 3 {
		m.AddHandoff(kb.MeetingHandoff{ID: string(rune('a' + i)), State: kb.HandoffSent})
	}
	if len(m.Handoffs) != kb.MeetingHandoffsKept || m.Handoffs[0].ID != string(rune('a'+3)) {
		t.Fatalf("kept %d, first %q", len(m.Handoffs), m.Handoffs[0].ID)
	}
}

// The hand-off's message: the meeting in a context block (the transcript cut in the middle when long), then the owner's
// line; without one, the plain ask.
func TestMeetingHandoffGolden(t *testing.T) {
	golden(t, "meeting_handoff.minutes", prompts.RenderMeetingHandoff(prompts.MeetingHandoff{Title: "Design review", Calendar: "Design sync",
		Started: "2026-11-10 14:00 CST", Minutes: "# Design review\n\n## Decisions\n- Keep the cap", Text: "Update the home page."}))
	golden(t, "meeting_handoff.transcript", prompts.RenderMeetingHandoff(prompts.MeetingHandoff{Title: "Design review",
		Started: "2026-11-10 14:00 CST", Transcript: "[0:00] We keep the cap.\n…\n[9:50] Bo owns the test.", Cut: true}))
}
