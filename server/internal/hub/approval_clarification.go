package hub

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/prompts"
)

const clarificationProofFor = 10 * time.Minute

type storedHubClarification struct {
	proof   assistantwire.ClarificationProof
	manager string
	expires time.Time
}

// hubClarificationProofBook holds full-device web clarification proofs. Channel
// proofs stay with the extension manager that verified their fresh owner action.
type hubClarificationProofBook struct {
	entries sync.Map // token -> storedHubClarification
}

func validHubClarification(p assistantwire.ClarificationProof) bool {
	trimmed := strings.TrimSpace(p.Text)
	return p.InteractionID != "" && p.MachineID != "" && p.SessionID != "" && p.ClientMsgID != "" &&
		trimmed != "" && utf8.RuneCountInString(trimmed) <= 1000
}

func (b *hubClarificationProofBook) issue(manager string, p assistantwire.ClarificationProof) string {
	now := time.Now()
	b.entries.Range(func(key, value any) bool {
		if value.(storedHubClarification).expires.Before(now) {
			b.entries.Delete(key)
		}
		return true
	})
	token := "cw_" + randomHex(12)
	b.entries.Store(token, storedHubClarification{proof: p, manager: manager, expires: now.Add(clarificationProofFor)})
	return token
}

func (b *hubClarificationProofBook) take(token, manager string) (assistantwire.ClarificationProof, bool) {
	value, ok := b.entries.LoadAndDelete(token)
	if !ok {
		return assistantwire.ClarificationProof{}, false
	}
	stored := value.(storedHubClarification)
	if stored.manager != manager || stored.expires.Before(time.Now()) || !validHubClarification(stored.proof) {
		return assistantwire.ClarificationProof{}, false
	}
	return stored.proof, true
}

func clarificationProofMatches(p assistantwire.ClarificationProof, interactionID, machineID, sessionID, clientMsgID, sentText string) bool {
	return p.InteractionID == interactionID && p.MachineID == machineID && p.SessionID == sessionID &&
		p.ClientMsgID == clientMsgID && prompts.ProtectUserMessage(p.Text) == sentText
}

func (h *Hub) takeClarificationProof(ctx context.Context, manager, token string) (assistantwire.ClarificationProof, error) {
	if strings.HasPrefix(token, "cw_") {
		if proof, ok := h.clarificationProofs.take(token, manager); ok {
			return proof, nil
		}
		return assistantwire.ClarificationProof{}, errors.New("clarification proof is unknown, expired, used, or owned by another manager")
	}
	var proof assistantwire.ClarificationProof
	if err := h.extCallTo(ctx, manager, extwire.HubClarificationTake, map[string]string{"proof": token}, &proof); err != nil {
		return assistantwire.ClarificationProof{}, err
	}
	if !validHubClarification(proof) {
		return assistantwire.ClarificationProof{}, errors.New("invalid clarification proof")
	}
	return proof, nil
}
