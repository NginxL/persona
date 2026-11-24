package assistant

import (
	"context"
	"errors"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larkhost"
	"github.com/nwverse/persona/server/internal/proto"
)

const ownerApprovalHandledParam = "owner_approval_handled"

// resolveAppOwnerReply consumes an authenticated app send's explicit proposal
// target. Human, own-words and via checks are completed by messageParams before
// this call; clientMsgID is the stable owner event, never a model-provided ID.
func (s *Service) resolveAppOwnerReply(ctx context.Context, machine, session, clientMsgID, target, text string) (bool, error) {
	if clientMsgID == "" || target == "" || !ownerApprovalPhrase(s.settings(ctx), text) {
		return false, nil
	}
	ix, pending := s.pendingIx(target)
	if !pending {
		book, err := s.ownerActions()
		if err != nil {
			return false, err
		}
		m, sess, found, err := book.targetOrigin(ctx, target)
		if err != nil {
			return false, err
		}
		if found && m == machine && sess == session {
			return true, nil // the first terminal decision already won; a retry has no new effect
		}
		return false, errOwnerActionProof
	}
	if ix.ID != target || ix.MachineID != machine || ix.SessionID != session || !ownerActionProposal(ix) {
		return false, errOwnerActionProof
	}
	allow := true
	if err := s.resolveOwnerProposal(ctx, ix, proto.Resolution{Allow: &allow}, ownerReplyProof{
		AuthenticatedOwner: true,
		DirectParent:       true,
		ExactPhrase:        true,
		EventID:            "app:" + machine + ":" + session + ":" + clientMsgID,
	}); err != nil && !errors.Is(err, errNotPending) {
		return false, err
	}
	return true, nil
}

// botApproveOwnerAction handles only an exact affirmative phrase directly
// replying to a server-owned proposal card in the authenticated owner's DM.
// Once a parent names such a card, every verification or delivery failure is
// final for this message: the words never fall through to a model turn.
func (s *Service) botApproveOwnerAction(ctx context.Context, ev botcard.MessageEvent, text, action string) bool {
	msg := ev.Message
	if msg.ChatType != "p2p" || msg.ParentID == "" || action == "" || !ownerApprovalPhrase(s.settings(ctx), text) {
		return false
	}

	b := &s.fbot
	b.mu.Lock()
	s.loadBotLocked(ctx)
	id := ""
	for candidate, ref := range b.st.Cards {
		if ref == nil || !ref.OwnerProposal || ref.MessageID != msg.ParentID {
			continue
		}
		if id != "" {
			id = "" // ambiguous persisted mapping: never guess which operation was approved
			break
		}
		id = candidate
	}
	b.mu.Unlock()
	if id == "" {
		return false
	}

	fail := func(err error) {
		s.logf("bots: exact owner approval %s: %v", id, err)
		s.chat().Note(ctx, larkhost.Target{}, msg.MessageID, i18n.T(s.botLang(ctx), "assistant-channels_plugin.answer_not_sent",
			i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
	}
	ix, pending := s.pendingIx(id)
	if !pending {
		s.chat().Note(ctx, larkhost.Target{}, msg.MessageID, i18n.T(s.botLang(ctx), "assistant-channels_plugin.answered_elsewhere"))
		return true
	}
	// ChatID is empty only for the owner's DM. The pending interaction, rather
	// than the compact card mapping, is authoritative for the complete proposal.
	if ix.ID != id || ix.MachineID == "" || ix.SessionID == "" || !ownerActionProposal(ix) {
		fail(errOwnerActionProof)
		return true
	}
	b.mu.Lock()
	ref := b.st.Cards[id]
	validLocation := ref != nil && ref.ChatID == "" && ref.MessageID == msg.ParentID &&
		ref.ApprovalBatchID == ix.ApprovalBatchID && ref.ApprovalItemID == ix.ApprovalItemID &&
		ref.MachineID == ix.MachineID && ref.SessionID == ix.SessionID
	b.mu.Unlock()
	if !validLocation {
		fail(errOwnerActionProof)
		return true
	}

	expected := assistantwire.OwnerApprovalProof{MessageID: msg.MessageID, ParentID: msg.ParentID, ChatID: msg.ChatID, Text: text}
	var got struct {
		Proof assistantwire.OwnerApprovalProof `json:"owner_approval_proof"`
	}
	if err := s.appCall(ctx, AppInteractions, assistantwire.InteractionsOwnerAction, map[string]any{
		"interaction_id": id, "action": action, "channel": channel.Feishu, "kind": ix.Kind,
		"approval_reply": true, "message_id": expected.MessageID, "parent_id": expected.ParentID,
		"chat_id": expected.ChatID, "text": expected.Text,
	}, &got); err != nil {
		fail(err)
		return true
	}
	if got.Proof != expected {
		fail(errOwnerActionProof)
		return true
	}
	allow := true
	err := s.resolveOwnerProposal(ctx, ix, proto.Resolution{Allow: &allow}, ownerReplyProof{
		AuthenticatedOwner: true,
		DirectParent:       true,
		ExactPhrase:        true,
		EventID:            "feishu:" + got.Proof.MessageID,
	})
	if err != nil {
		if errors.Is(err, errNotPending) {
			s.chat().Note(ctx, larkhost.Target{}, msg.MessageID, i18n.T(s.botLang(ctx), "assistant-channels_plugin.answered_elsewhere"))
		} else {
			fail(err)
		}
	}
	return true
}
