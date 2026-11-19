package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestAssistantManualCompactionRejectsCanceledExternalRequest(t *testing.T) {
	n := assistantNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	launcher := proto.ExternalLauncherName("example-adapter", "example-target")
	in := &stdinBuf{}
	p := &assistantProc{id: id, agent: "claude", launcher: launcher, stdin: in}
	n.procs[id] = p
	n.state.put(id, sessionState{Launcher: launcher})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if code := refusalCode(n.assistantCompact(ctx, id, false)); code != "adapter_unavailable" {
		t.Fatalf("canceled external compaction: %q", code)
	}
	if in.Len() != 0 || p.compacting || p.compacted || p.manual {
		t.Fatal("a canceled external compaction wrote input or changed the process")
	}
	if st, _ := n.state.get(id); st.Compaction != nil || n.externalStarts.has(id) {
		t.Fatal("a canceled external compaction recorded a start or retained its startup token")
	}
}

// The user's compaction: refused while the conversation answers or compacts, /compact sent otherwise; it is running
// until the boundary comes (done), the turn ends without one (failed, with claude's words), or the process is gone.
func TestAssistantManualCompaction(t *testing.T) {
	n := assistantNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	in := &stdinBuf{}
	p := &assistantProc{id: id, agent: "claude", stdin: in, busy: true}
	n.procs[id] = p
	stream := func(lines ...string) { n.readAssistant(p, strings.NewReader(strings.Join(lines, "\n")+"\n")) }
	state := func() proto.Compaction {
		t.Helper()
		st, _ := n.state.get(id)
		if st.Compaction == nil {
			t.Fatal("no compaction recorded")
		}
		return *st.Compaction
	}

	if code := refusalCode(n.assistantCompact(t.Context(), id, false)); code != "busy" {
		t.Fatalf("while it answers: %q", code)
	}
	p.busy = false
	if err := n.assistantCompact(t.Context(), id, true); err != nil { // only_running (the hub's, #8253): it runs
		t.Fatal(err)
	}
	if got := strings.TrimSpace(in.String()); got != `{"message":{"content":"/compact","role":"user"},"type":"user"}` {
		t.Fatalf("sent %s", got)
	}
	if c := state(); c.State != proto.CompactionRunning || c.StartedAt.IsZero() || c.FinishedAt != nil {
		t.Fatalf("started: %+v", c)
	}
	if code := refusalCode(n.assistantCompact(t.Context(), id, false)); code != "compacting" {
		t.Fatalf("twice: %q", code)
	}
	stream(`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"manual","pre_tokens":180902,"post_tokens":9500}}`,
		`{"type":"result","subtype":"success"}`)
	if c := state(); c.State != proto.CompactionDone || c.FinishedAt == nil || c.Error != "" {
		t.Fatalf("after the boundary: %+v", c)
	}
	// An automatic compaction in a turn of its own is not the user's: what they see stays.
	stream(`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto"}}`)
	if c := state(); c.State != proto.CompactionDone {
		t.Fatalf("an automatic one: %+v", c)
	}

	if err := n.assistantCompact(t.Context(), id, false); err != nil {
		t.Fatal(err)
	}
	stream(`{"type":"result","subtype":"success","is_error":false,"result":"Error: Not enough messages to compact."}`)
	if c := state(); c.State != proto.CompactionFailed || c.Error != "Error: Not enough messages to compact." {
		t.Fatalf("without a boundary: %+v", c)
	}
	p.mu.Lock()
	compacting := p.compacting
	p.mu.Unlock()
	if compacting {
		t.Fatal("still compacting after the turn ended")
	}

	if err := n.assistantCompact(t.Context(), id, false); err != nil {
		t.Fatal(err)
	}
	n.sweepCompactions()
	if c := state(); c.State != proto.CompactionRunning {
		t.Fatalf("its process is still there: %+v", c)
	}
	delete(n.procs, id) // it exited, or the node restarted under it
	n.sweepCompactions()
	if c := state(); c.State != proto.CompactionFailed || !strings.Contains(c.Error, "进程退出") {
		t.Fatalf("its process is gone: %+v", c)
	}
	s := proto.Session{SessionID: id}
	n.decorate(&s)
	if s.Compaction == nil || s.Compaction.State != proto.CompactionFailed {
		t.Fatalf("on the session list: %+v", s.Compaction)
	}
}

// What a new main conversation is told: the last compaction's summary (Claude Code's or Codex's), and the messages
// after it, the newest kept.
func TestHandoffReadsTheLastSummaryAndWhatCameAfter(t *testing.T) {
	jsonl := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"第一个问题"}}`,
		`{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"旧的摘要"}}`,
		`{"type":"system","subtype":"compact_boundary"}`,
		`{"type":"user","isCompactSummary":true,"message":{"role":"user","content":[{"type":"text","text":"示例摘要"}]}}`,
		`{"type":"user","message":{"role":"user","content":"之后的问题"}}`,
	}, "\n")
	summary, at := lastCompactSummary(strings.NewReader(jsonl))
	if summary != "示例摘要" || at != 4 {
		t.Fatalf("claude: %q at %d", summary, at)
	}
	if summary, at := lastCompactSummary(strings.NewReader(`{"type":"compacted","payload":{"message":"Codex 的摘要"}}`)); summary != "Codex 的摘要" || at != 1 {
		t.Fatalf("codex: %q at %d", summary, at)
	}
	if summary, at := lastCompactSummary(strings.NewReader(`{"type":"user","message":{"role":"user","content":"x"}}`)); summary != "" || at != 0 {
		t.Fatalf("never compacted: %q at %d", summary, at)
	}

	items := []proto.Item{
		{Seq: 1, Role: "user", Kind: proto.KindText, Text: "压缩前的问题"},
		{Seq: 5, Role: "user", Kind: proto.KindText, Text: "<persona-context kind=\"context\" title=\"接上旧主对话\" url=\"\">\n更早的\n</persona-context>\n<persona-context>\n快照\n</persona-context>\n之后的问题"},
		{Seq: 6, Role: "assistant", Kind: proto.KindToolUse, Text: "工具"},
		{Seq: 7, Role: "assistant", Kind: proto.KindText, Text: strings.Repeat("长", handoffTurnMax+50)},
		{Seq: 8, Role: "system", Kind: proto.KindNotice, Text: "提示"},
		{Seq: 9, Role: "user", Kind: proto.KindText, Text: "<persona-context kind=\"context\" title=\"接上旧主对话\" url=\"\">\n更早的\n</persona-context>\n<persona-context>\n快照\n</persona-context>\n"},
	}
	got := recentTurns(items, 4)
	if len(got) != 2 || got[0] != (HandoffTurn{Role: "user", Text: "之后的问题"}) || got[1].Role != "assistant" ||
		len([]rune(got[1].Text)) != handoffTurnMax+1 {
		t.Fatalf("recent: %+v", got)
	}
	var many []proto.Item
	for i := range 40 {
		many = append(many, proto.Item{Seq: i + 1, Role: "user", Kind: proto.KindText, Text: strings.Repeat("字", 700)})
	}
	got = recentTurns(many, 0)
	total := 0
	for _, t := range got {
		total += len([]rune(t.Text))
	}
	if len(got) == 0 || len(got) > handoffTurns || total > handoffRecentMax || got[len(got)-1].Text != many[39].Text {
		t.Fatalf("bounded, newest kept: %d messages, %d characters", len(got), total)
	}
}

// Compacting a meeting's room whose process is not running (#8253: the hub compacts long meetings' rooms) starts it as
// any resume of the room does: no built-in tools, the strict MCP config with only its meeting tools, the project's
// setting sources and its own settings, never the assistant's flags. The hub asks only_running, which refuses
// not_running instead: its process exited since the hub saw it running, and it is not started only to be compacted.
func TestCompactingAStoppedRoomKeepsItsRestrictions(t *testing.T) {
	fakeClaudeHome(t, "for a in \"$@\"; do printf ' [%s]' \"$a\"; done > \"$HOME/args-$$\"\nread line\necho '{\"type\":\"system\",\"subtype\":\"init\"}'\nsleep 30\n")
	n := codexTestNode(t)
	n.dir = filepath.Join(os.Getenv("HOME"), ".persona")
	if err := os.MkdirAll(n.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	withFakeHub(t, n)
	rec, room := "0f8fad5b-d9cb-469f-a165-70867728950e", "7d4a3c1e-1111-4222-8333-444455556666"
	out, err := n.assistantGuestPrepare(json.RawMessage(`{"dir":".persona/guest-meeting/` + rec + `","claude_md":"# room","kind":"meeting"}`))
	if err != nil {
		t.Fatal(err)
	}
	cwd, _ := out.(map[string]any)["cwd"].(string)
	tools := []string{"meeting_lines", "meeting_earlier", "meeting_topics_set"}
	if _, err := n.assistantSend(withNewID(withGuestKind(context.Background(), tools, guestMeeting), room), "", cwd, "claude", "the meeting's header", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.stopAssistant(room) })
	st, _ := n.state.get(room)
	transcript := filepath.Join(n.assistantConfigPath(st.ConfigDir), "projects", claudeProjectName(cwd), room+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte(`{"type":"user","message":{"role":"user","content":"the meeting's header"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n.setPath(room, transcript)
	n.stopAssistant(room) // idle long enough: let go

	// The hub compacting it asks only_running: refused, nothing starts, no compaction is recorded.
	if code := refusalCode(n.assistantCompact(context.Background(), room, true)); code != "not_running" {
		t.Fatalf("only_running on a room that is not running: %q", code)
	}
	n.mu.Lock()
	p := n.procs[room]
	n.mu.Unlock()
	if st, _ := n.state.get(room); p != nil || st.Compaction != nil {
		t.Fatalf("only_running started it: process %v, compaction %+v", p != nil, st.Compaction)
	}
	if err := n.assistantCompact(context.Background(), room, false); err != nil {
		t.Fatal(err)
	}
	line := startedArgs(t, "[--resume] ["+room+"]")
	for _, want := range []string{"[--tools] []", "[--strict-mcp-config]", "[--setting-sources] [project]",
		"[--allowedTools] [" + strings.Join(compat.MCPToolNames(tools), ",") + "]"} {
		if !strings.Contains(line, want) {
			t.Fatalf("the room's process for its compaction has no %s: %s", want, line)
		}
	}
	if strings.Contains(line, assistantBuiltinTools) {
		t.Fatalf("the room's process got the assistant's tools: %s", line)
	}
	assertGuestSettings(t, line, "assistant-room-"+room+".json", cwd)
	if st, _ := n.state.get(room); st.Compaction == nil || st.Compaction.State != proto.CompactionRunning {
		t.Fatalf("compaction %+v", st.Compaction)
	}
}

// A guest whose process is not running and whose launcher runs another agent by now (a meeting's room, an
// extension's guest) is never compacted: its process would start on that agent, with its own tools and the owner's
// setup. Refused before anything starts, and no compaction is recorded as running; spawnAssistant refuses it to any
// other caller too.
func TestCompactingAGuestOnAnotherAgentIsRefused(t *testing.T) {
	n := codexTestNode(t)
	n.dir = filepath.Join(n.home, ".persona")
	withFakeHub(t, n)
	probedLaunchers(t, map[string]string{"work": "codex"})
	room, guest := "7d4a3c1e-1111-4222-8333-444455556666", "8e5b4d2f-2222-4333-8444-555566667777"
	dir := n.assistantDirOf(".persona/guest-meeting/0f8fad5b-d9cb-469f-a165-70867728950e")
	n.state.put(room, sessionState{Launcher: "work", Cwd: dir, Guest: []string{"meeting_lines"}, GuestKind: guestMeeting})
	n.state.put(guest, sessionState{Launcher: "work", Cwd: filepath.Join(n.dir, "guest"), Guest: []string{"lark_send"}})
	for _, id := range []string{room, guest} {
		transcript := filepath.Join(n.home, ".claude", "projects", "x", id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(transcript, []byte(`{"type":"user","message":{"role":"user","content":"x"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		n.setPath(id, transcript)
		if err := n.assistantCompact(context.Background(), id, false); !errors.Is(err, errGuestAgent) {
			t.Fatalf("%s on a Codex launcher compacted: %v", id, err)
		}
		if _, err := n.spawnAssistant(id, dir, "work", true, false); !errors.Is(err, errGuestAgent) {
			t.Fatalf("%s started on a Codex launcher: %v", id, err)
		}
		n.mu.Lock()
		p := n.procs[id]
		n.mu.Unlock()
		if st, _ := n.state.get(id); p != nil || st.Compaction != nil {
			t.Fatalf("%s: process %v, compaction %+v", id, p != nil, st.Compaction)
		}
	}
}

// The hub compacting a meeting's room (#8253) asks only_running: the room's process exited since the hub saw it running
// (let go in a quiet spell), so it is refused not_running, a refusal the hub tries again after, and nothing starts:
// neither its process nor a compaction. Without only_running (the owner's compaction, an older hub) it starts as before.
func TestCompactingOnlyRunningLeavesAStoppedOneAlone(t *testing.T) {
	n := codexTestNode(t)
	n.dir = filepath.Join(n.home, ".persona")
	withFakeHub(t, n)
	probedLaunchers(t, map[string]string{"room-cc": "claude"})
	room := "7d4a3c1e-1111-4222-8333-444455556666"
	dir := n.assistantDirOf(".persona/guest-meeting/0f8fad5b-d9cb-469f-a165-70867728950e")
	n.state.put(room, sessionState{Launcher: "room-cc", Cwd: dir, Guest: []string{"meeting_lines"}, GuestKind: guestMeeting})
	transcript := filepath.Join(n.home, ".claude", "projects", "x", room+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte(`{"type":"user","message":{"role":"user","content":"x"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n.setPath(room, transcript)
	err := n.assistantCompact(context.Background(), room, true)
	if code := refusalCode(err); code != "not_running" {
		t.Fatalf("only_running on a room that is not running: %v", err)
	}
	n.mu.Lock()
	p := n.procs[room]
	n.mu.Unlock()
	if st, _ := n.state.get(room); p != nil || st.Compaction != nil {
		t.Fatalf("only_running: process %v, compaction %+v", p != nil, st.Compaction)
	}
}
