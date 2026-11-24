package hub

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

const (
	healthWaterCategory = "HEALTH_WATER"
	healthWaterCollapse = "health-water"
)

// healthWaterPayload carries the fixed category's button context. The manager owns the
// reminder's schedule, localized copy and records; the hub only delivers it.
func healthWaterPayload(n assistantwire.Notification) map[string]any {
	return map[string]any{
		"aps": map[string]any{
			"alert": map[string]any{"title": n.Title, "body": n.Body},
			"sound": "default", "category": healthWaterCategory, "thread-id": healthWaterCollapse,
			"interruption-level": "active",
		},
		"id": healthWaterCollapse, "health": map[string]any{"reminder": n.Health.Reminder},
	}
}

func (h *Hub) notifyHealthWater(owner string, n assistantwire.Notification) (*pushOutcome, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defaultOwner, err := h.defaultOwnerOrErr(ctx)
	if err != nil {
		return nil, err
	}
	if owner != defaultOwner {
		return nil, errors.New("only the default manager may send a water reminder")
	}
	if n.Health == nil || n.Health.Reminder == "" || len(n.Health.Reminder) > 128 ||
		strings.TrimSpace(n.Health.Reminder) != n.Health.Reminder || strings.ContainsAny(n.Health.Reminder, "\x00\r\n") ||
		n.DeviceID != "" || n.SessionID != "" || n.RelatedKey != "" || n.Level != assistantwire.LevelP1 {
		return nil, errors.New("a water reminder needs its reminder id, P1 and no conversation or related key")
	}
	build := func(i18n.Lang) map[string]any { return healthWaterPayload(n) }
	// Match /notify: input preferences, muted managers and phone/desktop presence; no quiet hours.
	wants := alertWanted((pushWants{Kind: "input"}).fn(), h.desktopOnline())
	policy := pushPolicy{level: assistantwire.LevelP1, manager: owner, title: n.Title, body: n.Body}
	selected, reachable, held := h.push.sendFromPolicy(owner, policy, build, healthWaterCollapse, wants, quietNone, nil, nil)
	var ws wsReach
	if held == "" {
		ws = h.notifyWSFrom(owner, kindAlert, build, wants, quietNone)
	}
	return &pushOutcome{devices: selected + ws.selected, reachable: reachable + ws.reachable, kept: ws.kept, held: held}, nil
}
