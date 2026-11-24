package db

import (
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestDecisionValidationDoesNotGuess(t *testing.T) {
	e := proto.AssistantEntry{ID: "example", Type: "decision", Text: "Stage the change.", By: "agent", Trust: "agent", Status: "active", Scope: proto.AssistantEntryScope{Executor: "assistant"}, Decision: &proto.AssistantEntryDecision{Options: []string{"Stage", "Release"}, Selected: []string{"Stage"}, Rejected: []string{"Release"}}}
	if err := ValidateAssistantEntry(e); err != nil {
		t.Fatal(err)
	}
	e.Decision.Rejected = []string{"Stage"}
	if ValidateAssistantEntry(e) == nil {
		t.Fatal("selected option also rejected")
	}
	e.Decision.Rejected = nil
	e.Decision.Selected = []string{"Unknown"}
	if ValidateAssistantEntry(e) == nil {
		t.Fatal("unknown option selected")
	}
	e.Decision = nil
	if ValidateAssistantEntry(e) == nil {
		t.Fatal("unstructured decision accepted")
	}
}
