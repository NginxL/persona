package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The background supervisor's rounds (#7315, assistant_round.go). The transcripts below are reduced to what Claude Code
// 2.1.322 writes and roundAnchor reads.

const roundConversation = "7c1e2d3f-4a5b-4c6d-8e7f-901a2b3c4d5e"

// roundLines builds a transcript's lines.
type roundLines struct {
	lines   []string
	version string
}

func (l *roundLines) add(v map[string]any) *roundLines {
	if _, set := v["version"]; !set && l.version != "" {
		v["version"] = l.version
	}
	raw, _ := json.Marshal(v)
	l.lines = append(l.lines, string(raw))
	return l
}

func roundParent(p string) any {
	if p == "" {
		return nil
	}
	return p
}

// user is a message someone wrote (text), hanging from parent.
func (l *roundLines) user(uuid, parent, text string) *roundLines {
	return l.add(map[string]any{"type": "user", "uuid": uuid, "parentUuid": roundParent(parent), "sessionId": roundConversation,
		"message": map[string]any{"role": "user", "content": text}})
}

// result is a tool's result going back to the model.
func (l *roundLines) result(uuid, parent string) *roundLines {
	return l.add(map[string]any{"type": "user", "uuid": uuid, "parentUuid": roundParent(parent), "message": map[string]any{"role": "user",
		"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "ok"}}}})
}

// reply is an answer of the model's that stopped for stop ("end_turn", "tool_use").
func (l *roundLines) reply(uuid, parent, stop string) *roundLines {
	return l.add(map[string]any{"type": "assistant", "uuid": uuid, "parentUuid": roundParent(parent), "message": map[string]any{"role": "assistant",
		"content": []any{map[string]any{"type": "text", "text": "好"}}, "stop_reason": stop}})
}

func (l *roundLines) turnEnd(uuid, parent string) *roundLines {
	return l.add(map[string]any{"type": "system", "subtype": "turn_duration", "uuid": uuid, "parentUuid": roundParent(parent)})
}

// roundStarted is a conversation's start turn (u0 → a0, the anchor) and n rounds hanging from a0.
func roundStarted(version string, rounds int) *roundLines {
	l := &roundLines{version: version}
	l.user("u0", "", "这是后台督办对话。回一句「好」就结束。").reply("a0", "u0", "end_turn").turnEnd("t0", "a0")
	for i := 1; i <= rounds; i++ {
		r := fmt.Sprintf("r%d", i)
		l.user(r, "a0", "📋 督办 "+r).reply(r+"a", r, "end_turn").turnEnd(r+"t", r+"a")
	}
	return l
}

func (l *roundLines) write(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(l.lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The anchor is the end of the first turn on the chain Claude Code resumes, or none.
func TestRoundAnchor(t *testing.T) {
	withTool := &roundLines{version: "2.1.322"}
	withTool.user("u0", "", "start").reply("a0", "u0", "tool_use").result("x0", "a0").reply("a1", "x0", "end_turn").turnEnd("t0", "a1").
		user("r1", "a1", "📋 round").reply("r1a", "r1", "end_turn")
	sidechain := roundStarted("2.1.322", 0)
	sidechain.add(map[string]any{"type": "assistant", "uuid": "side", "parentUuid": "a0", "isSidechain": true,
		"message": map[string]any{"role": "assistant", "content": []any{}, "stop_reason": "end_turn"}})
	sidechain.user("r1", "a0", "📋 round").reply("r1a", "r1", "end_turn")
	compacted := roundStarted("2.1.322", 2)
	compacted.add(map[string]any{"type": "system", "subtype": "compact_boundary", "uuid": "cb", "parentUuid": nil, "logicalParentUuid": "r2a"}).
		add(map[string]any{"type": "user", "uuid": "cs", "parentUuid": "cb", "isCompactSummary": true, "message": map[string]any{"role": "user", "content": "summary"}}).
		user("r3", "cs", "📋 round 3").reply("r3a", "r3", "end_turn")
	unanswered := &roundLines{version: "2.1.322"}
	unanswered.user("u0", "", "start").reply("a0", "u0", "tool_use").result("x0", "a0")
	meta := &roundLines{version: "2.1.322"}
	meta.add(map[string]any{"type": "user", "uuid": "m0", "parentUuid": nil, "isMeta": true, "message": map[string]any{"role": "user", "content": "caveat"}}).
		user("u0", "m0", "start").reply("a0", "u0", "end_turn").user("r1", "a0", "📋 round").reply("r1a", "r1", "end_turn")
	broken := roundStarted("2.1.322", 1)
	broken.user("r2", "gone", "📋 round 2").reply("r2a", "r2", "end_turn")
	owner := roundStarted("2.1.322", 2)
	owner.user("o1", "r2t", "a question of the owner's").reply("o1a", "o1", "end_turn") // a plain resume of the latest round
	for _, c := range []struct {
		name  string
		lines *roundLines
		want  string
	}{
		{"a plain start turn", roundStarted("2.1.322", 0), "a0"},
		{"a start turn with a tool call: its end", withTool, "a1"},
		{"a subagent's line is not the conversation's", sidechain, "a0"},
		{"rounds already branched from it", roundStarted("2.1.322", 3), "a0"},
		{"the owner wrote after the latest round", owner, "a0"},
		{"a meta line before the first message", meta, "a0"},
		{"compacted: the first turn is off the chain", compacted, ""},
		{"the first turn never ended", unanswered, ""},
		{"a chain that does not reach it", broken, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := c.lines.write(t, filepath.Join(t.TempDir(), "t.jsonl"))
			got, err := roundAnchor(path)
			if err != nil {
				t.Fatal(err)
			}
			st, _ := os.Stat(path)
			if got.anchor != c.want || got.version != "2.1.322" || got.size != st.Size() {
				t.Fatalf("roundAnchor = %+v, want anchor %q, version 2.1.322, size %d", got, c.want, st.Size())
			}
		})
	}
	if _, err := roundAnchor(filepath.Join(t.TempDir(), "none.jsonl")); err == nil {
		t.Fatal("a file that is not there has an anchor")
	}
}

// roundRefusal is what Claude Code 2.1.322 prints on stdout for a --resume-session-at it cannot find, before it says so
// on stderr and exits 1: a result with no turn, and no init before it.
const roundRefusal = `{"type":"result","subtype":"error_during_execution","duration_ms":0,"is_error":true,"num_turns":0,` +
	`"stop_reason":null,"session_id":"` + roundConversation + `","errors":["No message found with message.uuid of: x"]}`

// roundTestNode is a node with the conversation's transcript lines on disk and a fake claude that logs each start's
// arguments (one per line, "----" after each) to argv and each message it reads to input. failRound: it refuses a
// --resume-session-at as Claude Code does an anchor it cannot find (roundRefusal).
func roundTestNode(t *testing.T, lines *roundLines, failRound bool) (n *Node, argv, input string) {
	t.Helper()
	n, argv, input, _ = roundTestHub(t, lines, failRound)
	return n, argv, input
}

// roundTestHub is roundTestNode with the hub the node pushes its rows to (fakeHub.rows).
func roundTestHub(t *testing.T, lines *roundLines, failRound bool) (n *Node, argv, input string, hub *fakeHub) {
	t.Helper()
	dir := t.TempDir()
	argv, input = filepath.Join(dir, "argv"), filepath.Join(dir, "input")
	refuse := ""
	if failRound {
		refuse = "case \" $* \" in *' --resume-session-at '*) echo '" + roundRefusal + "'; " +
			"echo 'No message found with message.uuid of: x' >&2; exit 1;; esac\n"
	}
	fakeClaudeHome(t, "printf '%s\\n' \"$@\" >> "+argv+"\necho ---- >> "+argv+"\n"+refuse+
		"first=1\nwhile IFS= read -r line; do\n  printf '%s\\n' \"$line\" >> "+input+"\n"+
		"  if [ $first = 1 ]; then echo '{\"type\":\"system\",\"subtype\":\"init\"}'; first=0; fi\ndone\nsleep 30\n")
	n = kindTestNode(t)
	hub = withFakeHub(t, n)
	lines.write(t, filepath.Join(n.home, ".claude", "projects", "-assistant", roundConversation+".jsonl"))
	n.state.put(roundConversation, sessionState{Launcher: "claude", Cwd: t.TempDir(), OriginKnown: true})
	t.Cleanup(func() { n.stopAssistant(roundConversation) })
	return n, argv, input, hub
}

// roundStarts are the argument lists of each process the fake claude was started as.
func roundStarts(t *testing.T, argv string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for _, start := range strings.Split(strings.TrimSuffix(string(raw), "----\n"), "----\n") {
		out = append(out, strings.Split(strings.TrimSuffix(start, "\n"), "\n"))
	}
	return out
}

// argAfter is what follows name in args ("" when it is not there).
func argAfter(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func hasArg(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// roundInputs are the messages the fake claude read, in order.
func roundInputs(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// sendRound is the manager's assistant.send of a round, and the node's answer.
func sendRound(t *testing.T, n *Node, onceID string) map[string]any {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"session_id": roundConversation, "text": "📋 督办：一个任务要你处理", "resume_only": true,
		"once_id": onceID, "supervise_round": true})
	out, err := n.handle(context.Background(), "assistant.send", params)
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)
}

// idleRound makes the conversation's process idle, as a while after its turn's result: settled (stopSettled).
func idleRound(n *Node) *assistantProc {
	n.mu.Lock()
	p := n.procs[roundConversation]
	n.mu.Unlock()
	if p != nil {
		p.mu.Lock()
		p.busy = false
		p.lastOut.Store(int64(time.Since(p.born) - assistantSettle)) // its last line a settle ago (printed)
		p.mu.Unlock()
	}
	return p
}

const fullSnapshot = "实时快照（这条消息发出时的" // i18n-ignore: the turn context's own words (prompts/assistant/turn_context.md)

// A round goes into a new process resumed at the end of the first turn, with the round's tools only and the whole
// snapshot (the earlier rounds that saw it are out of its context); the answer says so. The idle process before it
// stops; the one the round runs in is never compacted when idle, and the owner's next message, which needs every tool,
// replaces it with a plain resume (assistant_round_switch_test.go).
func TestAssistantSendSuperviseRoundResumesAtTheFirstTurn(t *testing.T) {
	n, argv, input := roundTestNode(t, roundStarted("2.1.322", 2), false)
	if _, err := n.assistantSend(context.Background(), roundConversation, "", "", "the owner's message", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	before := idleRound(n)
	out := sendRound(t, n, "once-1")
	if out["context"] != roundFresh || out["why"] != nil || out["session_id"] != roundConversation {
		t.Fatalf("answer %v", out)
	}
	if size, _ := out["transcript_bytes"].(int64); size <= 0 {
		t.Fatalf("no transcript size in %v", out)
	}
	if before.alive() {
		t.Fatal("the idle process before the round still runs")
	}
	all := roundStarts(t, argv)
	if len(all) != 2 {
		t.Fatalf("%d starts", len(all))
	}
	plain, round := all[0], all[1]
	if argAfter(plain, "--resume") != roundConversation || hasArg(plain, "--resume-session-at") || argAfter(plain, "--tools") != assistantBuiltinTools || hasArg(plain, "--strict-mcp-config") {
		t.Fatalf("the owner's message started as %q", plain)
	}
	if argAfter(round, "--resume") != roundConversation || argAfter(round, "--resume-session-at") != "a0" || argAfter(round, "--tools") != roundBuiltinTools || !hasArg(round, "--strict-mcp-config") {
		t.Fatalf("the round started as %q", round)
	}
	var mcp struct {
		Servers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(argAfter(round, "--mcp-config")), &mcp); err != nil || len(mcp.Servers) == 0 {
		t.Fatalf("the round's MCP servers: %v %s", err, argAfter(round, "--mcp-config"))
	}
	for name, s := range mcp.Servers { // Persona's own, under its old name too for an old hub (compat), and no other
		if s.Command != n.bin || strings.Join(s.Args, " ") != "mcp" || s.Env["PERSONA_TOOL_SET"] != roundToolSet || s.Env["PERSONA_SESSION_ID"] != roundConversation {
			t.Fatalf("server %s: %+v", name, s)
		}
	}
	msgs := roundInputs(t, input)
	if len(msgs) != 2 || !strings.Contains(msgs[1], fullSnapshot) || !strings.Contains(msgs[1], "一个任务要你处理") {
		t.Fatalf("the round got %q", msgs)
	}
	p := idleRound(n)
	p.mu.Lock()
	p.context = assistantIdleCompact * 2
	compactable := p.idleCompactable()
	p.mu.Unlock()
	if !p.round || compactable {
		t.Fatalf("the round's process: round %v, compactable %v", p.round, compactable)
	}
	// The owner writing to it: a plain resume of the latest round with every tool, not the round's process.
	if _, err := n.assistantSend(context.Background(), roundConversation, "", "", "and this one?", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	if all := roundStarts(t, argv); len(all) != 3 || argAfter(all[2], "--resume") != roundConversation || hasArg(all[2], "--resume-session-at") ||
		argAfter(all[2], "--tools") != assistantBuiltinTools || hasArg(all[2], "--strict-mcp-config") {
		t.Fatalf("the owner's message after the round: %q", all)
	}
	// The same message asked again under its once id: not sent again, and its answer says nothing of a round.
	if again := sendRound(t, n, "once-1"); again["context"] != nil {
		t.Fatalf("the repeated message's answer: %v", again)
	}
	if got := len(roundInputs(t, input)); got != 3 {
		t.Fatalf("%d messages read", got)
	}
}

// A process that is answering, or runs background tasks, takes the round as any message, with the context it has:
// stopping it would lose its work.
func TestAssistantSendSuperviseRoundWhileBusyKeepsContext(t *testing.T) {
	n, argv, input := roundTestNode(t, roundStarted("2.1.322", 1), false)
	if _, err := n.assistantSend(context.Background(), roundConversation, "", "", "the owner's message", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	out := sendRound(t, n, "once-busy") // the first message's turn has not ended: busy
	if out["context"] != roundKept || out["why"] != roundWhyBusy {
		t.Fatalf("busy: %v", out)
	}
	p := idleRound(n)
	p.mu.Lock()
	p.tasks = []string{"a background command"}
	p.mu.Unlock()
	if out := sendRound(t, n, "once-tasks"); out["context"] != roundKept || out["why"] != roundWhyBusy {
		t.Fatalf("with a background task: %v", out)
	}
	if got := len(roundStarts(t, argv)); got != 1 {
		t.Fatalf("%d starts", got)
	}
	// The send returns once the message is written to the process; the fake claude appends it to its file after that.
	waitFor(t, "the third message", func() bool { return len(roundInputs(t, input)) == 3 })
	if msgs := roundInputs(t, input); len(msgs) != 3 || strings.Contains(msgs[1], fullSnapshot) {
		t.Fatalf("messages %q", msgs)
	}
	if p.round {
		t.Fatal("the process is marked a round's")
	}
}

// With no first turn on the chain (a compaction cut it off), a round goes in as any message does, with every tool, and
// the answer says why: the manager replaces the background supervisor.
func TestAssistantSendSuperviseRoundWithoutAnchorResumesAsBefore(t *testing.T) {
	compacted := roundStarted("2.1.322", 1)
	compacted.add(map[string]any{"type": "system", "subtype": "compact_boundary", "uuid": "cb", "parentUuid": nil}).
		user("r2", "cb", "📋 round 2").reply("r2a", "r2", "end_turn")
	n, argv, _ := roundTestNode(t, compacted, false)
	out := sendRound(t, n, "once-1")
	if out["context"] != roundKept || out["why"] != roundWhyNoAnchor {
		t.Fatalf("answer %v", out)
	}
	if size, _ := out["transcript_bytes"].(int64); size <= 0 {
		t.Fatalf("no transcript size in %v", out)
	}
	args := roundStarts(t, argv)[0]
	if argAfter(args, "--resume") != roundConversation || hasArg(args, "--resume-session-at") || argAfter(args, "--tools") != assistantBuiltinTools || hasArg(args, "--strict-mcp-config") {
		t.Fatalf("started as %q", args)
	}
	if p := idleRound(n); p.round {
		t.Fatal("the process is marked a round's")
	}
}

// A round's process that does not start (Claude Code refusing the anchor: the flag changed, or the anchor went
// meanwhile) is started once more as before, and the message goes in there. The refusal's result line on stdout is no
// start.
func TestAssistantSendSuperviseRoundStartFailureRetriesPlain(t *testing.T) {
	n, argv, input := roundTestNode(t, roundStarted("2.1.322", 1), true)
	out := sendRound(t, n, "once-1")
	if out["context"] != roundKept || out["why"] != roundWhyStartFailed {
		t.Fatalf("answer %v", out)
	}

	all := roundStarts(t, argv)
	if len(all) != 2 || argAfter(all[0], "--resume-session-at") != "a0" || hasArg(all[1], "--resume-session-at") || argAfter(all[1], "--tools") != assistantBuiltinTools {
		t.Fatalf("starts %q", all)
	}
	if msgs := roundInputs(t, input); len(msgs) != 1 || !strings.Contains(msgs[0], "一个任务要你处理") {
		t.Fatalf("messages %q", msgs)
	}
	if p := idleRound(n); p == nil || p.round {
		t.Fatal("the plain process is missing or marked a round's")
	}
}

// Only Claude Code takes a round: Codex and a guest conversation keep their context, with no anchor read. (Agent
// runtimes never get here: assistantSend refuses supervision on one before it looks at a round.)
func TestSuperviseRoundOnCodexOrGuestKeepsContext(t *testing.T) {
	n := codexTestNode(t)
	for _, c := range []struct {
		name  string
		agent string
		guest bool
	}{
		{"codex", "codex", false},
		{"a guest", "claude", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, r := withSuperviseRound(context.Background())
			if got := n.superviseRound(context.Background(), r, roundConversation, nil, c.agent, c.guest); got.anchor != "" {
				t.Fatalf("a round: %+v", got)
			}
			if out := r.answer(map[string]any{}); out["context"] != roundKept || out["why"] != roundWhyUnsupported {
				t.Fatalf("answer %v", out)
			}
		})
	}
	// A live process of another agent's, the same.
	_, r := withSuperviseRound(context.Background())
	p := &assistantProc{agent: "codex", exited: make(chan struct{})}
	if got := n.superviseRound(context.Background(), r, roundConversation, p, "claude", false); got.anchor != "" {
		t.Fatalf("a round on another agent's process: %+v", got)
	}
	if out := r.answer(map[string]any{}); out["context"] != roundKept || out["why"] != roundWhyUnsupported {
		t.Fatalf("answer %v", out)
	}
	// No round asked for: nothing on the answer, and nothing to set.
	var none *roundResult
	none.set(roundKept, roundWhyBusy)
	if out := none.answer(map[string]any{"session_id": "x"}); len(out) != 1 {
		t.Fatalf("answer %v", out)
	}
}

// A Claude Code older than the one --resume-session-at was measured on keeps the context; one whose version is not known
// either (the running process's, then the transcript's, then the machine's).
func TestSuperviseRoundOldClaudeKeepsContext(t *testing.T) {
	for _, c := range []struct {
		name, version, running string
		want                   string
	}{
		{"older", "2.1.300", "", roundWhyUnsupported},
		{"not known", "", "", roundWhyUnsupported},
		{"the running process is newer than its transcript's last line", "2.1.300", "2.1.322", ""},
		{"newer", "2.2.0", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			n, argv, _ := roundTestNode(t, roundStarted(c.version, 1), false)
			var p *assistantProc
			if c.running != "" {
				if _, err := n.assistantSend(context.Background(), roundConversation, "", "", "hello", nil, "", ""); err != nil {
					t.Fatal(err)
				}
				p = idleRound(n)
				p.mu.Lock()
				p.claudeVersion = c.running
				p.mu.Unlock()
			}
			_, r := withSuperviseRound(context.Background())
			got := n.superviseRound(context.Background(), r, roundConversation, p, "claude", false)
			out := r.answer(map[string]any{})
			if c.want == "" {
				if got.anchor != "a0" || got.toolSet != roundToolSet || out["context"] != roundFresh {
					t.Fatalf("%+v %v", got, out)
				}
				return
			}
			if got.anchor != "" || out["context"] != roundKept || out["why"] != c.want {
				t.Fatalf("%+v %v", got, out)
			}
			if _, err := os.Stat(argv); c.running == "" && err == nil {
				t.Fatal("a process was started")
			}
		})
	}
}

// A round's process is not compacted when it is let go idle: that would cut the first turn off the chain the next
// round resumes on.
func TestRoundProcessIsNotIdleCompacted(t *testing.T) {
	p := &assistantProc{agent: "claude", context: assistantIdleCompact + 1}
	if !p.idleCompactable() {
		t.Fatal("an ordinary long conversation is not compacted")
	}
	p.round = true
	if p.idleCompactable() {
		t.Fatal("a round's process is compacted")
	}
}

// The round's answer holds what was decided, its size once read, and nothing else.
func TestRoundResultAnswer(t *testing.T) {
	ctx, r := withSuperviseRound(context.Background())
	if superviseRoundFrom(ctx) != r || superviseRoundFrom(context.Background()) != nil {
		t.Fatal("the round is not on its context only")
	}
	if out := r.answer(map[string]any{"session_id": "x"}); len(out) != 1 {
		t.Fatalf("undecided: %v", out)
	}
	r.sized(1234)
	r.set(roundFresh, "")
	if out := r.answer(map[string]any{}); out["context"] != roundFresh || out["why"] != nil || out["transcript_bytes"] != int64(1234) {
		t.Fatalf("fresh: %v", out)
	}
	r.set(roundKept, roundWhyNoAnchor)
	if out := r.answer(map[string]any{}); out["context"] != roundKept || out["why"] != roundWhyNoAnchor {
		t.Fatalf("kept: %v", out)
	}
}
