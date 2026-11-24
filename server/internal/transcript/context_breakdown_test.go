package transcript

import (
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestBuildContextBreakdownExactAndEstimated(t *testing.T) {
	// Native exact breakdown wins over byte stats and fills missing free/auto_compact_threshold.
	native := &proto.ContextBreakdown{
		Source:         proto.ContextBreakdownExact,
		SystemAndRules: proto.Int(5000),
		ToolsAndMCP:    proto.Int(7000),
		Conversation:   proto.Int(8000),
		ToolOutputs:    proto.Int(10000),
	}
	got := BuildContextBreakdown(ContextStats{ConversationBytes: 4000, Native: native}, 30000, 200000, 167000, true)
	if got == nil || got.Source != proto.ContextBreakdownExact {
		t.Fatalf("expected exact breakdown, got %+v", got)
	}
	if *got.SystemAndRules != 5000 || *got.ToolsAndMCP != 7000 || *got.Conversation != 8000 || *got.ToolOutputs != 10000 {
		t.Fatalf("native categories altered: %+v", got)
	}
	if got.Free == nil || *got.Free != 170000 {
		t.Fatalf("free = %v, want 170000", got.Free)
	}
	if got.AutoCompactThreshold == nil || *got.AutoCompactThreshold != 167000 {
		t.Fatalf("auto_compact_threshold = %v, want 167000", got.AutoCompactThreshold)
	}

	// Full window estimated breakdown: attributes unrecorded prompt prefix to system_and_rules,
	// omits tools_and_mcp (not recorded in transcript), and conserves sum == contextTokens.
	est := BuildContextBreakdown(ContextStats{
		ConversationBytes: 4000, // 1000 tokens
		ToolOutputBytes:   8000, // 2000 tokens
	}, 25000, 200000, 180000, true)
	if est == nil || est.Source != proto.ContextBreakdownEstimated {
		t.Fatalf("expected estimated breakdown, got %+v", est)
	}
	if est.ToolsAndMCP != nil {
		t.Fatalf("tools_and_mcp must be omitted when unobserved, got %d", *est.ToolsAndMCP)
	}
	if est.SystemAndRules == nil || *est.SystemAndRules != 22000 {
		t.Fatalf("system_and_rules = %v, want 22000", est.SystemAndRules)
	}
	if est.Conversation == nil || *est.Conversation != 1000 || est.ToolOutputs == nil || *est.ToolOutputs != 2000 {
		t.Fatalf("conversation/tool_outputs = %v/%v, want 1000/2000", est.Conversation, est.ToolOutputs)
	}
	if est.Free == nil || *est.Free != 175000 || est.AutoCompactThreshold == nil || *est.AutoCompactThreshold != 180000 {
		t.Fatalf("free/threshold = %v/%v", est.Free, est.AutoCompactThreshold)
	}

	// Partial window (tail read only): proportionally apportions observed categories, omits
	// unobserved system_and_rules and tools_and_mcp without fabricating them.
	partial := BuildContextBreakdown(ContextStats{
		ConversationBytes: 4000, // 1:3 ratio with tool outputs
		ToolOutputBytes:   12000,
	}, 10000, 0, 0, false)
	if partial == nil || partial.Source != proto.ContextBreakdownEstimated {
		t.Fatalf("expected estimated partial breakdown, got %+v", partial)
	}
	if partial.SystemAndRules != nil || partial.ToolsAndMCP != nil || partial.Free != nil || partial.AutoCompactThreshold != nil {
		t.Fatalf("unobserved fields must be omitted on partial window without context window: %+v", partial)
	}
	if *partial.Conversation != 2500 || *partial.ToolOutputs != 7500 {
		t.Fatalf("partial split = %d/%d, want 2500/7500", *partial.Conversation, *partial.ToolOutputs)
	}

	// Over-estimated bytes in full window scale proportionally and conserve exact sum == contextTokens.
	scaled := BuildContextBreakdown(ContextStats{
		SystemBytes:       4000,
		ConversationBytes: 12000,
		ToolOutputBytes:   24000,
	}, 1001, 200000, 250000, true)
	if scaled == nil {
		t.Fatal("expected scaled breakdown")
	}
	sum := *scaled.SystemAndRules + *scaled.Conversation + *scaled.ToolOutputs
	if sum != 1001 {
		t.Fatalf("scaled category sum = %d, want 1001", sum)
	}
	if *scaled.AutoCompactThreshold != 200000 {
		t.Fatalf("auto_compact_threshold should clamp to contextWindow 200000, got %d", *scaled.AutoCompactThreshold)
	}

	// Omission when unknown: zero context tokens or empty stats must return nil (never fabricate).
	if BuildContextBreakdown(ContextStats{ConversationBytes: 400}, 0, 200000, 160000, true) != nil {
		t.Fatal("zero contextTokens must omit ContextBreakdown")
	}
	if BuildContextBreakdown(ContextStats{}, 25000, 200000, 160000, true) != nil {
		t.Fatal("empty stats without native breakdown must omit ContextBreakdown")
	}
}

func TestClaudeTranscriptContextBreakdownAndCompactionReset(t *testing.T) {
	lines := []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-11-21T10:00:00Z","message":{"role":"user","content":"` + strings.Repeat("hello world ", 100) + `"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-21T10:00:02Z","message":{"role":"assistant","id":"m1","model":"claude-sonnet-4-6","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"` + strings.Repeat("echo test && ", 60) + `"}}],"usage":{"input_tokens":1000,"cache_read_input_tokens":20000,"output_tokens":50}}}`,
		`{"type":"user","uuid":"u2","timestamp":"2026-11-21T10:00:03Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"` + strings.Repeat("output line\n", 200) + `"}]}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-11-21T10:00:05Z","message":{"role":"assistant","id":"m2","model":"claude-sonnet-4-6","content":[{"type":"text","text":"All checks passed cleanly."}],"usage":{"input_tokens":1200,"cache_read_input_tokens":22800,"output_tokens":20}}}`,
	}
	var m Meta
	for i, line := range lines {
		ParseLine(i+1, []byte(line), &m)
	}
	if m.ContextTokens != 24000 {
		t.Fatalf("ContextTokens = %d, want 24000", m.ContextTokens)
	}
	bd := BuildContextBreakdown(m.ContextStats, m.ContextTokens, 200000, 0, true)
	if bd == nil || bd.Source != proto.ContextBreakdownEstimated {
		t.Fatalf("expected estimated breakdown, got %+v", bd)
	}
	if bd.SystemAndRules == nil || bd.Conversation == nil || bd.ToolOutputs == nil || bd.Free == nil {
		t.Fatalf("expected populated categories, got %+v", bd)
	}
	if *bd.Conversation <= 0 || *bd.ToolOutputs <= 0 || *bd.SystemAndRules <= 0 {
		t.Fatalf("expected positive categories, got sys=%d conv=%d tools=%d", *bd.SystemAndRules, *bd.Conversation, *bd.ToolOutputs)
	}
	if got := *bd.SystemAndRules + *bd.Conversation + *bd.ToolOutputs; got != 24000 {
		t.Fatalf("category sum = %d, want 24000", got)
	}
	if *bd.Free != 176000 {
		t.Fatalf("free = %d, want 176000", *bd.Free)
	}

	// Compaction resets stats and ContextTokens until the next reply arrives.
	ParseLine(5, []byte(`{"type":"system","subtype":"compact_boundary","uuid":"c1","timestamp":"2026-11-21T10:01:00Z","compactMetadata":{"trigger":"auto","preTokens":24000}}`), &m)
	if !m.ContextStats.CompactedInWindow || m.ContextStats.ConversationBytes != 0 || m.ContextStats.ToolOutputBytes != 0 {
		t.Fatalf("compaction must reset stats: %+v", m.ContextStats)
	}
	if BuildContextBreakdown(m.ContextStats, m.ContextTokens, 200000, 0, true) != nil {
		t.Fatal("right after compaction before next reply, breakdown must be omitted")
	}

	// Native exact breakdown on a line takes precedence as "exact".
	ParseLine(6, []byte(`{"type":"assistant","uuid":"a3","timestamp":"2026-11-21T10:02:00Z","context_breakdown":{"source":"exact","system_and_rules":4000,"tools_and_mcp":3000,"conversation":2000,"tool_outputs":500},"message":{"role":"assistant","id":"m3","model":"claude-sonnet-4-6","content":[{"type":"text","text":"Resumed."}],"usage":{"input_tokens":500,"cache_read_input_tokens":9000,"output_tokens":10}}}`), &m)
	exact := BuildContextBreakdown(m.ContextStats, m.ContextTokens, 200000, 0, true)
	if exact == nil || exact.Source != proto.ContextBreakdownExact || *exact.ToolsAndMCP != 3000 || *exact.Free != 190500 {
		t.Fatalf("expected native exact breakdown, got %+v", exact)
	}
}

func TestCodexRolloutContextBreakdownAndCompaction(t *testing.T) {
	lines := []string{
		`{"timestamp":"2026-11-21T10:00:00Z","type":"session_meta","payload":{"cwd":"/work","cli_version":"0.170","originator":"codex_cli_rs"}}`,
		`{"timestamp":"2026-11-21T10:00:01Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"Inspect the build logs please."}]}}}`,
		`{"timestamp":"2026-11-21T10:00:02Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"c1","command":["go","test","./..."],"aggregated_output":"PASS ok all packages","status":"completed"}}}`,
		`{"timestamp":"2026-11-21T10:00:03Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"a1","content":[{"type":"text","text":"All packages passed."}]}}}`,
		`{"timestamp":"2026-11-21T10:00:04Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"output_tokens":42,"total_tokens":15000},"last_token_usage":{"input_tokens":14000,"output_tokens":42},"model_context_window":256000}}}`,
	}
	var m Meta
	m.Codex = true
	for i, line := range lines {
		ParseLine(i+1, []byte(line), &m)
	}
	bd := BuildContextBreakdown(m.ContextStats, m.ContextTokens, m.ContextWindow, 0, true)
	if bd == nil || bd.Source != proto.ContextBreakdownEstimated {
		t.Fatalf("expected Codex estimated breakdown, got %+v", bd)
	}
	if *bd.Conversation <= 0 || *bd.ToolOutputs <= 0 || *bd.Free != 242000 {
		t.Fatalf("unexpected Codex breakdown: %+v", bd)
	}

	ParseLine(6, []byte(`{"timestamp":"2026-11-21T10:01:00Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"ContextCompaction","id":"cmp1"}}}`), &m)
	if m.ContextTokens != 0 || !m.ContextStats.CompactedInWindow {
		t.Fatalf("Codex compaction did not reset context: tokens=%d stats=%+v", m.ContextTokens, m.ContextStats)
	}
}

func TestFoldWorkPreservesTTFTMS(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	items := []proto.Item{
		{ID: "u1#0", Seq: 1, TS: now, Role: "user", Kind: proto.KindText, Text: "check"},
		{ID: "t1#0", Seq: 2, TS: now, Role: "assistant", Kind: proto.KindThinking, Text: "thinking...", TTFTMS: proto.Int64(420)},
		{ID: "tu1#0", Seq: 3, TS: now, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "id1", ToolName: "Read", ToolInput: []byte(`{"file_path":"a.go"}`)},
		{ID: "tr1#0", Seq: 4, TS: now, Role: "user", Kind: proto.KindToolResult, ToolUseID: "id1", Text: "package main"},
		{ID: "a1#0", Seq: 5, TS: now, Role: "assistant", Kind: proto.KindText, Text: "done"},
	}
	folded := FoldWork(items)
	if len(folded) != 3 || folded[1].Kind != proto.KindFold {
		t.Fatalf("expected folded run, got %+v", folded)
	}
	if folded[1].TTFTMS == nil || *folded[1].TTFTMS != 420 {
		t.Fatalf("folded item lost TTFTMS: %v", folded[1].TTFTMS)
	}
	// Historical folded run without TTFT keeps nil TTFTMS.
	items[1].TTFTMS = nil
	foldedNoTTFT := FoldWork(items)
	if foldedNoTTFT[1].TTFTMS != nil {
		t.Fatalf("historical fold must not fabricate TTFTMS: %v", *foldedNoTTFT[1].TTFTMS)
	}
}

func TestCodexRolloutContextBreakdownCountsUnclippedLongContent(t *testing.T) {
	// Exceed maxTextLen (1 MiB) so clip() and clipJSON() actively truncate display items.
	longPrompt := strings.Repeat("p", maxTextLen+200_000) // ~1.25 MB -> ~312k tokens
	longOutput := strings.Repeat("o", maxTextLen+200_000) // ~1.25 MB -> ~312k tokens
	longReply := strings.Repeat("r", maxTextLen+100_000)  // ~1.15 MB -> ~287k tokens

	lines := []string{
		`{"timestamp":"2026-11-21T10:00:00Z","type":"session_meta","payload":{"cwd":"/work","cli_version":"0.170","originator":"codex_cli_rs"}}`,
		`{"timestamp":"2026-11-21T10:00:01Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"` + longPrompt + `"}]}}}`,
		// Duplicate item_completed for u1 must not be double-counted.
		`{"timestamp":"2026-11-21T10:00:01Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"` + longPrompt + `"}]}}}`,
		`{"timestamp":"2026-11-21T10:00:02Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"c1","command":["go","test","-v","./..."],"aggregated_output":"` + longOutput + `","status":"completed"}}}`,
		`{"timestamp":"2026-11-21T10:00:03Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"a1","content":[{"type":"text","text":"` + longReply + `"}]}}}`,
		`{"timestamp":"2026-11-21T10:00:04Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"output_tokens":10000,"total_tokens":960000},"last_token_usage":{"input_tokens":950000,"output_tokens":10000},"model_context_window":1000000}}}`,
	}

	var m Meta
	m.Codex = true
	var userItems, cmdItems, replyItems []proto.Item
	for i, line := range lines {
		got := ParseLine(i+1, []byte(line), &m)
		switch i {
		case 1:
			userItems = got
		case 3:
			cmdItems = got
		case 4:
			replyItems = got
		}
	}

	// Display items themselves must remain clipped by maxTextLen.
	if len(userItems) != 1 || len(userItems[0].Text) >= len(longPrompt) {
		t.Fatalf("expected display UserMessage to be clipped, got len=%d vs orig=%d", len(userItems[0].Text), len(longPrompt))
	}
	if len(cmdItems) != 2 || len(cmdItems[1].Text) >= len(longOutput) {
		t.Fatalf("expected display CommandExecution output to be clipped, got len=%d vs orig=%d", len(cmdItems[1].Text), len(longOutput))
	}
	if len(replyItems) != 1 || len(replyItems[0].Text) >= len(longReply) {
		t.Fatalf("expected display AgentMessage to be clipped, got len=%d vs orig=%d", len(replyItems[0].Text), len(longReply))
	}

	// ContextStats must reflect unclipped sizes (not clipped display text) and no duplicate counting.
	wantConv := int64(len(longPrompt) + len(longReply))
	if m.ContextStats.ConversationBytes < wantConv-100 || m.ContextStats.ConversationBytes > wantConv+500 {
		t.Fatalf("ConversationBytes = %d, want ~%d", m.ContextStats.ConversationBytes, wantConv)
	}
	wantToolMin := int64(len(longOutput))
	if m.ContextStats.ToolOutputBytes < wantToolMin {
		t.Fatalf("ToolOutputBytes = %d, want >= %d", m.ContextStats.ToolOutputBytes, wantToolMin)
	}

	bd := BuildContextBreakdown(m.ContextStats, m.ContextTokens, m.ContextWindow, 0, true)
	if bd == nil || bd.Source != proto.ContextBreakdownEstimated {
		t.Fatalf("expected estimated breakdown, got %+v", bd)
	}
	// ~2.40MB conversation (~599k tokens) + ~1.25MB tool output (~312k tokens) out of 950k total ->
	// system_and_rules should receive only the ~39k unrecorded prefix gap, not the clipped content.
	if *bd.Conversation < 580000 || *bd.ToolOutputs < 300000 || *bd.SystemAndRules > 60000 {
		t.Fatalf("clipped content misattributed: sys=%d conv=%d tool=%d", *bd.SystemAndRules, *bd.Conversation, *bd.ToolOutputs)
	}
	if got := *bd.SystemAndRules + *bd.Conversation + *bd.ToolOutputs; got != 950000 {
		t.Fatalf("category sum = %d, want 950000", got)
	}
}

func TestObserveSkippedUserLineMixedImagesAndText(t *testing.T) {
	b64 := strings.Repeat("QUJD", 25000) // 100 KB base64 image payload

	// 1. Single image only: counted as one image (~6400 bytes), not 100 KB.
	var mSingle Meta
	singleImgLine := `{"type":"user","uuid":"u1","timestamp":"2026-11-21T10:00:00Z","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}}`
	mSingle.ObserveSkippedUserLine([]byte(singleImgLine))
	if mSingle.ContextStats.ConversationBytes != imageTokenBytesEstimate {
		t.Fatalf("single image ConversationBytes = %d, want %d", mSingle.ContextStats.ConversationBytes, imageTokenBytesEstimate)
	}
	if mSingle.ContextStats.ToolOutputBytes != 0 {
		t.Fatalf("single image ToolOutputBytes = %d, want 0", mSingle.ContextStats.ToolOutputBytes)
	}

	// 2. Large user prompt (30 KB text) + 2 base64 images: retains text AND counts both images.
	var mMixed Meta
	bigText := strings.Repeat("important context sentence. ", 1100) // ~30.8 KB
	mixedLine := `{"type":"user","uuid":"u2","timestamp":"2026-11-21T10:00:01Z","message":{"role":"user","content":[{"type":"text","text":"` + bigText + `"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}},{"type":"image","source":{"data":"` + b64 + `","media_type":"image/jpeg","type":"base64"}}]}}`
	mMixed.ObserveSkippedUserLine([]byte(mixedLine))
	wantMixed := int64(len(bigText)) + 2*imageTokenBytesEstimate
	if mMixed.ContextStats.ConversationBytes < wantMixed-512 || mMixed.ContextStats.ConversationBytes > wantMixed+512 {
		t.Fatalf("mixed prompt+2 images ConversationBytes = %d, want ~%d", mMixed.ContextStats.ConversationBytes, wantMixed)
	}

	// 3. tool_result containing 40 KB text + 2 images (including "type":"tool_result" after "content").
	var mTool Meta
	bigToolText := strings.Repeat("log output entry 0123456789\n", 1450) // ~40.6 KB
	toolLine := `{"type":"user","uuid":"u3","timestamp":"2026-11-21T10:00:02Z","message":{"role":"user","content":[{"content":[{"type":"text","text":"` + strings.ReplaceAll(bigToolText, "\n", `\n`) + `"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}],"tool_use_id":"tu1","type":"tool_result"}]}}`
	mTool.ObserveSkippedUserLine([]byte(toolLine))
	wantTool := int64(len(bigToolText)) + 2*imageTokenBytesEstimate
	if mTool.ContextStats.ToolOutputBytes < wantTool-1024 || mTool.ContextStats.ToolOutputBytes > wantTool+2048 {
		t.Fatalf("tool_result text+2 images ToolOutputBytes = %d, want ~%d", mTool.ContextStats.ToolOutputBytes, wantTool)
	}
	if mTool.ContextStats.ConversationBytes != 0 {
		t.Fatalf("tool_result leaked into ConversationBytes = %d", mTool.ContextStats.ConversationBytes)
	}

	// 4. Plain large text mentioning "base64" (escaped JSON inside a string): NOT treated as an image.
	var mMention Meta
	mentionText := `The snippet uses \"type\":\"base64\" with \"data\":\"abc\" — ` + strings.Repeat("plain prose text ", 1500)
	mentionLine := `{"type":"user","uuid":"u4","timestamp":"2026-11-21T10:00:03Z","message":{"role":"user","content":"` + mentionText + `"}}`
	mMention.ObserveSkippedUserLine([]byte(mentionLine))
	if mMention.ContextStats.ConversationBytes < int64(len(mentionText))-512 {
		t.Fatalf("plain text mentioning base64 was capped: got %d, want >= %d", mMention.ContextStats.ConversationBytes, len(mentionText)-512)
	}

	// 5. Pure-image tool_result (no text): ToolOutputBytes increments by imageTokenBytesEstimate
	// and ConversationBytes remains 0, regardless of key order or truncation after the image block.
	for name, line := range map[string]string{
		"type_before_content": `{"type":"user","uuid":"u5","timestamp":"2026-11-21T10:00:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu2","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]}}`,
		"type_after_content":  `{"type":"user","uuid":"u6","timestamp":"2026-11-21T10:00:05Z","message":{"role":"user","content":[{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}],"tool_use_id":"tu3","type":"tool_result"}]}}`,
		"truncated_after_img": `{"type":"user","uuid":"u7","timestamp":"2026-11-21T10:00:06Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu4","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}`,
	} {
		var mPureTool Meta
		mPureTool.ObserveSkippedUserLine([]byte(line))
		if mPureTool.ContextStats.ToolOutputBytes != imageTokenBytesEstimate {
			t.Fatalf("%s: ToolOutputBytes = %d, want %d", name, mPureTool.ContextStats.ToolOutputBytes, imageTokenBytesEstimate)
		}
		if mPureTool.ContextStats.ConversationBytes != 0 {
			t.Fatalf("%s: ConversationBytes = %d, want 0", name, mPureTool.ContextStats.ConversationBytes)
		}
	}

	// 6. Sibling tool_result image and conversation image in the same skipped user line are attributed separately.
	var mSibling Meta
	siblingLine := `{"type":"user","uuid":"u8","timestamp":"2026-11-21T10:00:07Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu5","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}}`
	mSibling.ObserveSkippedUserLine([]byte(siblingLine))
	if mSibling.ContextStats.ToolOutputBytes != imageTokenBytesEstimate {
		t.Fatalf("sibling ToolOutputBytes = %d, want %d", mSibling.ContextStats.ToolOutputBytes, imageTokenBytesEstimate)
	}
	if mSibling.ContextStats.ConversationBytes != imageTokenBytesEstimate {
		t.Fatalf("sibling ConversationBytes = %d, want %d", mSibling.ContextStats.ConversationBytes, imageTokenBytesEstimate)
	}

	// 7. Sidechain line is ignored.
	var mSide Meta
	mSide.ObserveSkippedUserLine([]byte(`{"type":"user","isSidechain":true,"message":{"role":"user","content":"` + bigText + `"}}`))
	if !mSide.ContextStats.Empty() {
		t.Fatalf("sidechain line should be ignored: %+v", mSide.ContextStats)
	}
}

func TestEstimateWeightedBytesInvalidUTF8NearCutoff(t *testing.T) {
	// 1. Valid 4-byte emoji split across the 4096-byte sample cutoff (4094 ASCII + "😀" + 4096 ASCII).
	splitEmoji := strings.Repeat("a", breakdownSampleLimit-2) + "😀" + strings.Repeat("b", breakdownSampleLimit)
	gotEmoji := estimateWeightedBytes(splitEmoji)
	// Almost pure ASCII (~8194 bytes), should stay close to len(splitEmoji) without panic or skew.
	if gotEmoji < int64(len(splitEmoji))-64 || gotEmoji > int64(len(splitEmoji))+64 {
		t.Fatalf("splitEmoji weighted bytes = %d, want ~%d", gotEmoji, len(splitEmoji))
	}

	// 2. Valid 3-byte CJK rune split across the cutoff (4095 ASCII + "中" + 4096 ASCII).
	splitCJK := strings.Repeat("a", breakdownSampleLimit-1) + "中" + strings.Repeat("b", breakdownSampleLimit)
	gotCJK := estimateWeightedBytes(splitCJK)
	if gotCJK < int64(len(splitCJK))-64 || gotCJK > int64(len(splitCJK))+64 {
		t.Fatalf("splitCJK weighted bytes = %d, want ~%d", gotCJK, len(splitCJK))
	}

	// 3. Long input filled with invalid UTF-8 bytes at and before the 4096-byte cutoff:
	// must complete in O(1) bounded work without shrinking the sample down to 0.
	invalidAll := strings.Repeat("\xff", 100_000)
	gotInvalid := estimateWeightedBytes(invalidAll)
	if gotInvalid <= 0 {
		t.Fatalf("invalid UTF-8 weighted bytes = %d, want > 0", gotInvalid)
	}

	// 4. Long input with invalid continuation bytes right at the cutoff.
	invalidCont := strings.Repeat("a", breakdownSampleLimit-10) + strings.Repeat("\x80", 20) + strings.Repeat("b", 50_000)
	gotCont := estimateWeightedBytes(invalidCont)
	if gotCont < int64(len(invalidCont)) {
		t.Fatalf("invalid continuation weighted bytes = %d, want >= %d", gotCont, len(invalidCont))
	}

	// 5. Invalid byte 0xff followed by a truncated multi-byte start right before cutoff.
	invalidMix := strings.Repeat("a", breakdownSampleLimit-4) + "\xff\xff\xe4\xb8" + strings.Repeat("b", 10_000)
	gotMix := estimateWeightedBytes(invalidMix)
	if gotMix < int64(len(invalidMix)) {
		t.Fatalf("invalid mix weighted bytes = %d, want >= %d", gotMix, len(invalidMix))
	}
}

func BenchmarkEstimateWeightedBytesInvalidUTF8(b *testing.B) {
	s := strings.Repeat("\xff\xfe\x80\x81", 25_000) // 100 KB of invalid UTF-8
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = estimateWeightedBytes(s)
	}
}
