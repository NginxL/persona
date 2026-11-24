package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// linkedStateDir makes <root>/.persona-state/assistant and the link <root>/.persona -> .persona-state, as on a computer whose state
// directory was renamed: the node starts the assistant in .persona/assistant, Claude Code names the files it writes by
// the path it resolved (.persona-state/assistant/...). It returns both spellings of the assistant's directory.
func linkedStateDir(t *testing.T) (configured, real string) {
	t.Helper()
	root := t.TempDir()
	real = filepath.Join(root, ".persona-state", "assistant")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".persona-state", filepath.Join(root, ".persona")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	return filepath.Join(root, ".persona", "assistant"), real
}

func TestInWorkspaceResolvesLinks(t *testing.T) {
	configured, real := linkedStateDir(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(real, "out")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path string
		want bool
	}{
		{filepath.Join(real, "designs", "a.md"), true},       // the resolved spelling, a directory not made yet
		{filepath.Join(configured, "designs", "a.md"), true}, // the configured spelling, through the link
		{filepath.Join(real, "notes.md"), true},
		{filepath.Join(real, ".claude", "settings.local.json"), false}, // its own agent's settings still ask
		{filepath.Join(configured, ".git", "config"), false},
		{filepath.Join(real, "out", "x.md"), false},       // a link inside that leads out
		{filepath.Join(real+"-other", "x.md"), false},     // a sibling sharing the prefix
		{filepath.Join(real, "..", "health", "x"), false}, // out through ..
		{real, false},
		{filepath.Join(outside, "x.md"), false},
		{"designs/a.md", false},
	} {
		for _, dir := range []string{configured, real} {
			if got := inWorkspace(c.path, dir); got != c.want {
				t.Errorf("inWorkspace(%s, %s) = %v, want %v", c.path, dir, got, c.want)
			}
		}
	}
}

// An assistant conversation's own directory is written without a card, the path spelled through the link or
// resolved; a file elsewhere, its own agent's settings and a call in plan mode still ask the phone
// (ix_2d0efa1b033728ef: the main conversation's Write to <resolved>/assistant/designs/... went to the phone).
func TestAssistantWorkspaceWritesWithoutCard(t *testing.T) {
	configured, real := linkedStateDir(t)
	outside := t.TempDir()
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution {
		yes := true
		return proto.Resolution{Allow: &yes}
	}
	fh.mu.Unlock()
	const session = "77777777-7777-4777-8777-777777777777"
	n.mu.Lock()
	n.procs[session] = &assistantProc{id: session, agent: "claude", cwd: configured, exited: make(chan struct{})}
	n.mu.Unlock()
	seq := 0
	call := func(tool, mode, path string) string {
		t.Helper()
		seq++
		input, _ := json.Marshal(map[string]any{"file_path": path, "content": "x"})
		raw, _ := json.Marshal(hookInput{HookEventName: "PermissionRequest", SessionID: session, Cwd: real, PermissionMode: mode,
			ToolName: tool, ToolUseID: "toolu_ws" + strconv.Itoa(seq), ToolInput: input})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := n.handleHook(ctx, sockRequest{Input: raw, Attempt: "a1"})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(out)
		return string(b)
	}
	for _, c := range []struct {
		name, tool, mode, path string
		card                   bool
	}{
		{"resolved path", "Write", "default", filepath.Join(real, "designs", "decision-rights.md"), false},
		{"linked path", "Write", "default", filepath.Join(configured, "designs", "b.md"), false},
		{"edit", "Edit", "default", filepath.Join(real, "notes.md"), false},
		{"read", "Read", "default", filepath.Join(configured, "notes.md"), false},
		{"outside", "Write", "default", filepath.Join(outside, "c.md"), true},
		{"its settings", "Write", "default", filepath.Join(real, ".claude", "settings.local.json"), true},
		{"plan mode", "Write", "plan", filepath.Join(real, "d.md"), true},
	} {
		before := len(fh.interactions())
		out := call(c.tool, c.mode, c.path)
		cards := len(fh.interactions()) - before
		if c.card != (cards == 1) || cards > 1 {
			t.Fatalf("%s: %d cards, want card %v", c.name, cards, c.card)
		}
		if !strings.Contains(out, `"behavior":"allow"`) {
			t.Fatalf("%s: not allowed: %s", c.name, out)
		}
		if !c.card && !strings.Contains(out, `"content":"x"`) {
			t.Fatalf("%s: the call's input not kept: %s", c.name, out)
		}
	}
}

// A guest's conversation in its own directory keeps its cards.
func TestGuestWorkspaceWriteStillAsks(t *testing.T) {
	configured, real := linkedStateDir(t)
	n := codexTestNode(t)
	const session = "88888888-8888-4888-8888-888888888888"
	n.state.Sessions[session] = sessionState{Cwd: configured, Guest: []string{"ask_owner"}}
	n.procs[session] = &assistantProc{id: session, agent: "claude", cwd: configured, exited: make(chan struct{})}
	input, _ := json.Marshal(map[string]any{"file_path": filepath.Join(real, "a.md")})
	if n.ownWorkspaceCall(hookInput{SessionID: session, PermissionMode: "default", ToolName: "Write", ToolInput: input}) {
		t.Fatal("a guest's write allowed without a card")
	}
	delete(n.state.Sessions, session)
	if !n.ownWorkspaceCall(hookInput{SessionID: session, PermissionMode: "default", ToolName: "Write", ToolInput: input}) {
		t.Fatal("the assistant's own write asks")
	}
}

// A health conversation's reports rule names the directory both ways: Claude Code names the file by the resolved one.
func TestHealthReportRulesBothSpellings(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".persona-state", "health"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".persona-state", filepath.Join(root, ".persona")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	t.Setenv("HOME", root)
	health := filepath.Join(root, ".persona", "health")
	rules := healthReportRules(health)
	want := []string{"Edit(/" + filepath.Join(health, "reports") + "/**)", "Edit(/" + filepath.Join(realPath(health), "reports") + "/**)"}
	if len(rules) != 2 || rules[0] != want[0] || rules[1] != want[1] {
		t.Fatalf("rules %q, want %q", rules, want)
	}
}
