package assistant

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A stopped Agent cannot receive Continue, but the manager can still retire its own card. Resolving that card removes
// the pending count that made the offline session Needs you and kept archive disabled (#6600).
func TestStoppedAgentWorkCardDismissClearsNeedsYou(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	turn := time.Now().UTC().Truncate(time.Second)
	row := stopped("s1", "API Error: model unavailable")
	row.MachineID, row.Status, row.State = "mac", proto.StatusOffline, proto.StateOffline
	row.UpdatedAt = turn
	row.Capabilities = &proto.SessionCapabilities{Read: true, Terminal: true}
	row.ReadOnlyReason = "stopped"
	ft.d.setSessions("mac", []proto.Session{row})

	question := "The model service failed. What next?"
	w := proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitUser, TurnAt: turn, Questions: []proto.Question{{
		Question: question,
		Options:  []proto.QuestionOption{{Label: answerLabel("en", answerHandled)}, {Label: answerLabel("en", answerSkip)}},
	}}}
	ix := workCard("mac", row, w, "work_stopped")
	if ix.Questions[0].Options[0].Action != "" || ix.Questions[0].Options[1].Action != proto.QuestionOptionDefer {
		t.Fatalf("work-card actions were not explicit: %+v", ix.Questions[0].Options)
	}
	w.InteractionID = ix.ID
	entry := db.SessionWork{MachineID: "mac", SessionID: row.SessionID, TurnAt: turn, InteractionID: ix.ID, Work: w}
	s.work.mu.Lock()
	s.work.init()
	s.work.entries[[2]string{"mac", row.SessionID}] = &entry
	s.work.cards[ix.ID] = ix
	s.work.loaded = true
	s.work.mu.Unlock()
	putPending(s, &pending{ix: ix, local: func(r proto.Resolution) error {
		_, err := s.workAnswer(context.Background(), ix.ID, r)
		return err
	}})

	before := listed(t, s, "mac", row.SessionID)
	if before.State != proto.StateNeedsYou || before.PendingInteractions != 1 || !agentArchiveBusy(before) {
		t.Fatalf("pending stopped session: state %s, pending %d, busy %v", before.State, before.PendingInteractions, agentArchiveBusy(before))
	}
	if err := s.resolve(ix.ID, proto.Resolution{Answers: map[string]string{question: answerLabel("en", answerHandled)}}); err == nil {
		t.Fatal("Continue was accepted for a stopped Agent")
	} else {
		var rpcErr *rpc.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != "agent_stopped" {
			t.Fatalf("Continue refusal: %v", err)
		}
	}
	if got := ft.f.sentToAgents(); len(got) != 0 {
		t.Fatalf("Continue sent to a stopped Agent: %v", got)
	}
	if got := listed(t, s, "mac", row.SessionID); got.State != proto.StateNeedsYou || got.PendingInteractions != 1 {
		t.Fatalf("refused answer changed the pending card: %+v", got)
	}

	if err := s.resolve(ix.ID, proto.Resolution{Dismiss: true}); err != nil {
		t.Fatalf("dismiss stopped Agent's work card: %v", err)
	}
	after := listed(t, s, "mac", row.SessionID)
	if after.State != proto.StateOffline || after.PendingInteractions != 0 || agentArchiveBusy(after) {
		t.Fatalf("after dismiss: state %s, pending %d, busy %v", after.State, after.PendingInteractions, agentArchiveBusy(after))
	}
	stored, ok := s.workEntry([2]string{"mac", row.SessionID})
	if len(s.workCards()) != 0 || !ok || !stored.Work.Deferred {
		t.Fatal("the manager did not retire the dismissed work card")
	}
	if got := ft.f.sentToAgents(); len(got) != 0 {
		t.Fatalf("dismiss sent a message: %v", got)
	}
}
