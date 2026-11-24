package prompts

// RenderCompactInstructions follows the owner's configured language while preserving quoted source wording.
func RenderCompactInstructions(language string) string {
	return render("assistant/compact_instructions", struct{ Language string }{language})
}

// RecoveryEntry keeps a source reference and its exact text together under one byte budget.
type RecoveryEntry struct{ Source, Text string }
type CompactRecovery struct {
	Language, ID                  string
	State, Rules, Decisions, Pins []RecoveryEntry
}

func RenderCompactRecovery(d CompactRecovery) string { return render("assistant/compact_recovery", d) }
