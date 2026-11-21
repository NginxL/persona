package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	ownerActionPrepareTool = "owner_action_prepare"
	ownerActionBatchTool   = "owner_action_batch"
	// A proposal must fit in one Feishu card without safeCard replacing its
	// contents. Larger operations retain the ordinary approval flow.
	ownerActionMaxDisplay = 12000
)

func (s *Service) ownerActions() (*ownerActionBook, error) {
	book, ok := newOwnerActionBook(s.store())
	if !ok {
		return nil, errors.New("exact owner-action approval needs a persistent revision-aware database")
	}
	return book, nil
}

func ownerActionInteractionID(batchID, itemID string) string {
	if itemID != "" {
		return "ix_owner_item_" + strings.TrimPrefix(itemID, "oai_")
	}
	return "ix_owner_batch_" + strings.TrimPrefix(batchID, "oab_")
}

func ownerActionProposal(ix proto.Interaction) bool {
	if !ix.OwnerProposal || ix.Kind != proto.IxPermission || ix.ApprovalBatchID == "" {
		return false
	}
	if ix.ApprovalItemID != "" {
		return ix.ID == ownerActionInteractionID(ix.ApprovalBatchID, ix.ApprovalItemID) && ix.ToolName != ownerActionBatchTool
	}
	return ix.ID == ownerActionInteractionID(ix.ApprovalBatchID, "") && ix.ToolName == ownerActionBatchTool
}

func persistentOwnerProposal(ix proto.Interaction) bool {
	return ownerActionProposal(ix) && ix.Resumable && ix.NoExpiry
}

func ownerActionDisplay(item ownerActionItem) (string, error) {
	if len(item.Canonical) > ownerActionMaxDisplay {
		return "", errors.New("owner-action item is too large to display in full; use its ordinary approval card")
	}
	// RedactArgs is the existing channel privacy boundary. Secret values still
	// participate in the fingerprint, so changing one requires fresh approval,
	// but they are not copied into a chat card.
	redacted := proto.RedactArgs(json.RawMessage(item.Canonical))
	if redacted == "" {
		return "", errors.New("owner-action input cannot be displayed")
	}
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(redacted), "", "  "); err != nil {
		return "", err
	}
	if out.Len() > ownerActionMaxDisplay {
		return "", errors.New("owner-action item is too large to display in full; use its ordinary approval card")
	}
	return out.String(), nil
}

func (s *Service) toolOwnerActionPrepare(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	cfg := s.settings(ctx)
	persistent := persistentOwnerActionCardsOn(cfg)
	if !ownerReplyApprovalOn(cfg) {
		return "", errors.New("exact owner-reply approval is disabled in Settings")
	}
	var in struct {
		Items []struct {
			Tool  string          `json:"tool"`
			Input json.RawMessage `json:"input"`
		} `json:"items"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return "", err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return "", errors.New("owner_action_prepare accepts exactly one JSON object")
	}
	if s.ext == nil {
		return "", errors.New("connector tools are unavailable")
	}
	drafts := make([]ownerActionDraft, 0, len(in.Items))
	for _, item := range in.Items {
		known, readOnly := s.ext.ToolAccess(item.Tool)
		if !known || readOnly || !s.ext.ConnectorTool(item.Tool) {
			return "", fmt.Errorf("%s is not a known connector action; prepare only exact side-effecting connector calls", item.Tool)
		}
		canonical, err := canonicalOwnerAction(item.Input)
		if err != nil {
			return "", err
		}
		if len(canonical) > ownerActionMaxDisplay {
			return "", errors.New("an item is too large to display in full; call it directly for its ordinary approval card")
		}
		drafts = append(drafts, ownerActionDraft{Tool: item.Tool, Input: canonical})
	}
	book, err := s.ownerActions()
	if err != nil {
		return "", err
	}
	batch, err := book.createPersistent(ctx, machine, self, drafts, persistent)
	if err != nil {
		return "", err
	}
	for _, item := range batch.Items {
		if _, err := ownerActionDisplay(item); err != nil {
			_ = book.cancelPending(context.Background(), batch.ID)
			return "", err
		}
	}
	phrases, err := ownerApprovalPhrases(cfg)
	if err != nil {
		_ = book.cancelPending(context.Background(), batch.ID)
		return "", err
	}
	for _, item := range batch.Items {
		display, _ := ownerActionDisplay(item)
		ix := proto.Interaction{
			ID:                   ownerActionInteractionID(batch.ID, item.ID),
			MachineID:            machine,
			SessionID:            self,
			Kind:                 proto.IxPermission,
			CreatedAt:            batch.CreatedAt,
			ToolName:             item.Tool,
			ToolInput:            json.RawMessage(item.Canonical),
			Summary:              display,
			ApprovalBatchID:      batch.ID,
			ApprovalItemID:       item.ID,
			OwnerProposal:        true,
			OwnerApprovalPhrases: phrases,
			Resumable:            true,
			NoExpiry:             persistent,
		}
		notKept := false
		ix.AlwaysHonored = &notKept
		// Item proposals have no process-local Apply closure. Their waiter only
		// owns timeout cleanup; approval and execution live in the durable book.
		ready := make(chan struct{})
		go func() { _, _ = s.waitInteractionReady(context.Background(), ix, nil, ready) }()
		<-ready
	}
	batchIX := proto.Interaction{
		ID:                   ownerActionInteractionID(batch.ID, ""),
		MachineID:            machine,
		SessionID:            self,
		Kind:                 proto.IxPermission,
		CreatedAt:            batch.CreatedAt,
		ToolName:             ownerActionBatchTool,
		Summary:              i18n.T(s.lang(ctx), "assistant-owner_actions.batch", i18n.Args{"count": len(batch.Items), "phrases": strings.Join(phrases, " / "), "id": batch.ID}),
		ApprovalBatchID:      batch.ID,
		OwnerProposal:        true,
		OwnerApprovalPhrases: phrases,
		Resumable:            true,
		NoExpiry:             persistent,
	}
	notKept := false
	batchIX.AlwaysHonored = &notKept
	decided := false
	defer func() {
		if !decided && !persistent {
			_ = book.cancelPending(context.Background(), batch.ID)
			_ = s.syncOwnerActionBatch(context.Background(), batch.ID)
		}
	}()
	// Register the batch and every item before projecting decisions. A second
	// resumable waiter below is the original tool caller; the background waiter
	// retains timeout cleanup if that caller goes away.
	ready := make(chan struct{})
	go func() { _, _ = s.waitInteractionReady(context.Background(), batchIX, nil, ready) }()
	<-ready
	// Close the registration race in either direction: an item decision may
	// complete before the batch card exists, or a batch decision before an item
	// card exists. Durable state is authoritative in both cases.
	// Decision projection belongs to the durable proposal, not to the lifetime
	// of whichever tool caller happens to be waiting for it.
	syncCtx := context.WithoutCancel(ctx)
	if err := s.syncOwnerActionBatch(syncCtx, batch.ID); err != nil && !errors.Is(err, errNotPending) {
		return "", err
	}
	resolution, err := s.waitInteraction(ctx, batchIX)
	if err == nil && !resolution.Dismiss {
		if err := s.syncOwnerActionBatch(syncCtx, batch.ID); err != nil && !errors.Is(err, errNotPending) {
			return "", err
		}
	}
	if err != nil {
		return "", err
	}
	if resolution.Dismiss {
		if persistent {
			return "The exact approval batch is still pending in the original conversation and channels. Do not execute any item until its approval arrives.", nil
		}
		return "The exact approval batch closed unanswered. Nothing was approved or executed.", nil
	}
	if resolution.Allow == nil {
		return "", errors.New("exact owner-action proposals accept only Allow or Deny")
	}
	decided = true
	current, found, err := book.batch(ctx, batch.ID)
	if err != nil || !found {
		return "", errors.New("the approved batch could not be read back")
	}
	if !*resolution.Allow {
		return "The owner denied this exact batch. Do not execute any of its items.", nil
	}
	var out strings.Builder
	out.WriteString("The owner approved the following exact operations. Call each listed connector tool exactly once with the canonical arguments below, without edits. Any other call needs its own approval. Do not retry an item after an uncertain external result.\n")
	approved := 0
	for _, item := range current.Items {
		if item.State != ownerActionGranted && item.State != ownerActionConsumed {
			continue
		}
		approved++
		fmt.Fprintf(&out, "\nItem %s\nTool: %s\nArguments: %s\n", item.ID, item.Tool, item.Canonical)
	}
	if approved == 0 {
		return "The batch has no remaining approved items. Execute nothing.", nil
	}
	return out.String(), nil
}

func ownerActionCardProof(ix proto.Interaction, source string) ownerReplyProof {
	return ownerReplyProof{
		AuthenticatedOwner: true,
		DirectParent:       true, // a press is bound to this exact card, stronger than a text parent
		ExactPhrase:        true, // Allow/Deny is an explicit typed answer
		EventID:            "card:" + source + ":" + ix.ID,
		BatchID:            ix.ApprovalBatchID,
		ItemID:             ix.ApprovalItemID,
	}
}

func (s *Service) resolveOwnerProposal(ctx context.Context, ix proto.Interaction, r proto.Resolution, proof ownerReplyProof) error {
	if !ownerActionProposal(ix) || r.Allow == nil || r.Always || r.EditedText != "" || len(r.Values) > 0 {
		return errOwnerActionProof
	}
	proof.BatchID, proof.ItemID = ix.ApprovalBatchID, ix.ApprovalItemID
	book, err := s.ownerActions()
	if err != nil {
		return err
	}
	if _, err := book.decide(ctx, proof, *r.Allow); err != nil {
		return err
	}
	return s.syncOwnerActionBatch(ctx, ix.ApprovalBatchID)
}

// syncOwnerActionBatch projects durable item decisions onto the original
// cards. It never executes an operation and is safe to call after a restart or
// a duplicate reply.
func (s *Service) syncOwnerActionBatch(ctx context.Context, batchID string) error {
	book, err := s.ownerActions()
	if err != nil {
		return err
	}
	batch, found, err := book.batch(ctx, batchID)
	if err != nil {
		return err
	}
	if !found {
		return errOwnerActionProof
	}
	result := ownerActionDecisionResult{}
	ownerActionFillResult(batch, &result)
	for _, item := range batch.Items {
		if item.State == ownerActionPending {
			continue
		}
		allow := item.State == ownerActionGranted || item.State == ownerActionConsumed
		r := proto.Resolution{Allow: &allow, Reason: "exact owner-action decision " + batch.ID}
		id := ownerActionInteractionID(batch.ID, item.ID)
		if _, pending := s.pendingIx(id); !pending {
			continue
		}
		if err := s.resolveAnswer(ctx, id, r, true, nil, nil); err != nil && !errors.Is(err, errNotPending) {
			return err
		}
	}
	if result.Complete {
		r := proto.Resolution{Allow: &result.Allow, Reason: "exact owner-action decision " + batch.ID}
		id := ownerActionInteractionID(batch.ID, "")
		if _, pending := s.pendingIx(id); pending {
			if err := s.resolveAnswer(ctx, id, r, true, nil, nil); err != nil && !errors.Is(err, errNotPending) {
				return err
			}
		}
	}
	return nil
}
