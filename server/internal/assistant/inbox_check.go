package assistant

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// checkInboxFollowUps looks at waiting matters whose time to look again came. A matter with nobody to look goes back
// to the user without a model call. Otherwise each woken processing conversation takes one of the pass's conversation
// slots; startInboxAgents counts these looking conversations again before it starts queued ones, so both use the same
// configured ceiling. New messages and the user's own answers keep their existing uncapped wake-up path.
func (s *Service) checkInboxFollowUps(ctx context.Context, cfg InboxAgentConfig, now time.Time) {
	b := &s.inbox
	now = now.UTC()
	b.mu.Lock()
	running := 0
	var due []*InboxItem
	for _, it := range b.items {
		if a := it.Agent; a != nil && a.SessionID != "" && (it.Handling == nil && a.State == AgentRunning ||
			it.Handling != nil && it.Handling.State == HandlingLooking) {
			// Keep this count the same as startInboxAgents': a conversation woken below joins it immediately.
			running++
		}
		if inboxFollowCheckDue(it, now) && it.Agent != nil && it.Agent.Of == it.Last {
			// Content the conversation has not seen goes through escalate first: it is news, not an empty due check. This
			// also leaves it waiting while the twin is off, so enabling the twin again still delivers that content.
			due = append(due, it)
		}
	}
	// The longest-waiting matter gets a free slot first; ids make ties deterministic.
	slices.SortFunc(due, func(a, b *InboxItem) int {
		if c := a.Follow.CheckAt.Compare(*b.Follow.CheckAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	var changed []*inboxWrite
	for _, it := range due {
		f := it.Follow
		hasConversation := it.Agent != nil && it.Agent.SessionID != ""
		admit := cfg.Enabled && hasConversation && f.Checks < inboxFollowUpAskChecks
		if admit && running >= agentConcurrency(cfg) {
			continue // still due: the next pass offers it any slot that has freed
		}
		w := beginWrite(it)
		switch {
		case !cfg.Enabled || !hasConversation:
			f.Due, f.CheckAt = true, nil
			it.Tier = TierAction
			b.setHandling(it, HandlingDone, "", now)
			b.addInboxCheckChain(it, "check_due", now)
		case f.Checks >= inboxFollowUpAskChecks:
			f.Due, f.CheckAt = false, nil
			it.Tier = TierAction
			b.setHandling(it, HandlingDone, "", now)
			b.chainAdd(it, InboxChainEntry{Kind: ChainCheck, At: now, Via: ViaAssistant,
				Text: i18n.T(b.userLang(), "assistant-inbox_chain.check_follow_up_ask")})
		default:
			f.Checks++
			f.Due, f.CheckAt = false, nil
			b.addInboxCheckChain(it, "check_started", now)
			b.tell(it, inboxNote{Name: prompts.NoteCheck, Who: f.Who, Next: f.Next, Checks: f.Checks}, true, now)
			b.setHandling(it, HandlingLooking, "", now)
			running++
		}
		b.settle(w)
		changed = append(changed, w)
	}
	b.mu.Unlock()
	s.saveWrites(ctx, changed, "its due follow-up checks")
}

// inboxFollowCheckDue is one open matter waiting for its scheduled look, with no suggestion already awaiting the user.
// The caller holds the inbox lock.
func inboxFollowCheckDue(it *InboxItem, now time.Time) bool {
	return it.open() && it.Handling != nil && it.Handling.State == HandlingWaiting && it.Follow != nil &&
		it.Follow.CheckAt != nil && !it.Follow.CheckAt.After(now) && currentSuggestion(it) == nil
}

// addInboxCheckChain records a server-written due-check step, with the next step when there is one. Under b.mu.
func (b *inboxBook) addInboxCheckChain(it *InboxItem, key string, now time.Time) {
	var args i18n.Args
	if next := it.Follow.Next; next != "" {
		key += "_next"
		args = i18n.Args{"next": next}
	}
	b.chainAdd(it, InboxChainEntry{Kind: ChainCheck, At: now, Via: ViaAssistant, Detail: it.Follow.Next,
		Text: i18n.T(b.userLang(), "assistant-inbox_chain."+key, args)})
}

// resumeInboxWaiting ends a turn that came from waiting without a suggestion: a recap-only answer, a timeout or an
// idle conversation all wait again rather than fail. The number of the check already made stays; waiting always has a
// next time, so a check that cleared it gets the default 24 hours. Under b.mu.
func (b *inboxBook) resumeInboxWaiting(it *InboxItem, now time.Time) {
	ensureInboxFollowCheckAt(it, now)
	if it.Agent != nil {
		it.Agent.Wake = false
	}
	b.setHandling(it, HandlingWaiting, "", now)
}
