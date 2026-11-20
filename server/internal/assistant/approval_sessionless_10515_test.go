package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A prepared operation whose origin is not a live conversation keeps the old
// one-shot approval behavior. It cannot accept a clarification, but its owner
// can still deny it and the original tool call gets the only terminal outcome.
func TestSessionlessPreparedSettingUsesOneShotFallback10515(t *testing.T) {
	for _, tc := range []struct {
		name    string
		machine string
		session string
	}{
		{name: "missing machine", machine: "", session: "s1"},
		{name: "missing session", machine: "mac", session: ""},
		{name: "unknown machine", machine: "gone", session: "s1"},
		{name: "unlisted session", machine: "mac", session: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFleetTest(t)
			preparedTestConversation(ft) // another live conversation must never receive this outcome
			setSettings(t, ft.s, func(v *Settings) { on := true; v.MainTasksQuiet = &on })
			done := make(chan proto.ToolResult, 2)
			go func() {
				done <- ft.s.Tool(context.Background(), ToolCall{Name: "persona_settings", MachineID: tc.machine, SessionID: tc.session,
					Arguments: json.RawMessage(`{"action":"set","key":"assistant.main_tasks_quiet","value":false}`)})
			}()

			var card proto.Interaction
			tick := time.NewTicker(5 * time.Millisecond)
			timeout := time.NewTimer(5 * time.Second)
			defer tick.Stop()
			defer timeout.Stop()
			for card.ID == "" {
				if cards := ft.s.pendingQuestions(); len(cards) == 1 {
					card = cards[0]
					break
				}
				select {
				case out := <-done:
					t.Fatalf("sessionless call returned before its one-shot owner decision instead of showing a fallback card: %+v", out)
				case <-tick.C:
				case <-timeout.C:
					t.Fatal("sessionless call neither showed its one-shot fallback card nor returned")
				}
			}
			if card.CanClarify {
				t.Fatal("a card without a live origin conversation must use the non-clarifiable one-shot fallback")
			}
			if err := ft.s.clarify(context.Background(), card.ID, "Can this execute?", "sessionless-question", nil); err == nil {
				t.Fatal("the non-conversational fallback accepted a clarification")
			}
			if got := ownerQuestionSends(ft); len(got) != 0 {
				t.Fatalf("a non-conversational fallback sent a question: %+v", got)
			}

			raw, _ := json.Marshal(proto.Resolution{Allow: new(bool)})
			if _, err := ft.s.Action(context.Background(), Action{Op: "answer", InteractionID: card.ID, Answer: raw}); err != nil {
				t.Fatalf("owner Deny: %v", err)
			}
			var out proto.ToolResult
			select {
			case out = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("owner Deny did not release the original sessionless tool call")
			}
			text := out.Content[0].Text
			if out.IsError || !strings.Contains(text, "nothing changed") || strings.Contains(text, "uncertain") {
				t.Fatalf("sessionless Deny outcome = %+v; want one definite non-execution result", out)
			}
			if !ft.s.mainTasksQuiet(context.Background()) {
				t.Fatal("Deny applied the retained setting")
			}
			select {
			case duplicate := <-done:
				t.Fatalf("the original sessionless call returned another outcome: %+v", duplicate)
			default:
			}

			allow := true
			raw, _ = json.Marshal(proto.Resolution{Allow: &allow})
			if _, err := ft.s.Action(context.Background(), Action{Op: "answer", InteractionID: card.ID, Answer: raw}); err == nil {
				t.Fatal("a duplicate terminal answer was accepted")
			}
			ft.s.deliverApprovalResults()
			ft.s.deliverApprovalResults()
			ft.f.mu.Lock()
			var crossed []string
			for _, p := range ft.f.sends {
				text, _ := p["text"].(string)
				if strings.Contains(text, card.ID) || strings.Contains(text, "Approval ") {
					crossed = append(crossed, text)
				}
			}
			ft.f.mu.Unlock()
			if len(crossed) != 0 {
				t.Fatalf("the sessionless outcome was delivered to a conversation: %q", crossed)
			}
			if !ft.s.mainTasksQuiet(context.Background()) || len(ft.s.pendingQuestions()) != 0 {
				t.Fatal("a duplicate terminal attempt applied the setting or revived its card")
			}
		})
	}
}
