package node

import (
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// A session whose file a live pane may be writing is never resumed: it is not a stopped one (ompdisk.go), so a send is
// refused like any other to a session that is not running, and no tab opens.
func TestOmpLivePaneIsNotResumed(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home, _ := piHerdrNode(t)
	cwd := t.TempDir()
	id := "01c00000-0000-7000-8000-000000000001"
	writeOmpSession(t, ompSessionsDir(home), "-w-live", id, "live", cwd, time.Now())
	f.reply("session.snapshot", snapshot(nil, nil, []any{ompPaneAgent("w1:p8", cwd, "")}, nil))
	_ = listSessions(t, n) // the live pane, known only by its fallback id
	f.reset()
	_, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": "hi", "client_msg_id": "m-live"})
	if code, _ := refusalOf(t, err); code == "" {
		t.Fatalf("send: %v", err)
	}
	if _, ok := n.ompSession(id); ok {
		t.Fatal("a file a live pane may be writing was taken as stopped")
	}
	if got := f.trace("tab.create"); len(got) != 0 {
		t.Fatalf("tab.create %v", got)
	}
}

// session.start resuming an omp a runtime lists: refused as another tool's, like any resume of a session open elsewhere.
func TestOmpStartResumeOfARuntimeSessionIsRefused(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home, rh := piHerdrNode(t)
	cwd := t.TempDir()
	id := "01c00000-0000-7000-8000-000000000002"
	writeOmpSession(t, ompSessionsDir(home), "-w-rt", id, "live", cwd, time.Now())
	ompExtension(t, rh, "omp-live", map[string]any{"session_id": id, "status": "working", "pane_id": "w1:p9", "cwd": cwd})
	f.reset()
	_, err := request(t, n, "session.start", map[string]any{"cwd": cwd, "launcher": "omp", "agent_type": "omp", "resume_session_id": id})
	if code, _ := refusalOf(t, err); code != "runtime_session" {
		t.Fatalf("start: %v", err)
	}
	if got := f.trace("tab.create"); len(got) != 0 {
		t.Fatalf("tab.create %v", got)
	}
}

// A row that started by its fallback id and learned its session: the pane becomes the session's, its state moves, and
// herdr gets a resume command from the live session's own file.
func TestOmpFallbackToSessionKeepsRestore(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, f, home, rh := piHerdrNode(t)
	cwd := t.TempDir()
	id := "01c00000-0000-7000-8000-000000000003"
	file := writeOmpSession(t, ompSessionsDir(home), "-w-res", id, "restore", cwd, time.Now())
	seedAgents(t, n, f, ompPaneAgent("w1:p10", cwd, ""))
	fallback := otherSession(host.Agent{Agent: ompAgent, PaneID: "w1:p10"})
	onlyRow(t, listSessions(t, n), fallback)
	ompExtension(t, rh, "omp-r", map[string]any{"session_id": id, "status": "idle", "pane_id": "w1:p10", "cwd": cwd})
	onlyRow(t, listSessions(t, n), id)

	n.mu.Lock()
	pane := n.panes["w1:p10"]
	n.mu.Unlock()
	if pane != id {
		t.Fatalf("the pane is still %q", pane)
	}
	argv, ok := n.agentResumeArgv(agentLaunch{Agent: ompAgent, Launcher: ompAgent, ID: id}, id)
	if !ok || !strings.Contains(strings.Join(argv, " "), "omp --resume "+file) {
		t.Fatalf("resume_argv %q %v", argv, ok)
	}
}

// A fallback row's id in session.start's resume_session_id is the session the pane runs now.
func TestOmpStartResumeAliasesTheFallback(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, _, _, _ := piHerdrNode(t)
	id := "01c00000-0000-7000-8000-000000000004"
	n.mu.Lock()
	n.agents[id] = host.Agent{Agent: ompAgent, PaneID: "w1:p11", AgentStatus: proto.StatusIdle}
	n.idAlias = map[string]paneAlias{"omp-w1-p11": {id: id, pane: "w1:p11"}}
	n.mu.Unlock()
	res, err := request(t, n, "session.start", map[string]any{"cwd": n.home, "launcher": "omp", "resume_session_id": "omp-w1-p11"})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := res["session"].(map[string]any)
	if s["session_id"] != id {
		t.Fatalf("session.start answered %v", res)
	}
}

// A stopped omp answers a rename or interrupt with a refusal instead of pretending to act on a session that is not
// running.
func TestOmpOfflineOtherCallsAreRefused(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "")
	n, _, home, _ := piHerdrNode(t)
	id := "01c00000-0000-7000-8000-000000000005"
	writeOmpSession(t, ompSessionsDir(home), "-w-off", id, "offline", t.TempDir(), time.Now())
	_ = listSessions(t, n)
	for _, method := range []string{"session.rename", "session.interrupt", "session.modes", "session.close", "session.queue.edit", "session.queue.send_now"} {
		params := map[string]any{"session_id": id}
		if method == "session.rename" {
			params["name"] = "new name"
		}
		if _, err := request(t, n, method, params); !isRefusal(err, "omp_stopped") {
			t.Errorf("%s: %v", method, err)
		}
	}
}
