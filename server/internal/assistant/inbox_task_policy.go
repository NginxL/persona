package assistant

import "strings"

// Bot/app notifications are broadcasts, not requests to work on a task. Older Lark
// connectors classified human private chats as bot too; their explicit user sender
// identity takes precedence over that legacy kind. Do not infer this from message
// text: a human can quote or forward a notification while asking for help.
func inboxTaskLinkEligible(it *InboxItem) bool {
	if it.Kind != KindBot {
		return true
	}
	if it.Connector != nil && strings.HasPrefix(it.Connector.Thread, "larkcli:chat:") {
		for _, tag := range it.Connector.Tags {
			if tag.Key == "sender" && strings.HasPrefix(tag.Value, "ou_") {
				return true
			}
		}
	}
	return false
}
