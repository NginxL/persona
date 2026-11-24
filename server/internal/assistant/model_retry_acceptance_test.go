package assistant

import (
	"context"
	"testing"
	"time"
)

func TestTemporaryFailuresContinueBeyondFive(t *testing.T) {
	s, f := transientService(t)
	ctx := context.Background()
	const errText = "API Error: 529 {\"type\":\"overloaded_error\"}"

	f.setSessions("box", apiErrorTurn("s1", errText))
	triage(t, s, "s1")

	for continuation := 1; continuation <= 6; continuation++ {
		e := entryOf(t, s, "s1")
		if e.WakeAt == nil {
			t.Fatalf("continuation %d was not scheduled after %d failed continuations: entry %+v; sent %v",
				continuation, continuation-1, e, f.sentToAgents())
		}

		s.workTick(ctx, e.WakeAt.Add(time.Nanosecond))
		got := f.sentToAgents()
		if len(got) != continuation {
			t.Fatalf("after continuation %d: sent %d continuations %v, want %d", continuation, len(got), got, continuation)
		}
		want := "s1: " + autoContinueTextForTest(continueText)
		if got[continuation-1] != want {
			t.Fatalf("continuation %d sent %q, want %q", continuation, got[continuation-1], want)
		}

		// The continuation's turn fails transiently again. Recovery must keep the
		// same session eligible for the next continuation, including past five.
		failAgain(t, s, f, continuation, errText)
	}

	if e := entryOf(t, s, "s1"); e.WakeAt == nil {
		t.Fatalf("seventh continuation was not scheduled after six failed continuations: entry %+v; sent %v",
			e, f.sentToAgents())
	}
}
