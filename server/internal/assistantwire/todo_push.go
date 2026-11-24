package assistantwire

import "strings"

// A to-do's pushes to the owner (its decision, a needs-user action, an overdue decision) are keyed by the to-do and
// what they are about, not by when they were asked: RelatedKey "todo:<id>:<reason>" (TodoPushKey). The hub pushes one
// such key once and holds every later push with it while the to-do still waits on the owner for that reason; the
// manager says the to-do no longer does (answered, withdrawn, closed) with EventFleetResolved naming TodoAskCardID.
// The pushes themselves carry no card id: the iOS app clears a delivered push whose interaction_id is not in its card
// list as stale, and a to-do's ask is never in that list, so they cannot be taken back from the lock screen.

// Reasons a to-do's push to the owner is about (TodoPushKey).
const (
	TodoPushDecide        = "decide"         // a decision with a recommended option waits for the owner
	TodoPushDecideOverdue = "decide_overdue" // that decision is past its deadline and nothing was taken
	TodoPushNeedsUser     = "needs_user"     // the work stopped on an action only the owner can take
)

// todoAskCardPrefix starts TodoAskCardID.
const todoAskCardPrefix = "todo-ask:"

// TodoPushKey is the RelatedKey of to-do id's push to the owner about reason.
func TodoPushKey(id, reason string) string { return "todo:" + id + ":" + reason }

// TodoAskCardID names to-do id's owner ask in EventFleetResolved: it ends its TodoPushKey dedup. It is never a push's
// card id (see above).
func TodoAskCardID(id string) string { return todoAskCardPrefix + id }

// TodoOfAskCard is the to-do TodoAskCardID names ("" when id is another card's).
func TodoOfAskCard(id string) string {
	todo, ok := strings.CutPrefix(id, todoAskCardPrefix)
	if !ok {
		return ""
	}
	return strings.TrimSpace(todo)
}

// TodoPushKeyParts splits a TodoPushKey into its to-do and reason; ok is false for any other key, including the
// legacy "todo:<id>@<round>" form, which carries no reason.
func TodoPushKeyParts(key string) (id, reason string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(key), "todo:")
	if !found || strings.Contains(rest, "@") {
		return "", "", false
	}
	id, reason, found = strings.Cut(rest, ":")
	id, reason = strings.TrimSpace(id), strings.TrimSpace(reason)
	if !found || id == "" || reason == "" {
		return "", "", false
	}
	return id, reason, true
}

// RelatedTodoID is the to-do a related key names: "todo:<id>", the legacy "todo:<id>@<round>" and
// "todo:<id>:<reason>" alike ("" for none).
func RelatedTodoID(key string) string {
	id, ok := strings.CutPrefix(strings.TrimSpace(key), "todo:")
	if !ok {
		return ""
	}
	id, _, _ = strings.Cut(id, "@")
	id, _, _ = strings.Cut(id, ":")
	return strings.TrimSpace(id)
}
