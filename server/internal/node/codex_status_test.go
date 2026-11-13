package node

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// herdr said idle through a whole turn of a Codex joined on its app-server (#4865): a reporter holding the pane decides
// its state there (#4657), and the node took herdr's word for a Codex as it was. A Codex the host calls done, idle or
// unknown is working while its title shows Codex's spinner; blocked and working keep the host's word, as do botmux's
// sessions and agents other than Claude and Codex.
func TestCodexStatusOverTheHost(t *testing.T) {
	n := &Node{}
	for _, tc := range []struct {
		name, agent, pane, status, title, want string
	}{
		{"idle with the spinner", "codex", "w1:p1", proto.StatusIdle, "⠦ fix the build | repo", proto.StatusWorking},
		{"done with the spinner", "codex", "w1:p1", proto.StatusDone, "⠋ fix the build | repo", proto.StatusWorking},
		{"unknown with the spinner", "codex", "w1:p1", proto.StatusUnknown, "⠏ fix | repo", proto.StatusWorking},
		{"the spinner after a status prefix", "codex", "w1:p1", proto.StatusIdle, "[ . ] ⠸ fix | repo", proto.StatusWorking},
		{"no spinner", "codex", "w1:p1", proto.StatusIdle, "fix the build | repo", proto.StatusIdle},
		{"no title", "codex", "w1:p1", proto.StatusDone, "", proto.StatusDone},
		{"asking the user", "codex", "w1:p1", proto.StatusIdle, "[ ! ] Action Required | ⠸ fix | repo", proto.StatusIdle},
		{"braille inside a word", "codex", "w1:p1", proto.StatusDone, "⠦⠦ fix | repo", proto.StatusDone},
		{"a braille cell that is no frame", "codex", "w1:p1", proto.StatusDone, "⣿ fix | repo", proto.StatusDone},
		{"blocked stays", "codex", "w1:p1", proto.StatusBlocked, "⠦ fix | repo", proto.StatusBlocked},
		{"working stays", "codex", "w1:p1", proto.StatusWorking, "fix | repo", proto.StatusWorking},
		{"botmux's word", "codex", "botmux:s1", proto.StatusIdle, "⠦ fix | repo", proto.StatusIdle},
		{"not a Codex", "pi", "w1:p1", proto.StatusIdle, "⠦ fix", proto.StatusIdle},
	} {
		a := host.Agent{Agent: tc.agent, PaneID: tc.pane, AgentStatus: tc.status, RawTitle: tc.title}
		if got := n.liveStatus("00000001-0000-4000-8000-000000000000", a, time.Now()); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

// A Codex pane joined on its app-server is working while its turn runs there, whatever its host says (#4865): a message
// from the phone as soon as turn/start answers, a turn started on the computer from turn/started. Its row goes back to
// the host's word when the turn completes, and agent says what the row says.
func TestCodexDaemonPaneIsWorkingWhileItsTurnRuns(t *testing.T) {
	for _, hostStatus := range []string{proto.StatusIdle, proto.StatusDone} {
		t.Run(hostStatus, func(t *testing.T) {
			n, f, c, _ := daemonPane(t)
			n.mu.Lock()
			a := n.agents[remoteRaw]
			a.AgentStatus = hostStatus
			a.RawTitle = ""
			n.agents[remoteRaw] = a
			n.mu.Unlock()
			joinPane(t, n)
			status := func() string { return paneRow(t, n, remoteRaw).Status }
			active := func(on bool) { // what the app-server's own read says from now on
				f.mu.Lock()
				f.active = on
				f.mu.Unlock()
			}
			if got := status(); got != hostStatus {
				t.Fatalf("no turn yet: %s, want host status %s", got, hostStatus)
			}

			active(true)
			if err := n.send(context.Background(), remoteRaw, "run the tests", nil, sendOpts{}); err != nil {
				t.Fatal(err)
			}
			if got := status(); got != proto.StatusWorking {
				t.Fatalf("a message from the phone: %s", got)
			}
			if a, _ := n.agent(remoteRaw); a.AgentStatus != proto.StatusWorking {
				t.Fatalf("agent says %s", a.AgentStatus)
			}
			active(false)
			endTurn(t, f, c)
			waitFor(t, "the host's word once the turn completed", func() bool { return status() == hostStatus })
			if a, _ := n.agent(remoteRaw); a.AgentStatus != hostStatus {
				t.Fatalf("agent after the completed turn: %s, want %s", a.AgentStatus, hostStatus)
			}

			active(true)
			f.push(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": remoteRaw, "turn": map[string]any{"id": "turn-2"}}})
			waitFor(t, "working from turn/started", func() bool { return status() == proto.StatusWorking })
			active(false)
			endTurn(t, f, c)
			waitFor(t, "host status again", func() bool { return status() == hostStatus })
			if a, _ := n.agent(remoteRaw); a.AgentStatus != hostStatus {
				t.Fatalf("agent after the terminal turn: %s, want %s", a.AgentStatus, hostStatus)
			}
		})
	}
}
