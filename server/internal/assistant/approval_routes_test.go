package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

func startPreparedInteraction(t *testing.T, ft *fleetTest, ix proto.Interaction, op approvalops.Operation) <-chan preparedTestResult {
	t.Helper()
	if ix.ExpiresAt.IsZero() {
		ix.ExpiresAt = time.Now().Add(time.Hour)
	}
	if op.Apply == nil {
		op.Apply = func(context.Context, proto.Resolution) (string, error) { return "applied", nil }
	}
	done := make(chan preparedTestResult, 1)
	go func() {
		out, err := ft.s.waitPreparedCard(context.Background(), ix, op)
		done <- preparedTestResult{out: out, err: err}
	}()
	ft.waitPending(1)
	t.Cleanup(func() {
		ft.s.fleet.mu.Lock()
		p := ft.s.fleet.interactions[ix.ID]
		ft.s.fleet.mu.Unlock()
		if p != nil {
			ft.s.endPrepared(p, "cancelled", "test cleanup")
		}
	})
	return done
}

func TestWebClarifyRouteKeepsPrivateCardInItsPrivateConversation(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	var applies atomic.Int32
	done := startPreparedInteraction(t, ft, proto.Interaction{ID: "ix_private_web", MachineID: "mac", SessionID: "s1",
		Kind: proto.IxPermission, ToolName: "persona_settings", Private: true}, approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
		applies.Add(1)
		return "private setting applied", nil
	}})
	h := ft.s.fleetHandler(http.NotFoundHandler())
	body := `{"text":"  Why is this private change needed?  ","client_msg_id":"private-owner-message","resolution":{"allow":true},"values":{"password":"must-not-travel"}}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/interactions/ix_private_web/clarify", strings.NewReader(body)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"pending":true`) {
		t.Fatalf("private clarify route = %d %s", w.Code, w.Body)
	}
	if got := awaitPreparedTest(t, done); got.err != nil || !got.out.Clarifying {
		t.Fatalf("private original tool = %+v, %v", got.out, got.err)
	}
	if applies.Load() != 0 {
		t.Fatal("the Web clarification executed the private operation")
	}
	sent := ownerQuestionSends(ft)
	if len(sent) != 1 || sent[0]["session_id"] != "s1" || sent[0]["text"] != "  Why is this private change needed?  " {
		t.Fatalf("private question delivery = %+v", sent)
	}
	if raw, _ := json.Marshal(sent); strings.Contains(string(raw), "must-not-travel") {
		t.Fatalf("a field value travelled with the question: %s", raw)
	}
	if runs := ft.s.channelRuns(channel.Feishu); len(runs) != 0 {
		t.Fatalf("a Private clarification entered Feishu: %+v", runs)
	}
	allow := true
	if err := ft.s.resolve("ix_private_web", proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	if applies.Load() != 1 {
		t.Fatalf("private Allow applied %d times", applies.Load())
	}
	waitApprovalResult(t, ft, "ix_private_web")
}

func TestFeishuClarifyRequiresFreshCardBoundOwnerActionAndRoutesAnswerHome(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	done := startPreparedTest(t, ft, "ix_lark_question", "", nil)
	var mu sync.Mutex
	used := map[string]bool{}
	ft.f.answerApp(AppInteractions, assistantwire.InteractionsOwnerAction, func(raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		if p["interaction_id"] != "ix_lark_question" || p["channel"] != channel.Feishu || p["kind"] != proto.IxPermission {
			return nil, errors.New("owner action was not bound to this channel, card and kind")
		}
		action, _ := p["action"].(string)
		mu.Lock()
		defer mu.Unlock()
		if action == "" || used[action] {
			return nil, errors.New("owner action is missing or already used")
		}
		used[action] = true
		return map[string]any{}, nil
	})
	press := func(token, action, question string) *botcard.CardResponse {
		var a botcard.CardAction
		a.Token = token
		a.Action.Value = map[string]any{"a": "ix", "id": "ix_lark_question", "clarify": "1"}
		a.Action.FormValue = map[string]any{"question": question, "password": "must-not-travel"}
		a.Context.ChatID = "oc_owner_private"
		a.Context.MessageID = "om_original_card"
		return ft.s.botCardAction(context.Background(), &a, action)
	}
	res := press("callback-token-1", "owner-action-1", "  Why this change?  ")
	if res == nil || res.Toast == nil || res.Card != nil || res.Toast.Type != "success" {
		t.Fatalf("clarify callback response = %+v", res)
	}
	if got := awaitPreparedTest(t, done); got.err != nil || !got.out.Clarifying {
		t.Fatalf("original Lark tool = %+v, %v", got.out, got.err)
	}
	sent := ownerQuestionSends(ft)
	wantID := channel.ClientMsgID(channel.Feishu, "approval-callback-token-1")
	if len(sent) != 1 || sent[0]["client_msg_id"] != wantID || sent[0]["text"] != "  Why this change?  " {
		t.Fatalf("Lark owner question = %+v, want client_msg_id %q", sent, wantID)
	}
	if raw, _ := json.Marshal(sent); strings.Contains(string(raw), "must-not-travel") {
		t.Fatalf("a form value other than the question travelled: %s", raw)
	}
	runs := ft.s.channelRuns(channel.Feishu)
	if len(runs) != 1 {
		t.Fatalf("Lark answer runs = %+v", runs)
	}
	run := runs[0]
	if run.MachineID != "mac" || run.SessionID != "s1" || run.ReplyTo != "om_original_card" ||
		run.Ext["chat"] != "oc_owner_private" || run.Ext["clarify"] != "ix_lark_question" ||
		run.TargetKind != channel.TargetConversation || len(run.MsgIDs) != 1 || run.MsgIDs[0] != wantID {
		t.Fatalf("Lark answer destination = %+v", run)
	}
	// The same callback action is one-use. It cannot send a duplicate owner turn.
	if res := press("callback-token-1", "owner-action-1", "  Why this change?  "); res == nil || res.Toast == nil || res.Toast.Type != "error" {
		t.Fatalf("replayed owner action response = %+v", res)
	}
	if got := len(ownerQuestionSends(ft)); got != 1 {
		t.Fatalf("replayed owner action produced %d total sends", got)
	}
	// A later deliberate press is a fresh owner action and a fresh owner message.
	if res := press("callback-token-2", "owner-action-2", "A second question"); res == nil || res.Toast == nil || res.Toast.Type != "success" {
		t.Fatalf("second clarification = %+v", res)
	}
	if got := len(ownerQuestionSends(ft)); got != 2 {
		t.Fatalf("fresh second question sends = %d", got)
	}
	ft.waitPending(1)
	deny := false
	if err := ft.s.resolve("ix_lark_question", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	waitApprovalResult(t, ft, "ix_lark_question")
}

func TestFeishuTokenlessCallbacksDoNotCollapseDistinctQuestions(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	done := startPreparedTest(t, ft, "ix_lark_tokenless", "", nil)
	ft.f.answerApp(AppInteractions, assistantwire.InteractionsOwnerAction, func(json.RawMessage) (any, error) {
		return map[string]any{}, nil
	})
	press := func(action, question string) {
		t.Helper()
		var a botcard.CardAction
		a.Action.Value = map[string]any{"a": "ix", "id": "ix_lark_tokenless", "clarify": "1"}
		a.Action.FormValue = map[string]any{"question": question}
		a.Context.ChatID, a.Context.MessageID = "oc_owner", "om_card"
		if res := ft.s.botCardAction(context.Background(), &a, action); res == nil || res.Toast == nil || res.Toast.Type != "success" {
			t.Fatalf("tokenless callback = %+v", res)
		}
	}
	press("owner-action-tokenless-1", "First question")
	awaitPreparedTest(t, done)
	press("owner-action-tokenless-2", "Second question")
	sent := ownerQuestionSends(ft)
	if len(sent) != 2 || sent[0]["client_msg_id"] == sent[1]["client_msg_id"] {
		t.Fatalf("tokenless questions shared a delivery identity: %+v", sent)
	}
	deny := false
	if err := ft.s.resolve("ix_lark_tokenless", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	waitApprovalResult(t, ft, "ix_lark_tokenless")
}

func TestChannelClarifyRejectsPrivateAndInvalidBeforeUsingOwnerAction(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	done := startPreparedInteraction(t, ft, proto.Interaction{ID: "ix_channel_guards", MachineID: "mac", SessionID: "s1",
		Kind: proto.IxPermission, Private: true}, approvalops.Operation{})
	ft.f.answerApp(AppInteractions, assistantwire.InteractionsOwnerAction, func(json.RawMessage) (any, error) {
		return map[string]any{}, nil
	})
	for _, tc := range []struct{ ch, text string }{{channel.Feishu, "Private must stay out"}, {channel.Feishu, " \n "}, {channel.Feishu, strings.Repeat("界", 1001)}} {
		if _, err := ft.s.channelClarify(context.Background(), tc.ch, "ix_channel_guards", tc.text, "invalid-question", "owner-action-unused",
			&clarificationOrigin{Channel: tc.ch, ChatID: "oc_owner", MessageID: "om_card"}); err == nil {
			t.Fatalf("channelClarify accepted private or invalid input %q", tc.text)
		}
	}
	if got := len(ft.f.appParams(AppInteractions, assistantwire.InteractionsOwnerAction)); got != 0 {
		t.Fatalf("invalid/private questions consumed %d owner actions", got)
	}
	select {
	case got := <-done:
		t.Fatalf("a rejected channel question released the original tool: %+v", got)
	default:
	}
	deny := false
	if err := ft.s.resolve("ix_channel_guards", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
}

func TestFeishuClarifyFormOmitsPrivateAndNeverCarriesFieldValues(t *testing.T) {
	ft := newFleetTestNoStore(t)
	base := proto.Interaction{ID: "ix_render", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
		CanClarify: true, Fields: []proto.Field{{Key: "password", Label: "Password"}},
		ToolInput: json.RawMessage(`{"setting":"Example","action":"set"}`)}
	public := ft.s.interactionCard(context.Background(), base).JSON()
	if !strings.Contains(public, "permission_clarify") || !strings.Contains(public, `"name":"question"`) {
		t.Fatalf("public field card has no question-only form: %s", public)
	}
	base.Private = true
	private := ft.s.interactionCard(context.Background(), base).JSON()
	if strings.Contains(private, "permission_clarify") || strings.Contains(private, `"name":"question"`) {
		t.Fatalf("Private card exposes a Lark question form: %s", private)
	}
}

func TestFeishuFieldCardCanAskButStillCannotAllowInChannel(t *testing.T) {
	ft := newFleetTestNoStore(t)
	preparedTestConversation(ft)
	var applies atomic.Int32
	done := startPreparedInteraction(t, ft, proto.Interaction{ID: "ix_field_question", MachineID: "mac", SessionID: "s1",
		Kind: proto.IxPermission, Fields: []proto.Field{{Key: "password", Label: "Password"}}}, approvalops.Operation{Apply: func(context.Context, proto.Resolution) (string, error) {
		applies.Add(1)
		return "unexpected", nil
	}})
	ft.f.answerApp(AppInteractions, assistantwire.InteractionsOwnerAction, func(json.RawMessage) (any, error) {
		return map[string]any{}, nil
	})
	if _, err := ft.s.channelClarify(context.Background(), channel.Feishu, "ix_field_question", "Why is a password needed?", "owner-field-question", "oa_field_question",
		&clarificationOrigin{Channel: channel.Feishu, ChatID: "oc_owner", MessageID: "om_card"}); err != nil {
		t.Fatal(err)
	}
	awaitPreparedTest(t, done)
	allow := true
	if _, err := ft.s.channelResolve(context.Background(), channel.Feishu, "ix_field_question", proto.Resolution{Allow: &allow,
		Values: map[string]string{"password": "channel-secret"}}, "oa_field_allow"); err == nil {
		t.Fatal("a field-bearing card accepted Allow in a channel")
	}
	if applies.Load() != 0 {
		t.Fatal("channel Allow executed a field-bearing operation")
	}
	deny := false
	if _, err := ft.s.channelResolve(context.Background(), channel.Feishu, "ix_field_question", proto.Resolution{Allow: &deny,
		Values: map[string]string{"password": "channel-secret"}}, "oa_field_deny"); err != nil {
		t.Fatal(err)
	}
	if applies.Load() != 0 {
		t.Fatal("channel Deny executed the field operation")
	}
	waitApprovalResult(t, ft, "ix_field_question")
}
