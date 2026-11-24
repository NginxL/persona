package transcript

import (
	"testing"
	"time"
)

func TestScheduleWakeupFallbackNextAtIsCapped(t *testing.T) {
	meta := &Meta{}
	lines := []string{
		`{"type":"assistant","uuid":"a1","timestamp":"2026-10-25T03:26:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"w1","name":"ScheduleWakeup","input":{"delaySeconds":7200,"prompt":"check the synthetic result","reason":"synthetic fallback"}}]}}`,
		`{"type":"user","uuid":"u1","timestamp":"2026-10-25T03:26:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"w1","content":"The wakeup was registered, but this result has an unknown format."}]}}`,
	}
	for i, line := range lines {
		ParseLine(i+1, []byte(line), meta)
	}
	if meta.Loop == nil {
		t.Fatal("the successful ScheduleWakeup result did not retain its loop")
	}
	want := time.Date(2026, 10, 25, 3, 26, 0, 0, time.UTC).Add(time.Hour)
	if !meta.Loop.NextAt.Equal(want) {
		t.Fatalf("fallback NextAt = %v, want the one-hour cap %v", meta.Loop.NextAt, want)
	}
}
