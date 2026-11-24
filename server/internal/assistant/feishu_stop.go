package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
)

// stopProgressCard accepts the existing button payload only while the callback's
// actual message still belongs to a current, watched Feishu run. History and thread
// bindings cannot grant this ownership. Owner/chat authorization remains in extmgr.
func (s *Service) stopProgressCard(ctx context.Context, f FeishuFeatures, t botTarget, mid string) stopOutcome {
	l := s.botLang(ctx)
	if err := botAllowed(l, f, t); err != nil {
		return stopOutcome{kind: "error", message: err.Error()}
	}
	expired := stopOutcome{kind: "info", message: i18n.T(l, "assistant-feishu_cards.card_expired")}
	if mid == "" || t.MachineID == "" || t.SessionID == "" {
		return expired
	}
	c := &s.chans
	c.mu.Lock()
	var run *channel.Run
	var prog *chanLive
	for _, r := range c.st.Runs {
		if r.Channel == channel.Feishu && botTargetOf(r) == t && r.Ext["card"] == mid {
			run, prog = r, c.live[r.ID]
			break
		}
	}
	c.mu.Unlock()
	if run == nil || prog == nil {
		return expired
	}
	rt, err := s.resolveBotTarget(ctx, t)
	if err != nil || rt.isNew || rt.machine != t.MachineID || rt.session.SessionID != t.SessionID {
		return expired
	}
	// Match the lifecycle's lock order (progress, then channel state). Resolution
	// can yield to a finish, fold, recall or replacement; recheck the actual objects.
	prog.mu.Lock()
	defer prog.mu.Unlock()
	c.mu.Lock()
	current := false
	for _, r := range c.st.Runs {
		if r == run {
			current = c.live[r.ID] == prog && r.Channel == channel.Feishu && botTargetOf(r) == t &&
				r.Ext["card"] == mid && !r.Recalled && !c.answering[r.ID] && !prog.finished && prog.failing.IsZero() &&
				prog.stoppedCard != mid && (!turnOver(rt.session) || !r.Seen && !r.Held)
			break
		}
	}
	c.mu.Unlock()
	if !current {
		return expired
	}
	// Keep the progress lock through delivery: concurrent presses act once, while a
	// failed RPC stays retryable. This is an assistant-side ownership check; the
	// node's conversation-level RPC has no atomic turn-generation precondition.
	out := s.interruptBotConversation(ctx, rt)
	if out.kind == "success" {
		prog.stoppedCard = mid
	}
	return out
}
