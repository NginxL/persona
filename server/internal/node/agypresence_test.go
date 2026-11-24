//go:build unix

package node

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/nwverse/persona/server/internal/herdrcore"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgyPresenceConversation(t *testing.T) {
	lock := func(id string) string { return "/home/u/.gemini/antigravity-cli/presence/" + id + ".lock" }
	for _, tc := range []struct {
		name  string
		files []string
		want  string
	}{
		{"one", []string{"/dev/null", lock(agyTestID), "socket:[1]"}, agyTestID},
		{"same twice", []string{lock(agyTestID), lock(agyTestID)}, agyTestID},
		{"two conversations", []string{lock(agyTestID), lock(agyOtherTestID)}, ""},
		{"not a presence dir", []string{"/tmp/other/" + agyTestID + ".lock"}, ""},
		{"not a conversation", []string{lock("settings")}, ""},
		{"none", nil, ""},
	} {
		if got := agyPresenceConversation(tc.files); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// agyPresenceLockFile is the presence lock an agy in a terminal holds for id under dir, held until the test ends or
// release is called.
func agyPresenceLockFile(t *testing.T, dir, id string) (release func()) {
	t.Helper()
	p := filepath.Join(dir, "presence", id+".lock")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	done := false
	release = func() {
		if !done {
			done = true
			f.Close()
		}
	}
	t.Cleanup(release)
	return release
}

func TestAgyPresenceHeld(t *testing.T) {
	dir := t.TempDir()
	if agyPresenceHeld(dir, agyTestID) {
		t.Fatal("no lock file: held")
	}
	release := agyPresenceLockFile(t, dir, agyTestID)
	if !agyPresenceHeld(dir, agyTestID) {
		t.Fatal("a held lock reads as free")
	}
	release()
	if agyPresenceHeld(dir, agyTestID) {
		t.Fatal("a lock left behind by an agy that exited reads as held")
	}
	// The probe let the lock go: an agy starting on the conversation now takes it.
	f, err := os.Open(filepath.Join(dir, "presence", agyTestID+".lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("the probe kept the lock: %v", err)
	}
}

// A herdr-core terminal running agy is an agy pane whose conversation is the presence lock its agy holds (#13603).
func TestHerdrCoreAgyTerminal(t *testing.T) {
	old := agyPresenceOf
	agyPresenceOf = func(pid int) string {
		if pid == 42 {
			return agyTestID
		}
		return ""
	}
	t.Cleanup(func() { agyPresenceOf = old })

	byStatus := herdrcore.Terminal{Address: "t51", Cwd: "/w", Status: &herdrcore.Status{App: "antigravity", State: "done"},
		Foreground: &herdrcore.Foreground{Pid: 42, Name: "agy", Argv: []string{"agy"}}}
	a := fromTerminal(byStatus)
	if a.Agent != agyAgent || a.AgentStatus != proto.StatusDone || reportedAgyConversation(a) != agyTestID {
		t.Fatalf("status antigravity: %+v session %+v", a, a.AgentSession)
	}
	byProgram := herdrcore.Terminal{Address: "t52", Cwd: "/w", Foreground: &herdrcore.Foreground{Pid: 7, Argv: []string{"/usr/local/bin/agy"}}}
	if a := fromTerminal(byProgram); a.Agent != agyAgent || a.AgentSession != nil {
		t.Fatalf("agy before its first status, no presence lock: %+v", a)
	}
	shell := herdrcore.Terminal{Address: "t53", Cwd: "/w", Status: &herdrcore.Status{App: "antigravity", State: "done"},
		Foreground: &herdrcore.Foreground{Pid: 42, Name: "zsh", Argv: []string{"-zsh"}}}
	if a := fromTerminal(shell); a.Agent != "" {
		t.Fatalf("agy exited to its shell: %+v", a)
	}

	n, _, _ := herdrNode(t)
	id := otherSession(a)
	n.mu.Lock()
	n.agents[id] = a
	if n.hostAgents == nil {
		n.hostAgents = map[string][]host.Agent{}
	}
	n.hostAgents[host.HerdrCore] = []host.Agent{a}
	n.mu.Unlock()
	if got := n.agyConversation(id, a.PaneID); got != agyTestID {
		t.Fatalf("herdr-core pane conversation %q, want %q", got, agyTestID)
	}
}

// A message or interrupt for the companion's agy-<conversation> goes to the pane whose agy has that conversation open,
// never to the companion, which would run a second agy on it (#13603).
func TestAgyCompanionRowCallsGoToItsPane(t *testing.T) {
	n, f, home := herdrNode(t)
	n.runtimes = newRuntimeRegistry()
	companion := n.runtimes.add(nil) // a call that reached it would fail: it has no peer
	companion.ready, companion.hello.Runtime = true, agyAgent
	acpRow := "agy-" + agyTestID
	n.runtimes.setSessions(companion, []proto.Session{{SessionID: acpRow, Agent: agyAgent, Cwd: home, Status: proto.StatusIdle}})
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))

	agyTestCall(t, n, "session.send", acpRow, map[string]any{"text": "给 ppe_dev 补 LF"})
	agyTestCall(t, n, "session.interrupt", acpRow, nil)
	assertTrace(t, f.trace("agent.prompt", "pane.send_keys", "pane.send_input"), []string{
		call("agent.prompt", map[string]any{"target": agyTestPane, "text": "给 ppe_dev 补 LF"}),
		sendKeys(agyTestPane, "escape"),
	})
	if _, err := request(t, n, "session.fork", map[string]any{"session_id": acpRow}); refusalCode(err) != codeAgyInTerminal {
		t.Fatalf("fork of a conversation open in a pane: %v", err)
	}
	if pane, _, held := n.agyOpenCall("session.items", acpRow); held || pane != "" {
		t.Fatal("a read of the companion's row was taken from the companion")
	}
}

// With no pane the node knows, a held presence lock keeps the companion out of the conversation; a lock left behind
// by an agy that exited does not.
func TestAgyCompanionRowRefusedWhileATerminalHoldsIt(t *testing.T) {
	n, _, _ := herdrNode(t)
	dir := t.TempDir()
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", dir)
	acpRow := "agy-" + agyTestID
	release := agyPresenceLockFile(t, dir, agyTestID)

	for _, method := range []string{"session.send", proto.MethodSessionSendUnseen, "session.interrupt", "session.rewind"} {
		_, err := request(t, n, method, map[string]any{"session_id": acpRow, "text": "x", "client_msg_id": "c1"})
		if refusalCode(err) != codeAgyInTerminal {
			t.Errorf("%s while a terminal holds the conversation: %v", method, err)
		}
	}
	if _, _, held := n.agyOpenCall("session.items", acpRow); held {
		t.Error("a read was refused")
	}
	if _, _, held := n.agyOpenCall("session.send", "agy-"+agyOtherTestID); held {
		t.Error("another conversation was refused")
	}
	release()
	if _, _, held := n.agyOpenCall("session.send", acpRow); held {
		t.Error("refused after the terminal's agy exited")
	}
}
