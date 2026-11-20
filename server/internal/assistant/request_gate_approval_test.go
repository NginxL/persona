package assistant

import (
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func TestRequestGateKeepsApprovedEdgesAndChecksNewEdges(t *testing.T) {
	var s Service
	s.todos.todos = map[string]*Todo{
		"request":  {ID: "request", RequireAcceptedAfter: true},
		"piece":    {ID: "piece", PartOf: "request", After: []string{"new"}},
		"approved": {ID: "approved", PartOf: "request"},
		"new":      {ID: "new", PartOf: "request"},
	}
	task := PlanTask{TodoID: "piece", RequestGate: "request", After: []string{"approved"}}
	if got := s.requestBlockers(t.Context(), task); !slices.Equal(got, []string{"approved", "new"}) {
		t.Fatalf("after an edge edit, blockers = %v", got)
	}
}

func TestRequestGateOwnerReplyRound(t *testing.T) {
	closed := time.Now().UTC()
	asked, answered := closed.Add(-time.Minute), closed.Add(time.Minute)
	var s Service
	dep := &Todo{ID: "dep", PartOf: "root", DoneAt: &closed, DoneBy: db.DoneByReview,
		Supervision: &Supervision{State: reviewAccepted, Reviews: []TaskReview{{Verdict: reviewAccepted, UserCheck: true}}}}
	s.todos.todos = map[string]*Todo{"root": {ID: "root", RequireAcceptedAfter: true}, "piece": {ID: "piece", PartOf: "root", After: []string{"dep"}}, "dep": dep}
	s.accept.loaded = true
	task := PlanTask{TodoID: "piece", RequestGate: "root"}
	for _, tc := range []struct {
		name        string
		ask         acceptAsk
		reopen      *time.Time
		wantBlocked bool
	}{
		{"owner passes pending card after review", acceptAsk{AskedAt: asked, AcceptedAt: answered}, nil, false},
		{"assistant", acceptAsk{AskedAt: closed, AcceptedAt: answered, ByAssistant: true}, nil, true},
		{"owner passed before review", acceptAsk{AskedAt: asked, AcceptedAt: asked}, nil, true},
		{"card from before reopen", acceptAsk{AskedAt: asked, AcceptedAt: answered}, &closed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dep.ReopenedAt = tc.reopen
			s.accept.asks = map[string]*acceptAsk{"dep": &tc.ask}
			if got := s.requestBlockers(t.Context(), task); (len(got) > 0) != tc.wantBlocked {
				t.Fatalf("blockers = %v, want blocked %t", got, tc.wantBlocked)
			}
		})
	}
}
