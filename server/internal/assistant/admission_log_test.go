package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// A refusal writes one structured log line naming the machine, direction, stable reason key and args, so a blocked
// dispatch is visible in the hub log without reading the todo ledger.
func TestAdmissionRefusalIsLogged(t *testing.T) {
	s, _, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	// 16 GiB total, 15 GiB used: only 1 GiB free, below the 2 GiB line.
	d.samples["busy"] = admissionSample(1.0, 16*1024*1024*1024, 15*1024*1024*1024, 2)
	ctx := context.Background()

	var mu sync.Mutex
	var lines []string
	s.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}

	task := admissionPlanTask("busy", machineFixed)
	if _, err := s.admitTaskStart(ctx, task); err == nil {
		t.Fatal("the overloaded start must be refused")
	}
	want := "admission: refused: machine=busy direction=dir-1 reason=assistant-tasks.admission_memory"
	if !containsLine(lines, want) {
		t.Fatalf("want a refusal log line containing %q, got %v", want, lines)
	}

	// The agent_start path has no direction: the line omits the field rather than printing an empty direction.
	if _, err := s.admitAgentStart(ctx, "busy"); err == nil {
		t.Fatal("the agent_start check must be refused too")
	}
	wantNoDirection := "admission: refused: machine=busy reason=assistant-tasks.admission_memory"
	if !containsLine(lines, wantNoDirection) {
		t.Fatalf("want an agent_start refusal log line containing %q, got %v", wantNoDirection, lines)
	}
}

// An admitted start writes no refusal log: the line is reserved for actual denials.
func TestAdmissionAllowIsNotLogged(t *testing.T) {
	s, _, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	d.samples["busy"] = admissionSample(0.8, 16*1024*1024*1024, 2*1024*1024*1024, 1)
	ctx := context.Background()

	var mu sync.Mutex
	var lines []string
	s.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}

	release, err := s.admitTaskStart(ctx, admissionPlanTask("busy", machineFixed))
	if err != nil {
		t.Fatalf("the healthy start must be admitted: %v", err)
	}
	release()
	for _, line := range lines {
		if strings.Contains(line, "admission: refused") {
			t.Fatalf("an admitted start must not log a refusal: %q", line)
		}
	}
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}
