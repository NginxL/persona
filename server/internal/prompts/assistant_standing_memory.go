package prompts

// StandingMemory is the snapshot section of the memory entries a main-space conversation keeps at hand
// (assistant/snapshot_memory, #13586, docs/assistant/health.md "Memory by scene"): the memory extension's standing
// entries the assembler let through for it. Each text is on one line, without the closing tags ("</persona-context>").
type StandingMemory struct {
	Language string // the answer language's own name; "" is Chinese
	Entries  []MemoryEntry
}

// RenderStandingMemory is the section; empty when there are no entries.
func RenderStandingMemory(d StandingMemory) string {
	if len(d.Entries) == 0 {
		return ""
	}
	return render("assistant/snapshot_memory", d)
}
