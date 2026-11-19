package assistant

import (
	"maps"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// mailNoticeFailures gives automatic recovery two complete mail rounds before a
// transient read failure appears in the apps. A refused login needs action now.
const mailNoticeFailures = 3

// recordMailReads updates visible notices under b.mu. Polls without mail preserve
// state; a complete mail round replaces it, clearing recovered or removed accounts.
// Counts are bounded, memory-only, and independent of connector read diagnostics.
func (b *inboxBook) recordMailReads(r assistantwire.InboxRound) bool {
	if _, there := r.Names["mail"]; r.Mail == nil && (r.Names == nil || there) {
		return false
	}
	notices := map[string]assistantwire.InboxMailFailed{}
	failures := map[string]int{}
	if r.Mail != nil {
		for _, f := range r.Mail.Failed {
			if f.ID == "" || f.ReadError == "" {
				continue
			}
			n := min(b.mailReadFailures[f.ID]+1, mailNoticeFailures)
			failures[f.ID] = n
			_, alreadyShown := b.mailReadErrs[f.ID]
			if f.ReadError == assistantwire.ReadLoginRefused || n == mailNoticeFailures || alreadyShown {
				notices[f.ID] = f
			}
		}
	}
	changed := !maps.Equal(notices, b.mailReadErrs)
	b.mailReadErrs, b.mailReadFailures = notices, failures
	return changed
}
