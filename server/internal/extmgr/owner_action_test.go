package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// An owner action answers only in the channel it was issued in: a card's press that card alone, a message only a
// kind of card the channel takes a typed answer for; once, within its time (#5011).
func TestOwnerActionIsBoundToItsCardAndChannel(t *testing.T) {
	const plugin = "ext-chat"
	press := func(h *Manager) string { return h.issueOwnerAction(channel.Feishu, "ix_card") }
	feishuMessage := func(h *Manager) string { return h.issueMessageAction(channel.Feishu, proto.IxQuestion) }
	pluginMessage := func(h *Manager) string { return h.issueMessageAction(plugin, channel.TypedAnswerKinds...) }
	type take struct{ channel, interaction, kind string }
	cases := []struct {
		name  string
		issue func(*Manager) string
		take  take
		ok    bool
	}{
		{"the press, its card", press, take{channel.Feishu, "ix_card", proto.IxPlan}, true},
		{"the press, another card", press, take{channel.Feishu, "ix_other", proto.IxPlan}, false},
		{"the press, another channel", press, take{plugin, "ix_card", proto.IxPlan}, false},
		{"the press, no channel", press, take{"", "ix_card", proto.IxPlan}, false},
		{"a Feishu message, a question", feishuMessage, take{channel.Feishu, "ix_q", proto.IxQuestion}, true},
		{"a Feishu message, a plan", feishuMessage, take{channel.Feishu, "ix_plan", proto.IxPlan}, false},
		{"a Feishu message, a permission", feishuMessage, take{channel.Feishu, "ix_perm", proto.IxPermission}, false},
		{"a Feishu message, a terminal", feishuMessage, take{channel.Feishu, "ix_screen", proto.IxScreen}, false},
		{"a Feishu message, no kind", feishuMessage, take{channel.Feishu, "ix_q", ""}, false},
		{"a Feishu message, no card", feishuMessage, take{channel.Feishu, "", proto.IxQuestion}, false},
		{"a Feishu message, another channel", feishuMessage, take{plugin, "ix_q", proto.IxQuestion}, false},
		{"a plugin message, a plan", pluginMessage, take{plugin, "ix_plan", proto.IxPlan}, true},
		{"a plugin message, a form", pluginMessage, take{plugin, "ix_form", proto.IxElicitation}, false},
		{"a plugin message, in Feishu", pluginMessage, take{channel.Feishu, "ix_plan", proto.IxPlan}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newTestManager(t)
			a := c.issue(h)
			err := h.takeOwnerAction(a, c.take.channel, c.take.interaction, c.take.kind)
			if c.ok != (err == nil) || err != nil && !errors.Is(err, errNoOwnerAction) {
				t.Fatalf("take %+v: %v", c.take, err)
			}
			if !c.ok {
				return
			}
			if err := h.takeOwnerAction(a, c.take.channel, c.take.interaction, c.take.kind); !errors.Is(err, errNoOwnerAction) {
				t.Fatalf("used twice: %v", err)
			}
		})
	}
	t.Run("expired", func(t *testing.T) {
		h := newTestManager(t)
		a := press(h)
		in := &h.chans.in
		in.mu.Lock()
		x := in.actions[a]
		x.expires = time.Now().Add(-time.Second)
		in.actions[a] = x
		in.mu.Unlock()
		if err := h.takeOwnerAction(a, channel.Feishu, "ix_card", proto.IxPlan); !errors.Is(err, errNoOwnerAction) {
			t.Fatalf("an expired action: %v", err)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		h := newTestManager(t)
		if err := h.takeOwnerAction("oa_000000000000000000000000", channel.Feishu, "ix_card", proto.IxPlan); !errors.Is(err, errNoOwnerAction) {
			t.Fatalf("an action never issued: %v", err)
		}
	})
}

// The strict proof exchange returns the authenticated event once, while the
// legacy question capability still cannot resolve a permission card.
func TestOwnerApprovalProofIsExactAndOneUse(t *testing.T) {
	original := assistantwire.OwnerApprovalProof{MessageID: "om_reply", ParentID: "om_card", ChatID: "oc_owner", Text: "发"}
	for _, tc := range []struct {
		name        string
		channel     string
		interaction string
		kind        string
		mutate      func(*assistantwire.OwnerApprovalProof)
	}{
		{name: "valid", channel: channel.Feishu, interaction: "ix_perm", kind: proto.IxPermission},
		{name: "wrong channel", channel: "ext-chat", interaction: "ix_perm", kind: proto.IxPermission},
		{name: "legacy question kind", channel: channel.Feishu, interaction: "ix_perm", kind: proto.IxQuestion},
		{name: "message", channel: channel.Feishu, interaction: "ix_perm", kind: proto.IxPermission, mutate: func(p *assistantwire.OwnerApprovalProof) { p.MessageID = "om_other" }},
		{name: "parent", channel: channel.Feishu, interaction: "ix_perm", kind: proto.IxPermission, mutate: func(p *assistantwire.OwnerApprovalProof) { p.ParentID = "om_other" }},
		{name: "chat", channel: channel.Feishu, interaction: "ix_perm", kind: proto.IxPermission, mutate: func(p *assistantwire.OwnerApprovalProof) { p.ChatID = "oc_other" }},
		{name: "text", channel: channel.Feishu, interaction: "ix_perm", kind: proto.IxPermission, mutate: func(p *assistantwire.OwnerApprovalProof) { p.Text = "同意" }},
		{name: "missing parent", channel: channel.Feishu, interaction: "ix_perm", kind: proto.IxPermission, mutate: func(p *assistantwire.OwnerApprovalProof) { p.ParentID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestManager(t)
			action := h.issueOwnerReplyAction(channel.Feishu, original)
			expected := original
			if tc.mutate != nil {
				tc.mutate(&expected)
			}
			proof, err := h.takeOwnerApproval(action, tc.channel, tc.interaction, tc.kind, expected)
			wantOK := tc.name == "valid"
			if wantOK {
				if err != nil || proof != original {
					t.Fatalf("valid proof = %+v, %v", proof, err)
				}
				if _, err := h.takeOwnerApproval(action, tc.channel, tc.interaction, tc.kind, expected); !errors.Is(err, errNoOwnerAction) {
					t.Fatalf("proof used twice: %v", err)
				}
				return
			}
			if !errors.Is(err, errNoOwnerAction) {
				t.Fatalf("mismatched proof: %v", err)
			}
			// A failed comparison does not consume the owner's action.
			if proof, err := h.takeOwnerApproval(action, channel.Feishu, "ix_perm", proto.IxPermission, original); err != nil || proof != original {
				t.Fatalf("original proof after rejection = %+v, %v", proof, err)
			}
		})
	}
}

func TestOwnerApprovalProofWireIsSeparateFromLegacyAnswers(t *testing.T) {
	h := newTestManager(t)
	original := assistantwire.OwnerApprovalProof{MessageID: "om_reply", ParentID: "om_card", ChatID: "oc_owner", Text: "同意"}
	action := h.issueOwnerReplyAction(channel.Feishu, original)
	if err := h.takeOwnerAction(action, channel.Feishu, "ix_perm", proto.IxPermission); !errors.Is(err, errNoOwnerAction) {
		t.Fatalf("legacy typed-answer path gained permission approval: %v", err)
	}
	params, _ := json.Marshal(map[string]any{
		"interaction_id": "ix_perm", "action": action, "channel": channel.Feishu, "kind": proto.IxPermission,
		"approval_reply": true, "message_id": original.MessageID, "parent_id": original.ParentID,
		"chat_id": original.ChatID, "text": original.Text,
	})
	raw, handled, err := h.AppCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppInteractions,
		Method: assistantwire.InteractionsOwnerAction, Params: params})
	if err != nil || !handled {
		t.Fatalf("strict owner proof call: handled=%v err=%v", handled, err)
	}
	var out struct {
		Proof assistantwire.OwnerApprovalProof `json:"owner_approval_proof"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Proof != original {
		t.Fatalf("wire proof = %+v, %v", out.Proof, err)
	}
	_, _, err = h.AppCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppInteractions,
		Method: assistantwire.InteractionsOwnerAction, Params: params})
	if !errors.Is(err, errNoOwnerAction) {
		t.Fatalf("wire proof replay: %v", err)
	}
}

// The assistant's interactions.owner_action carries the channel and the card's kind; a call without them (an
// assistant of before #5011) is refused.
func TestOwnerActionCallNeedsChannelAndKind(t *testing.T) {
	h := newTestManager(t)
	call := func(params map[string]any) error {
		p, _ := json.Marshal(params)
		_, _, err := h.AppCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppInteractions,
			Method: assistantwire.InteractionsOwnerAction, Params: p})
		return err
	}
	a := h.issueMessageAction(channel.Feishu, proto.IxQuestion)
	if err := call(map[string]any{"interaction_id": "ix_q", "action": a}); !errors.Is(err, errNoOwnerAction) {
		t.Fatalf("without channel and kind: %v", err)
	}
	if err := call(map[string]any{"interaction_id": "ix_q", "action": a, "channel": channel.Feishu, "kind": proto.IxQuestion}); err != nil {
		t.Fatalf("with them: %v", err)
	}
}
