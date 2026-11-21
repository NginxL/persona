package node

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// The thinking effort of a Codex thread the phone drives through its app-server (codex_effort.go), against the
// synthetic app-server of codex_remote_test.go.

func paneRow(t *testing.T, n *Node, id string) proto.Session {
	t.Helper()
	for _, s := range n.sessionList(context.Background(), false) {
		if s.SessionID == id {
			return s
		}
	}
	t.Fatalf("no row for %s", id)
	return proto.Session{}
}

// effortPane is daemonPane on a node that keeps the phone's choices (as New's does).
func effortPane(t *testing.T) (*Node, *remoteFixture, *codexRemote) {
	t.Helper()
	n, f, c, _ := daemonPane(t)
	n.modes = map[string]sessionModes{}
	return n, f, c
}

func joinPane(t *testing.T, n *Node) {
	t.Helper()
	n.joinCodexPanes(context.Background())
	waitFor(t, "joined", func() bool { route, _ := n.codexRoute(remoteRaw); return route == codexRouteAppServer })
}

func threadOf(c *codexRemote, id string) remoteThread {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.threads[id]
}

// pendingOf is the choice kept for a session's next turn/start, as stored ("" none; not checked against a connection).
func pendingOf(n *Node, id string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if p := n.modes[id].Codex; p != nil {
		return p.effort
	}
	return ""
}

// endTurn finishes the pane's running turn, so the phone's next message is a turn/start again.
func endTurn(t *testing.T, f *remoteFixture, c *codexRemote) {
	t.Helper()
	turn := threadOf(c, remoteRaw).turn
	f.push(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": remoteRaw, "turn": map[string]any{"id": turn}}})
	waitFor(t, "the turn ends", func() bool { return threadOf(c, remoteRaw).turn == "" })
}

// A herdr pane: on the terminal route it is refused as before; joined on its app-server only its effort changes, through
// thread/settings/update, among Codex's levels (model/list names no model here), and the row says so.
func TestCodexEffortOnAJoinedPane(t *testing.T) {
	n, f, c := effortPane(t)
	ctx := context.Background()
	writeRollout(t, n.home, remoteRaw, n.home, time.Now(), time.Now())
	if s := paneRow(t, n, remoteRaw); len(s.Efforts) != 0 || s.Effort != "" {
		t.Fatalf("a pane on the terminal route offers efforts: %v %q", s.Efforts, s.Effort)
	}
	if err := n.setModes(ctx, remoteRaw, "", "high"); err == nil || !strings.Contains(err.Error(), "Codex / TRAE CLI 的会话不支持") {
		t.Fatalf("terminal route: %v", err)
	}
	joinPane(t, n)
	s := paneRow(t, n, remoteRaw)
	if !slices.Equal(s.Efforts, codexEffortLevels) || s.PermissionMode != "" {
		t.Fatalf("joined: efforts %v mode %q", s.Efforts, s.PermissionMode)
	}
	if err := n.setModes(ctx, remoteRaw, "plan", ""); err == nil || !strings.Contains(err.Error(), "只能改思考强度") {
		t.Fatalf("a permission mode: %v", err)
	}
	if err := n.setModes(ctx, remoteRaw, "plan", "high"); err == nil {
		t.Fatal("a permission mode with an effort went through")
	}
	if err := n.setModes(ctx, remoteRaw, "", "turbo"); err == nil || !strings.Contains(err.Error(), "minimal / low") {
		t.Fatalf("an unknown level: %v", err)
	}
	if f.count("thread/settings/update") != 0 {
		t.Fatal("refused changes reached the app-server")
	}
	if err := n.setModes(ctx, remoteRaw, "", "ultra"); err != nil { // Codex's level, not one of Claude's
		t.Fatal(err)
	}
	up := f.sent("thread/settings/update")
	if len(up) != 1 || up[0]["threadId"] != remoteRaw || up[0]["effort"] != "ultra" || len(up[0]) != 2 {
		t.Fatalf("thread/settings/update %v", up)
	}
	if s := paneRow(t, n, remoteRaw); s.Effort != "ultra" {
		t.Fatalf("row effort %q", s.Effort)
	}
	// The app-server reads it back on its own refresh.
	if err := c.refresh(); err != nil || threadOf(c, remoteRaw).effort != "ultra" {
		t.Fatalf("refresh: %v %q", err, threadOf(c, remoteRaw).effort)
	}
	// Taken at once: the phone's next turn does not carry it again, nor does a turn started on the computer need it.
	if err := n.send(ctx, remoteRaw, "go on", nil, sendOpts{}); err != nil {
		t.Fatal(err)
	}
	if starts := f.sent("turn/start"); len(starts) != 1 || starts[0]["effort"] != nil {
		t.Fatalf("turn/start %v", starts)
	}
	if info, err := n.sessionInfo(ctx, remoteRaw); err != nil || info["effort"] != "ultra" {
		t.Fatalf("session.info %v %v", info["effort"], err)
	}
}

// An app-server without thread/settings/update (-32601): the choice waits for the phone's next turn/start (a steer
// takes none), is shown as the effort meanwhile, and goes once.
func TestCodexEffortPendingGoesWithTheNextTurn(t *testing.T) {
	n, f, c := effortPane(t)
	ctx := context.Background()
	joinPane(t, n)
	f.mu.Lock()
	f.reject, f.rejectCode = "thread/settings/update", -32601
	f.mu.Unlock()
	f.push(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": remoteRaw, "turn": map[string]any{"id": "turn-0"}}})
	waitFor(t, "a turn runs", func() bool { return threadOf(c, remoteRaw).turn == "turn-0" })
	if err := n.setModes(ctx, remoteRaw, "", "low"); err != nil {
		t.Fatal(err)
	}
	if f.count("thread/settings/update") != 1 || pendingOf(n, remoteRaw) != "low" {
		t.Fatalf("not pending: %d %q", f.count("thread/settings/update"), pendingOf(n, remoteRaw))
	}
	if s := paneRow(t, n, remoteRaw); s.Effort != "low" {
		t.Fatalf("row effort %q", s.Effort)
	}
	if err := n.send(ctx, remoteRaw, "while it works", nil, sendOpts{}); err != nil {
		t.Fatal(err)
	}
	if steers := f.sent("turn/steer"); len(steers) != 1 || steers[0]["effort"] != nil || pendingOf(n, remoteRaw) != "low" {
		t.Fatalf("turn/steer %v, pending %q", steers, pendingOf(n, remoteRaw))
	}
	f.push(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": remoteRaw, "turn": map[string]any{"id": "turn-0"}}})
	waitFor(t, "the turn ends", func() bool { return threadOf(c, remoteRaw).turn == "" })
	if err := n.send(ctx, remoteRaw, "next", nil, sendOpts{}); err != nil {
		t.Fatal(err)
	}
	if starts := f.sent("turn/start"); len(starts) != 1 || starts[0]["effort"] != "low" {
		t.Fatalf("turn/start %v", starts)
	}
	if pendingOf(n, remoteRaw) != "" || threadOf(c, remoteRaw).effort != "low" {
		t.Fatalf("still pending %q, thread %q", pendingOf(n, remoteRaw), threadOf(c, remoteRaw).effort)
	}
	f.push(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": remoteRaw, "turn": map[string]any{"id": "turn-1"}}})
	waitFor(t, "the turn ends", func() bool { return threadOf(c, remoteRaw).turn == "" })
	if err := n.send(ctx, remoteRaw, "after", nil, sendOpts{}); err != nil {
		t.Fatal(err)
	}
	if starts := f.sent("turn/start"); len(starts) != 2 || starts[1]["effort"] != nil {
		t.Fatalf("turn/start %v", starts)
	}
	if s := paneRow(t, n, remoteRaw); s.Effort != "low" {
		t.Fatalf("row effort %q", s.Effort)
	}
}

// Every failure but -32601 is the phone's error and keeps nothing for a later turn: Codex answering with another code
// (a thread it no longer has, a level it refuses), no answer in time, the connection dropping mid-call. A choice already
// waiting goes too, and so does one a turn/start carrying it is refused for (else every message after would fail).
func TestCodexEffortFailuresKeepNothing(t *testing.T) {
	n, f, c := effortPane(t)
	ctx := context.Background()
	joinPane(t, n)
	set := func(reject string, code int, effort string) error {
		t.Helper()
		f.mu.Lock()
		f.reject, f.rejectCode, f.drop = reject, code, ""
		f.mu.Unlock()
		return n.setModes(ctx, remoteRaw, "", effort)
	}
	pend := func() {
		t.Helper()
		if err := set("thread/settings/update", -32601, "low"); err != nil || pendingOf(n, remoteRaw) != "low" {
			t.Fatalf("not pending: %v %q", err, pendingOf(n, remoteRaw))
		}
	}
	unchanged := func(what string, err error, says string) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), says) {
			t.Fatalf("%s: %v", what, err)
		}
		if pendingOf(n, remoteRaw) != "" {
			t.Fatalf("%s: still pending %q", what, pendingOf(n, remoteRaw))
		}
		if s := paneRow(t, n, remoteRaw); s.Effort != "" { // the thread's own, which neither call changed
			t.Fatalf("%s: row effort %q", what, s.Effort)
		}
	}
	pend()
	err := set("thread/settings/update", -32000, "high") // e.g. a thread it no longer has
	if refusalCode(err) != "codex_refused" {
		t.Fatalf("another code: %v", err)
	}
	unchanged("another code", err, "思考强度没有改成：Codex 回复「rejected」")
	f.mu.Lock()
	f.reject = ""
	f.mu.Unlock()
	if err := n.send(ctx, remoteRaw, "next", nil, sendOpts{}); err != nil {
		t.Fatal(err)
	}
	if starts := f.sent("turn/start"); len(starts) != 1 || starts[0]["effort"] != nil {
		t.Fatalf("turn/start after a failure %v", starts)
	}
	endTurn(t, f, c)

	pend()
	err = set("thread/settings/update", -32602, "high")
	if refusalCode(err) != "codex_refused" {
		t.Fatalf("-32602: %v", err)
	}
	unchanged("-32602", err, "Codex 没有接受这个思考强度")

	pend()
	f.mu.Lock()
	f.reject, f.drop = "", "thread/settings/update"
	f.mu.Unlock()
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	err = n.setModes(short, remoteRaw, "", "high")
	cancel()
	unchanged("no answer", err, "没有及时回应")

	// A turn/start Codex refuses with the waiting choice drops it: the next message goes without.
	pend()
	f.mu.Lock()
	f.reject, f.rejectCode = "turn/start", -32602
	f.mu.Unlock()
	if err := n.send(ctx, remoteRaw, "refused", nil, sendOpts{}); err == nil {
		t.Fatal("a refused turn/start went through")
	}
	if starts := f.sent("turn/start"); starts[len(starts)-1]["effort"] != "low" || pendingOf(n, remoteRaw) != "" {
		t.Fatalf("refused turn/start %v, pending %q", starts[len(starts)-1], pendingOf(n, remoteRaw))
	}
	f.mu.Lock()
	f.reject = ""
	f.mu.Unlock()
	if err := n.send(ctx, remoteRaw, "again", nil, sendOpts{}); err != nil {
		t.Fatal(err)
	}
	if starts := f.sent("turn/start"); starts[len(starts)-1]["effort"] != nil {
		t.Fatalf("turn/start after the refusal %v", starts[len(starts)-1])
	}
	endTurn(t, f, c)

	// The connection drops before the answer.
	pend()
	f.mu.Lock()
	f.hangup = "thread/settings/update"
	f.mu.Unlock()
	err = n.setModes(ctx, remoteRaw, "", "high")
	if err == nil || !strings.Contains(err.Error(), "断开了") || pendingOf(n, remoteRaw) != "" {
		t.Fatalf("hang-up: %v, pending %q", err, pendingOf(n, remoteRaw))
	}
	if f.count("thread/settings/update") != 9 {
		t.Fatalf("thread/settings/update calls: %d", f.count("thread/settings/update"))
	}
}

// A kept choice holds for the connection and thread it was made on: not after its app-server unloads the thread (the
// session ended there), not on a new connection (a reconnect, maybe to an app-server that takes the setting).
func TestCodexEffortPendingStaysWithItsConnection(t *testing.T) {
	n := remoteTestNode(t)
	n.modes = map[string]sessionModes{}
	home := t.TempDir()
	f, c := connectRemoteFixture(t, n, home, "one", remoteRaw)
	id := n.registerCodex(home, remoteRaw, "")
	ctx := context.Background()
	f.mu.Lock()
	f.reject, f.rejectCode = "thread/settings/update", -32601
	f.mu.Unlock()
	if err := n.setModes(ctx, id, "", "low"); err != nil || pendingOf(n, id) != "low" {
		t.Fatalf("not pending: %v %q", err, pendingOf(n, id))
	}
	f.mu.Lock()
	f.threads = nil
	f.mu.Unlock()
	if err := c.refresh(); err != nil || pendingOf(n, id) != "" {
		t.Fatalf("unloaded: %v, pending %q", err, pendingOf(n, id))
	}
	f.mu.Lock()
	f.threads = []string{remoteRaw}
	f.mu.Unlock()
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	if err := n.setModes(ctx, id, "", "low"); err != nil || pendingOf(n, id) != "low" {
		t.Fatalf("not pending again: %v %q", err, pendingOf(n, id))
	}
	// Asked for another thread of the same connection: nothing, and the stale choice goes.
	if e := n.codexPendingEffort(id, c, remoteThread{id: remoteOther}); e != "" || pendingOf(n, id) != "" {
		t.Fatalf("another thread: %q, pending %q", e, pendingOf(n, id))
	}
	if err := n.setModes(ctx, id, "", "low"); err != nil || pendingOf(n, id) != "low" {
		t.Fatalf("not pending a third time: %v %q", err, pendingOf(n, id))
	}
	c.close()
	f2, _ := connectRemoteFixture(t, n, home, "one", remoteRaw)
	var row proto.Session
	for _, s := range n.codexSessions(map[string]bool{}) {
		if s.SessionID == id {
			row = s
		}
	}
	if row.SessionID == "" || len(row.Efforts) == 0 || row.Effort != "" || pendingOf(n, id) != "" {
		t.Fatalf("after the reconnect: row %v %q, pending %q", row.Efforts, row.Effort, pendingOf(n, id))
	}
	if err := n.sendCodex(ctx, id, "next", nil); err != nil {
		t.Fatal(err)
	}
	if starts := f2.sent("turn/start"); len(starts) != 1 || starts[0]["effort"] != nil {
		t.Fatalf("turn/start on the new connection %v", starts)
	}
}

// The levels are the thread's model's (model/list), checked on the way in, and the effort is what the app-server says
// the thread is configured with. A session an app-server lists (not a pane) goes the same way, through the node's
// external branch; a level Codex itself refuses is said and not kept; a model without any offers and takes none.
func TestCodexEffortFollowsTheModel(t *testing.T) {
	n := remoteTestNode(t)
	n.modes = map[string]sessionModes{}
	home := t.TempDir()
	f, c := connectRemoteFixture(t, n, home, "one", remoteRaw)
	f.mu.Lock()
	f.models = map[string]any{"data": []any{
		map[string]any{"id": "gpt-test", "model": "gpt-test", "defaultReasoningEffort": "low", "supportedReasoningEfforts": []any{
			map[string]any{"reasoningEffort": "low", "description": "快"}, map[string]any{"reasoningEffort": "high", "description": "慢"}}},
		map[string]any{"id": "plain", "model": "plain", "supportedReasoningEfforts": []any{}},
	}}
	f.threadModel = "gpt-test"
	f.mu.Unlock()
	c.loadModels()
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	if lists := f.sent("model/list"); len(lists) == 0 || lists[len(lists)-1]["includeHidden"] != true {
		t.Fatalf("model/list %v", lists)
	}
	id := n.registerCodex(home, remoteRaw, "")
	row := func() proto.Session {
		t.Helper()
		for _, s := range n.codexSessions(map[string]bool{}) {
			if s.SessionID == id {
				return s
			}
		}
		t.Fatal("no row")
		return proto.Session{}
	}
	// A thread that sets no effort (reasoningEffort null) runs with its model's default.
	if s := row(); s.Effort != "low" {
		t.Fatalf("unset effort shows %q", s.Effort)
	}
	f.mu.Lock()
	f.threadEffort = "high"
	f.mu.Unlock()
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	if s := row(); s.SourceKind != "codex_app_server" || !slices.Equal(s.Efforts, []string{"low", "high"}) || s.Effort != "high" {
		t.Fatalf("row %s %v %q", s.SourceKind, s.Efforts, s.Effort)
	}
	modes := func(effort string) error {
		_, err := request(t, n, "session.modes", map[string]any{"session_id": id, "effort": effort})
		return err
	}
	if err := modes("xhigh"); err == nil || !strings.Contains(err.Error(), "low / high") {
		t.Fatalf("a level the model does not take: %v", err)
	}
	if err := modes("low"); err != nil {
		t.Fatal(err)
	}
	if up := f.sent("thread/settings/update"); len(up) != 1 || up[0]["effort"] != "low" {
		t.Fatalf("thread/settings/update %v", up)
	}
	if s := row(); s.Effort != "low" {
		t.Fatalf("row effort %q", s.Effort)
	}
	f.mu.Lock()
	f.reject, f.rejectCode = "thread/settings/update", -32602
	f.mu.Unlock()
	if err := modes("high"); refusalCode(err) != "codex_refused" || pendingOf(n, id) != "" {
		t.Fatalf("refused by Codex: %v, pending %q", err, pendingOf(n, id))
	}
	f.mu.Lock()
	f.reject, f.threadModel = "", "plain"
	f.mu.Unlock()
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	if s := row(); len(s.Efforts) != 0 || s.Effort != "" {
		t.Fatalf("a model without efforts: %v %q", s.Efforts, s.Effort)
	}
	if err := modes("low"); err == nil || !strings.Contains(err.Error(), "不支持设置思考强度") {
		t.Fatalf("a model without efforts took one: %v", err)
	}
	if f.count("thread/settings/update") != 2 {
		t.Fatalf("thread/settings/update calls: %d", f.count("thread/settings/update"))
	}
	// Its settings changed on the computer: thread/settings/updated says so at once.
	f.push(map[string]any{"method": "thread/settings/updated", "params": map[string]any{"threadId": remoteRaw,
		"threadSettings": map[string]any{"model": "gpt-test", "effort": "low"}}})
	waitFor(t, "the new settings", func() bool { t := threadOf(c, remoteRaw); return t.model == "gpt-test" && t.effort == "low" })
}

// Codex in a pane with no app-server (the terminal route) and a compatible CLI keep the old refusal.
func TestCodexEffortRefusedOffTheAppServer(t *testing.T) {
	n := remoteTestNode(t)
	n.modes = map[string]sessionModes{}
	codexID, traexID := "11111111-2222-3333-4444-555555555555", "66666666-7777-8888-9999-000000000000"
	writeRollout(t, n.home, codexID, n.home, time.Now(), time.Now())
	dir := filepath.Join(n.traexHome(), "sessions", "2026", "01", "01")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-01-01T00-00-00-"+traexID+".jsonl"), []byte(`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"`+traexID+`"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	n.agents[codexID] = host.Agent{Agent: "codex", PaneID: "w1:p1", AgentStatus: proto.StatusIdle}
	n.agents[traexID] = host.Agent{Agent: traexAgent, PaneID: "w1:p2", AgentStatus: proto.StatusIdle}
	n.mu.Unlock()
	for _, id := range []string{codexID, traexID} {
		_, err := request(t, n, "session.modes", map[string]any{"session_id": id, "effort": "high"})
		if err == nil || !strings.Contains(err.Error(), "Codex / TRAE CLI 的会话不支持") {
			t.Errorf("%s: %v", id, err)
		}
		if s := paneRow(t, n, id); len(s.Efforts) != 0 {
			t.Errorf("%s offers efforts: %v", id, s.Efforts)
		}
	}
}

func TestCodexAppServerModelSwitchAndPendingFallback(t *testing.T) {
	n, f, c := effortPane(t)
	ctx := context.Background()
	f.mu.Lock()
	f.models = map[string]any{"data": []any{
		map[string]any{"id": "acme/gpt-5-pro", "model": "acme/gpt-5-pro", "defaultReasoningEffort": "medium", "supportedReasoningEfforts": []any{
			map[string]any{"reasoningEffort": "low"}, map[string]any{"reasoningEffort": "medium"}}},
		map[string]any{"id": "beta/gpt-5-pro", "model": "beta/gpt-5-pro", "defaultReasoningEffort": "high", "supportedReasoningEfforts": []any{
			map[string]any{"reasoningEffort": "low"}, map[string]any{"reasoningEffort": "high"}, map[string]any{"reasoningEffort": "xhigh"}}},
	}}
	f.threadModel = "beta/gpt-5-pro"
	f.threadEffort = "xhigh"
	f.mu.Unlock()
	c.loadModels()
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	joinPane(t, n)
	s := paneRow(t, n, remoteRaw)
	if s.Capabilities.Model == nil || !*s.Capabilities.Model || s.Model != "beta/gpt-5-pro" || s.Effort != "xhigh" {
		t.Fatalf("joined row: model=%q effort=%q caps=%+v", s.Model, s.Effort, s.Capabilities)
	}

	// Switching to acme/gpt-5-pro reconciles xhigh -> medium.
	if _, err := n.setModelAndModes(ctx, remoteRaw, "acme/gpt-5-pro", "", "", false); err != nil {
		t.Fatal(err)
	}
	up := f.sent("thread/settings/update")
	if len(up) != 1 || up[0]["model"] != "acme/gpt-5-pro" || up[0]["effort"] != "medium" {
		t.Fatalf("thread/settings/update = %v", up)
	}
	if s := paneRow(t, n, remoteRaw); s.Model != "acme/gpt-5-pro" || s.Effort != "medium" {
		t.Fatalf("after switch: model=%q effort=%q", s.Model, s.Effort)
	}

	// Older app-server (-32601): sequential {model} then {effort} merges into pending and goes with next turn/start.
	f.mu.Lock()
	f.reject, f.rejectCode = "thread/settings/update", -32601
	f.mu.Unlock()
	if _, err := n.setModelAndModes(ctx, remoteRaw, "beta/gpt-5-pro", "", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := n.setModelAndModes(ctx, remoteRaw, "", "", "xhigh", false); err != nil {
		t.Fatal(err)
	}
	if s := paneRow(t, n, remoteRaw); s.Model != "beta/gpt-5-pro" || s.Effort != "xhigh" {
		t.Fatalf("pending row: model=%q effort=%q", s.Model, s.Effort)
	}
	if err := n.send(ctx, remoteRaw, "run next turn", nil, sendOpts{}); err != nil {
		t.Fatal(err)
	}
	starts := f.sent("turn/start")
	if len(starts) != 1 || starts[0]["model"] != "beta/gpt-5-pro" || starts[0]["effort"] != "xhigh" {
		t.Fatalf("turn/start = %v", starts)
	}
}
