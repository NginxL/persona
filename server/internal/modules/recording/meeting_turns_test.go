package recording

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// A meeting turn's mark: sent, started, ended, then gone when the next turn starts; the user speaking during it makes
// the rest of the turn theirs; an end before its start is an earlier turn's; it survives a restart, and a batch whose
// turn never started runs out.
func TestMeetingTurnLifecycle(t *testing.T) {
	st := &memStore{rows: map[string]json.RawMessage{}}
	f := newFakeHub()
	s := newServiceOn(t, f, st)
	now := time.Date(2026, 11, 10, 7, 0, 0, 0, time.UTC)
	recClock(s, func() time.Time { return now })
	running := func(s *Service) bool {
		t.Helper()
		on, err := s.meetingTurnRunning(context.Background(), "server", "c1")
		if err != nil {
			t.Fatal(err)
		}
		return on
	}
	if _, err := s.meetingTurnSent("server", "c1", "rec", false); err != nil {
		t.Fatal(err)
	}
	if !running(s) || s.meetingTurnNow("server", "c1") {
		t.Fatal("a batch sent: its turn may run (tools), but the hooks of the turn before are not its")
	}
	if s.meetingTurnEnded("server", "c1") {
		t.Fatal("an end before the batch's turn started is the turn before's")
	}
	if !s.meetingTurnStarted("server", "c1", false) || !s.meetingTurnNow("server", "c1") || !running(s) {
		t.Fatal("the batch's turn started")
	}
	if !s.meetingTurnStarted("server", "c1", false) {
		t.Fatal("the same turn seen again (a reconnect) is still the meeting's")
	}
	// A restart: the mark is read back.
	again := newServiceOn(t, f, st)
	recClock(again, func() time.Time { return now })
	if !again.meetingTurnNow("server", "c1") || !running(again) {
		t.Fatal("the mark did not survive a restart")
	}
	if !s.meetingTurnEnded("server", "c1") || running(s) || !s.meetingTurnNow("server", "c1") {
		t.Fatal("ended: no tools are its any more, the hooks of its end still find it")
	}
	if !s.meetingTurnEnded("server", "c1") {
		t.Fatal("the same end, told twice, says the same")
	}
	s.meetingTurnUserSpoke("server", "c1") // the user's message after it: the hooks of its end still find it
	if !s.meetingTurnNow("server", "c1") {
		t.Fatal("the user's next message dropped an ended mark before its turn started")
	}
	if s.meetingTurnStarted("server", "c1", true) || s.meetingTurnNow("server", "c1") {
		t.Fatal("the turn after a meeting turn is not one")
	}

	// The user speaks while the batch's turn runs: from there on it is theirs.
	_, _ = s.meetingTurnSent("server", "c1", "rec", false)
	s.meetingTurnStarted("server", "c1", true)
	s.meetingTurnUserSpoke("server", "c1")
	if running(s) || s.meetingTurnNow("server", "c1") {
		t.Fatal("the user's own message did not end the meeting turn")
	}

	// A send that was refused takes its mark back; one whose turn never starts runs out.
	at, _ := s.meetingTurnSent("server", "c1", "rec", false)
	s.meetingTurnUnsent("server", "c1", at)
	if running(s) {
		t.Fatal("a refused batch left its mark")
	}
	_, _ = s.meetingTurnSent("server", "c1", "rec", false)
	now = now.Add(meetingTurnStartWithin + time.Second)
	if running(s) || s.meetingTurnStarted("server", "c1", true) {
		t.Fatal("a batch whose turn never started still marks the next one")
	}
}
