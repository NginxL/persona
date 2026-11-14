package kb

import (
	"context"
	"slices"
	"testing"
	"time"
)

// purposeTagged answers through m and notes, for every call, the purpose it was asked for (Engine.ModelFor).
type purposeTagged struct {
	purpose string
	asked   *[]string
	m       Model
}

func (p purposeTagged) JSONMax(ctx context.Context, system, user string, maxTokens int, out any) error {
	*p.asked = append(*p.asked, p.purpose)
	return p.m.JSONMax(ctx, system, user, maxTokens, out)
}

// Every call of a minutes job runs on the Meeting minutes model (#7980): a long meeting's parts and their merge, with
// the summary card in the same answers. A recording's summary stays on kb.compress.
func TestMinutesAskTheMeetingMinutesModel(t *testing.T) {
	answers := &funcModel{f: func(int, string, string) (string, error) { return `{}`, nil }}
	var asked []string
	e := goldenEngine(t, nil)
	e.ModelFor = func(purpose string) Model { return purposeTagged{purpose: purpose, asked: &asked, m: answers} }
	e.minutesCall, e.minutesPart = 40, 40 // a few lines a part: a long meeting, read in parts and merged
	now := time.Date(2026, 11, 10, 18, 0, 0, 0, goldenZone)
	putRecording(t, e, recID, meetingUpload(1, now.Add(-time.Hour), "first topic was the release date",
		"second topic was the export limit", "third topic was the regression tests", "fourth topic was the owner"), now)
	e.WriteMinutes(context.Background(), 2)
	if len(answers.calls) < 3 || len(asked) != len(answers.calls) || slices.ContainsFunc(asked, func(p string) bool { return p != PurposeMeetingMinutes }) {
		t.Fatalf("a minutes job of %d calls asked for %v", len(answers.calls), asked)
	}
	if PurposeMeetingMinutes != "meeting.minutes" {
		t.Fatalf("the purpose is %q", PurposeMeetingMinutes)
	}

	asked = nil
	summary := &captureModel{reply: `{"title":"Review","summary":"A review","one_line":"A review","importance":"P2","relevance":50,"tags":["review"]}`}
	e.ModelFor = func(purpose string) Model { return purposeTagged{purpose: purpose, asked: &asked, m: summary} }
	if n := e.SummarizeRecordings(context.Background(), 3); n != 1 || !slices.Equal(asked, []string{"kb.compress"}) {
		t.Fatalf("the recording's summary: %d, asked for %v", n, asked)
	}
}
