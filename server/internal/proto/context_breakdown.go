package proto

// ContextBreakdown source honesty markers.
const (
	// ContextBreakdownExact indicates category counts reported natively by the engine.
	ContextBreakdownExact = "exact"
	// ContextBreakdownEstimated indicates category counts derived from bounded transcript estimation.
	ContextBreakdownEstimated = "estimated"
)

// ContextBreakdown splits a session's current context window occupancy into structured categories.
// Nil category pointers mean unavailable and are omitted on the wire; non-nil zero means genuinely zero.
// Source is ContextBreakdownExact ("exact") or ContextBreakdownEstimated ("estimated").
type ContextBreakdown struct {
	Source               string `json:"source"`
	SystemAndRules       *int   `json:"system_and_rules,omitempty"`
	ToolsAndMCP          *int   `json:"tools_and_mcp,omitempty"`
	Conversation         *int   `json:"conversation,omitempty"`
	ToolOutputs          *int   `json:"tool_outputs,omitempty"`
	Free                 *int   `json:"free,omitempty"`
	AutoCompactThreshold *int   `json:"auto_compact_threshold,omitempty"`
}

// Int returns a heap pointer to v for optional integer wire fields.
func Int(v int) *int { return &v }

// Int64 returns a heap pointer to v for optional int64 wire fields.
func Int64(v int64) *int64 { return &v }

func cloneIntPtr(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func equalIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func nonNegativeIntPtr(p *int) *int {
	if p == nil || *p < 0 {
		return nil
	}
	v := *p
	return &v
}

// Clone returns a deep copy of b, or nil when b is nil.
func (b *ContextBreakdown) Clone() *ContextBreakdown {
	if b == nil {
		return nil
	}
	return &ContextBreakdown{
		Source:               b.Source,
		SystemAndRules:       cloneIntPtr(b.SystemAndRules),
		ToolsAndMCP:          cloneIntPtr(b.ToolsAndMCP),
		Conversation:         cloneIntPtr(b.Conversation),
		ToolOutputs:          cloneIntPtr(b.ToolOutputs),
		Free:                 cloneIntPtr(b.Free),
		AutoCompactThreshold: cloneIntPtr(b.AutoCompactThreshold),
	}
}

// Equal reports whether b and other hold the exact same source and category values (including nil vs zero).
func (b *ContextBreakdown) Equal(other *ContextBreakdown) bool {
	if b == nil || other == nil {
		return b == other
	}
	return b.Source == other.Source &&
		equalIntPtr(b.SystemAndRules, other.SystemAndRules) &&
		equalIntPtr(b.ToolsAndMCP, other.ToolsAndMCP) &&
		equalIntPtr(b.Conversation, other.Conversation) &&
		equalIntPtr(b.ToolOutputs, other.ToolOutputs) &&
		equalIntPtr(b.Free, other.Free) &&
		equalIntPtr(b.AutoCompactThreshold, other.AutoCompactThreshold)
}

// HasCategory reports whether at least one category field is populated.
func (b *ContextBreakdown) HasCategory() bool {
	if b == nil {
		return false
	}
	return b.SystemAndRules != nil ||
		b.ToolsAndMCP != nil ||
		b.Conversation != nil ||
		b.ToolOutputs != nil ||
		b.Free != nil ||
		b.AutoCompactThreshold != nil
}

// Valid reports whether b has a recognized honesty source, at least one category, and no negative counts.
func (b *ContextBreakdown) Valid() bool {
	if b == nil {
		return false
	}
	if b.Source != ContextBreakdownExact && b.Source != ContextBreakdownEstimated {
		return false
	}
	if !b.HasCategory() {
		return false
	}
	for _, p := range []*int{b.SystemAndRules, b.ToolsAndMCP, b.Conversation, b.ToolOutputs, b.Free, b.AutoCompactThreshold} {
		if p != nil && *p < 0 {
			return false
		}
	}
	return true
}

// Sanitized returns a deep copy of b with invalid negative categories stripped, or nil if the source is
// unrecognized or no valid category remains.
func (b *ContextBreakdown) Sanitized() *ContextBreakdown {
	if b == nil || (b.Source != ContextBreakdownExact && b.Source != ContextBreakdownEstimated) {
		return nil
	}
	out := &ContextBreakdown{
		Source:               b.Source,
		SystemAndRules:       nonNegativeIntPtr(b.SystemAndRules),
		ToolsAndMCP:          nonNegativeIntPtr(b.ToolsAndMCP),
		Conversation:         nonNegativeIntPtr(b.Conversation),
		ToolOutputs:          nonNegativeIntPtr(b.ToolOutputs),
		Free:                 nonNegativeIntPtr(b.Free),
		AutoCompactThreshold: nonNegativeIntPtr(b.AutoCompactThreshold),
	}
	if !out.HasCategory() {
		return nil
	}
	return out
}
