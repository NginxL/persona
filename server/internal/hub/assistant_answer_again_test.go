package hub

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestAssistantAnswerAgainRoutesGuardAndMarksThePrompt(t *testing.T) {
	h := newTestHub(t)
	h.store = newSentStore()
	n := newAssistantNode(t, h)
	prompt := prompts.RenderAssistantAnswerAgain(prompts.AssistantAnswerAgain{Language: "English"})
	params, _ := json.Marshal(map[string]any{
		"session_id": "main-1", "client_msg_id": "answer-recovery", "answer_again_after": "a1#0", "text": prompt,
	})
	for range 2 {
		_, err := (assistantLink{h}).DeviceCall(t.Context(), assistantwire.DeviceCall{
			Device: "server", Method: assistantwire.DeviceAssistantAnswerAgain, Params: params,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := n.sent(); got != 1 {
		t.Fatalf("guarded node sends = %d, want one", got)
	}
	sent, ok := n.lastSend(t)["answer_again"].(map[string]any)
	if !ok || sent["once_id"] != "answer-recovery" || sent["client_msg_id"] != nil || sent["answer_again_after"] != "a1#0" {
		t.Fatalf("guarded send to node = %+v", sent)
	}
	items := h.withInjected([]proto.Item{{Role: "user", Kind: proto.KindText, Text: prompt}})
	if in := items[0].Injected; in == nil || in.Kind != proto.InjectedSystem || in.State != "answer_again" || in.Line == "" {
		t.Fatalf("the app got an unmarked recovery prompt: %+v", items[0])
	}
}
