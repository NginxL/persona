package assistant

import (
	"context"
	"maps"
	"testing"
	"time"
)

// The recording module's knobs in the core's tests (Options.RecordingTimings, through its Host): its times by name
// (its timings.go), its clock and the feeder's timer. Each lasts one test.

// needsRecordingModule skips a test of the recording module's routes when the build leaves the module out.
func needsRecordingModule(t testing.TB) {
	t.Helper()
	if s := New(Options{}); s.rec == nil {
		t.Skip("the recording module is not in this build (persona_without_recording, persona_core_only)")
	}
}

// recTimings sets s's recording times by name, over those set before; the lists likewise.
func recTimings(t testing.TB, s *Service, one map[string]time.Duration, lists map[string][]time.Duration) {
	t.Helper()
	o := &s.recOpts
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.timings == nil {
		o.timings = map[string]time.Duration{}
	}
	if o.lists == nil {
		o.lists = map[string][]time.Duration{}
	}
	maps.Copy(o.timings, one)
	maps.Copy(o.lists, lists)
}

// recClock gives s's recording module the clock now (nil: the real one).
func recClock(s *Service, now func() time.Time) {
	s.recOpts.mu.Lock()
	s.recOpts.now = now
	s.recOpts.mu.Unlock()
}

// recAfter gives s's attending feeder the timer after (nil: the real one).
func recAfter(s *Service, after func(time.Duration, func()) func() bool) {
	s.recOpts.mu.Lock()
	s.recOpts.after = after
	s.recOpts.mu.Unlock()
}

// recWait ends the module's hub runs and waits for its background work.
func recWait(t testing.TB, s *Service) {
	t.Helper()
	if s.rec == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.rec.Wait(ctx); err != nil {
		t.Errorf("the recording module's background work still runs after the test: %v", err)
	}
}

// recTimes are the recording module's times a test sets before its service is made.
type recTimes struct {
	one   map[string]time.Duration
	lists map[string][]time.Duration
}

// shortRec are the times set for the test running before its service is made (nil: none); the meeting fixture hands
// them to its service (applyShortRec).
var shortRec *recTimes

// pendingRec adds one and lists to the times the next meeting fixture's service gets, for one test.
func pendingRec(t testing.TB, one map[string]time.Duration, lists map[string][]time.Duration) {
	t.Helper()
	if shortRec == nil {
		shortRec = &recTimes{one: map[string]time.Duration{}, lists: map[string][]time.Duration{}}
		t.Cleanup(func() { shortRec = nil })
	}
	maps.Copy(shortRec.one, one)
	maps.Copy(shortRec.lists, lists)
}

// applyShortRec hands s the times set before it was made, if any.
func applyShortRec(t testing.TB, s *Service) {
	if shortRec != nil {
		recTimings(t, s, shortRec.one, shortRec.lists)
	}
}

// recTiming is the time name s's recording module runs with as the test set it (0: not set).
func recTiming(s *Service, name string) time.Duration {
	s.recOpts.mu.Lock()
	defer s.recOpts.mu.Unlock()
	return s.recOpts.timings[name]
}
