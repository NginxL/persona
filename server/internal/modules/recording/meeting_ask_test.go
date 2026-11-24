package recording

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
)

// A failed question asked again before the failed attempt let go of its slot (it stored its failure, then waits for
// the lock the retry took first): the old attempt's cleanup leaves the retry's slot alone, so the retry is not taken for
// an interrupted one and its answer is stored.
func TestMeetingAskRetryKeepsItsSlot(t *testing.T) {
	m := newMeetingFixture(t)
	m.upload(meetingRec, 1, map[string]any{"attendance": "participant"})
	e := m.engine()
	if _, err := e.LiveUpdate(meetingRec, func(l *kb.Live, _ kb.Recording) error {
		l.Asks = append(l.Asks, failAsk(kb.LiveAsk{ID: askOne, QuestionSent: "Who owns the test?", Question: "Who owns the test?",
			QuestionSource: askTyped, State: askPending, Sources: []kb.AskSource{}, AskedAt: time.Now().UTC().Truncate(time.Second)}, askModelUnavailable))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	m.model.gate = gate
	first := make(chan struct{}) // the failed attempt's own channel
	m.s.asks.mu.Lock()
	m.s.asks.running = map[string]map[string]chan struct{}{meetingRec: {askOne: first}}
	m.s.asks.mu.Unlock()
	_, retry, err := m.s.askStart(context.Background(), e, meetingRec, askRequest{ID: askOne, Question: "Who owns the test?"}, "en")
	if err != nil || retry == nil {
		t.Fatalf("retry: %v", err)
	}
	m.s.asks.finished(meetingRec, askOne, first) // the failed attempt's deferred cleanup, after the retry took its slot
	if asks := m.asksOf(meetingRec); len(asks) != 1 || asks[0]["state"] != "pending" {
		t.Fatalf("the retry read as %v", asks)
	}
	close(gate)
	<-retry
	if asks := m.asksOf(meetingRec); asks[0]["state"] != "answered" || asks[0]["answer"] != "We kept the cap." {
		t.Fatalf("the retry's answer: %v", asks)
	}
}

// The answer is plain text of at most 400 characters, cut at a sentence end when one ends in its latter half.
func TestPlainAnswer(t *testing.T) {
	long := strings.Repeat("Bo owns it. ", 40)
	if got := plainAnswer(long); len([]rune(got)) > maxAnswerRunes || !strings.HasSuffix(got, "it.") {
		t.Fatalf("%d %q", len([]rune(got)), got)
	}
	if got := plainAnswer(strings.Repeat("会", 500)); len([]rune(got)) != maxAnswerRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("%d", len([]rune(got)))
	}
	if got := plainAnswer("  **Kept**\n the `cap`.  "); got != "Kept the cap." {
		t.Fatalf("%q", got)
	}
}

// Asks as the routes show them: no answer or sources unless answered, no error unless failed, the hub's own fields
// left out.
func TestViewAsk(t *testing.T) {
	at, next := 3.5, 4
	done := time.Date(2026, 11, 10, 7, 12, 9, 0, time.UTC)
	raw, _ := json.Marshal(viewAsk(kb.LiveAsk{ID: askOne, QuestionSent: "q", QuestionSource: askTyped, At: &at, LiveNext: &next, State: askPending,
		Answer: "leftover", Sources: []kb.AskSource{{Kind: "projects", ID: "x"}}, Error: "old", AnsweredAt: &done, Fed: true,
		AskedAt: time.Date(2026, 11, 10, 7, 12, 3, 0, time.UTC)}))
	if string(raw) != `{"id":"`+askOne+`","question":"","question_source":"typed","at":3.5,"state":"pending","answer":"","lang":"","speak":false,`+
		`"sources":[],"asked_at":"2026-11-10T07:12:03Z"}` {
		t.Fatalf("%s", raw)
	}
}
