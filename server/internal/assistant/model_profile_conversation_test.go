package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func (h *spawnHarness) openConversation(args string) proto.ToolResult {
	return h.s.Tool(context.Background(), ToolCall{Name: "conversation_new", MachineID: "server", SessionID: h.parent.SessionID, Arguments: json.RawMessage(args)})
}

func TestConversationNewTier(t *testing.T) {
	h := newSpawnHarness(t)
	h.f.launcherList = []proto.Launcher{{Name: "claude"}, {Name: "cx", Agent: "codex"}, {Name: "terminal", Source: "paseo"}}
	codex := ModelTier{Launcher: "cx", Model: "test-codex", Effort: "high", Speed: "fast", PermissionMode: proto.ModePlan}
	putTierProfiles(t, h.s, "performance", map[string]ModelTier{"review": {Launcher: "claude"}}, map[string]ModelTier{"review": codex, "terminal": {Launcher: "terminal"}, "gone": {Launcher: "uninstalled"}})
	ctx := context.Background()
	fit, refusal := h.s.mainChatFit(ctx, "server", "terminal")
	if fit != mainChatRefused {
		t.Fatal("terminal fixture must be unavailable for assistant conversations")
	}
	for _, tc := range []struct{ tier, reason string }{{"unknown", "unknown"}, {"terminal", refusal.Error()}, {"gone", "uninstalled"}} {
		r := h.openConversation(`{"text":"Review this","tier":"` + tc.tier + `","pin":true}`)
		if !r.IsError || !strings.Contains(r.Content[0].Text, tc.reason) || h.f.sent() != 0 || len(h.s.pendingQuestions()) != 0 {
			t.Fatalf("tier %s: result %+v, sends %d", tc.tier, r, h.f.sent())
		}
	}
	r := h.openConversation(`{"text":"Review this","tier":" review "}`)
	if r.IsError || !strings.Contains(r.Content[0].Text, "cx · test-codex · high") {
		t.Fatalf("start: %+v", r)
	}
	match := startedConvRe.FindStringSubmatch(r.Content[0].Text)
	if match == nil {
		t.Fatalf("missing session ID in %q", r.Content[0].Text)
	}
	sent := h.f.lastSend(t)
	if sent["launcher"] != codex.Launcher || sent["model"] != codex.Model || sent["effort"] != codex.Effort || sent["permission_mode"] == proto.ModePlan || sent["service_tier"] != nil {
		t.Fatalf("first turn: %+v", sent)
	}
	// A new manager reads the pick from storage.
	reloaded := newSpawnHarnessOn(t, h.s.store())
	pick, ok := reloaded.s.modelPickOf(ctx, "server", match[1])
	if !ok || pick.Launcher != codex.Launcher || pick.Model != codex.Model || pick.Effort != codex.Effort {
		t.Fatalf("saved pick: %+v, found %v", pick, ok)
	}
}

func TestConversationNewPinAfterFirstTurn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pin, allow bool
	}{{"ordinary", false, false}, {"decline", true, false}, {"approve", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h := newSpawnHarness(t)
			existing := h.parent.SessionID
			if _, err := h.s.updateSettings(ctx, func(v *Settings) error { v.Leaders = []string{existing}; return nil }); err != nil {
				t.Fatal(err)
			}
			args := `{"text":"Start here"}`
			if tc.pin {
				args = `{"text":"Start here","pin":true}`
			}
			r := h.openConversation(args)
			if r.IsError {
				t.Fatal(r.Content)
			}
			match := startedConvRe.FindStringSubmatch(r.Content[0].Text)
			if match == nil {
				t.Fatal(r.Content)
			}
			id := match[1]
			if len(h.s.pendingQuestions()) != 0 || !slices.Equal(h.s.settings(ctx).Leaders, []string{existing}) {
				t.Fatal("pin changed before the first turn ended")
			}
			h.f.setSessions("server", h.parent, proto.Session{MachineID: "server", SessionID: id, Assistant: true, Status: proto.StatusDone})
			h.s.spawnPinAfterTurn(ctx, "other", id) // a turn on another computer cannot consume this request
			h.s.TurnEnded(ctx, SessionRef{MachineID: "server", SessionID: id})
			if !tc.pin {
				if len(h.s.pendingQuestions()) != 0 {
					t.Fatal("ordinary chat raised a card")
				}
				return
			}
			card, ok := awaitCard(h.s)
			if !ok || card.ToolName != "persona_settings" || card.Kind != proto.IxPermission || card.SessionID != existing || !strings.Contains(card.Summary, id) || !strings.Contains(card.Summary, existing) {
				t.Fatalf("leaders card: %+v", card)
			}
			if !slices.Equal(h.s.settings(ctx).Leaders, []string{existing}) {
				t.Fatal("pin changed while approval pending")
			}
			h.s.spawnPinAfterTurn(ctx, "server", id)
			answer, _ := json.Marshal(proto.Resolution{Allow: &tc.allow})
			if _, err := h.s.Action(ctx, Action{Op: "answer", InteractionID: card.ID, Answer: answer}); err != nil {
				t.Fatal(err)
			}
			want := []string{existing}
			if tc.allow {
				want = append(want, id)
			}
			waitFor(t, "owner's decision", func() bool { return slices.Equal(h.s.settings(ctx).Leaders, want) && len(h.s.pendingQuestions()) == 0 })
		})
	}
}
