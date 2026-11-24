package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// planArgsWithStep is planArgs(false) with the task's owner_step set to step.
func planArgsWithStep(t *testing.T, step string) json.RawMessage {
	t.Helper()
	var in map[string]any
	if err := json.Unmarshal(planArgs(false), &in); err != nil {
		t.Fatal(err)
	}
	in["tasks"].([]any)[0].(map[string]any)["owner_step"] = step
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Decision rights: the main conversation is the supervisor of what it plans, so its writable plan starts at once
// without the owner having asked for that very task (user_requested false): no card, no push.
func TestMainPlanStartsAtOnce(t *testing.T) {
	s, f := newQuietService(t)
	s.mainPlansWait = false
	ctx := context.Background()
	res, err := s.toolTaskPlan(ctx, "conv1", planArgs(false))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "without asking") {
		t.Fatalf("the main conversation's plan waits on a card: %s", res)
	}
	waitFor(t, "the main conversation's task to start", func() bool { return len(onlyTodo(s).Agents) == 1 })
	if got := f.pushed(); len(got) != 0 {
		t.Fatalf("the main conversation's plan was pushed: %+v", got)
	}
}

// A task that spends money, speaks outward as the owner or takes an irreversible step they have not agreed to still
// waits for their Start, from the main conversation as from a lead, and its push is counted as that kind of the six.
func TestOwnerStepKeepsTheCard(t *testing.T) {
	for step, want := range map[string]struct {
		source string
		kind   int
	}{
		ownerStepMoney:        {cardPlanMoney, cardCatMoney},
		ownerStepOutward:      {cardPlanOutward, cardCatOutward},
		ownerStepIrreversible: {cardPlanIrreversible, cardCatIrreversible},
	} {
		for _, self := range []string{"conv1", "lead1"} {
			s, f := newQuietService(t)
			s.mainPlansWait = false
			setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
			f.known["lead1"] = true
			ctx := context.Background()
			res, err := s.toolTaskPlan(ctx, self, planArgsWithStep(t, step))
			if err != nil {
				t.Fatalf("%s %s: %v", self, step, err)
			}
			if strings.Contains(res, "without asking") || !strings.Contains(res, "card") {
				t.Fatalf("%s %s: the plan did not wait on its card: %s", self, step, res)
			}
			s.todos.mu.RLock()
			started := len(s.todos.todos)
			s.todos.mu.RUnlock()
			if started != 0 {
				t.Fatalf("%s %s: %d task(s) started before the owner's Start", self, step, started)
			}
			if got := f.pushed(); len(got) != 1 {
				t.Fatalf("%s %s: the card, with the owner away, pushes once: %+v", self, step, got)
			}
			if c := cardsToday(t, s)[want.source]; c.Category != want.kind || c.N != 1 {
				t.Errorf("%s %s: counted %+v, want %s kind %d once", self, step, c, want.source, want.kind)
			}
		}
	}
}

// owner_step takes only the three kinds; anything else is refused before a plan exists.
func TestOwnerStepRefusesAnUnknownKind(t *testing.T) {
	s, _ := newQuietService(t)
	s.mainPlansWait = false
	_, err := s.toolTaskPlan(context.Background(), "conv1", planArgsWithStep(t, "priority"))
	if err == nil || !strings.Contains(err.Error(), "owner_step") {
		t.Fatalf("an unknown owner_step was taken: %v", err)
	}
}

// An ordinary conversation is not the supervisor of what it plans (the main conversation supervises it): its writable
// plan still waits on its card.
func TestOrdinaryConversationPlanStillWaits(t *testing.T) {
	s, f := newQuietService(t)
	s.mainPlansWait = false
	f.known["side1"] = true
	res, err := s.toolTaskPlan(context.Background(), "side1", planArgs(false))
	if err != nil || strings.Contains(res, "without asking") || !strings.Contains(res, "card") {
		t.Fatalf("an ordinary conversation's writable plan did not wait on its card: %s %v", res, err)
	}
}
