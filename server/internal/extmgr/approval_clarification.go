package extmgr

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

type storedClarificationProof struct {
	proof   assistantwire.ClarificationProof
	expires time.Time
}

// clarificationProofBook holds only short-lived, one-time proofs for questions
// whose fresh channel owner action was already consumed. It is process-local,
// like the prepared operation, and never grants execution authority.
type clarificationProofBook struct {
	entries sync.Map // token -> storedClarificationProof
}

func validClarificationProof(p assistantwire.ClarificationProof) bool {
	trimmed := strings.TrimSpace(p.Text)
	return p.InteractionID != "" && p.MachineID != "" && p.SessionID != "" && p.ClientMsgID != "" &&
		trimmed != "" && utf8.RuneCountInString(trimmed) <= 1000
}

func (b *clarificationProofBook) issue(p assistantwire.ClarificationProof) string {
	now := time.Now()
	b.entries.Range(func(key, value any) bool {
		if value.(storedClarificationProof).expires.Before(now) {
			b.entries.Delete(key)
		}
		return true
	})
	token := "cq_" + randomHex(12)
	b.entries.Store(token, storedClarificationProof{proof: p, expires: now.Add(ownerActionFor)})
	return token
}

func (b *clarificationProofBook) take(token string) (assistantwire.ClarificationProof, bool) {
	value, ok := b.entries.LoadAndDelete(token)
	if !ok {
		return assistantwire.ClarificationProof{}, false
	}
	stored := value.(storedClarificationProof)
	if stored.expires.Before(time.Now()) || !validClarificationProof(stored.proof) {
		return assistantwire.ClarificationProof{}, false
	}
	return stored.proof, true
}
