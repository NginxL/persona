package node

import (
	"context"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

// The local socket forwards one short manager request and attributes the source
// from trusted node context, not a source field in the script's JSON.
func TestDecisionScriptSocketRaisesWithNodeSource(t *testing.T) {
	h := newScriptHub(t, func(context.Context, proto.Interaction) (any, error) { return nil, nil }, func() (any, error) { return nil, nil })
	h.decisionAnswer = assistantwire.OutsideDecisionReply{Inserted: true, Decision: &assistantwire.OutsideDecision{
		ID: "odc_local", AskTodoID: "todo_od_local", State: assistantwire.OutsideDecisionPending,
	}}
	reversible := true
	in := nodeclient.DecisionRaiseRequest{Cwd: "/work/nightly", Decision: assistantwire.OutsideDecisionRequest{
		Ref: "local-1", Title: "Publish?", Background: "Checks passed", WhyOwner: "priority", Recommend: "Hold",
		CostOfWaiting: "The next build waits", Reversible: &reversible,
		Options: []assistantwire.OutsideDecisionOption{{Label: "Hold", LeadsTo: "Nothing goes out"},
			{Label: "Publish", LeadsTo: "Users receive it"}},
	}}
	reply, err := nodeclient.DecisionRaiseNode(context.Background(), h.dial, in, "pane-outside")
	if err != nil || reply.Decision == nil || reply.Decision.ID != "odc_local" || !reply.Inserted {
		t.Fatalf("raise: %+v, %v", reply, err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.decisions) != 1 || h.decisions[0]["source"] != "nightly" {
		t.Fatalf("manager requests: %v", h.decisions)
	}
	decision, _ := h.decisions[0]["decision"].(map[string]any)
	if decision["ref"] != "local-1" {
		t.Fatalf("raise payload: %v", h.decisions[0])
	}
}
