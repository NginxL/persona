package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

func TestNodeContextBreakdownFromTranscriptAndLauncher(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	lines := []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-11-21T10:00:00Z","cwd":"/work","message":{"role":"user","content":"` + strings.Repeat("please review this change ", 40) + `"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-21T10:00:01Z","message":{"role":"assistant","id":"m1","model":"claude-sonnet-4-6","content":[{"type":"tool_use","id":"tu1","name":"Read","input":{"file_path":"/work/main.go"}}],"usage":{"input_tokens":1000,"cache_read_input_tokens":19000,"output_tokens":40}}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-11-21T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"` + strings.Repeat("func main() {}\n", 100) + `"}]}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-11-21T10:00:03Z","message":{"role":"assistant","id":"m2","model":"claude-sonnet-4-6","content":[{"type":"text","text":"Looks good."}],"usage":{"input_tokens":1500,"cache_read_input_tokens":20500,"output_tokens":20}}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	m := readQuickMeta(path, st)

	n := &Node{}
	n.setCatalog([]proto.ManagedLauncher{{
		Name:          "gw",
		CompactWindow: 150000,
		Models:        []proto.ProviderModel{{ID: "claude-sonnet-4-6", ContextWindow: 200000}},
	}})
	s := proto.Session{SessionID: "s1", Launcher: "gw", Model: "claude-sonnet-4-6"}
	n.setSessionContext(&s, m)

	bd := s.ContextBreakdown()
	if bd == nil {
		t.Fatal("expected estimated ContextBreakdown on session")
	}
	if bd.Source != proto.ContextBreakdownEstimated {
		t.Fatalf("source = %q, want estimated", bd.Source)
	}
	if bd.ToolsAndMCP != nil {
		t.Fatalf("tools_and_mcp should be omitted when unavailable, got %d", *bd.ToolsAndMCP)
	}
	if bd.SystemAndRules == nil || bd.Conversation == nil || bd.ToolOutputs == nil {
		t.Fatalf("missing estimated categories: %+v", bd)
	}
	if sum := *bd.SystemAndRules + *bd.Conversation + *bd.ToolOutputs; sum != int(s.ContextTokens) {
		t.Fatalf("category sum = %d, want context_tokens %d", sum, s.ContextTokens)
	}
	if bd.Free == nil || *bd.Free != 178000 {
		t.Fatalf("free = %v, want 178000", bd.Free)
	}
	if bd.AutoCompactThreshold == nil || *bd.AutoCompactThreshold != 150000 {
		t.Fatalf("auto_compact_threshold = %v, want 150000", bd.AutoCompactThreshold)
	}
}

func TestNodeContextBreakdownOmittedWhenUnknown(t *testing.T) {
	var s proto.Session
	s.SessionID = "s-empty"
	setContext(&s, quickMeta{})
	if s.ContextBreakdown() != nil {
		t.Fatalf("expected nil breakdown when context tokens are unknown, got %+v", s.ContextBreakdown())
	}
}

func TestRuntimeNormalizeAndMergePiPreservesExactBreakdown(t *testing.T) {
	exact := &proto.ContextBreakdown{
		Source:         proto.ContextBreakdownExact,
		SystemAndRules: proto.Int(3000),
		ToolsAndMCP:    proto.Int(4500),
		Conversation:   proto.Int(2500),
		ToolOutputs:    proto.Int(5000),
	}
	row := proto.Session{
		SessionID:     "pi-1",
		Agent:         "pi",
		Status:        proto.StatusIdle,
		ContextTokens: 15000,
		ContextWindow: 200000,
		UpdatedAt:     time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC),
	}
	row.SetContextBreakdown(exact)

	invalid := proto.Session{
		SessionID:     "pi-bad",
		Agent:         "pi",
		Status:        proto.StatusIdle,
		ContextTokens: 15000,
		ContextWindow: 200000,
	}
	invalid.SetContextBreakdown(&proto.ContextBreakdown{Source: "fabricated", Conversation: proto.Int(100)})

	h := &runtimeConn{hello: runtimeHello{Runtime: "pi", Agent: "pi", Name: "pi"}}
	normalized := normalizeRuntimeSessions(h, []proto.Session{row, invalid})
	if len(normalized) != 2 {
		t.Fatalf("expected 2 normalized rows, got %d", len(normalized))
	}
	got := normalized[0].ContextBreakdown()
	if got == nil || got.Source != proto.ContextBreakdownExact || *got.ToolsAndMCP != 4500 {
		t.Fatalf("exact runtime breakdown not preserved: %+v", got)
	}
	if got.Free == nil || *got.Free != 185000 {
		t.Fatalf("free should be derived from window-tokens when absent: %+v", got.Free)
	}
	if normalized[1].ContextBreakdown() != nil {
		t.Fatalf("invalid runtime breakdown must be dropped, got %+v", normalized[1].ContextBreakdown())
	}

	// mergePiRow carries ContextTokens, ContextWindow, and ContextBreakdown onto the herdr row.
	reg := newRuntimeRegistry()
	rh := reg.add(nil)
	rh.hello = h.hello
	rh.ready = true
	reg.setSessions(rh, normalized)
	n := &Node{runtimes: reg}
	herdrRow := proto.Session{SessionID: "pi-1", Agent: "pi", Status: proto.StatusIdle}
	n.mergePiRow(&herdrRow)
	if herdrRow.ContextTokens != 15000 || herdrRow.ContextWindow != 200000 {
		t.Fatalf("mergePiRow lost context counts: %d/%d", herdrRow.ContextTokens, herdrRow.ContextWindow)
	}
	if bd := herdrRow.ContextBreakdown(); bd == nil || bd.Source != proto.ContextBreakdownExact || *bd.Free != 185000 {
		t.Fatalf("mergePiRow lost ContextBreakdown: %+v", bd)
	}
}

func TestSetSessionContextPreservesExistingExactBreakdown(t *testing.T) {
	n := &Node{}
	n.setCatalog([]proto.ManagedLauncher{{
		Name:          "gw1m",
		CompactWindow: 800000,
		Models:        []proto.ProviderModel{{ID: "claude-sonnet-4-6", ContextWindow: 1000000}},
	}})

	exact := &proto.ContextBreakdown{
		Source:         proto.ContextBreakdownExact,
		SystemAndRules: proto.Int(4200),
		ToolsAndMCP:    proto.Int(6800),
		Conversation:   proto.Int(9000),
		ToolOutputs:    proto.Int(5000),
	}
	m := quickMeta{
		model:             "claude-sonnet-4-6",
		contextTokens:     25000,
		contextFullWindow: true,
		contextStats: transcript.ContextStats{
			ConversationBytes: 4000,
			ToolOutputBytes:   4000,
		},
	}

	// Direct setContext preserves existing exact breakdown and derives free from fallback window.
	direct := proto.Session{SessionID: "s-direct", Model: "claude-sonnet-4-6"}
	direct.SetContextBreakdown(exact)
	setContext(&direct, m)
	dbd := direct.ContextBreakdown()
	if dbd == nil || dbd.Source != proto.ContextBreakdownExact ||
		*dbd.SystemAndRules != 4200 || *dbd.ToolsAndMCP != 6800 || *dbd.Conversation != 9000 || *dbd.ToolOutputs != 5000 {
		t.Fatalf("setContext overwrote exact breakdown: %+v", dbd)
	}
	if dbd.Free == nil || *dbd.Free != int(direct.ContextWindow)-25000 {
		t.Fatalf("setContext free = %v, want %d", dbd.Free, int(direct.ContextWindow)-25000)
	}

	// setSessionContext preserves existing exact breakdown and derives Free / AutoCompactThreshold from launcher's 1M window.
	s := proto.Session{SessionID: "s-launcher", Launcher: "gw1m", Model: "claude-sonnet-4-6"}
	s.SetContextBreakdown(exact)
	n.setSessionContext(&s, m)
	bd := s.ContextBreakdown()
	if bd == nil || bd.Source != proto.ContextBreakdownExact {
		t.Fatalf("setSessionContext replaced exact breakdown: %+v", bd)
	}
	if *bd.SystemAndRules != 4200 || *bd.ToolsAndMCP != 6800 || *bd.Conversation != 9000 || *bd.ToolOutputs != 5000 {
		t.Fatalf("setSessionContext altered exact categories: %+v", bd)
	}
	if s.ContextWindow != 1000000 {
		t.Fatalf("ContextWindow = %d, want 1000000", s.ContextWindow)
	}
	if bd.Free == nil || *bd.Free != 975000 {
		t.Fatalf("Free = %v, want 975000 (from final 1M launcher window)", bd.Free)
	}
	if bd.AutoCompactThreshold == nil || *bd.AutoCompactThreshold != 800000 {
		t.Fatalf("AutoCompactThreshold = %v, want 800000", bd.AutoCompactThreshold)
	}
}
