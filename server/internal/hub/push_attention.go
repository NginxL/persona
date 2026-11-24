package hub

import "github.com/nwverse/persona/server/internal/assistantwire"

// Attention modes, as the hub delivers pushes in them (#13503, docs/server/server.md "Attention modes"). The mode is the
// assistant's to keep: the user sets it through the manager's routes, and the manager reports it in its profile
// (assistantwire.Profile.Attention), which the hub keeps across a restart like the rest of the profile. The hub only
// reads it when it filters a push: in a mode other than normal the grading filter runs whatever hub.ln_mode says, and
// assistantwire.DecideAttention says what the mode does to each level. The hub never writes hub.ln_mode for a mode, so
// when the mode ends (the hub reads its end itself, Attention.Effective) the switch is what it was.

// attentionMode is the attention mode in the assistant's last profile (nil: normal).
func (h *Hub) attentionMode() *assistantwire.Attention {
	return h.assistantProfile().Attention
}

// attentionNow is the mode in force now: normal when none was reported or its time is over.
func (p *Pusher) attentionNow() assistantwire.Attention {
	if p == nil || p.attention == nil {
		return assistantwire.Attention{Mode: assistantwire.AttentionNormal}
	}
	return p.attention().Effective(p.now())
}

// filtering: the grading filter runs for this push: an attention mode is in force, or hub.ln_mode is on.
func (p *Pusher) filtering() bool {
	if p == nil {
		return false
	}
	return p.attentionNow().Active() || p.pushGradingOn()
}

// attentionHushes: quiet mode is in force, so every push it lets go goes without a sound (hushed): the stricter of it
// and a device's quiet hours.
func (p *Pusher) attentionHushes() bool {
	return p != nil && p.attentionNow().Mode == assistantwire.AttentionQuiet
}

// hushRule is rule as the attention mode leaves it: every device hushed in quiet mode, else rule unchanged.
func (p *Pusher) hushRule(rule quietRule) quietRule {
	if p.attentionHushes() {
		return quietHushAll
	}
	return rule
}

// attentionPush is what the attention filter knows of policy, at level. The track counts only from the manager that
// reports the mode (the built-in assistant, whose tracks the focus names): another manager could name the track in
// focus to slip a push past immersive mode, so its pushes carry none.
func attentionPush(policy pushPolicy, level string) assistantwire.AttentionPush {
	track := ""
	if policy.manager == assistantwire.AssistantID {
		track = policy.track
	}
	return assistantwire.AttentionPush{Level: level, Depth: policy.depth, Track: track, MachineID: policy.machineID,
		SessionID: policy.sessionID, Done: policy.done, System: policy.system}
}

// applyAttentionLocked is applyPushGrading in attention mode attn (in force): the push is held, with the mode's reason,
// or goes; a P0 that goes keeps its key quiet for the window as with hub.ln_mode on. gradingMu is held.
func (p *Pusher) applyAttentionLocked(attn assistantwire.Attention, policy pushPolicy, level string, in heldPushRecord) (allow bool, reason string) {
	v := assistantwire.DecideAttention(attn, attentionPush(policy, level))
	if v.Hold {
		switch in.Level {
		case assistantwire.LevelP0, assistantwire.LevelP2:
		default:
			in.Level = assistantwire.LevelP1
		}
		in.Reason = v.Reason
		p.addInterceptLocked(in, false)
		return false, v.Reason
	}
	if in.RelatedKey != "" {
		p.noteKeyPushedLocked(in.RelatedKey, in.At)
	}
	p.addSentLocked(pushSent{At: in.At, Level: level, RelatedKey: in.RelatedKey, Manager: policy.manager})
	return true, ""
}
