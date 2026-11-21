package proto

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStopCauseJSONAndResumeEligibility(t *testing.T) {
	tests := []struct {
		name     string
		cause    StopCause
		eligible bool
	}{
		{name: "unknown"},
		{name: "user", cause: StopUser},
		{name: "idle", cause: StopIdle, eligible: true},
		{name: "task completed", cause: StopTaskCompleted, eligible: true},
		{name: "restart", cause: StopRestart, eligible: true},
		{name: "resume failed", cause: StopResumeFailed, eligible: true},
		{name: "compact failed", cause: StopCompactFailed, eligible: true},
		{name: "gateway quota", cause: StopUsageLimit, eligible: true},
		{name: "gateway capacity", cause: StopTransientError, eligible: true},
		{name: "exit", cause: StopExit},
		{name: "future", cause: StopCause("future_cause")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cause.AllowsSupervisorResume(); got != tt.eligible {
				t.Fatalf("AllowsSupervisorResume() = %v, want %v", got, tt.eligible)
			}
			if tt.cause == StopUnknown {
				return
			}
			raw, err := json.Marshal(tt.cause)
			if err != nil {
				t.Fatal(err)
			}
			var decoded StopCause
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded != tt.cause {
				t.Fatalf("round trip = %q, want %q", decoded, tt.cause)
			}
		})
	}

	var legacy SessionCapabilities
	if err := json.Unmarshal([]byte(`{"read":true}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.StopCause.AllowsSupervisorResume() {
		t.Fatal("legacy capabilities without stop_cause permitted resume")
	}
	var future SessionCapabilities
	if err := json.Unmarshal([]byte(`{"stop_cause":"future_cause"}`), &future); err != nil {
		t.Fatal(err)
	}
	if future.StopCause.AllowsSupervisorResume() {
		t.Fatal("unknown future stop_cause permitted resume")
	}

	empty, err := json.Marshal(SessionCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), `"stop_cause"`) {
		t.Fatalf("empty stop_cause was not omitted: %s", empty)
	}
	capabilities := &SessionCapabilities{Read: true, StopCause: StopRestart}
	session := Session{Capabilities: capabilities}
	got, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"capabilities":{"read":true`) || !strings.Contains(string(got), `"stop_cause":"restart"`) {
		t.Fatalf("session wire encoder omitted capabilities.StopCause: %s", got)
	}
	var decoded Session
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Capabilities == nil || decoded.Capabilities.StopCause != StopRestart {
		t.Fatalf("session capabilities round trip = %+v", decoded.Capabilities)
	}
}
