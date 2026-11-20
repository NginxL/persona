package node

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// A home reached through a link (/home/me -> /data/home/me): a rule written with ~ blocks a working folder recorded
// with the link followed (as Codex records it) and one recorded through the link (#10053).
func TestBlockedAgentDirMatchesSymlinkedHome(t *testing.T) {
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(real, ".bots", "runs", "workspaces", "w1")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	n := &Node{home: link, logger: log.New(io.Discard, "", 0)}
	n.setBlockedAgentDirs([]string{"~/.bots/runs/*", " ~/.bots/runs/* ", "relative"})
	if !slices.Equal(n.agentDirsBlocked, []string{"~/.bots/runs/*"}) {
		t.Fatalf("patterns kept %v", n.agentDirsBlocked)
	}
	for dir, want := range map[string]bool{
		work: true, // the link followed
		filepath.Join(link, ".bots", "runs", "workspaces", "w1"): true, // through the link
		"~/.bots/runs/workspaces/w2":                             true,
		filepath.Join(real, "code", "app"):                       false,
		"":                                                       false,
	} {
		if got := n.agentDirBlocked(dir); got != want {
			t.Errorf("%q blocked=%v", dir, got)
		}
	}
	if !n.cwdBlocked(work) {
		t.Fatal("the list's check leaves a blocked Agent folder in")
	}
}

// The patterns outlive a restart: a hook or a replay before the hub's node.configure is held to them.
func TestBlockedAgentDirsKeptAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	n := &Node{dir: dir, logger: log.New(io.Discard, "", 0)}
	n.setBlockedAgentDirs([]string{"/work/bot"})
	again := &Node{dir: dir, logger: log.New(io.Discard, "", 0)}
	again.loadBlockedAgentDirs()
	if !again.agentDirBlocked("/work/bot/run-1") {
		t.Fatalf("after a restart: %v", again.agentDirsBlocked)
	}
	n.setBlockedAgentDirs(nil)
	again.loadBlockedAgentDirs()
	if again.agentDirBlocked("/work/bot/run-1") {
		t.Fatal("unblocking is kept too")
	}
}

// A runtime's session in a blocked folder is not listed, nothing it pushes reaches the hub, and its card gets no
// decision at once (dismiss: handed back to the computer), without asking the hub.
func TestBlockedAgentDirRuntimeSession(t *testing.T) {
	n, hub := runtimeNode(t)
	n.setBlockedAgentDirs([]string{"/w/bot"})
	host := dialRuntime(t, n, dshHello())
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": "bot-1", "title": "bot", "cwd": "/w/bot/run-1"},
		{"session_id": "mine", "title": "mine", "cwd": "/w/app"},
	}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["mine"]; return ok })
	if _, ok := listed(t, n)["bot-1"]; ok {
		t.Fatal("a blocked runtime session is listed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	var res proto.Resolution
	if err := host.peer.Call(ctx, "interaction.create", map[string]any{"interaction": map[string]any{
		"kind": "permission", "session_id": "bot-1", "tool_name": "bash", "tool_use_id": "call_1", "summary": "rm -rf build"}}, &res); err != nil {
		t.Fatal(err)
	}
	if !res.Dismiss || res.Allow != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("a blocked session's card: %+v after %v", res, time.Since(start))
	}
	_ = host.peer.Notify("session.items", map[string]any{"session_id": "bot-1", "items": []map[string]any{{"seq": 1, "kind": "text", "text": "x"}}})
	_ = host.peer.Notify("session.delta", map[string]any{"session_id": "bot-1", "text": "x"})
	_ = host.peer.Notify("session.delta", map[string]any{"session_id": "mine", "text": "y"})
	waitFor(t, "the visible session's delta", func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return len(hub.deltas) > 0
	})
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.asked) != 0 || len(hub.items) != 0 || len(hub.deltas) != 1 {
		t.Fatalf("the hub heard of the blocked session: asked=%d items=%d deltas=%d", len(hub.asked), len(hub.items), len(hub.deltas))
	}
	for _, list := range hub.lists {
		if slices.ContainsFunc(list, func(s proto.Session) bool { return s.SessionID == "bot-1" }) {
			t.Fatal("a pushed list carries the blocked session")
		}
	}
}

// A Claude Code hook from a blocked folder: no decision at once, no card and no hook.event; the same hook elsewhere
// reaches the hub.
func TestBlockedAgentDirClaudeHook(t *testing.T) {
	n, hub := runtimeNode(t)
	n.setBlockedAgentDirs([]string{"/w/bot"})
	hook := func(sid, cwd string) (any, error) {
		raw, _ := json.Marshal(hookInput{HookEventName: "PermissionRequest", SessionID: sid, Cwd: cwd, ToolName: "Bash",
			ToolInput: json.RawMessage(`{"command":"rm -rf build"}`)})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return n.handleHook(ctx, sockRequest{Input: raw, Attempt: "a-" + sid})
	}
	start := time.Now()
	if out, err := hook("11111111-0000-0000-0000-00000000b0b1", "/w/bot/run-1"); err != nil || out != nil || time.Since(start) > time.Second {
		t.Fatalf("blocked hook answered %v after %v", out, time.Since(start))
	}
	hub.mu.Lock()
	events, asked := slices.Clone(hub.events), len(hub.asked)
	hub.mu.Unlock()
	if asked != 0 || slices.Contains(events, "hook.event") {
		t.Fatalf("the hub heard of a blocked hook: asked=%d events=%v", asked, events)
	}
	go func() { _, _ = hook("22222222-0000-0000-0000-00000000a991", "/w/app") }() // waits on the hub's answer
	waitFor(t, "the visible session's hook", func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return slices.Contains(hub.events, "hook.event")
	})
}

// node.configure as the manager sends it: agent_dirs_blocked null (the manager could not read its rules) or absent
// leaves the node's blocks as they are; a list, the empty one included, replaces them.
func TestBlockedAgentDirsConfigureNullKeepsBlocks(t *testing.T) {
	n, _ := testNode(t)
	n.setBlockedAgentDirs([]string{"/w/bot"})
	for _, params := range []map[string]any{{"agent_dirs_blocked": nil}, {"machine_id": "m1"}} {
		if _, err := request(t, n, "node.configure", params); err != nil {
			t.Fatal(err)
		}
		if !n.agentDirBlocked("/w/bot/run-1") {
			t.Fatalf("node.configure %v unblocked the node", params)
		}
	}
	if _, err := request(t, n, "node.configure", map[string]any{"agent_dirs_blocked": []string{}}); err != nil {
		t.Fatal(err)
	}
	if n.agentDirBlocked("/w/bot/run-1") {
		t.Fatal("an empty list did not clear the blocks")
	}
}

// A blocked Agent folder never takes the assistant's own conversations or the sessions the hub keeps out of the
// list (docs/protocol/protocol.md "Agent directory visibility"), at rest as well as running: the offline scan exempts
// them as the live list does. Another session there stays out.
func TestBlockedAgentDirKeepsAssistantAndKeptSessionsAtRest(t *testing.T) {
	t.Setenv("PERSONA_HOME", "")
	n, _, home := herdrNode(t)
	assistant := identity.AssistantDir(home)
	work := filepath.Join(home, "work", "bots")
	for _, d := range []string{assistant, work} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	const mine, kept, other = "eeeeeeee-0000-0000-0000-000000000001", "eeeeeeee-0000-0000-0000-000000000002", "eeeeeeee-0000-0000-0000-000000000003"
	writeDrift(t, home, mine, assistant, assistant)
	for _, id := range []string{kept, other} {
		// Padded past what history takes for a probe that never said anything (sessions_history.go).
		f, err := os.OpenFile(writeDrift(t, home, id, work, work), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(strings.Repeat(" ", 4096) + "\n")
		_ = f.Close()
	}
	if _, err := request(t, n, "node.configure", map[string]any{"keep_sessions": []string{kept}, "agent_dirs_blocked": []string{assistant, work}}); err != nil {
		t.Fatal(err)
	}
	offlineCache.Lock()
	offlineCache.at = time.Time{}
	offlineCache.Unlock()
	got := map[string]bool{}
	for _, s := range n.sessionList(context.Background(), false) {
		got[s.SessionID] = true
	}
	if !got[mine] || !got[kept] || got[other] {
		t.Fatalf("listed at rest: assistant %v, kept %v, other %v", got[mine], got[kept], got[other])
	}
	// sessions.history lists at rest by the same rule: the kept session, not the other one (the assistant's
	// conversations are never history rows).
	history := func() map[string]bool {
		t.Helper()
		ans, err := n.sessionsHistory(t.Context(), mustHistoryJSON(t, map[string]any{"since": time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339Nano), "source": "claude"}))
		if err != nil {
			t.Fatal(err)
		}
		in := map[string]bool{}
		for _, r := range ans.Rows {
			in[r.Session.SessionID] = true
		}
		return in
	}
	if h := history(); !h[kept] || h[other] {
		t.Fatalf("history: kept %v, other %v", h[kept], h[other])
	}
	// A blocked Claude config folder still takes a kept session out of history, as it does of the list at rest.
	n.cdirs = newClaudeDirs()
	n.cdirs.scan = func() map[int]procEnv { return nil }
	n.cdirs.blocked = []string{work}
	if h := history(); h[kept] {
		t.Fatal("history: a kept session under a blocked Claude config folder is listed")
	}
}
