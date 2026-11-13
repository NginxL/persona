package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// codexThread registers a Codex CLI rollout in n's own Codex home, the home whose threads are listed by their bare thread
// id, as a node that scanned ~/.codex has it.
func codexThread(t *testing.T, n *Node, thread string) {
	t.Helper()
	n.codices = newCodexRegistry()
	n.codices.scan = func() []codexProcess { return nil }
	n.dir = filepath.Join(n.home, ".persona")
	home := canonicalCodexHome(t.TempDir(), n.home)
	n.configureCodex(proto.CodexConfig{LegacyHome: home})
	raw, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": thread, "originator": "codex-tui", "cwd": "/w/app"}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, thread+".jsonl")
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if id := n.registerCodex(home, thread, path); id != thread {
		t.Fatalf("the thread is registered as %q, want its own id", id)
	}
}

// A Codex CLI session a runtime hosts and lists under its thread id is also a thread in the node's own Codex
// inventory. A call to stop, rename or close it goes to the runtime that lists it, not to the read-only refusal of an
// external Codex session (#6639).
func TestRuntimeListedCodexSessionAnswersStopRenameAndClose(t *testing.T) {
	const thread = "0e000000-0000-4000-8000-0000000000b1"
	n, _ := runtimeNode(t)
	codexThread(t, n, thread)
	host := dialRuntime(t, n, paseoHello())
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": thread, "agent": "codex", "title": "Hosted CLI", "cwd": "/w/app", "status": "working"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)[thread]; return ok })

	for method, params := range map[string]map[string]any{
		"session.interrupt": {"session_id": thread},
		"session.rename":    {"session_id": thread, "title": "New name"},
		"session.close":     {"session_id": thread},
	} {
		if _, err := nodeCall(t, n, method, params); err != nil {
			t.Errorf("%s: %v", method, err)
		} else if len(host.got(method)) != 1 {
			t.Errorf("%s: the runtime was asked %d times, want 1", method, len(host.got(method)))
		}
	}
}

// A Codex session no runtime lists stays read-only: the refusal is for a thread this machine only found itself.
func TestCodexSessionNoRuntimeListsStaysReadOnly(t *testing.T) {
	const thread = "0e000000-0000-4000-8000-0000000000b2"
	n, _ := runtimeNode(t)
	codexThread(t, n, thread)
	host := dialRuntime(t, n, paseoHello())
	_, err := nodeCall(t, n, "session.rename", map[string]any{"session_id": thread, "title": "New name"})
	if code, _ := refusalOf(t, err); code != "codex_read_only" {
		t.Fatalf("rename: %v", err)
	}
	if len(host.got("session.rename")) != 0 {
		t.Fatal("the runtime was asked about a session it does not list")
	}
}
