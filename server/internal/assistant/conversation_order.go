package assistant

import (
	"errors"
	"strings"
)

// A manual order is display data, not a conversation roster: valid ids are kept even when the current reader cannot
// list that conversation. A later client with a wider roster can therefore show the rows without losing their places.
const conversationOrderMax = 10000

func normalizeConversationOrder(in []string) ([]string, error) {
	if len(in) > conversationOrderMax {
		return nil, errors.New("too many conversations in manual order")
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if !validSessionID(id) {
			return nil, errors.New("invalid conversation id in manual order")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}
