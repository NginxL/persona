package prompts

// MemoryEntries is the context a dispatched task's first message carries of the user's memory (todo/memory_entries,
// #13532, docs/assistant/health.md "Memory by scene"): the entries the assembler let through for the task. The caller
// leaves out the closing tags ("</persona-context>") from the texts.
type MemoryEntries struct {
	Language string // the answer language's own name; "" is Chinese
	Entries  []MemoryEntry
}

// MemoryEntry is one entry: N counts from 1.
type MemoryEntry struct {
	N    int
	Text string
}

// RenderMemoryEntries is the context block of the entries; empty when there are none.
func RenderMemoryEntries(d MemoryEntries) string {
	if len(d.Entries) == 0 {
		return ""
	}
	return render("todo/memory_entries", d)
}
