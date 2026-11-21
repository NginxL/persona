package assistant

import "errors"

const conversationTagFiltersMax = 50

// Filter entries are labels, not conversations: they may name a tag no currently loaded conversation carries.
func normalizeConversationTagFilters(in []string) ([]string, error) {
	return normalizeTagList(in, conversationTagFiltersMax, errors.New("too many conversation tag filters"))
}
