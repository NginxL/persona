package extmgr

import (
	"context"
	"encoding/json"
	"math"

	"github.com/nwverse/persona/server/internal/i18n"
)

// botNudgeAfterMax bounds nudge_after (six days, in minutes): the chat keeps a card's record seven days, and the nudge
// may come up to a day after it is due.
const botNudgeAfterMax = 6 * 24 * 60

// nudgeAfterValue reads nudge_after from a request: nil when it is absent (or null), else a whole number of minutes
// from 0 to botNudgeAfterMax; anything else (a fraction, a string, out of range) is the same error.
func nudgeAfterValue(raw json.RawMessage) (*int, error) {
	return botMinutesValue(raw, botNudgeAfterMax, "hub-bots.nudge_after_range")
}

func botMinutesValue(raw json.RawMessage, max int, rangeKey string) (*int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil || f != math.Trunc(f) || f < 0 || f > float64(max) {
		return nil, i18n.Err(rangeKey, i18n.Args{"max": max})
	}
	n := int(f)
	return &n, nil
}

// setNudgeAfter keeps nudge_after (PUT /api/v1/bots/feishu): the minutes a card waits before the assistant nudges
// under it once, 0 off (docs/extensions/bots.md "Nudging a card nobody answered"). The range is checked by the caller.
func (h *Manager) setNudgeAfter(ctx context.Context, minutes int) error {
	b := &h.bots
	b.mu.Lock()
	if err := h.loadBotsLocked(ctx); err != nil {
		b.mu.Unlock()
		return err
	}
	previous := b.set.NudgeAfter
	changed := b.set.nudgeAfter() != minutes
	b.set.NudgeAfter = &minutes
	if err := h.saveBotSettingsLocked(); err != nil {
		b.set.NudgeAfter = previous
		b.mu.Unlock()
		return err
	}
	b.mu.Unlock()
	if changed {
		h.auditBot(ctx, botRecord{auditBotNudgeAfter, map[string]any{"minutes": minutes}})
	}
	return nil
}
