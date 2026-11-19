package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	agyTestID      = "11111111-2222-4333-8444-555555555555"
	agyOtherTestID = "22222222-3333-4444-8555-666666666666"
	agyTestPane    = "w1:p1"
	agyTestRow     = "agy-w1-p1"
)

func agyTestAgent(pane, cwd, id string) map[string]any {
	a := agentOf(agyAgent, "", proto.StatusIdle, pane, "w1:t1", "w1", cwd, "agy")
	if id != "" {
		a["agent_session"] = map[string]any{"kind": "id", "value": id, "source": "herdr:antigravity_cli", "agent": agyAgent}
	}
	return a
}

func agyTestFile(t *testing.T, root, id, contents string) string {
	t.Helper()
	file := filepath.Join(root, "brain", id, agyTranscriptPath)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	agyTestWrite(t, file, contents)
	return file
}

func agyTestWrite(t *testing.T, file, contents string) {
	t.Helper()
	if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func agyTestAppend(t *testing.T, file, contents string) {
	t.Helper()
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(contents); err != nil {
		t.Fatal(err)
	}
}

func agyTestCall(t *testing.T, n *Node, method, id string, params map[string]any) any {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	params["session_id"] = id
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	out, err := n.handle(i18n.With(context.Background(), i18n.En), method, raw)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return out
}

func agyTestItems(t *testing.T, n *Node, id string, params map[string]any) ([]proto.Item, map[string]any) {
	t.Helper()
	page := agyTestCall(t, n, "session.items", id, params).(map[string]any)
	return page["items"].([]proto.Item), page
}

func TestAgyPanePagesAndFullItems(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))
	const fixture = `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2026-11-01T00:00:00Z","content":"<USER_REQUEST>\n<persona-context>hidden</persona-context>\nCheck the files\n</USER_REQUEST><ADDITIONAL_METADATA>hidden</ADDITIONAL_METADATA>"}
{"step_index":1,"type":"USER_INPUT","source":"SYSTEM","content":"hidden system input"}
{"step_index":2,"type":"PLANNER_RESPONSE","created_at":"2026-11-01T00:00:01Z","thinking":"  First find the file. ","content":" I will check. ","tool_calls":[{"name":"run_command","args":{"CommandLine":"ls","toolSummary":"List files"}},{"name":"view_file","args":{"AbsolutePath":"sample.txt"}}]}
{"step_index":3,"type":"GENERIC","content":"Created At: 2026-11-01\nOutput:\nsample.txt\n","truncated_fields":["content"]}
{"step_index":4,"type":"GENERIC","status":"ERROR","error":"Read failed"}
{"step_index":5,"type":"PLANNER_RESPONSE","content":%q}
{"step_index":6,"type":"UNKNOWN"}
`
	long := strings.Repeat("result ", 5000)
	agyTestFile(t, n.agyDataRoot(), agyTestID, fmt.Sprintf(fixture, long))
	output := filepath.Join(n.agyDataRoot(), "brain", agyTestID, ".system_generated", "steps", "3", "output.txt")
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		t.Fatal(err)
	}
	agyTestWrite(t, output, "Created At: 2026-11-01\nOutput:\nsample.txt\nsecond.txt\n")
	items, page := agyTestItems(t, n, agyTestRow, map[string]any{"limit": 1})
	if len(items) != 1 || items[0].Seq != 6 || items[0].FullSize != len(long)-1 || page["to_seq"] != 7 || page["has_more"] != true {
		t.Fatalf("latest page: %+v", page)
	}
	full := agyTestCall(t, n, "session.item", agyTestRow, map[string]any{"item_id": items[0].ID}).(proto.Item)
	if full.ID != items[0].ID || full.Text != strings.TrimSpace(long) {
		t.Fatalf("full item %s has %d bytes", full.ID, len(full.Text))
	}
	items, page = agyTestItems(t, n, agyTestRow, map[string]any{"before_seq": 4, "limit": 1})
	if page["has_more"] != true || page["to_seq"] != 3 || len(items) != 4 ||
		items[0].Kind != "thinking" || items[0].Text != "First find the file." || items[1].Text != "I will check." ||
		items[2].ToolName != "Bash" || string(items[2].ToolInput) != `{"command":"ls","description":"List files"}` ||
		items[3].ToolName != "Read" || string(items[3].ToolInput) != `{"file_path":"sample.txt"}` {
		t.Fatalf("planner page: %+v", page)
	}
	results, _ := agyTestItems(t, n, agyTestRow, map[string]any{"from_seq": 4, "through_seq": 5})
	if len(results) != 2 || results[0].Kind != "tool_result" || results[0].Text != "sample.txt\n" ||
		results[0].ToolUseID != items[2].ToolUseID || results[1].ToolUseID != items[3].ToolUseID ||
		results[1].Text != "Read failed" || !results[1].IsError {
		t.Fatalf("tool results: %+v", results)
	}
	expanded := agyTestCall(t, n, "session.item", agyTestRow, map[string]any{"item_id": results[0].ID}).(proto.Item)
	if expanded.ID != results[0].ID || expanded.ToolUseID != items[2].ToolUseID || expanded.Text != "sample.txt\nsecond.txt\n" {
		t.Fatalf("expanded tool result: %+v", expanded)
	}
	items, page = agyTestItems(t, n, agyTestRow, map[string]any{"before_seq": 3})
	if len(items) != 1 || items[0].Text != "Check the files" || items[0].Role != "user" || page["has_more"] != false {
		t.Fatalf("oldest page: %+v", page)
	}
	items, page = agyTestItems(t, n, agyTestRow, map[string]any{"after_seq": 5})
	if len(items) != 1 || items[0].ID != full.ID || page["from_seq"] != 5 || page["to_seq"] != 7 {
		t.Fatalf("catch-up page: %+v", page)
	}
}

func TestAgyPaneConversationIsolation(t *testing.T) {
	n, f, home := herdrNode(t)
	root := t.TempDir()
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", root)
	const user = `{"step_index":0,"type":"USER_INPUT","content":%q}` + "\n"
	agyTestFile(t, root, agyTestID, fmt.Sprintf(user, "first conversation"))
	agyTestFile(t, root, agyOtherTestID, fmt.Sprintf(user, "second conversation"))
	first := agyTestAgent(agyTestPane, home, agyTestID)
	const pane2, row2 = "w1:p2", "agy-w1-p2"
	valid := agyTestAgent(pane2, home, agyOtherTestID)
	seedAgents(t, n, f, first, valid)
	items, _ := agyTestItems(t, n, agyTestRow, nil)
	other, _ := agyTestItems(t, n, row2, nil)
	if len(items) != 1 || len(other) != 1 || items[0].Text != "first conversation" || other[0].Text != "second conversation" || items[0].ID == other[0].ID {
		t.Fatalf("same-directory panes: first %+v, second %+v", items, other)
	}
	assertMissingItem := func(id, itemID string) {
		t.Helper()
		if _, err := request(t, n, "session.item", map[string]any{"session_id": id, "item_id": itemID}); err == nil {
			t.Fatal("expanded an item outside the verified conversation")
		}
	}
	assertMissingItem(row2, items[0].ID)
	t.Run("snapshot_mismatch", func(t *testing.T) {
		n.mu.Lock()
		row := n.agents[row2]
		stale := *row.AgentSession
		stale.Value = "33333333-4444-4555-8666-777777777777"
		row.AgentSession = &stale
		// Leave the raw snapshot untouched so the merged row disagrees with its pane's report.
		n.agents[row2] = row
		n.mu.Unlock()
		got, _ := agyTestItems(t, n, row2, nil)
		if len(got) != 1 || got[0].Kind != "notice" {
			t.Fatalf("snapshot mismatch: %+v", got)
		}
		assertMissingItem(row2, items[0].ID)
		assertMissingItem(row2, other[0].ID)
	})
	for _, tc := range []struct{ name, kind, source, agent, id string }{
		{name: "missing"},
		{"path kind", "path", "herdr:antigravity_cli", "agy", agyOtherTestID},
		{"traversal", "id", "herdr:antigravity_cli", "agy", "../" + agyTestID},
		{"invalid UUID", "id", "herdr:antigravity_cli", "agy", "11111111"},
		{"other agent", "id", "herdr:antigravity_cli", "cursor", agyOtherTestID},
		{"other source", "id", "custom:persona", "agy", agyOtherTestID},
		{"duplicate ID", "id", "herdr:antigravity_cli", "agy", agyTestID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			second := agyTestAgent(pane2, home, "")
			if tc.id != "" {
				second["agent_session"] = map[string]any{"kind": tc.kind, "agent": tc.agent, "source": tc.source, "value": tc.id}
			}
			seedAgents(t, n, f, first, second)
			got, _ := agyTestItems(t, n, row2, nil)
			if len(got) != 1 || got[0].Kind != "notice" || got[0].TS.IsZero() || !strings.Contains(got[0].Text, "herdr integration install antigravity-cli") {
				t.Fatalf("terminal notice: %+v", got)
			}
			assertMissingItem(row2, items[0].ID)
		})
	}

	// A conversation directory symlink cannot redirect even to a sibling inside the data root.
	const linked = "33333333-4444-4555-8666-777777777777"
	if err := os.Symlink(agyTestID, filepath.Join(root, "brain", linked)); err != nil {
		t.Fatal(err)
	}
	seedAgents(t, n, f, first, agyTestAgent(pane2, home, linked))
	if got, _ := agyTestItems(t, n, row2, nil); len(got) != 0 {
		t.Fatalf("redirected conversation was read: %+v", got)
	}
}

// A watch from the end of the history, then a step that maps to no item, then a visible one: the empty step moves the
// cursor once, and the visible one comes as its own batch, nothing sent twice (review on #10364).
func TestAgyPaneWatchFromTheEndPastAnInvisibleStep(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))
	file := agyTestFile(t, n.agyDataRoot(), agyTestID, `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","content":"Check the files"}`+"\n"+
		`{"step_index":1,"type":"PLANNER_RESPONSE","content":"Looking"}`+"\n")
	_, page := agyTestItems(t, n, agyTestRow, nil)
	hub := withHub(t, n)
	agyTestCall(t, n, "session.watch", agyTestRow, map[string]any{"since_seq": page["to_seq"]})
	t.Cleanup(func() { n.unwatch(agyTestRow) })
	n.mu.Lock()
	w := n.watches[agyTestRow]
	n.mu.Unlock()

	agyTestAppend(t, file, `{"step_index":2,"type":"USER_INPUT","source":"SYSTEM","content":"hidden"}`+"\n")
	for range 3 {
		n.pollAgyPane(w)
	}
	if got := hub.spans(); len(got) != 1 || got[0].n != 0 || got[0].fromSeq != 2 || got[0].toSeq != 3 {
		t.Fatalf("after the invisible step: %+v", got)
	}
	agyTestAppend(t, file, `{"step_index":3,"type":"PLANNER_RESPONSE","content":"Visible"}`+"\n")
	waitFor(t, "the visible step", func() bool { return len(hub.spans()) >= 2 })
	if got := hub.spans(); len(got) != 2 || got[1].n != 1 || got[1].fromSeq != 3 || got[1].toSeq != 4 || hub.got()[0].Text != "Visible" {
		t.Fatalf("after the visible step: %+v, items %+v", got, hub.got())
	}
}

func TestAgyPaneWatchAppendPartialAndRewrites(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))
	const first = `{"step_index":0,"type":"USER_INPUT","content":"Check the files"}` + "\n"
	const planner = `{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-01T00:00:01Z","content":%q}` + "\n"
	file := agyTestFile(t, n.agyDataRoot(), agyTestID, first)
	_, page := agyTestItems(t, n, agyTestRow, nil)
	hub := withHub(t, n)
	agyTestCall(t, n, "session.watch", agyTestRow, map[string]any{"since_seq": page["to_seq"]})
	t.Cleanup(func() { n.unwatch(agyTestRow) })

	line := fmt.Sprintf(planner, "Draft")
	record := strings.TrimSuffix(line, "\n")
	agyTestAppend(t, file, record[:len(record)-1])
	if got, _ := agyTestItems(t, n, agyTestRow, nil); len(got) != 1 {
		t.Fatalf("truncated JSON was visible: %+v", got)
	}
	agyTestAppend(t, file, record[len(record)-1:]) // Valid JSON is still incomplete until the newline.
	n.mu.Lock()
	w := n.watches[agyTestRow]
	n.mu.Unlock()
	n.pollAgyPane(w) // one read after the append, as the pump's next tick would do
	if got, _ := agyTestItems(t, n, agyTestRow, nil); len(got) != 1 || len(hub.spans()) != 0 {
		t.Fatalf("partial record was visible: page %+v, batches %+v", got, hub.spans())
	}
	agyTestAppend(t, file, "\n")
	waitFor(t, "the complete record", func() bool { return len(hub.spans()) >= 1 })
	draft := hub.got()[0]
	if batch := hub.spans()[0]; draft.Text != "Draft" || batch.fromSeq != 1 || batch.toSeq != 2 || batch.history != page["history"] {
		t.Fatalf("append: %+v, %+v", draft, batch)
	}

	agyTestAppend(t, file, fmt.Sprintf(planner, "Final"))
	waitFor(t, "the appended step completion", func() bool { return len(hub.spans()) >= 2 })
	items, page := agyTestItems(t, n, agyTestRow, nil)
	if batch := hub.spans()[1]; len(items) != 2 || items[1].ID != draft.ID || items[1].Text != "Final" ||
		batch.fromSeq != 0 || batch.toSeq != 2 || batch.n != 2 || batch.history == hub.spans()[0].history || batch.history != page["history"] ||
		hub.got()[2].ID != draft.ID || hub.got()[2].Text != "Final" {
		t.Fatalf("same-step completion: page %+v, batches %+v, items %+v", page, hub.spans(), hub.got())
	}

	agyTestWrite(t, file, first+line+fmt.Sprintf(planner, "Fixed")) // same length and sequence
	waitFor(t, "the in-place same-size rewrite", func() bool { return len(hub.spans()) >= 3 })
	full := agyTestCall(t, n, "session.item", agyTestRow, map[string]any{"item_id": draft.ID}).(proto.Item)
	if batch := hub.spans()[2]; full.Text != "Fixed" || batch.history == page["history"] || batch.fromSeq != 0 || batch.toSeq != 2 || hub.got()[4].Text != "Fixed" {
		t.Fatalf("same-size rewrite: %+v, %+v", full, batch)
	}
	last := hub.spans()[2]
	agyTestAppend(t, file, `{"step_index":2,"type":"PLANNER_RESPONSE","content":"Next"}`+"\n")
	waitFor(t, "an append after rewriting", func() bool { return len(hub.spans()) >= 4 })
	if batch := hub.spans()[3]; batch.history != last.history || batch.fromSeq != 2 || batch.toSeq != 3 || batch.n != 1 {
		t.Fatalf("append after rewrite: %+v", batch)
	}

	agyTestAppend(t, file, `{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-02T00:00:01Z","content":"Replacement"}`+"\n")
	waitFor(t, "the replacement branch", func() bool { return len(hub.spans()) >= 5 })
	items, page = agyTestItems(t, n, agyTestRow, nil)
	if batch := hub.spans()[4]; len(items) != 2 || items[1].Text != "Replacement" || items[1].ID == draft.ID ||
		batch.history == last.history || batch.history != page["history"] || batch.fromSeq != 0 || batch.toSeq != 2 {
		t.Fatalf("replacement branch: page %+v, batch %+v", page, batch)
	}
	if _, err := request(t, n, "session.item", map[string]any{"session_id": agyTestRow, "item_id": draft.ID}); err == nil {
		t.Fatal("expanded an item from the abandoned branch")
	}

	agyTestCall(t, n, "session.unwatch", agyTestRow, nil)
	agyTestAppend(t, file, `{"step_index":3,"type":"PLANNER_RESPONSE","content":"After unwatch"}`+"\n")
	n.mu.Lock()
	_, watched := n.watches[agyTestRow]
	n.mu.Unlock()
	n.pollAgyPane(w) // a read the pump had already started for the ended watch pushes nothing
	if got := hub.spans(); watched || len(got) != 5 {
		t.Fatalf("watch pushed after unwatch: %+v", got)
	}
}

func TestAgyPaneWatchConversationIsolation(t *testing.T) {
	n, home := testNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	agent := func(pane, id string) host.Agent {
		a := host.Agent{PaneID: pane, Agent: agyAgent, Cwd: home}
		if id != "" {
			a.AgentSession = &host.AgentSession{Kind: "id", Value: id, Agent: agyAgent, Source: "herdr:antigravity_cli"}
		}
		return a
	}
	setSnapshot := func(pane host.Agent, others ...host.Agent) {
		n.mu.Lock()
		n.hostAgents = map[string][]host.Agent{host.Herdr: append([]host.Agent{pane}, others...)}
		n.agents[agyTestRow] = pane
		n.mu.Unlock()
	}
	const user = `{"step_index":0,"type":"USER_INPUT","content":%q}` + "\n"
	agyTestFile(t, n.agyDataRoot(), agyTestID, fmt.Sprintf(user, "first conversation"))
	agyTestFile(t, n.agyDataRoot(), agyOtherTestID, fmt.Sprintf(user, "second conversation"))
	first := agent(agyTestPane, agyTestID)
	setSnapshot(first)
	items, page := agyTestItems(t, n, agyTestRow, nil)
	if len(items) != 1 || items[0].Text != "first conversation" {
		t.Fatalf("initial page: %+v", page)
	}
	hub := withHub(t, n)
	agyTestCall(t, n, "session.watch", agyTestRow, map[string]any{"since_seq": page["to_seq"]})
	t.Cleanup(func() { n.unwatch(agyTestRow) })
	const noticeKey = "node-engine.agy_pane_integration"
	noticeText := i18n.T(i18n.Default, noticeKey) // the node's own language; the hub says it in each client's
	noticeToken := "agy-pane:" + agyTestRow
	firstToken := page["history"].(string)
	for i, tc := range []struct {
		name                string
		pane                host.Agent
		others              []host.Agent
		kind, text, history string
	}{
		{"ambiguous", first, []host.Agent{agent("w1:p2", agyTestID)}, "notice", noticeText, noticeToken},
		{"verified again", first, nil, "text", "first conversation", firstToken},
		{"ID disappeared", agent(agyTestPane, ""), nil, "notice", noticeText, noticeToken},
		{"ID returned", first, nil, "text", "first conversation", firstToken},
		{"switched conversation", agent(agyTestPane, agyOtherTestID), nil, "text", "second conversation", "agy-pane:" + agyOtherTestID + ":steps"},
	} {
		setSnapshot(tc.pane, tc.others...)
		waitFor(t, tc.name, func() bool { return len(hub.spans()) >= i+1 })
		batches, got := hub.spans(), hub.got()
		want := pushed{n: 1, history: tc.history, fromSeq: 0, toSeq: 1}
		if len(batches) != i+1 || batches[i] != want || len(got) != i+1 || got[i].Kind != tc.kind || got[i].Text != tc.text || got[i].TS.IsZero() {
			t.Fatalf("%s: batches %+v, items %+v", tc.name, batches, got)
		}
	}
	if got := hub.got(); got[len(got)-1].ID == items[0].ID {
		t.Fatal("new conversation reused an item ID from the previous conversation")
	}
}

func assertAgyResumeReport(t *testing.T, f *fakeHerdr, index int, model, conversation string) {
	t.Helper()
	wantID, wantCommand := agyTestRow, "agy --model "+model
	if conversation != "" {
		wantID = conversation
		wantCommand += " --conversation " + conversation
	}
	waitFor(t, "the agy resume report", func() bool { return len(f.trace("pane.report_agent_session")) > index })
	got := f.trace("pane.report_agent_session")
	if len(got) != index+1 || !strings.Contains(got[index], `"agent_session_id":"`+wantID+`"`) ||
		!strings.Contains(got[index], wantCommand+`"`) || (conversation == "" && strings.Contains(got[index], "--conversation")) {
		t.Fatalf("want report %d with session ID %s and command %q, got %v", index, wantID, wantCommand, got)
	}
}

func TestAgyPaneResumeAndTerminalControls(t *testing.T) {
	n, f, home := herdrNode(t)
	without := agyTestAgent(agyTestPane, home, "")
	seedAgents(t, n, f, without)
	const model = "test-model"
	n.noteWorkAgent(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), agyTestPane, agyTestRow, home, agyAgent, agyAgent, model)
	assertAgyResumeReport(t, f, 0, model, "")
	agyTestCall(t, n, "session.send", agyTestRow, map[string]any{"text": "first\nsecond"})
	agyTestCall(t, n, "session.interrupt", agyTestRow, nil)
	assertTrace(t, f.trace("agent.prompt", "pane.send_keys", "pane.send_input"), []string{
		call("agent.prompt", map[string]any{"target": agyTestPane, "text": "first\nsecond"}),
		sendKeys(agyTestPane, "escape"),
	})
	f.reply("session.snapshot", snapshot(nil, nil, []any{agyTestAgent(agyTestPane, home, agyTestID)}, nil))
	listSessions(t, n)
	assertAgyResumeReport(t, f, 1, model, agyTestID)
}

func TestAgyPaneResumeBackfillTracksConversation(t *testing.T) {
	n, f, home := herdrNode(t)
	first := agyTestAgent(agyTestPane, home, agyTestID)
	seedAgents(t, n, f, first)
	const model = "test-model"
	n.noteWorkAgent(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), agyTestPane, agyTestRow, home, agyAgent, agyAgent, model)
	assertAgyResumeReport(t, f, 0, model, agyTestID)
	n.mu.Lock()
	kept := n.launches[agyTestPane].AgyConversation
	n.mu.Unlock()
	if kept != agyTestID { // what was reported is what the launch keeps (review on #10364)
		t.Fatalf("launch keeps conversation %q, reported %q", kept, agyTestID)
	}

	second := agyTestAgent(agyTestPane, home, agyOtherTestID)
	f.reply("session.snapshot", snapshot(nil, nil, []any{second}, nil))
	listSessions(t, n)
	assertAgyResumeReport(t, f, 1, model, agyOtherTestID)

	duplicate := agyTestAgent("w1:p2", home, agyOtherTestID)
	f.reply("session.snapshot", snapshot(nil, nil, []any{second, duplicate}, nil))
	listSessions(t, n)
	assertAgyResumeReport(t, f, 2, model, "")
}

func TestAgyPaneResumeRejectsAmbiguousReports(t *testing.T) {
	for _, tc := range []struct{ name, pane, id string }{
		{"conflicting_reports", agyTestPane, agyOtherTestID},
		{"duplicate_id", "w1:p2", agyTestID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID), agyTestAgent(tc.pane, home, tc.id))
			const model = "test-model"
			n.noteWorkAgent(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), agyTestPane, agyTestRow, home, agyAgent, agyAgent, model)
			assertAgyResumeReport(t, f, 0, model, "")
		})
	}
}

func TestAgyPaneKeepsCursorTerminalBehavior(t *testing.T) {
	n, f, home := herdrNode(t)
	cursor := agentOf(cursorAgent, agyTestID, proto.StatusIdle, agyTestPane, "w1:t1", "w1", home, "cursor")
	seedAgents(t, n, f, cursor)
	const cursorRow = "cursor-w1-p1"
	items, _ := agyTestItems(t, n, cursorRow, nil)
	if len(items) != 1 || items[0].ID != "cursor#0" || items[0].Kind != "notice" || strings.Contains(items[0].Text, "antigravity-cli") {
		t.Fatalf("cursor notice: %+v", items)
	}
	if _, err := request(t, n, "session.item", map[string]any{"session_id": cursorRow, "item_id": items[0].ID}); err == nil {
		t.Fatal("Cursor unexpectedly expanded a transcript item")
	}
	agyTestCall(t, n, "session.watch", cursorRow, nil)
	n.mu.Lock()
	w := n.watches[cursorRow]
	n.mu.Unlock()
	if w != nil {
		t.Fatal("Cursor unexpectedly started a transcript watch")
	}
	agyTestCall(t, n, "session.unwatch", cursorRow, nil)
	agyTestCall(t, n, "session.send", cursorRow, map[string]any{"text": "check the file"})
	agyTestCall(t, n, "session.interrupt", cursorRow, nil)
	assertTrace(t, f.trace("agent.prompt", "pane.send_keys", "pane.send_input"), []string{
		call("agent.prompt", map[string]any{"target": agyTestPane, "text": "check the file"}),
		sendKeys(agyTestPane, "escape"),
	})
	if got := workAgentResume(cursorAgent, "cursor-agent", agyTestID); got != "cursor-agent" {
		t.Fatalf("Cursor resume: %s", got)
	}
}
