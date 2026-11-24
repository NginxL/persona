package assistant

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/chatsearch"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// agyTurn is a conversation switched from Claude Code (segment one, frozen at line 6) to agy (segment two, from
// offset 6), whose Antigravity companion pages its transcript as the runtime SDK does (historyRuntime.sdkEnd).
// While a turn runs the companion numbers the turn's live steps itself; when the turn ends agy's export replaces
// them with fewer steps under a new history (renumber), so a cursor taken during the turn lies past the new end.
type agyTurn struct {
	s   *Service
	f   *fakeHub
	d   *db.DB
	agy *historyRuntime
}

var (
	// agyLiveSteps is the turn as the companion streams it: one step per thought, tool call, tool result and message.
	agyLiveSteps = []proto.Item{
		{ID: "step1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "When does the ferry leave?"},
		{ID: "step2", Seq: 2, Role: "assistant", Kind: proto.KindThinking, Text: "Look up the timetable."},
		{ID: "step3", Seq: 3, Role: "assistant", Kind: proto.KindToolUse, ToolName: "WebSearch", ToolUseID: "call1"},
		{ID: "step4", Seq: 4, Role: "user", Kind: proto.KindToolResult, ToolUseID: "call1", Text: "09:00, 13:00"},
		{ID: "step5", Seq: 5, Role: "assistant", Kind: proto.KindThinking, Text: "Check the way back."},
		{ID: "step6", Seq: 6, Role: "assistant", Kind: proto.KindToolUse, ToolName: "WebSearch", ToolUseID: "call2"},
		{ID: "step7", Seq: 7, Role: "user", Kind: proto.KindToolResult, ToolUseID: "call2", Text: "17:00"},
		{ID: "step8", Seq: 8, Role: "assistant", Kind: proto.KindText, Text: "LiveDraftWord: the ferry leaves at nine"},
	}
	// agyExportedSteps is the same turn in agy's export, in fewer steps.
	agyExportedSteps = []proto.Item{
		{ID: "step1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "When does the ferry leave?"},
		{ID: "step2", Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolName: "WebSearch", ToolUseID: "call1"},
		{ID: "step3", Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "call1", Text: "09:00, 13:00"},
		{ID: "step4", Seq: 4, Role: "assistant", Kind: proto.KindThinking, Text: "Look up the timetable and the way back."},
		{ID: "step5", Seq: 5, Role: "assistant", Kind: proto.KindText, Text: "ExportWord: the ferry leaves at nine"},
	}
)

func agyTurnFixture(t *testing.T) *agyTurn {
	t.Helper()
	s, f, d, _ := logicalPair(t, "agy")
	claude := &historyRuntime{history: "h1", items: []proto.Item{
		{ID: "c1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "Plan the island trip"},
		{ID: "c6", Seq: 6, Role: "assistant", Kind: proto.KindText, Text: "Here is the plan"},
	}}
	agy := &historyRuntime{history: "h2", sdkEnd: true, items: agyLiveSteps}
	f.answerNode(DeviceSessionItems, func(machine string, raw json.RawMessage) (any, error) {
		var p historyParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if p.SessionID == "target-chat" {
			return agy.page(machine, raw)
		}
		return claude.page(machine, raw)
	})
	f.answerNode("session.watch", func(string, json.RawMessage) (any, error) { return map[string]bool{"ok": true}, nil })
	activatePair(t, d)
	return &agyTurn{s: s, f: f, d: d, agy: agy}
}

// read is a reader's next page after its cursor, on the path the apps and the search index take.
func (x *agyTurn) read(t *testing.T, after int) historyPage {
	t.Helper()
	var page historyPage
	params := map[string]any{"session_id": "side-chat", "after_seq": after, "lazy_images": true}
	if err := x.s.call(t.Context(), "server", DeviceSessionItems, params, &page); err != nil {
		t.Fatalf("read after %d: %v", after, err)
	}
	return page
}

// renumber ends the turn: agy's export replaces the live steps under a new history, and the node lists the
// rewritten transcript with a new rev.
func (x *agyTurn) renumber() {
	x.agy.mu.Lock()
	x.agy.history, x.agy.items = "h2:export", agyExportedSteps
	x.agy.mu.Unlock()
	x.f.setSessions("server", proto.Session{SessionID: "side-chat", Assistant: true, Agent: "claude", Rev: 100},
		proto.Session{SessionID: "target-chat", Assistant: true, Agent: "agy", Rev: 101})
}

func (x *agyTurn) searchHits(t *testing.T, word string) int {
	t.Helper()
	rows, err := x.d.SearchAssistantItems(t.Context(), []string{"server"}, []string{"side-chat"}, chatsearch.Words(word), 10)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// lastWatch is the params of the last session.watch the node got.
func (x *agyTurn) lastWatch(t *testing.T) map[string]any {
	t.Helper()
	x.f.mu.Lock()
	defer x.f.mu.Unlock()
	for i := len(x.f.calls) - 1; i >= 0; i-- {
		if x.f.calls[i].Method == "session.watch" {
			var p map[string]any
			if err := json.Unmarshal(x.f.calls[i].Params, &p); err != nil {
				t.Fatal(err)
			}
			return p
		}
	}
	t.Fatal("the node got no session.watch")
	return nil
}

// event hands the node's session.items event to the manager and returns what reached the apps, if anything.
func (x *agyTurn) event(t *testing.T, raw string) (historyPage, bool) {
	t.Helper()
	x.f.mu.Lock()
	x.f.told = nil
	x.f.mu.Unlock()
	x.s.NodeEvent("server", "session.items", json.RawMessage(raw))
	x.f.mu.Lock()
	defer x.f.mu.Unlock()
	for _, sent := range x.f.told {
		var e devicewire.NodeEvent
		if sent.event != devicewire.EventNode || json.Unmarshal(sent.params, &e) != nil || e.Event != "session.items" {
			continue
		}
		var page struct {
			SessionID string `json:"session_id"`
			historyPage
		}
		if err := json.Unmarshal(e.Params, &page); err != nil {
			t.Fatal(err)
		}
		if e.MachineID == "server" && page.SessionID == "side-chat" {
			return page.historyPage, true
		}
	}
	return historyPage{}, false
}

// A reader of a switched conversation holds a cursor from the agy turn's live steps. Once the export renumbers the
// turn, the read after that cursor answers with the conversation's new history, from which the reader starts over,
// instead of failing with "invalid source span"; a read from the start then has the export.
func TestLogicalReadPastARenumberedAgyTurnStartsOver(t *testing.T) {
	x := agyTurnFixture(t)
	live := x.read(t, 0)
	if intValue(live.ToSeq) != 14 || len(live.Items) != 10 || live.History == "" {
		t.Fatalf("the live turn: %+v", live)
	}
	x.renumber()
	page := x.read(t, 14)
	if page.History == "" || page.History == live.History || len(page.Items) != 0 {
		t.Fatalf("read past the export's end: %+v (the live turn's history %q)", page, live.History)
	}
	again := x.read(t, 0)
	if again.History != page.History || intValue(again.ToSeq) != 11 || len(again.Items) != 7 {
		t.Fatalf("read again from the start: %+v", again)
	}
	if last := again.Items[len(again.Items)-1]; last.Seq != 11 || !strings.HasPrefix(last.Text, "ExportWord") {
		t.Fatalf("the export's last step: %+v", last)
	}
	for _, it := range again.Items {
		if strings.Contains(it.Text, "LiveDraftWord") {
			t.Fatalf("a live step outlived the export: %+v", it)
		}
	}
}

// The chat search index read the agy turn's live steps. After the export renumbers the turn, its next read brings
// the conversation's new history: it reads the conversation again from the start, keeps the export's words and
// drops the live steps', instead of failing every minute until agy's steps pass its stale cursor.
func TestSearchIndexStartsOverWhenAgyRenumbersASwitchedConversation(t *testing.T) {
	x := agyTurnFixture(t)
	ctx := t.Context()
	chat := proto.Session{MachineID: "server", SessionID: "side-chat"}
	cur, done, err := x.s.searchRead(ctx, x.d, chat, searchCursor{}, false)
	if err != nil || !done || cur.afterSeq != 14 || x.searchHits(t, "LiveDraftWord") != 1 {
		t.Fatalf("indexing the live turn: %+v done %v: %v", cur, done, err)
	}
	x.renumber()
	next, done, err := x.s.searchRead(ctx, x.d, chat, cur, false)
	if err != nil || !done || next.history == cur.history || next.afterSeq != 11 {
		t.Fatalf("after the export: %+v done %v: %v", next, done, err)
	}
	if x.searchHits(t, "ExportWord") != 1 || x.searchHits(t, "LiveDraftWord") != 0 {
		t.Fatal("the index kept the live steps' words or missed the export's")
	}
}

// After the export renumbers the turn, a client reconnects with its cursor from the live steps. The runtime's
// backlog for that watch has from_seq past to_seq; the manager forwards it as an empty batch at the source's end
// with the conversation's new history, which tells the client to start over, where it used to drop it. The next
// turn's live steps then follow on the new numbering.
func TestLogicalBacklogPastARenumberedAgyTurnReachesTheClient(t *testing.T) {
	x := agyTurnFixture(t)
	ctx := t.Context()
	live := x.read(t, 0)
	x.renumber()
	x.read(t, 0) // records the export's history, as the manager's history sync does
	c, err := x.d.LogicalConversation(ctx, "server", "side-chat")
	if err != nil || c == nil || c.History == live.History {
		t.Fatalf("the conversation after the export: %+v %v", c, err)
	}
	if _, handled, err := x.s.logicalCall(ctx, "server", "session.watch", json.RawMessage(`{"session_id":"side-chat","since_seq":14}`)); !handled || err != nil {
		t.Fatalf("watch: handled %v: %v", handled, err)
	}
	if w := x.lastWatch(t); w["session_id"] != "target-chat" || w["since_seq"] != float64(8) || w["history"] != "h2:export" {
		t.Fatalf("the node's watch %v", w)
	}
	// The runtime SDK's catch-up for that watch: nothing after line 8, and the source ends at line 5.
	got, sent := x.event(t, `{"session_id":"target-chat","history":"h2:export","from_seq":8,"to_seq":5,"items":[]}`)
	if !sent || got.History != c.History || intValue(got.FromSeq) != 11 || intValue(got.ToSeq) != 11 || len(got.Items) != 0 {
		t.Fatalf("the backlog: sent %v %+v", sent, got)
	}
	got, sent = x.event(t, `{"session_id":"target-chat","history":"h2:export","from_seq":5,"to_seq":6,"items":[{"id":"step6","seq":6,"kind":"text","role":"user","text":"And the way back?"}]}`)
	if !sent || got.History != c.History || intValue(got.FromSeq) != 11 || intValue(got.ToSeq) != 12 || len(got.Items) != 1 || got.Items[0].Seq != 12 {
		t.Fatalf("the next turn's first step: sent %v %+v", sent, got)
	}
}
