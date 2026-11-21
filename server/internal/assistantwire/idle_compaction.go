package assistantwire

// IdleCompaction applies only to unattended messages in the main conversation.
// A missing policy keeps older managers' delivery behavior unchanged.
//
// ReleaseMinutes and ReleaseTokens are for the node letting the main conversation's idle process go (#12008): how long
// it is kept after its last activity, and the context at which it is compacted first. 0 (left out) keeps the node's own
// rule: 20 minutes, and 75% of the conversation's window. An older node ignores both.
type IdleCompaction struct {
	Enabled        bool `json:"enabled"`
	IdleMinutes    int  `json:"idle_minutes"`
	ContextTokens  int  `json:"context_tokens"`
	ReleaseMinutes int  `json:"release_minutes,omitempty"`
	ReleaseTokens  int  `json:"release_tokens,omitempty"`
}

// The ranges ReleaseMinutes and ReleaseTokens are taken in; a value outside them is treated as 0. Releases stay below
// the prompt cache's one-hour lifetime, so the /compact before a release still reads the cache; the reaper looks once a
// minute, which the margin covers.
const (
	MinReleaseMinutes = 5
	MaxReleaseMinutes = 55
	MinReleaseTokens  = 10000
	MaxReleaseTokens  = 10000000
)

// ReleaseMinutesValid and ReleaseTokensValid say whether a value is in its range.
func ReleaseMinutesValid(m int) bool { return m >= MinReleaseMinutes && m <= MaxReleaseMinutes }
func ReleaseTokensValid(t int) bool  { return t >= MinReleaseTokens && t <= MaxReleaseTokens }
