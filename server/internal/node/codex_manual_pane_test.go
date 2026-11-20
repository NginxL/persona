package node

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestManualCodexPaneOwnsItsFirstConversation(t *testing.T) {
	n, f, home := herdrNode(t)
	withCodexWaits(t)
	n.codices = newCodexRegistry()
	n.codices.checked = func() ([]codexProcess, bool) { return nil, true }
	n.configureCodex(proto.CodexConfig{Homes: []string{filepath.Join(home, ".codex")}})
	cwd := realPath(mkdir(t, filepath.Join(home, "Playground")))
	const old = "01a19a00-0000-7000-8000-0000000000f1"
	const next = "01a19a00-0000-7000-8000-0000000000f2"
	yesterday := time.Now().Add(-18 * time.Hour)
	path := writeWaitRollout(t, home, cwd, old, yesterday, "Earlier prompt")
	if err := os.Chtimes(path, yesterday, yesterday); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "session_index.jsonl"), []byte(`{"id":"`+old+`","thread_name":"Yesterday's notes"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshotOf := func(id string, live bool) {
		var agents []any
		if live {
			agents = []any{agentOf("codex", id, proto.StatusIdle, "w1:p2", "w1:t2", "w1", cwd, "Playground")}
		}
		workspaces := []any{map[string]any{"workspace_id": "w1", "label": "Playground", "number": 1}}
		tabs := []any{map[string]any{"tab_id": "w1:t2", "workspace_id": "w1", "label": "test agent"}}
		f.reply("session.snapshot", snapshot(workspaces, tabs, agents, nil))
	}
	snapshotOf("", false)
	var history proto.Session
	for _, row := range listSessions(t, n) {
		if row.SessionID == old {
			history = row
		}
	}
	if history.PaneID != "" || history.Status != proto.StatusOffline || history.Title != "Yesterday's notes" || history.UpdatedAt.IsZero() {
		t.Fatalf("setup history: id=%s pane=%q status=%q title=%q updated=%v", history.SessionID, history.PaneID, history.Status, history.Title, history.UpdatedAt)
	}
	snapshotOf("", true)
	var waiting proto.Session
	for _, row := range listSessions(t, n) {
		if row.PaneID == "w1:p2" {
			waiting = row
		}
	}
	if !isCodexWaitID(waiting.SessionID) || !waiting.AwaitingFirstMessage || waiting.Title != "test agent" || waiting.SpaceName != "Playground" || waiting.StartedByPersona || !waiting.UpdatedAt.After(history.UpdatedAt) {
		t.Fatalf("new tab: id=%q awaiting=%v title=%q was_started_by_persona=%v updated=%v; finished id=%s", waiting.SessionID, waiting.AwaitingFirstMessage, waiting.Title, waiting.StartedByPersona, waiting.UpdatedAt, old)
	}
	items := func(id string) string {
		t.Helper()
		reply, err := request(t, n, "session.items", map[string]any{"session_id": id})
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(reply["items"])
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if page := items(waiting.SessionID); page != "[]" {
		t.Fatalf("new tab's transcript: %s", page)
	}
	writeWaitRollout(t, home, cwd, next, time.Now(), "First words in this tab")
	snapshotOf(next, true)
	listSessions(t, n)
	if w := n.codexWaitByID(waiting.SessionID); w != nil {
		n.lookForCodexThreadsOnce(context.Background(), []*codexWait{w}, true)
	}
	foundOld, foundNew := false, false
	for _, row := range listSessions(t, n) {
		switch row.SessionID {
		case old:
			foundOld = row.PaneID == "" && row.Title == history.Title && row.Status == history.Status && row.UpdatedAt.Equal(history.UpdatedAt)
		case next:
			foundNew = row.PaneID == "w1:p2" && !row.AwaitingFirstMessage && !row.StartedByPersona
		case waiting.SessionID:
			t.Fatalf("old waiting row remained in list: %s", row.SessionID)
		}
	}
	if !foundOld || !foundNew {
		t.Fatalf("finished history intact=%v, manual thread in its own pane=%v", foundOld, foundNew)
	}
	if page := items(old); !strings.Contains(page, "Earlier prompt") || strings.Contains(page, "First words in this tab") {
		t.Fatalf("finished transcript changed: %s", page)
	}
	if _, owned := n.state.get(next); owned || len(f.trace("pane.report_metadata", "pane.report_agent_session")) > 0 {
		t.Fatal("discovering a manual Codex claimed its launch or changed the user's tab metadata")
	}
	if page := items(waiting.SessionID); !strings.Contains(page, "First words in this tab") || strings.Contains(page, "Earlier prompt") {
		t.Fatalf("waiting id did not follow the new transcript: %s", page)
	}
}

func TestManualCodexPaneNeedsAnUnambiguousThread(t *testing.T) {
	n, f, home := herdrNode(t)
	withCodexWaits(t)
	cwd := realPath(mkdir(t, filepath.Join(home, "Playground")))
	const first, second = "01a19a00-0000-7000-8000-0000000000d1", "01a19a00-0000-7000-8000-0000000000d2"
	const reported = "01a19a00-0000-7000-8000-0000000000d3"
	for _, id := range []string{first, second} {
		writeWaitRollout(t, home, cwd, id, time.Now().Add(-18*time.Hour), "Earlier work")
	}
	index := `{"id":"` + first + `","thread_name":"Release review"}` + "\n" + `{"id":"` + second + `","thread_name":"Release review"}` + "\n"
	if err := os.WriteFile(filepath.Join(home, ".codex", "session_index.jsonl"), []byte(index), 0600); err != nil {
		t.Fatal(err)
	}
	snapshotOf := func(firstID string) {
		f.reply("session.snapshot", snapshot(nil, nil, []any{
			agentOf("codex", firstID, proto.StatusIdle, "w1:p2", "w1:t2", "w1", cwd, "Release review | Playground"),
			agentOf("codex", "", proto.StatusIdle, "w1:p3", "w1:t3", "w1", cwd, "Playground"),
			agentOf("codex", reported, proto.StatusIdle, "w1:p4", "w1:t4", "w1", cwd, "Release review | Playground"),
		}, nil))
		listSessions(t, n)
	}
	snapshotOf("")
	a, b := n.codexWaitOf("w1:p2"), n.codexWaitOf("w1:p3")
	if a == nil || b == nil || a == b {
		t.Fatal("a title shared by two threads must leave the new panes waiting")
	}
	if row, ok := n.agent(reported); !ok || row.PaneID != "w1:p4" {
		t.Fatal("the ambiguous title overruled a thread id the host explicitly reported")
	}
	if !n.claimCodexThread("w1:p9", first) {
		t.Fatal("could not reserve a thread for a concurrent app start")
	}
	if n.attachCodexWait(context.Background(), a, first, "same thread") {
		t.Fatal("the manual pane took another start's in-flight claim")
	}
	n.unclaimCodexThread(first)
	n.panes["w1:p77"] = first // a previous app start is remembered for restore, but its tab is closed
	// Both panes can hear the same thread before a refresh has published its owner.
	var group sync.WaitGroup
	var attached [2]bool
	for i, w := range []*codexWait{a, b} {
		group.Go(func() { attached[i] = n.attachCodexWait(context.Background(), w, first, "simultaneous observation") })
	}
	group.Wait()
	if attached[0] == attached[1] {
		t.Fatalf("expected one pane to have acquired the thread: %v", attached)
	}
	for i, w := range []*codexWait{a, b} {
		if attached[i] != (n.codexAttachedThread(w.id) == first) || (!attached[i] && n.codexWaitByID(w.id) != w) {
			t.Fatalf("wrong waiting identity after the claims: %s", w.id)
		}
	}
}

func TestManualCodexPanesKeepTheirIdentityAcrossRefreshAndRestart(t *testing.T) {
	n, f, home := herdrNode(t)
	withCodexWaits(t)
	cwd := realPath(mkdir(t, filepath.Join(home, "Playground")))
	const firstPane, secondPane = "w1:p2", "w1:p3"
	first := host.Agent{Agent: "codex", PaneID: firstPane, TabID: "w1:t2", Cwd: cwd, AgentStatus: proto.StatusIdle}
	second := host.Agent{Agent: "codex", PaneID: secondPane, TabID: "w1:t3", Cwd: cwd, AgentStatus: proto.StatusIdle}
	agents := []host.Agent{first, second}
	n.peer = rpc.New("node", io.Discard)
	n.state.beginLoad()
	n.setAgents(agents)
	if len(n.agents) != 0 || len(n.state.Sessions) != 0 {
		t.Fatal("observed a new binding before the manager returned this computer's saved bindings")
	}
	n.state.endLoad()
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() { n.setAgents(agents) })
	}
	group.Wait()
	a, b := n.codexWaitOf(firstPane), n.codexWaitOf(secondPane)
	if a == nil || b == nil || a.id == b.id || len(n.state.Sessions) != 2 {
		t.Fatalf("one record per live pane: %v", n.state.panes())
	}
	if firstRow, ok := n.agent(a.id); !ok || firstRow.PaneID != firstPane {
		t.Fatal("a concurrent refresh hid the first live pane")
	}
	if secondRow, ok := n.agent(b.id); !ok || secondRow.PaneID != secondPane {
		t.Fatal("a concurrent refresh hid the second live pane")
	}
	n.peer.Close(io.EOF)
	waitFor(t, "the disconnected node to stop scanning for rollouts", func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		return !n.codexWaitLooking
	})

	// A new connection has only the state.load JSON and the same computer's herdr socket.
	restarted, _, _ := herdrNode(t)
	restarted.home, restarted.hosts = home, n.hosts
	wire, err := json.Marshal(n.state.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wire, &restarted.state.Sessions); err != nil {
		t.Fatal(err)
	}
	restarted.panes = restarted.state.panes()
	restarted.codices = newCodexRegistry()
	codeHome := filepath.Join(home, ".codex")
	restarted.configureCodex(proto.CodexConfig{Homes: []string{codeHome}})
	restarted.codices.checked = func() ([]codexProcess, bool) { return nil, true }
	publish := func(firstID, secondID string, showSecond bool) {
		rows := []any{agentOf("codex", firstID, proto.StatusIdle, firstPane, first.TabID, "w1", cwd, "Playground")}
		if showSecond {
			rows = append(rows, agentOf("codex", secondID, proto.StatusIdle, secondPane, second.TabID, "w1", cwd, "Playground"))
		}
		f.reply("session.snapshot", snapshot(nil, nil, rows, nil))
		listSessions(t, restarted)
	}
	publish("", "", true)
	againA, againB := restarted.codexWaitOf(firstPane), restarted.codexWaitOf(secondPane)
	if againA == nil || againB == nil || againA.id != a.id || againB.id != b.id || !againA.began.Equal(a.began) || !againB.began.Equal(b.began) || !againA.manual || !againB.manual {
		t.Fatal("restart lost or swapped the waiting rows")
	}

	// The rollout is fresh in both panes' directory. Only the first pane's TUI holds the file.
	const threadA = "01a19a00-0000-7000-8000-0000000000c1"
	const threadB = "01a19a00-0000-7000-8000-0000000000c2"
	path := writeWaitRollout(t, home, cwd, threadA, time.Now(), "A's work")
	pid := holdOpenPID(t, path)
	restarted.codices.checked = func() ([]codexProcess, bool) {
		return []codexProcess{{pid: pid, home: codeHome, pane: firstPane}}, true
	}
	restarted.lookForCodexThreadsOnce(context.Background(), []*codexWait{againB, againA}, true)
	if restarted.codexAttachedThread(a.id) != threadA || restarted.codexWaitByID(b.id) != againB {
		t.Fatal("the directory shared by two live Codex panes determined their ownership")
	}
	// The other pane's stale report cannot claim the thread even when its TUI no longer keeps the file open.
	restarted.codices.checked = func() ([]codexProcess, bool) { return nil, true }
	publish(threadA, threadA, true)
	restarted.lookForCodexThreadsOnce(context.Background(), []*codexWait{againB}, true)
	if restarted.codexWaitByID(b.id) != againB {
		t.Fatal("two panes attached the same thread")
	}
	writeWaitRollout(t, home, cwd, threadB, time.Now(), "B's work")
	publish(threadA, threadB, true)
	restarted.lookForCodexThreadsOnce(context.Background(), []*codexWait{againB}, true)
	if restarted.codexAttachedThread(a.id) != threadA || restarted.codexAttachedThread(b.id) != threadB {
		t.Fatal("the original rows did not resolve to their own threads")
	}
	publish(threadA, "", false)
	publish(threadA, "", true)
	if fresh := restarted.codexWaitOf(secondPane); fresh == nil || fresh.id == b.id || restarted.codexAttachedThread(b.id) != threadB {
		t.Fatal("a new Codex in the reopened tab inherited the previous thread or its alias")
	}
}
