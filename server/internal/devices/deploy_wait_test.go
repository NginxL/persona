package devices

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWaitDeployCancellationLeavesOutcomeUnconfirmed(t *testing.T) {
	run := &deployRun{done: make(chan struct{})}
	m := &Machine{deploying: true, deployRun: run}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := m.waitDeploy(ctx, run)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("waitDeploy error = %v, want an unconfirmed running deployment", err)
	}
}

func TestWaitDeployKeepsTheJoinedRunsOutcome(t *testing.T) {
	m := &Machine{deploying: true, deployRun: &deployRun{done: make(chan struct{})}}
	first := m.deployRun
	m.endDeployLocked(errors.New("first deployment failed"))
	m.beginDeployLocked("transferring")
	defer m.endDeployLocked(nil)
	if err := m.waitDeploy(context.Background(), first); err == nil || err.Error() != "first deployment failed" {
		t.Fatalf("first deployment outcome = %v, want its own failure", err)
	}
}

func TestWaitDeployPrefersCompletionOverCancellation(t *testing.T) {
	run := &deployRun{done: make(chan struct{})}
	m := &Machine{deploying: true, deployRun: run}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	m.endDeployLocked(nil)
	if err := m.waitDeploy(ctx, run); err != nil {
		t.Fatalf("completed deployment returned cancellation: %v", err)
	}
}
