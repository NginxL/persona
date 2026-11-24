package recording

import (
	"reflect"
	"testing"
	"time"
)

// The times a manager runs with are the ones the module's package-level variables had before they became per-instance
// configuration (#13407): pinned here, so a change of a default is a decision made in review, never a side effect.
// A name a core does not know is logged and changes nothing.
func TestDefaultTimings(t *testing.T) {
	want := map[string]time.Duration{
		"audioStartWait": 10 * time.Second, "audioAckEvery": 5 * time.Second, "audioPingEvery": 30 * time.Second,
		"audioEndWait": 15 * time.Second, "fallbackAfter": 30 * time.Second, "jobRetry": time.Minute,
		"jobReadyWait": 30 * time.Second, "jobIdle": time.Minute, "jobAwayIdle": 10 * time.Second, "jobStall": 30 * time.Second,
		"jobDoneWait": 5 * time.Minute, "echoHoldMost": 3 * time.Minute, "hubSegmentsEvery": 15 * time.Second,
		"hubTick": 250 * time.Millisecond, "interimShown": 10 * time.Second, "finalEvery": time.Minute, "finalGapWait": 30 * time.Minute,
		"liveAhead": 30 * time.Second, "replayAhead": time.Minute,
		"askHold": 20 * time.Second, "askLiveWait": 15 * time.Second, "askTimeout": 3 * time.Minute, "askLivePoll": 250 * time.Millisecond,
		"askRoomPoll": time.Second, "askRoomWait": 3 * time.Minute,
	}
	tm := defaultTimings()
	got := map[string]time.Duration{}
	for name, p := range tm.byName() {
		got[name] = *p
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default times\n got %v\nwant %v", got, want)
	}
	if !reflect.DeepEqual(tm.finalRetries, []time.Duration{time.Minute, 5 * time.Minute}) ||
		!reflect.DeepEqual(tm.finalUnreachableRetries, []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour}) {
		t.Fatalf("default retries %v %v", tm.finalRetries, tm.finalUnreachableRetries)
	}
	if tm.askRoomWait != tm.askTimeout {
		t.Fatal("a room ask waits as long as an answer may take")
	}
	var logged []string
	over := timingsWith(map[string]time.Duration{"jobIdle": time.Second, "nosuch": time.Hour}, map[string][]time.Duration{"finalRetries": {0}},
		func(f string, _ ...any) { logged = append(logged, f) })
	if over.jobIdle != time.Second || len(over.finalRetries) != 1 || over.askHold != tm.askHold || len(logged) != 1 {
		t.Fatalf("overridden %+v, logged %v", over, logged)
	}
	if New(nil).tm().jobIdle != time.Minute {
		t.Fatal("a module with no host runs on the defaults")
	}
}
