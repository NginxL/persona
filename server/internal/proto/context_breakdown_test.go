package proto

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestContextBreakdownWireRoundTripAndOmission(t *testing.T) {
	exact := &ContextBreakdown{
		Source:               ContextBreakdownExact,
		SystemAndRules:       Int(4200),
		ToolsAndMCP:          Int(8500),
		Conversation:         Int(12300),
		ToolOutputs:          Int(0), // explicit zero must survive wire round-trip
		Free:                 Int(175000),
		AutoCompactThreshold: Int(167000),
	}
	var s Session
	s.SessionID = "s-exact"
	s.ContextTokens = 25000
	s.ContextWindow = 200000
	s.SetContextBreakdown(exact)

	checkWire(t, "session with exact context_breakdown", s)
	raw, ok := appendSession(nil, &s)
	if !ok {
		t.Fatal("appendSession fell back to reflect")
	}
	for _, want := range []string{
		`"context_breakdown":{`,
		`"source":"exact"`,
		`"system_and_rules":4200`,
		`"tools_and_mcp":8500`,
		`"conversation":12300`,
		`"tool_outputs":0`,
		`"free":175000`,
		`"auto_compact_threshold":167000`,
	} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}

	var decoded Session
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded.ContextBreakdown(), exact) {
		t.Fatalf("round-trip mismatch:\ngot  %+v\nwant %+v", decoded.ContextBreakdown(), exact)
	}

	// Partial estimated breakdown: nil categories are omitted cleanly.
	est := &ContextBreakdown{
		Source:       ContextBreakdownEstimated,
		Conversation: Int(1200),
		ToolOutputs:  Int(3400),
	}
	var partial Session
	partial.SessionID = "s-est"
	partial.SetContextBreakdown(est)
	checkWire(t, "session with partial estimated context_breakdown", partial)
	pb, err := json.Marshal(&partial)
	if err != nil {
		t.Fatalf("marshal partial: %v", err)
	}
	if !strings.Contains(string(pb), `"source":"estimated"`) ||
		!strings.Contains(string(pb), `"conversation":1200`) ||
		!strings.Contains(string(pb), `"tool_outputs":3400`) {
		t.Fatalf("unexpected partial JSON: %s", pb)
	}
	for _, absent := range []string{`"system_and_rules"`, `"tools_and_mcp"`, `"free"`, `"auto_compact_threshold"`} {
		if strings.Contains(string(pb), absent) {
			t.Fatalf("unavailable category %s must be omitted: %s", absent, pb)
		}
	}

	// Session without ContextBreakdown omits context_breakdown completely.
	var empty Session
	empty.SessionID = "s-empty"
	eb, err := json.Marshal(&empty)
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if strings.Contains(string(eb), `"context_breakdown"`) {
		t.Fatalf("absent context_breakdown must be omitted: %s", eb)
	}

	// Backward compatibility: older JSON without context_breakdown decodes with nil breakdown.
	var legacy Session
	if err := json.Unmarshal([]byte(`{"session_id":"legacy","context_tokens":5000,"context_window":200000}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if legacy.ContextBreakdown() != nil {
		t.Fatalf("legacy payload should decode nil breakdown, got %+v", legacy.ContextBreakdown())
	}
}

func TestContextBreakdownCopyOnWriteIsolation(t *testing.T) {
	orig := &ContextBreakdown{
		Source:       ContextBreakdownEstimated,
		Conversation: Int(500),
		Free:         Int(199500),
	}
	var a Session
	a.SessionID = "s1"
	a.SetContextBreakdown(orig)

	// Mutating caller's struct after SetContextBreakdown must not affect stored state.
	*orig.Conversation = 9999
	if got := *a.ContextBreakdown().Conversation; got != 500 {
		t.Fatalf("caller mutation leaked into session: got %d, want 500", got)
	}

	// Copying Session and updating ContextBreakdown must not mutate the original copy.
	b := a
	b.SetContextBreakdown(&ContextBreakdown{Source: ContextBreakdownExact, Conversation: Int(800)})
	if got := *a.ContextBreakdown().Conversation; got != 500 {
		t.Fatalf("copy-on-write violated: original changed to %d", got)
	}
	if got := *b.ContextBreakdown().Conversation; got != 800 || b.ContextBreakdown().Source != ContextBreakdownExact {
		t.Fatalf("updated copy mismatch: %+v", b.ContextBreakdown())
	}

	// Clearing ContextBreakdown leaves Hierarchy nil when no other sparse field is set.
	b.SetContextBreakdown(nil)
	if b.ContextBreakdown() != nil || b.Hierarchy != nil {
		t.Fatalf("clearing breakdown should release empty hierarchy: %+v", b.Hierarchy)
	}
}

func TestContextBreakdownValidationAndSanitize(t *testing.T) {
	if (*ContextBreakdown)(nil).Valid() || (*ContextBreakdown)(nil).Sanitized() != nil {
		t.Fatal("nil breakdown must be invalid")
	}
	if (&ContextBreakdown{Source: "guess", Conversation: Int(10)}).Valid() {
		t.Fatal("unknown source must be invalid")
	}
	if (&ContextBreakdown{Source: ContextBreakdownExact}).Valid() {
		t.Fatal("breakdown with no categories must be invalid")
	}
	neg := &ContextBreakdown{
		Source:         ContextBreakdownEstimated,
		SystemAndRules: Int(-5),
		Conversation:   Int(100),
		Free:           Int(0),
	}
	if neg.Valid() {
		t.Fatal("negative category must be invalid")
	}
	san := neg.Sanitized()
	if san == nil || !san.Valid() || san.SystemAndRules != nil || san.Conversation == nil || *san.Conversation != 100 || san.Free == nil || *san.Free != 0 {
		t.Fatalf("unexpected sanitized breakdown: %+v", san)
	}
	onlyNeg := &ContextBreakdown{Source: ContextBreakdownExact, Free: Int(-1)}
	if onlyNeg.Sanitized() != nil {
		t.Fatal("all-negative breakdown must sanitize to nil")
	}
}
