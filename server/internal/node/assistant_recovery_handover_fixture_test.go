package node

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Handover tests exercise ownership and retry budgets, not the installed shell or Agent CLI.
// Join each recovery before restoring its hooks: receiving a send is not the worker's completion.
func stubHandoverRecovery(t *testing.T, nodes ...*Node) {
	t.Helper()
	models, send := assistantRecoveryModels, assistantRecoverySend
	assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
		return proto.LauncherModels{Models: []proto.AgentModel{{ID: "primary-model"}, {ID: "fallback-model"}}}, nil
	}
	t.Cleanup(func() {
		for _, n := range nodes {
			waitHandoverRecovery(t, n)
		}
		assistantRecoveryModels, assistantRecoverySend = models, send
	})
}

func waitHandoverRecovery(t *testing.T, n *Node, retired ...*assistantTurnRecovery) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		n.mu.Lock()
		var workers []chan struct{}
		for _, r := range n.recoveries {
			if r.done != nil {
				workers = append(workers, r.done)
			}
		}
		for _, r := range retired {
			if r.done != nil {
				workers = append(workers, r.done)
			}
		}
		n.mu.Unlock()
		if len(workers) == 0 {
			return
		}
		for _, done := range workers {
			select {
			case <-done:
			case <-timer.C:
				t.Fatal("handover recovery worker did not finish before fixture teardown")
			}
		}
	}
}

func TestAssistantCarriedFailureWithoutFallbackEndsTurn(t *testing.T) {
	for _, name := range []string{"catalog unavailable", "catalog empty"} {
		t.Run(name, func(t *testing.T) {
			k := newAssistantKeeper()
			a, b := assistantNode(t), assistantNode(t)
			a.keeper, b.keeper = k, k
			const id = "11111111-2222-3333-4444-555555555555"
			p := keptProc(t, a, id, "claude")
			stubHandoverRecovery(t, a, b)
			p.launcher, p.model, p.busy = "claude", modelExecution{Model: "primary-model"}, true
			assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
				if name == "catalog unavailable" {
					return proto.LauncherModels{}, errors.New("synthetic catalog unavailable")
				}
				return proto.LauncherModels{}, nil
			}
			assistantRecoverySend = func(context.Context, *Node, *assistantProc, string, string, modelExecution) error {
				t.Error("a carried failure without a fallback attempted a recovery send")
				return nil
			}
			a.handOverAssistants()
			if !a.recoverAssistantResult(p, true, "API Error: safeguards flagged") {
				t.Fatal("the first failure in the handover gap was not carried")
			}
			if adopted := k.adopt(b); len(adopted) != 1 || adopted[0] != p {
				t.Fatalf("the live conversation was not adopted: %+v", adopted)
			}
			r := b.recoveries[id]
			b.resumeAdoptedFailures(true)
			waitHandoverRecovery(t, b, r)
			b.mu.Lock()
			recovery, current := b.recoveries[id], b.procs[id]
			b.mu.Unlock()
			p.mu.Lock()
			busy := p.busy
			p.mu.Unlock()
			if recovery != nil || busy {
				t.Fatal("an undeliverable carried failure left the conversation recovering")
			}
			if current != p || !p.alive() {
				t.Fatal("fallback discovery failure replaced or stopped the handed-over process")
			}
		})
	}
}
