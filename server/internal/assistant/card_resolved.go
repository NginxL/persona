package assistant

import "github.com/nwverse/persona/server/internal/assistantwire"

// When one of the assistant's own cards is over (card rules, rule 3: "a card over is gone everywhere at once"), the
// manager says so in one way, resolveCard, naming the card: a plan waiting for Start (its id), a to-do's decision
// (assistantwire.TodoAskCardID), a failed start's push (taskFailedCardID). The hub then does the rest for every card
// alike (fleet_link.go, EventFleetResolved): the web and the apps drop it from their lists (interaction_resolved), the
// devices its push went to take the push off the lock screen (push_withdraw.go, by the push's card_id), and a to-do's
// next push about the same thing may ring. A card that was never pushed or shown costs a no-op.

// taskFailedCardID names to-do id's failed-start push (notifyTaskFailed): it is over once the task is started again.
// "" for none (a to-do without an id): its push then carries no card.
func taskFailedCardID(id string) string {
	if id == "" {
		return ""
	}
	return "task-failed:" + id
}

// resolveCard tells the hub card id is over.
func (s *Service) resolveCard(id string) {
	if s.hub == nil || id == "" {
		return
	}
	if err := s.hub.Tell(assistantwire.EventFleetResolved, assistantwire.FleetResolved{ID: id}); err != nil {
		s.logf("card %s over: %v", id, err)
	}
}
