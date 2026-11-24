package node

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/identity"
)

// When a background supervision round's Claude process forks a new session_id, the node maps the forked branch back to
// its base supervisor so it retains assistant identity for hooks/MCP calls and stays hidden from the ordinary Agents
// list (online and offline).
func TestRoundForkInheritsAssistantIdentityAndStaysHidden(t *testing.T) {
	const forkID = "9f8e7d6c-5b4a-4321-8765-abcdef012345"

	dir := t.TempDir()
	argv, input := filepath.Join(dir, "argv"), filepath.Join(dir, "input")
	fakeClaudeHome(t, "printf '%s\\n' \"$@\" >> "+argv+"\necho ---- >> "+argv+"\n"+
		"sid='"+roundConversation+"'\n"+
		"case \" $* \" in *' --resume-session-at '*) sid='"+forkID+"';; esac\n"+
		"first=1\nwhile IFS= read -r line; do\n  printf '%s\\n' \"$line\" >> "+input+"\n"+
		"  if [ $first = 1 ]; then echo '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"'\"$sid\"'\"}'; first=0; fi\ndone\nsleep 30\n")
	n := kindTestNode(t)
	withFakeHub(t, n)
	roundStarted("2.1.322", 1).write(t, filepath.Join(n.home, ".claude", "projects", "-assistant", roundConversation+".jsonl"))
	n.state.put(roundConversation, sessionState{
		Launcher:                "claude",
		Cwd:                     identity.AssistantDir(n.home),
		Origin:                  "conv1",
		OriginKnown:             true,
		SupervisionInstructions: "rules",
	})
	t.Cleanup(func() { n.stopAssistant(roundConversation) })

	out := sendRound(t, n, "once-fork-1")
	if out["context"] != roundFresh {
		t.Fatalf("sendRound = %v, want context=%q", out, roundFresh)
	}
	waitFor(t, "round fork registered", func() bool { return n.isRoundFork(forkID) })

	if got := n.roundForkParent(forkID); got != roundConversation {
		t.Fatalf("roundForkParent = %q, want %q", got, roundConversation)
	}
	if got := n.canonicalAssistantSession(forkID); got != roundConversation {
		t.Fatalf("canonicalAssistantSession = %q, want %q", got, roundConversation)
	}
	if !n.isAssistantSession(forkID) {
		t.Fatal("forked round branch is not recognized as an assistant session")
	}
	st, ok := n.state.get(forkID)
	if !ok || st.RoundForkOf != roundConversation || !st.OriginKnown || st.Origin != "conv1" || st.SupervisionInstructions != "rules" {
		t.Fatalf("persisted fork state = %+v (ok=%v)", st, ok)
	}

	// Write offline transcripts for the forked round (both under assistant project and with drifted cwd) and confirm
	// neither scanOffline nor sessionList surfaces the forked branch.
	padded := `{"type":"user","uuid":"u1","sessionId":"` + forkID + `","cwd":"/code/example","message":{"role":"user","content":"supervision round"}}` + "\n" +
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"` + forkID + `","cwd":"/code/example","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n" +
		strings.Repeat(" ", 4096) + "\n"
	for _, proj := range []string{n.assistantProject(), "-code-example"} {
		pdir := filepath.Join(n.home, ".claude", "projects", proj)
		if err := os.MkdirAll(pdir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pdir, forkID+".jsonl"), []byte(padded), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	forgetOfflineScan()
	for _, row := range n.scanOffline() {
		if row.SessionID == forkID {
			t.Fatalf("scanOffline leaked forked round branch: %+v", row)
		}
	}
	for _, row := range n.sessionList(context.Background(), false) {
		if row.SessionID == forkID {
			t.Fatalf("sessionList leaked forked round branch: %+v", row)
		}
	}

	// Verify MCP tools and calls from the forked round session are canonicalized to the base supervisor id.
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		n2, hub2 := runtimeNode(t)
		ask := mcpRelay(t, n2, hub2)
		n2.noteRoundFork(forkID, roundConversation)
		n2.mu.Lock()
		n2.procs[roundConversation] = &assistantProc{
			id:     roundConversation,
			cmd:    &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}},
			exited: make(chan struct{}),
		}
		n2.mu.Unlock()
		reqTools := mcpTools(true, forkID)
		reqTools["tool_set"] = roundToolSet
		if got := ask(reqTools); got["assistant"] != true || got["session_id"] != roundConversation || got["tool_set"] != roundToolSet {
			t.Fatalf("mcp.tools relayed %v, want assistant=true session_id=%q tool_set=%q", got, roundConversation, roundToolSet)
		}
		if got := ask(mcpCall(true, forkID)); got["assistant"] != true || got["session_id"] != roundConversation {
			t.Fatalf("mcp.call relayed %v, want assistant=true session_id=%q", got, roundConversation)
		}
	}
}

// An offline transcript in a symlinked-home assistant project whose tail cwd drifted to a non-assistant folder stays
// classified in the assistant directory instead of leaking into the ordinary Agents list.
func TestScanOfflinePreservesAssistantDirWhenCwdDrifts(t *testing.T) {
	t.Setenv("PERSONA_HOME", "")
	n, home := testNode(t)
	n.bgScans, n.metas = map[string]*bgScan{}, map[string]quickMeta{}
	const sid = "12345678-1234-4321-8765-123456789abc"
	projDir := filepath.Join(home, ".claude", "projects", "-data-home-u--persona-assistant")
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","uuid":"u1","sessionId":"` + sid + `","cwd":"/code/example","message":{"role":"user","content":"check status"}}` + "\n" +
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"` + sid + `","cwd":"/code/example","message":{"role":"assistant","content":[{"type":"text","text":"checked"}]}}` + "\n" +
		strings.Repeat(" ", 4096) + "\n"
	if err := os.WriteFile(filepath.Join(projDir, sid+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	forgetOfflineScan()
	var found bool
	for _, row := range n.scanOffline() {
		if row.SessionID != sid {
			continue
		}
		found = true
		if !identity.IsAssistantDir(row.Cwd) {
			t.Fatalf("offline assistant transcript cwd drifted to %q, want assistant dir", row.Cwd)
		}
	}
	if !found {
		t.Fatal("offline assistant transcript was not scanned")
	}
}

// Forking an assistant session copies its assistant metadata (origin, supervision instructions, config dir, model
// follow flag) to the new branch so the branch does not lose assistant state.
func TestForkPreservesAssistantState(t *testing.T) {
	n, _, id := forkFixture(t)
	n.state.put(id, sessionState{
		Launcher:                "claude",
		Cwd:                     identity.AssistantDir(n.home),
		Origin:                  "conv1",
		OriginKnown:             true,
		SupervisionInstructions: "supervision-rules",
		ConfigDir:               "/tmp/claude-cfg",
		FollowsSetting:          true,
	})
	newID, err := n.fork(context.Background(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	st, ok := n.state.get(newID)
	if !ok {
		t.Fatal("forked session missing state")
	}
	if !st.OriginKnown || st.Origin != "conv1" || st.SupervisionInstructions != "supervision-rules" || st.ConfigDir != "/tmp/claude-cfg" || !st.FollowsSetting {
		raw, _ := json.Marshal(st)
		t.Fatalf("forked assistant state lost fields: %s", raw)
	}
}
