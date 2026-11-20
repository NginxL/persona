package hub

import (
	"context"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/prompts"
)

func TestClarificationProofIsBoundAndOneShot(t *testing.T) {
	h := &Hub{}
	question := assistantwire.ClarificationProof{InteractionID: "ix1", MachineID: "mac", SessionID: "s1",
		Text: "  Why this change?\n", ClientMsgID: "question-1"}
	token := h.clarificationProofs.issue("assistant", question)
	if _, ok := h.clarificationProofs.take(token, "another-manager"); ok {
		t.Fatal("another manager consumed the proof")
	}
	token = h.clarificationProofs.issue("assistant", question)
	proof, err := h.takeClarificationProof(context.Background(), "assistant", token)
	if err != nil || proof != question {
		t.Fatalf("take proof = %+v, %v", proof, err)
	}
	if _, err := h.takeClarificationProof(context.Background(), "assistant", token); err == nil {
		t.Fatal("a consumed proof was accepted again")
	}

	protected := prompts.ProtectUserMessage(question.Text)
	for _, tc := range []struct {
		name string
		ok   bool
		edit func(*assistantwire.ClarificationProof)
	}{
		{name: "exact", ok: true, edit: func(*assistantwire.ClarificationProof) {}},
		{name: "card", edit: func(p *assistantwire.ClarificationProof) { p.InteractionID = "other" }},
		{name: "machine", edit: func(p *assistantwire.ClarificationProof) { p.MachineID = "other" }},
		{name: "session", edit: func(p *assistantwire.ClarificationProof) { p.SessionID = "other" }},
		{name: "client", edit: func(p *assistantwire.ClarificationProof) { p.ClientMsgID = "other" }},
		{name: "text", edit: func(p *assistantwire.ClarificationProof) { p.Text = "different" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := question
			tc.edit(&candidate)
			if got := clarificationProofMatches(candidate, question.InteractionID, question.MachineID, question.SessionID,
				question.ClientMsgID, protected); got != tc.ok {
				t.Errorf("proof match = %v, want %v", got, tc.ok)
			}
		})
	}
}

func TestVerifiedClarificationAttachesOnlyHubSubmission(t *testing.T) {
	ctx := withCaller(context.Background(), "assistant")
	ctx = withVerifiedClarification(ctx, "Why?")
	var call devicewire.NodeCall
	attachNodeCaller(ctx, &call)
	if call.Caller == nil || call.Caller.Kind != "system" || call.Submission == nil ||
		call.Submission.Kind != devicewire.SubmissionApprovalClarification || call.Submission.ID == "" || call.Submission.OriginalText != "Why?" {
		t.Fatalf("verified clarification node call = %+v", call)
	}
}
