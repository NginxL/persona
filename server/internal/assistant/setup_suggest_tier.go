package assistant

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// A lower tier for the background supervisor, as one more line of the weekly block (#5805; section 9c of
// docs/design/proposals/main-chat-availability.md, docs/assistant/assistant.md "Recommended setup"). Unlike the other
// lines it is not a mechanism that is off: it says that the reviews the background supervisor made on the model its
// purpose (purposeSupervise) is bound to held, and that the provider it runs on has a tier below that one which is
// not its smallest. It is the block's in everything else: computed from the task_reviewed and todo_reopened rows of
// availability_supervision.go with no model call, named at most once in setupQuietFor, and never acted on (the
// binding is the owner's to change in Model Settings).
//
// Only down: nothing here suggests a stronger tier, whatever the numbers.
const (
	// setupTierMin is how many settled acceptances (supervisorModel.Settled) the background supervisor must have made
	// on its current model within setupLookback before its reviews are judged at all.
	setupTierMin = 20
	// setupTierReopenedIn: the reviews held when at most one in this many of the settled acceptances was reopened
	// (reopened over settled at most 5 percent). Never over accepted: an acceptance younger than
	// supervisionReopenWithin has not had its day to be reopened in.
	setupTierReopenedIn = 20
)

// tierBelow is the model one tier below current on managed launcher l, and whether the manager can tell there is one
// that is not the launcher's smallest. All it knows of tiers is the launcher's own tier models, which the owner (or
// the import of their shell alias) filled in, smallest first; an empty slot is a tier the launcher does not name.
// The smallest slot is read as the node runs it (providerEnv in node/launcherenv.go): HaikuModel, else SmallModel,
// which is where the web's provider form keeps its "light" tier.
// current must be one of them (at its lowest slot, when it fills several), the tier below is the nearest named slot
// under it with another model, and under that there must be yet another named one: acceptance is a judgment call, so
// the supervisor is never pointed at the smallest tier, nor at a tier the manager cannot tell is not the smallest.
// Agent presets (ModelProfile.Tiers) are no ladder: they have names and descriptions, no order.
func tierBelow(l proto.ManagedLauncher, current string) (string, bool) {
	slots := []string{cmp.Or(l.HaikuModel, l.SmallModel), l.SonnetModel, l.OpusModel, l.FableModel}
	for i := range slots {
		slots[i], _ = strip1M(slots[i])
	}
	if current == "" {
		return "", false
	}
	// below is the nearest named slot under i whose model is none of not; -1 when there is none.
	below := func(i int, not ...string) int {
		for j := i - 1; j >= 0; j-- {
			if slots[j] != "" && !slices.Contains(not, slots[j]) {
				return j
			}
		}
		return -1
	}
	for i, m := range slots {
		if m != current {
			continue
		}
		j := below(i, current)
		if j < 0 || below(j, current, slots[j]) < 0 {
			return "", false
		}
		return slots[j], true
	}
	return "", false
}

// tierModelName is what the owner's provider calls model id: the name its record has for it (ProviderModel.Name, what
// the model picker of Model Settings lists it under), else the id itself.
func tierModelName(l proto.ManagedLauncher, id string) string {
	for _, m := range l.Models {
		if got, _ := strip1M(m.ID); got == id && m.Name != "" {
			return m.Name
		}
	}
	return id
}

// supervisorTier is what setupTierOf found: the model id the background supervisor's purpose runs on and the one a
// tier below it, and what the provider calls the two (tierModelName).
type supervisorTier struct {
	model, lower         string
	modelName, lowerName string
}

// setupTierOf says what the legacy background supervisor's purpose runs on and the tier below it: ok only while an
// already configured background supervisor remains enabled to finish its old work (#5732), its purpose resolves to a
// provider added in Persona that starts a CLI, and tierBelow can tell. New work is supervised by the planning lead or main and does not
// create or enable this endpoint. Anything that cannot be read is "not known", and nothing is suggested.
func (s *Service) setupTierOf(ctx context.Context) (supervisorTier, bool) {
	cfg := s.settings(ctx)
	if !s.backgroundOn(cfg) || cfg.BackgroundSupervisor == "" || cfg.BackgroundSupervisor == cfg.Main {
		return supervisorTier{}, false
	}
	b, err := s.resolveModel(ctx, purposeSupervise)
	if err != nil {
		return supervisorTier{}, false
	}
	name, managed := strings.CutPrefix(b.Provider, "launcher.")
	list, known := s.storedLaunchersKnown(ctx)
	if !managed || !known {
		return supervisorTier{}, false
	}
	for _, l := range list {
		if l.Name != name || l.Kind == "api" {
			continue
		}
		current, _ := strip1M(cmp.Or(b.Model, l.Model))
		lower, ok := tierBelow(l.ManagedLauncher, current)
		return supervisorTier{model: current, lower: lower,
			modelName: tierModelName(l.ManagedLauncher, current), lowerName: tierModelName(l.ManagedLauncher, lower)}, ok
	}
	return supervisorTier{}, false
}

// setupTierCount reads, out of events (task_reviewed and todo_reopened since the lookback, oldest first), how the
// background supervisor's acceptances on model held: n the settled ones, reopened those of them reopened within
// supervisionReopenWithin, by the one rule of summarizeSupervision. Only the background supervisor's own reviews on
// exactly that model count (its role and model as the row recorded them, any effort): a review by the main
// conversation, a leader or a task leader's agent, and one made on another model, says nothing about this binding.
// A reopen is held against the background supervisor's latest acceptance of the task before it even when another
// conversation accepted the task in between, which can only make the share higher.
func setupTierCount(events []db.WorkEvent, model string, since, now time.Time) setupNumber {
	var own []db.WorkEvent
	for _, e := range events {
		if e.Kind == taskReviewedEvent {
			role, _ := e.Detail["role"].(string)
			ran, _ := e.Detail["model"].(string)
			if ran, _ = strip1M(ran); role != supervisorBackground || ran != model {
				continue
			}
		}
		own = append(own, e)
	}
	c := setupNumber{model: model}
	for _, m := range summarizeSupervision(since, now, own, time.UTC).ByModel {
		c.n += m.Settled
		c.reopened += m.Reopened
	}
	return c
}

// setupTier is the tier line's number for a run at now, from ev's reviews, its two models under the names the
// provider has for them; ok false when the manager cannot tell a
// lower tier that is not the smallest (setupTierOf). Whether the number is enough is suggests'.
func (s *Service) setupTier(ctx context.Context, ev setupEvidence, now time.Time) (setupNumber, bool) {
	tier, ok := s.setupTierOf(ctx)
	if !ok {
		return setupNumber{}, false
	}
	c := setupTierCount(ev.reviews, tier.model, now.Add(-setupLookback), now)
	c.model, c.lower = tier.modelName, tier.lowerName // the line names them as the owner's provider does
	return c, true
}
