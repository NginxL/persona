package node

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A turn whose end the node missed (#14351, assistant_stale_turn.go): the row is checked against Claude Code's own
// record and the transcript where it is computed (assistantSessions), so every reader of it (the relay drain, a
// deployment waiting for the main conversation) sees the turn over.

const staleTurnID = "14351000-aaaa-4bbb-8ccc-000000014351"

// staleTurnNode is a node with conversation staleTurnID running in p, said busy and quiet for longer than
// assistantStaleTurn, its transcript ending on the lines given, and Claude Code's record saying status since then.
func staleTurnNode(t *testing.T, status string, lines ...string) (*Node, *assistantProc) {
	t.Helper()
	n := assistantNode(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(n.home, ".claude"))
	path := writeAssistantTranscript(t, n.home, staleTurnID)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &assistantProc{id: staleTurnID, agent: "claude", cmd: &exec.Cmd{}, exited: make(chan struct{}), busy: true,
		born: time.Now().Add(-time.Hour)}
	p.lastOut.Store(int64(10 * time.Minute)) // its last line 50 minutes ago
	n.procs[staleTurnID] = p
	since := time.Now().Add(-49 * time.Minute)
	runtime := claudeRuntimeTurn
	claudeRuntimeTurn = func(_ *Node, q *assistantProc) (string, time.Time, bool) {
		if q != p {
			return "", time.Time{}, false
		}
		return status, since, status != ""
	}
	t.Cleanup(func() { claudeRuntimeTurn = runtime })
	return n, p
}

const (
	staleUser     = `{"type":"user","uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`
	staleEnded    = `{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}`
	staleToolCall = `{"type":"assistant","uuid":"a2","parentUuid":"u1","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1"}]}}`
	staleTrailer  = `{"type":"system","subtype":"stop_hook_summary"}` + "\n" + `{"type":"last-prompt"}` + "\n" + `{"type":"cost-state"}`
)

func rowStatus(t *testing.T, n *Node, id string) string {
	t.Helper()
	for _, s := range n.assistantSessions() {
		if s.SessionID == id {
			return s.Status
		}
	}
	t.Fatalf("no row for %s", id)
	return ""
}

// The main conversation's turn ended (its transcript ends on the answer, then Claude Code's bookkeeping lines) and Claude
// Code says idle, but the node never saw the turn's end: the row says done, and the process is no longer busy.
func TestAStaleTurnIsOverWhenClaudeCodeSaysIdle(t *testing.T) {
	n, p := staleTurnNode(t, "idle", staleUser, staleEnded, staleTrailer)
	if got := rowStatus(t, n, staleTurnID); got != proto.StatusDone {
		t.Fatalf("status %q, want done: the turn ended", got)
	}
	p.mu.Lock()
	busy := p.busy
	p.mu.Unlock()
	if busy {
		t.Fatal("the process is still busy")
	}
}

// A message Claude Code said it queued and then said nothing more of keeps a round's process from settling (workingLocked):
// an idle Claude Code holds no queued message, so the row settles too.
func TestAStaleHeldMessageIsDroppedWhenClaudeCodeSaysIdle(t *testing.T) {
	n, p := staleTurnNode(t, "idle", staleUser, staleEnded)
	p.mu.Lock()
	p.busy, p.round, p.lifecycle = false, true, true
	p.written = []assistantInput{{id: "w1", text: "relayed", queued: true, at: time.Now().Add(-time.Hour)}}
	p.mu.Unlock()
	if got := rowStatus(t, n, staleTurnID); got != proto.StatusDone {
		t.Fatalf("status %q, want done", got)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.written) != 0 || p.workingLocked() {
		t.Fatalf("still holds %+v", p.written)
	}
}

// What keeps the turn: a tool that runs long prints nothing either (its transcript ends on the call), Claude Code saying
// busy, no record of the process, a recent idle, background tasks, a compaction, a model recovery, output meanwhile.
func TestAQuietTurnStaysWorkingUnlessItEnded(t *testing.T) {
	cases := []struct {
		name   string
		status string
		lines  []string
		setup  func(n *Node, p *assistantProc)
	}{
		{name: "tool call running", status: "idle", lines: []string{staleUser, staleToolCall}},
		{name: "prompt not answered", status: "idle", lines: []string{staleEnded, staleUser}},
		{name: "claude code busy", status: "busy", lines: []string{staleUser, staleEnded}},
		{name: "claude code waiting", status: "waiting", lines: []string{staleUser, staleEnded}},
		{name: "no record", status: "", lines: []string{staleUser, staleEnded}},
		{name: "no message line", status: "idle", lines: []string{staleTrailer}},
		{name: "printed recently", status: "idle", lines: []string{staleUser, staleEnded}, setup: func(_ *Node, p *assistantProc) {
			p.printed()
		}},
		{name: "background tasks", status: "idle", lines: []string{staleUser, staleEnded}, setup: func(_ *Node, p *assistantProc) {
			p.tasks = []string{"a monitor"}
		}},
		{name: "compacting", status: "idle", lines: []string{staleUser, staleEnded}, setup: func(_ *Node, p *assistantProc) {
			p.compacting = true
		}},
		{name: "model recovery", status: "idle", lines: []string{staleUser, staleEnded}, setup: func(n *Node, _ *assistantProc) {
			n.recoveries = map[string]*assistantTurnRecovery{staleTurnID: {}}
		}},
		{name: "exited", status: "idle", lines: []string{staleUser, staleEnded}, setup: func(_ *Node, p *assistantProc) {
			close(p.exited)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, p := staleTurnNode(t, c.status, c.lines...)
			if c.setup != nil {
				c.setup(n, p)
			}
			if n.reconcileStaleTurn(p) {
				t.Fatal("the turn was taken for over")
			}
			p.mu.Lock()
			busy := p.busy
			p.mu.Unlock()
			if !busy {
				t.Fatal("busy was cleared")
			}
		})
	}
}

// Claude Code's idle has to have lasted: one written a moment ago may be a turn's end whose result is still in the pipe.
func TestARecentIdleKeepsTheTurn(t *testing.T) {
	n, p := staleTurnNode(t, "idle", staleUser, staleEnded)
	claudeRuntimeTurn = func(*Node, *assistantProc) (string, time.Time, bool) { return "idle", time.Now(), true }
	if n.reconcileStaleTurn(p) {
		t.Fatal("a fresh idle ended the turn")
	}
}

// A process is looked at once per assistantStaleRecheck, not on every list (the list is built every second).
func TestAStaleTurnIsLookedAtOncePerRecheck(t *testing.T) {
	n, p := staleTurnNode(t, "busy", staleUser, staleEnded)
	looks := 0
	claudeRuntimeTurn = func(*Node, *assistantProc) (string, time.Time, bool) {
		looks++
		return "busy", time.Now().Add(-time.Hour), true
	}
	for range 5 {
		n.reconcileStaleTurn(p)
	}
	if looks != 1 {
		t.Fatalf("looked %d times, want once", looks)
	}
}

// Claude Code's record of the process is its own sessions/<pid>.json, with the status and when it was written.
func TestClaudeSessionFileTurnReadsTheProcessRecord(t *testing.T) {
	n := assistantNode(t)
	dir := filepath.Join(n.home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	p := &assistantProc{id: staleTurnID, agent: "claude", cmd: &exec.Cmd{Process: self}, exited: make(chan struct{})}
	if _, _, ok := n.claudeSessionFileTurn(p); ok {
		t.Fatal("a record with no file")
	}
	at := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	body := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"kind":"interactive","entrypoint":"sdk-cli","status":"idle","statusUpdatedAt":%d}`,
		os.Getpid(), staleTurnID, at.UnixMilli())
	if err := os.WriteFile(filepath.Join(dir, "sessions", fmt.Sprintf("%d.json", os.Getpid())), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	status, since, ok := n.claudeSessionFileTurn(p)
	if !ok || status != "idle" || !since.Equal(at) {
		t.Fatalf("got %q %v %v, want idle since %v", status, since, ok, at)
	}
	if _, _, ok := n.claudeSessionFileTurn(&assistantProc{cmd: &exec.Cmd{}}); ok {
		t.Fatal("a process with no pid has a record")
	}
}

// The transcript's end: its last message line decides, Claude Code's bookkeeping lines after it and subagents' lines do
// not, and a tail cut in the middle of a line skips that line.
func TestTranscriptTurnOver(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, fmt.Sprintf("%d.jsonl", time.Now().UnixNano()))
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	sidechain := `{"type":"user","isSidechain":true,"message":{"role":"user","content":"sub"}}`
	cases := []struct {
		name        string
		body        string
		over, known bool
	}{
		{"answer", staleUser + "\n" + staleEnded + "\n", true, true},
		{"answer then bookkeeping", staleUser + "\n" + staleEnded + "\n" + staleTrailer + "\n", true, true},
		{"answer then a subagent", staleEnded + "\n" + sidechain + "\n", true, true},
		{"tool call", staleUser + "\n" + staleToolCall + "\n", false, true},
		{"tool result", staleToolCall + "\n" + `{"type":"user","message":{"role":"user","content":[{"type":"tool_result"}]}}` + "\n", false, true},
		{"no newline at the end", staleUser + "\n" + staleEnded, true, true},
		{"nothing", staleTrailer + "\n", false, false},
	}
	for _, c := range cases {
		over, known := transcriptTurnOver(write(c.body))
		if over != c.over || known != c.known {
			t.Errorf("%s: over %v known %v, want %v %v", c.name, over, known, c.over, c.known)
		}
	}
	if _, known := transcriptTurnOver(filepath.Join(dir, "none.jsonl")); known {
		t.Error("a missing file is known")
	}
	// A tail that starts inside a huge line: that line is not read, the ones after it are.
	huge := `{"type":"user","message":{"role":"user","content":"` + strings.Repeat("x", assistantStaleTail) + `"}}`
	if over, known := transcriptTurnOver(write(huge + "\n" + staleEnded + "\n")); !over || !known {
		t.Errorf("after a cut line: over %v known %v", over, known)
	}
}
