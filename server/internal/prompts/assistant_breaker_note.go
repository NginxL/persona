package prompts

// Breaker note kinds: why a background supervisor's signal fell back once to the main conversation.
const (
	BreakerAuth       = "auth"        // a tool call of the background supervisor was denied (Tool names it)
	BreakerAllStopped = "all_stopped" // Rounds rounds in a row made no progress on all_stopped (Reason says which)
)

// BreakerNote is the localized fact put above a supervision entry that the background supervisor's circuit breaker
// handed to the main conversation.
type BreakerNote struct {
	Language     string
	Kind         string // BreakerAuth or BreakerAllStopped
	Tool, Reason string
	Rounds       int
}

func RenderBreakerNote(p BreakerNote) string { return render("assistant/breaker_note", p) }
