package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/proto"
)

// A detached command approval does not make the manager a second shell. Its
// only continuation is a durable, one-time grant for the exact call the node
// already classified. The originating conversation retries that call after the
// result message arrives, and the node executes it through its ordinary hook.
const detachedGrantSetting = "assistant.detached_approval_grants"

var (
	detachedGrantTTL  = 24 * time.Hour
	detachedGrantKeep = 128
)

type detachedGrant struct {
	Fingerprint string    `json:"fingerprint"`
	EditedText  string    `json:"edited_text,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type detachedGrantBook struct {
	mu     sync.Mutex
	loaded bool
	grants map[string]detachedGrant // authoritative interaction id -> one unused approval
}

// detachedFingerprint deliberately compares the complete input bytes, not a
// parsed command or a route. Reformatting JSON is insufficient to consume a
// grant, which is conservative and keeps a changed call behind a fresh card.
func detachedFingerprint(ix proto.Interaction) (string, error) {
	b, err := json.Marshal(struct {
		Machine string          `json:"machine"`
		Session string          `json:"session"`
		Cwd     string          `json:"cwd"`
		Kind    string          `json:"kind"`
		Tool    string          `json:"tool"`
		Input   json.RawMessage `json:"input"`
	}{ix.MachineID, ix.SessionID, ix.ExecutionCwd, ix.Kind, ix.ToolName, ix.ToolInput})
	return string(b), err
}

func (s *Service) loadDetachedGrantsLocked(ctx context.Context) error {
	b := &s.detached
	if b.loaded {
		return nil
	}
	var grants map[string]detachedGrant
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, detachedGrantSetting, &grants); err != nil {
			return fmt.Errorf("read detached approval grants: %w", err)
		}
	}
	if grants == nil {
		grants = map[string]detachedGrant{}
	}
	b.grants, b.loaded = grants, true
	return nil
}

func (s *Service) saveDetachedGrantsLocked(ctx context.Context, grants map[string]detachedGrant) error {
	if st := s.store(); st != nil {
		return st.PutSetting(ctx, detachedGrantSetting, grants)
	}
	return nil
}

func cloneDetachedGrants(grants map[string]detachedGrant) map[string]detachedGrant {
	out := make(map[string]detachedGrant, len(grants))
	for id, grant := range grants {
		out[id] = grant
	}
	return out
}

// putDetachedGrant persists the grant before the approved result is journalled.
// A repeated Apply of the same card is idempotent; a different grant under the
// same card id is refused rather than replacing an unused owner decision.
func (s *Service) putDetachedGrant(ctx context.Context, ix proto.Interaction, r proto.Resolution) error {
	fingerprint, err := detachedFingerprint(ix)
	if err != nil {
		return err
	}
	b := &s.detached
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.loadDetachedGrantsLocked(ctx); err != nil {
		return err
	}
	if old, ok := b.grants[ix.ID]; ok {
		if old.Fingerprint == fingerprint && old.EditedText == r.EditedText {
			return nil
		}
		return errors.New("the approval already has a different continuation grant")
	}
	now := time.Now().UTC()
	grants := cloneDetachedGrants(b.grants)
	for id, grant := range grants {
		if !grant.ExpiresAt.After(now) {
			delete(grants, id)
		}
	}
	for len(grants) >= detachedGrantKeep {
		oldest := ""
		for id, grant := range grants {
			if oldest == "" || grant.CreatedAt.Before(grants[oldest].CreatedAt) {
				oldest = id
			}
		}
		delete(grants, oldest)
	}
	grants[ix.ID] = detachedGrant{Fingerprint: fingerprint, EditedText: r.EditedText,
		CreatedAt: now, ExpiresAt: now.Add(detachedGrantTTL)}
	if err := s.saveDetachedGrantsLocked(ctx, grants); err != nil {
		return fmt.Errorf("save detached approval grant: %w", err)
	}
	b.grants = grants
	return nil
}

// consumeDetachedGrant removes and persists one matching grant before allowing
// the retry. If that write fails, the in-memory grant remains and the command
// is not allowed; a later attempt cannot execute twice on a partially consumed
// approval. A changed input or a second retry creates no Allow.
func (s *Service) consumeDetachedGrant(ctx context.Context, ix proto.Interaction) (proto.Resolution, bool, error) {
	fingerprint, err := detachedFingerprint(ix)
	if err != nil {
		return proto.Resolution{}, false, err
	}
	b := &s.detached
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.loadDetachedGrantsLocked(ctx); err != nil {
		return proto.Resolution{}, false, err
	}
	ids := make([]string, 0, len(b.grants))
	for id := range b.grants {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, c := b.grants[ids[i]].CreatedAt, b.grants[ids[j]].CreatedAt
		if a.Equal(c) {
			return ids[i] < ids[j]
		}
		return a.Before(c)
	})
	now := time.Now().UTC()
	for _, id := range ids {
		grant := b.grants[id]
		if grant.Fingerprint != fingerprint || !grant.ExpiresAt.After(now) {
			continue
		}
		grants := cloneDetachedGrants(b.grants)
		delete(grants, id)
		if err := s.saveDetachedGrantsLocked(ctx, grants); err != nil {
			return proto.Resolution{}, false, fmt.Errorf("consume detached approval grant: %w", err)
		}
		b.grants = grants
		allow := true
		return proto.Resolution{Allow: &allow, ApprovalID: id, EditedText: grant.EditedText,
			Message: "Allowed once by the earlier approval of this exact call."}, true, nil
	}
	return proto.Resolution{}, false, nil
}

// detachedOriginalReplay identifies the original resumable request whose card has
// a late grant or any recorded terminal outcome. The unused grant is enough
// while the outcome write is still in flight; history then prevents an Always
// rule from replacing the original Allow, Deny, expiry or failed outcome.
func (s *Service) detachedOriginalReplay(ctx context.Context, ix proto.Interaction) (bool, error) {
	if !ix.Resumable {
		return false, nil
	}
	b := &s.detached
	b.mu.Lock()
	if err := s.loadDetachedGrantsLocked(ctx); err != nil {
		b.mu.Unlock()
		return false, err
	}
	_, unused := b.grants[ix.ID]
	b.mu.Unlock()
	if unused {
		return true, nil
	}
	st := s.fleetStore()
	if st == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	status, _, err := st.InteractionOutcome(ctx, ix.ID)
	if err != nil {
		return false, fmt.Errorf("read the original detached approval outcome: %w", err)
	}
	return status != "" && status != "pending", nil
}

// revokeDetachedGrant makes an interrupted or otherwise unsuccessful approval
// unusable. A failed persistent delete still removes the in-memory grant, so
// this process fails closed and the next recovery retries the durable delete.
func (s *Service) revokeDetachedGrant(ctx context.Context, id string) error {
	b := &s.detached
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.loadDetachedGrantsLocked(ctx); err != nil {
		return err
	}
	if _, ok := b.grants[id]; !ok {
		return nil
	}
	grants := cloneDetachedGrants(b.grants)
	delete(grants, id)
	if err := s.saveDetachedGrantsLocked(ctx, grants); err != nil {
		delete(b.grants, id)
		return fmt.Errorf("revoke detached approval grant: %w", err)
	}
	b.grants = grants
	return nil
}

// detachedContinuationPendingLocked reports whether the exact retry is still
// available. s.detached.mu is held across the result handoff too, so a matching
// call either consumes first (and the message becomes a no-retry notice) or the
// continuation reaches the conversation first.
func (s *Service) detachedContinuationPendingLocked(ctx context.Context, id string) (bool, error) {
	if err := s.loadDetachedGrantsLocked(ctx); err != nil {
		return false, err
	}
	grant, ok := s.detached.grants[id]
	return ok && grant.ExpiresAt.After(time.Now()), nil
}

// waitDetachedCard uses the prepared-approval coordinator for its first-answer-
// wins claim, clarification, durable result delivery and short initial wait.
// Apply creates a continuation grant only after the original caller was
// released. An immediate Allow returns through the original hook and leaves no
// unused second execution behind.
func (s *Service) waitDetachedCard(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
	state := &preparedApproval{caller: ctx, release: make(chan struct{}), deliveries: map[string]clarifyDelivery{}}
	state.op = approvalops.Operation{
		Denied:     "The owner denied this command. Nothing was executed. Do not retry it or create another approval for the same call.",
		Unanswered: "The approval ended without an owner Allow. Nothing was executed; do not retry unless the owner asks.",
		Apply: func(applyCtx context.Context, r proto.Resolution) (string, error) {
			s.fleet.mu.Lock()
			continued := state.clarified
			s.fleet.mu.Unlock()
			if !continued {
				if r.Remembered {
					return "A newer Always allow covers this exact command through the original hook.", nil
				}
				return "The owner allowed this exact command through the original hook.", nil
			}
			if r.Remembered {
				// A newer explicit Always decision covers this sibling card. Its
				// late card must not mint another one-time grant or ask the model to
				// repeat a call that a later owner turn may already have run.
				state.reportStatus = "covered"
				return "A newer Always allow covers this exact call. This older card executed nothing and requests no automatic retry; check whether the call already ran before making a later explicit call.", nil
			}
			if err := s.putDetachedGrant(applyCtx, ix, r); err != nil {
				return "", fmt.Errorf("%w: %v", approvalops.ErrNotApplied, err)
			}
			state.reportStatus = "approved"
			return detachedContinuationText(ix, r), nil
		},
	}
	res, err := s.waitInteractionPrepared(ctx, ix, state)
	if errors.Is(err, errApprovalClarifying) {
		s.fleet.mu.Lock()
		waiting := state.waiting
		s.fleet.mu.Unlock()
		text := approvalops.ClarifyingText
		if waiting {
			text = approvalops.WaitingText + " The command was not executed."
		}
		return proto.Resolution{Dismiss: true, Pending: true, ApprovalID: ix.ID, Message: text}, nil
	}
	if err != nil {
		return proto.Resolution{}, err
	}
	if state.err != nil {
		return proto.Resolution{}, state.err
	}
	if res.ApprovalID != "" && res.Allow != nil && *res.Allow {
		// A detached Allow is a continuation fact, never an execution permit. The
		// only Allow for a late-approved call comes from consumeDetachedGrant.
		// This also closes a resumable interaction.create replay after its grant
		// was consumed; an immediate Allow has no ApprovalID and keeps the ordinary
		// resumable-hook behavior.
		return proto.Resolution{Dismiss: true, ApprovalID: res.ApprovalID,
			Message: "The one-time continuation for this approval was already consumed or must be consumed by its exact retry; do not replay the original approval."}, nil
	}
	return res, nil
}

func detachedContinuationText(ix proto.Interaction, r proto.Resolution) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The owner approved one retry of this exact tool call. Nothing has run yet. Retry the same tool now with exactly this input: %s. The one-time grant is consumed by that retry; do not repeat the call after it.", ix.ToolInput)
	if r.EditedText != "" {
		fmt.Fprintf(&b, " The owner replaced the editable text with: %s. Use the normal edited-answer path for that retry.", r.EditedText)
	}
	return b.String()
}
