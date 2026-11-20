package node

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/rpc"
)

func TestTaskExecutionResumesRuntimeOwnedConversation(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	host := dialRuntime(t, n, assistantRuntimeHello())
	dir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	const id = "session-task"
	uuid := newUUID()
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": id, "cwd": dir, "launcher": "dsh", "started_by_persona": true, "status": "idle"},
		{"session_id": uuid, "cwd": dir, "started_by_persona": true, "status": "idle"},
	}})
	waitFor(t, "runtime task listed", func() bool { _, _, ok := n.runtimes.rowOf(id); return ok })
	if _, ok := n.state.get(id); ok {
		t.Fatal("fixture must exercise runtime-only state")
	}
	var sendError error
	host.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.send" {
			return map[string]any{}, sendError
		}
		return map[string]any{}, nil
	}
	send := func(sid, cwd, launcher string) error {
		_, err := nodeCall(t, n, "assistant.send", map[string]any{"session_id": sid, "cwd": cwd, "launcher": launcher, "text": "Check files", "task_execution": true, "resume_only": true})
		return err
	}
	for _, sid := range []string{id, uuid} {
		for _, launcher := range []string{"", "dsh"} {
			for range 2 {
				if err := send(sid, dir, launcher); err != nil {
					t.Fatalf("valid strict runtime resume with launcher %q refused: %v", launcher, err)
				}
			}
		}
	}
	if got := len(host.got("session.send")); got != 8 || len(host.got("session.start")) != 0 {
		t.Fatalf("runtime was restarted: sends=%d starts=%d", got, len(host.got("session.start")))
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("runtime task preparation wrote into the selected directory: %v %v", entries, err)
	}
	for _, args := range [][2]string{{t.TempDir(), ""}, {t.TempDir(), "dsh"}, {dir, "other-agent"}} {
		if err := send(id, args[0], args[1]); err == nil || !strings.Contains(err.Error(), "context differs") {
			t.Fatalf("incompatible runtime context accepted or classified as gone: %v", err)
		}
	}
	// The sender resolves a stored launcher before falling back to the session's runtime owner.
	// A stale stored launcher must not route an inherited send to a different connected runtime.
	hello := assistantRuntimeHello()
	hello["runtime"], hello["name"], hello["launcher"] = "other-runtime", "Other", "other-agent"
	other := dialRuntime(t, n, hello)
	stored, _ := n.state.get(id)
	stale := stored
	stale.Launcher = "other-agent"
	n.state.put(id, stale)
	if err := send(id, dir, ""); err == nil || !strings.Contains(err.Error(), "context differs") {
		t.Fatalf("inherited stale runtime owner accepted: %v", err)
	}
	if len(host.got("session.send")) != 8 || len(other.got("session.send")) != 0 || len(other.got("session.start")) != 0 {
		t.Fatal("owner mismatch delivered or replaced the conversation")
	}
	n.state.put(id, stored)
	if err := send("session-gone", dir, "dsh"); err == nil || !strings.Contains(err.Error(), "is gone") {
		t.Fatalf("missing runtime conversation was not refused before delivery: %v", err)
	}
	sendError = &rpc.Error{Code: "delivery_unknown", Message: "runtime reply lost"}
	if err := send(id, dir, "dsh"); err == nil || !strings.Contains(err.Error(), "runtime reply lost") || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime delivery error was replaced: %v", err)
	}
	if len(host.got("session.send")) != 9 || len(host.got("session.start")) != 0 {
		t.Fatal("refused/unknown runtime sends caused a replacement or retry")
	}
	// A known runtime without authoritative execution metadata is uncertain, not a confirmed missing session.
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{}})
	waitFor(t, "runtime list cleared", func() bool { _, _, ok := n.runtimes.rowOf(id); return !ok })
	n.state.persistMu.Lock()
	n.state.mu.Lock()
	delete(n.state.Sessions, id)
	n.state.mu.Unlock()
	n.state.persistMu.Unlock()
	if err := send(id, dir, "dsh"); err == nil || strings.Contains(err.Error(), "is gone") || len(host.got("session.send")) != 9 {
		t.Fatalf("uncertain runtime ownership was treated as gone or sent without validation: %v", err)
	}
}
