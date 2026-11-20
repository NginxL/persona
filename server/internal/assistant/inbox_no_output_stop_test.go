package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestInboxNoOutputConversationStopDoesNotSpendItsRetry(t *testing.T) {
	for _, phase := range []string{"before receipt", "replayed receipt", "manager reload"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			s, f, todo := inboxRetryService(t, todoTestDB(t))
			s.inboxToAgent(ctx, inboxRetryEvent(todo.ID, "stopped-review"))
			sent := sentToAgent(f)
			prompt, before := sent[len(sent)-1], len(sent)
			items := turnPage("stopped-external-turn", "", true)
			items[0].Text = prompt
			f.setItems(items...)
			f.setSessions("box", turnRow(proto.StatusWorking, 10, ""))
			s.taskWorking(ctx, "box", "s1")
			s.noteStopped("box", "s1", s.taskStretch(todo))
			row := turnRow(proto.StatusDone, 11, "")
			f.setSessions("box", row)
			s.stopTurnEnded("box", "s1", time.Now())
			if phase == "before receipt" {
				s.taskInboxTurnEnded(ctx, todo, row, items, true)
			}
			s.taskTurnSettled(ctx, "box", "s1")
			if e, ok := s.turnOf(ctx, todo.ID); !ok || e.Silent != proto.TurnErrorStopped {
				t.Fatalf("the conversation stop lost its upstream verdict: %+v", e)
			}
			if phase == "manager reload" {
				s.tasks.turns.mu.Lock()
				s.tasks.turns.loaded = false
				s.tasks.turns.mu.Unlock()
				s.tasks.quiet.mu.Lock()
				s.tasks.quiet.marks = nil
				s.tasks.quiet.stops = nil
				s.tasks.quiet.mu.Unlock()
			}
			if !s.recoverTurn(ctx, todo, row) {
				t.Fatal("the recorded stopped turn was not recoverable")
			}
			if got := len(sentToAgent(f)); got != before {
				t.Fatalf("a conversation stop spent the event retry: sends=%d, want %d", got, before)
			}
			if got := unhandledInboxSignals(s); len(got) != 0 {
				t.Fatalf("a conversation stop escalated an external event: %+v", got)
			}
			if !s.taskInboxRecipient(ctx, todo.ID, "box", "s1") {
				t.Fatal("the stopped turn acknowledged its unhandled event")
			}
			inboxRetryTurn(s, f, "resumed-external-empty", prompt, "", 20)
			if got := len(sentToAgent(f)); got != before+1 {
				t.Fatalf("the stop consumed the resumed event's one retry: sends=%d, want %d", got, before+1)
			}
		})
	}
}
