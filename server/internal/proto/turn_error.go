package proto

import "strings"

// UnrecoverableTurnErrorCode identifies failures that retrying this conversation cannot repair.
// Keep permanent causes ahead of a runtime's generic transient kind or an upstream HTTP status.
func UnrecoverableTurnErrorCode(text string) string {
	switch {
	case containsErrorMarker(text, "compacted natively") && containsErrorMarker(text, "compaction is sealed") &&
		containsErrorMarker(text, "to that account"):
		return TurnErrorCompactionSealed
	default:
		return ""
	}
}

// Markers are ASCII. Do not lowercase whole errors on the session-list hot path.
func containsErrorMarker(text, marker string) bool {
	for i := 0; i+len(marker) <= len(text); i++ {
		if (text[i] == marker[0] || text[i] == marker[0]-('a'-'A')) &&
			strings.EqualFold(text[i:i+len(marker)], marker) {
			return true
		}
	}
	return false
}
