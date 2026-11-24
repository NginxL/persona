package assistant

// The owner's own kinds of step a planned task can declare (PlanTask.OwnerStep): what keeps a plan waiting for the
// owner's Start when its supervisor releases it. Kinds 4, 3 and 5 of the six the owner decides (card_layers.go).
const (
	ownerStepMoney        = "money"
	ownerStepOutward      = "outward"
	ownerStepIrreversible = "irreversible"
)

func validOwnerStep(step string) bool {
	switch step {
	case "", ownerStepMoney, ownerStepOutward, ownerStepIrreversible:
		return true
	}
	return false
}

// planOwnerStep is the first owner step a plan's tasks declare, or "".
func planOwnerStep(tasks []PlanTask) string {
	for _, t := range tasks {
		if t.OwnerStep != "" {
			return t.OwnerStep
		}
	}
	return ""
}

// planCardSource is the card ledger's source for a plan waiting for Start: the owner step it waits for, or the plain
// plan card (none of the six) when it waits for something else.
func planCardSource(step string) string {
	switch step {
	case ownerStepMoney:
		return cardPlanMoney
	case ownerStepOutward:
		return cardPlanOutward
	case ownerStepIrreversible:
		return cardPlanIrreversible
	}
	return cardPlanWaiting
}
