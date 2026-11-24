package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func resetTurnLedgerForIdleReplayTest(s *Service) {
	b := &s.tasks.turns
	b.mu.Lock()
	b.loaded, b.doc = false, taskTurnsDoc{}
	b.mu.Unlock()
}

func idlePendingForReplayTest(s *Service, g superviseSignal) bool {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	cur, ok := b.pending[g.id()]
	return ok && cur.Key == g.Key
}

// A ledger-only idle is the remaining durable form after a manager stopped between judgment and gathering. Resuming
// work acknowledges exactly that old turn; a genuinely newer stopped turn still replays after its own restart.
func TestWorkingRetractsLedgerIdleAcrossRestart(t *testing.T) {
	oldRecoverMost := turnRecoverMost
	turnRecoverMost = 0 // exercise the ledger replay itself, without a transcript-recovery goroutine
	t.Cleanup(func() { turnRecoverMost = oldRecoverMost })
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, _, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.State = reviewPending
		return true
	}); err != nil {
		t.Fatal(err)
	}
	agent := proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusIdle, UpdatedAt: time.Now().UTC()}
	old := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "ledger-old-stop", Machine: "box", Detail: "old ledger idle", At: time.Now()}
	markLeaderTurnSignalled(t, s, old)
	if idlePendingForReplayTest(s, old) {
		t.Fatal("the ledger-only fixture was gathered before the restart")
	}

	s.supervisedWorking(ctx, todoNow(s, todo.ID))
	simulateSupervisionRestart(t, s)
	resetTurnLedgerForIdleReplayTest(s)
	s.watchTurn(ctx, todoNow(s, todo.ID), []proto.Session{agent})
	if idlePendingForReplayTest(s, old) {
		t.Error("the previous stretch's ledger-only idle replayed after work resumed and the manager restarted")
	}
	if got := storedSupervisePending(t, s)[old.id()]; got.Key == old.Key {
		t.Errorf("the replayed old idle became durable again: %+v", got)
	}

	newer := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "ledger-new-stop", Machine: "box", Detail: "new ledger idle", At: time.Now()}
	markLeaderTurnSignalled(t, s, newer)
	simulateSupervisionRestart(t, s)
	resetTurnLedgerForIdleReplayTest(s)
	s.watchTurn(ctx, todoNow(s, todo.ID), []proto.Session{agent})
	if !idlePendingForReplayTest(s, newer) {
		t.Error("the genuinely newer post-resume ledger idle did not replay")
	}
	if idlePendingForReplayTest(s, old) {
		t.Error("the newer ledger replay also revived the previous stretch's idle")
	}
}

// A pre-structured queue has no signal key, but its one-entry idle text is still unambiguous. Retire that old form on
// resume; leave a mixed legacy digest alone because its other members cannot be reconstructed exactly.
func TestWorkingRetractsLegacyQueuedIdleAcrossRestart(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mixed        bool
		startControl bool
		wantQueue    int
	}{
		{name: "single idle", wantQueue: 0},
		{name: "mixed digest control", mixed: true, wantQueue: 1},
		{name: "provisional start control", startControl: true, wantQueue: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
			s, _, todo := newSupervisedServiceOn(t, st)
			ctx := context.Background()
			if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
				v.State = reviewPending
				return true
			}); err != nil {
				t.Fatal(err)
			}
			text := "📋 督办：你派出去的任务有 1 个要你处理\n1. 任务「Legacy」（" + todo.ID + "）：Agent 停下了，但没做完" // i18n-ignore: legacy rendered prompt fixture
			if tc.mixed {
				text += "\n2. 任务「Other」（todo_other）：Agent 停下了，但没做完" // i18n-ignore: legacy rendered prompt fixture
			}
			if got := s.queueRelay("conv1", relayMsg{From: superviseTool, Tool: superviseTool, Text: text, At: time.Now()}); got != 1 {
				t.Fatal("the legacy supervision message was not admitted to the durable queue")
			}
			if tc.startControl {
				startText := "1. 任务「Legacy」（" + todo.ID + "）：Agent 停下了，hub 没判断出来是不是做完了（" + startUnknownDetail + "）" // i18n-ignore: legacy rendered prompt fixture
				if got := s.queueRelay("conv1", relayMsg{From: superviseTool, Tool: superviseTool, Text: startText, At: time.Now()}); got != 2 {
					t.Fatal("the legacy provisional-start control was not admitted")
				}
			}
			s.supervisedWorking(ctx, todoNow(s, todo.ID))
			simulateSupervisionRestart(t, s)
			if got := s.queuedFor("conv1"); got != tc.wantQueue {
				t.Fatalf("legacy queue length after resume and restart = %d, want %d", got, tc.wantQueue)
			}
			if tc.startControl {
				for _, msg := range queuedRelays(s, "conv1") {
					if msg.Text == text {
						t.Error("idle retraction kept the idle but withdrew the provisional-start control")
					}
				}
			}
		})
	}
}
