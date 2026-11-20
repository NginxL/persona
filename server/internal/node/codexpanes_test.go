package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// writeRollout writes a made-up Codex rollout that started on day (its directory) in cwd, last written at written.
func writeRollout(t testing.TB, home, id, cwd string, day, written time.Time, events ...string) {
	t.Helper()
	dir := filepath.Join(home, ".codex", "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"` + id + `","cwd":"` + cwd + `"}}`}
	lines = append(lines, events...)
	p := filepath.Join(dir, "rollout-"+day.Format("2006-01-02T15-04-05")+"-"+id+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, written, written); err != nil {
		t.Fatal(err)
	}
}

func event(typ string) string {
	return `{"timestamp":"2026-01-01T00:00:01Z","type":"event_msg","payload":{"type":"` + typ + `"}}`
}

// The made-up turn a pane is in the middle of: the last turn completed, the user wrote again, Codex ran a command and
// is compacting its context (it has written the compaction, not yet anything after it).
var turnAfterTurn = []string{
	event("task_started"),
	`{"timestamp":"2026-01-01T00:00:01Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"第一轮"}]}}}`,
	event("task_complete"),
	event("task_started"),
	`{"timestamp":"2026-01-01T00:00:02Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u2","content":[{"type":"text","text":"再看一下主分支"}]}}}`,
	`{"timestamp":"2026-01-01T00:00:03Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"c1","command":["git","status"],"aggregated_output":"clean","exit_code":0}}}`,
	`{"timestamp":"2026-01-01T00:00:04Z","type":"compacted","payload":{"message":""}}`,
	`{"timestamp":"2026-01-01T00:00:04Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"ContextCompaction","id":"cc1"}}}`,
}

// 2026-10-31: five Codex panes in one repository, herdr naming none of their sessions. Node gave each the newest
// rollout in the directory not yet taken: four showed another pane's thread, and the one at work — a thread started
// six weeks earlier, still written to in its first day's directory, outside the two weeks node looked at — showed a
// finished thread, idle, its new turn nowhere. Each pane's title names its thread.
func TestCodexPanesAreFoundByTheThreadInTheirTitle(t *testing.T) {
	withCodexWaits(t)
	n, home := testNode(t)
	n.metas, n.bgScans, n.panes, n.panePIDs = map[string]quickMeta{}, map[string]*bgScan{}, map[string]string{}, map[string]panePID{}
	now := time.Now()
	const (
		oldThread  = "0a000000-0000-4000-8000-000000000001" // started 40 days ago, working now
		newThread  = "0a000000-0000-4000-8000-000000000002" // started yesterday, finished an hour ago
		otherNew   = "0a000000-0000-4000-8000-000000000003" // started 2 days ago, no name yet
		longName   = "0a000000-0000-4000-8000-000000000004"
		staleStart = "0a000000-0000-4000-8000-000000000005" // what herdr said the pane started with
		cwd        = "/work/repo"
	)
	writeRollout(t, home, oldThread, cwd, now.AddDate(0, 0, -40), now.Add(-time.Second), turnAfterTurn...)
	writeRollout(t, home, newThread, cwd, now.AddDate(0, 0, -1), now.Add(-time.Hour), event("task_started"), event("task_complete"))
	writeRollout(t, home, otherNew, cwd, now.AddDate(0, 0, -2), now.Add(-2*time.Hour), event("task_started"), event("task_complete"))
	writeRollout(t, home, longName, cwd, now.AddDate(0, 0, -3), now.Add(-3*time.Hour), event("task_started"), event("task_complete"))
	writeRollout(t, home, staleStart, cwd, now.AddDate(0, 0, -4), now.Add(-4*time.Hour), event("task_started"), event("task_complete"))
	index := strings.Join([]string{
		`{"id":"` + oldThread + `","thread_name":"参考时区替换能力"}`,
		`{"id":"` + newThread + `","thread_name":"补测多客户端请求特征"}`,
		`{"id":"` + longName + `","thread_name":"一个很长很长的线程名字会被标题截断"}`,
		`{"id":"` + staleStart + `","thread_name":"早先的线程"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(home, ".codex", "session_index.jsonl"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	pane := func(id, title, status string) host.Agent {
		return host.Agent{Agent: "codex", PaneID: id, Cwd: cwd, ForegroundCwd: cwd, Title: title, AgentStatus: status}
	}
	resumed := pane("w:p5", "参考时区替换能力 | repo", proto.StatusWorking)
	resumed.AgentSession = &host.AgentSession{Kind: "id", Value: staleStart} // a /resume in Codex since
	n.panes["w:p9"] = staleStart
	n.setAgents([]host.Agent{
		pane("w:p9", "Unrecognised current title | repo", proto.StatusBlocked),
		pane("w:p1", "repo", proto.StatusIdle), // no thread name or identity yet
		pane("w:p2", "[ . ] Action Required | 补测多客户端请求特征 | repo", proto.StatusBlocked),
		pane("w:p3", "[ ! ] Action Required | 一个很长很长的线程名字会... | repo", proto.StatusBlocked),
		resumed,
	})
	wait := n.codexWaitOf("w:p1")
	if wait == nil || !isCodexWaitID(wait.id) {
		t.Fatal("the unnamed pane needs its own waiting row")
	}
	want := map[string]string{"w:p5": oldThread, "w:p2": newThread, "w:p3": longName, "w:p1": wait.id}
	for id, a := range n.agents {
		if want[a.PaneID] != id {
			t.Errorf("pane %s shows %s, want %s", a.PaneID, id, want[a.PaneID])
		}
	}
	if len(n.agents) != len(want) {
		t.Fatalf("%d panes mapped, want %d: %v", len(n.agents), len(want), n.agents)
	}
	// A named pane that cannot be identified must not acquire another thread from this directory.
	if id := n.codexSession(pane("w:p9", "Unknown title | repo", proto.StatusBlocked), nil); id != "" {
		t.Fatalf("unrecognised named pane was assigned another conversation: %s", id)
	}

	// The working pane's session is listed working, with the new turn readable.
	var got *proto.Session
	for _, s := range n.sessionList(context.Background(), false) {
		if s.SessionID == oldThread {
			got = &s
		}
	}
	if got == nil || got.Status != proto.StatusWorking || got.PaneID != "w:p5" {
		t.Fatalf("working thread listed as %+v", got)
	}
	out, err := n.handle(context.Background(), "session.items", json.RawMessage(`{"session_id":"`+oldThread+`","turns":2}`))
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []proto.Item `json:"items"`
	}
	b, _ := json.Marshal(out)
	if err := json.Unmarshal(b, &page); err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, it := range page.Items {
		if it.Role == "user" && it.Kind == "text" {
			texts = append(texts, it.Text)
		}
	}
	if len(texts) != 2 || texts[1] != "再看一下主分支" {
		t.Fatalf("user messages %q", texts)
	}
}

// Rollouts in day directories older than the window count while they are written to, and only then.
func TestOldRolloutsCountWhileWritten(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	writeRollout(t, home, "0b000000-0000-4000-8000-000000000001", "/w", now.AddDate(0, 0, -60), now.Add(-time.Minute))
	writeRollout(t, home, "0b000000-0000-4000-8000-000000000002", "/w", now.AddDate(0, 0, -60), now.AddDate(0, 0, -30))
	writeRollout(t, home, "0b000000-0000-4000-8000-000000000003", "/w", now, now)
	var ids []string
	for _, r := range rollouts(filepath.Join(home, ".codex")) {
		ids = append(ids, r.id[len(r.id)-1:])
	}
	if strings.Join(ids, ",") != "3,1" {
		t.Fatalf("rollouts %v, want the new one and the old one still written to", ids)
	}
}
