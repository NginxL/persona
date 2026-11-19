package extmgr

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestOwnerActionIssuesOnlyOneBoundClarificationProof(t *testing.T) {
	h := &Manager{}
	action := h.issueOwnerAction(channel.Feishu, "ix1")
	params, _ := json.Marshal(map[string]any{"interaction_id": "ix1", "action": action, "channel": channel.Feishu,
		"kind": proto.IxPermission, "clarify": true, "text": "Why?", "client_msg_id": "question-1",
		"machine_id": "mac", "session_id": "s1"})
	call := assistantwire.AppCall{AppID: assistantwire.AppInteractions, Method: assistantwire.InteractionsOwnerAction, Params: params}
	raw, handled, err := h.channelsAppCall(context.Background(), call)
	if err != nil || !handled {
		t.Fatalf("owner action = %s, %v, %v", raw, handled, err)
	}
	var out struct {
		Token string `json:"clarification_proof"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Token == "" {
		t.Fatalf("owner action proof = %s", raw)
	}
	proof, ok := h.clarificationProofs.take(out.Token)
	if !ok || proof.Text != "Why?" || proof.InteractionID != "ix1" || proof.MachineID != "mac" || proof.SessionID != "s1" || proof.ClientMsgID != "question-1" {
		t.Fatalf("bound proof = %+v, %v", proof, ok)
	}
	if _, _, err := h.channelsAppCall(context.Background(), call); err == nil {
		t.Fatal("the same owner action issued another proof")
	}
}

func TestChannelClarificationProofIsBoundAndOneShot(t *testing.T) {
	h := &Manager{}
	question := assistantwire.ClarificationProof{InteractionID: "ix1", MachineID: "mac", SessionID: "s1",
		Text: "Why this change?", ClientMsgID: "question-1"}
	token := h.clarificationProofs.issue(question)
	proof, ok := h.clarificationProofs.take(token)
	if !ok || proof != question {
		t.Fatalf("take proof = %+v, %v", proof, ok)
	}
	if _, ok := h.clarificationProofs.take(token); ok {
		t.Fatal("a consumed channel proof was accepted again")
	}
	for _, invalid := range []assistantwire.ClarificationProof{{}, {InteractionID: "ix", MachineID: "mac", SessionID: "s", ClientMsgID: "c", Text: " "}} {
		if validClarificationProof(invalid) {
			t.Errorf("invalid proof accepted: %+v", invalid)
		}
	}
}
