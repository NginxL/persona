package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// tagsFleet is a fleet manager whose machine "mac" carries two assistant conversations: conv-a-0001 (the main one)
// and conv-b-0002, both in the assistant's own space.
func tagsFleet(t *testing.T) *fleetTest {
	t.Helper()
	ft := newFleetTest(t)
	ft.f.hello.Assistant.MachineID = "mac"
	ft.f.hello.Assistant.Machines = []string{"mac"}
	setSettings(t, ft.s, func(v *Settings) { v.MachineID, v.Main = "mac", "conv-a-0001" })
	convs := []proto.Session{
		{MachineID: "mac", SessionID: "conv-a-0001", Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusIdle, Title: "Main chat"},
		{MachineID: "mac", SessionID: "conv-b-0002", Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusIdle, Title: "Other chat"},
	}
	ft.sessions("mac", convs...)
	ft.f.setSessions("mac", convs...)
	return ft
}

func tagRequest(s *Service, role, id, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, "/api/v1/assistant/conversations/"+id+"/tags", strings.NewReader(body))
	if role != "" {
		r.Header.Set(RoleHeader, role)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func rowByID(list []proto.Session, id string) proto.Session {
	for _, c := range list {
		if c.SessionID == id {
			return c
		}
	}
	return proto.Session{}
}

// The write rules every path shares: trim, drop empties, de-duplicate to the first occurrence, then enforce the
// per-tag length and per-conversation count limits.
func TestNormalizeConversationTags(t *testing.T) {
	got, err := normalizeConversationTags([]string{"  planning ", "", "planning", "trip"})
	if err != nil || len(got) != 2 || got[0] != "planning" || got[1] != "trip" {
		t.Fatalf("normalize: %v %v", got, err)
	}
	if got, err = normalizeConversationTags(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty input must be a non-nil empty list: %v %v", got, err)
	}
	if _, err = normalizeConversationTags([]string{strings.Repeat("好", conversationTagMaxRunes+1)}); !errors.Is(err, errTagTooLong) {
		t.Fatalf("over-long tag: %v", err)
	}
	eleven := make([]string, conversationTagsMax+1)
	for i := range eleven {
		eleven[i] = "tag" + string(rune('a'+i))
	}
	if _, err = normalizeConversationTags(eleven); !errors.Is(err, errTagTooMany) {
		t.Fatalf("over-count: %v", err)
	}
}

// The PUT route writes through the settings store, the decorated list shows the new tags, a second manager on the
// same store reads them back, an empty list clears the record, and an unchanged replacement reports changed=false.
func TestConversationTagsPutPersistsDecoratesAndClears(t *testing.T) {
	ft := tagsFleet(t)
	w := tagRequest(ft.s, roleFull, "conv-a-0001", `{"tags":[" planning ","trip","trip"]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"changed":true`) {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	row := rowByID(ft.s.decorated("mac"), "conv-a-0001")
	if row.Tags == nil || strings.Join(*row.Tags, ",") != "planning,trip" {
		t.Fatalf("decorated tags: %+v", row.Tags)
	}
	records, ok := ft.s.conversationTagsRecords()
	if !ok {
		t.Fatal("tag book not loaded")
	}
	rec := records["conv-a-0001"]
	if rec == nil || rec.MachineID != "mac" || rec.By != titleByUser {
		t.Fatalf("record provenance: %+v", rec)
	}

	// A fresh manager over the same store restores the book (no database migration, settings only).
	next := newFleetTestOn(t, ft.st)
	next.f.hello.Assistant.MachineID = "mac"
	next.f.hello.Assistant.Machines = []string{"mac"}
	setSettings(t, next.s, func(v *Settings) { v.MachineID, v.Main = "mac", "conv-a-0001" })
	next.sessions("mac",
		proto.Session{MachineID: "mac", SessionID: "conv-a-0001", Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusIdle},
		proto.Session{MachineID: "mac", SessionID: "conv-b-0002", Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusIdle})
	if row = rowByID(next.s.decorated("mac"), "conv-a-0001"); row.Tags == nil || strings.Join(*row.Tags, ",") != "planning,trip" {
		t.Fatalf("tags lost across restart: %+v", row.Tags)
	}

	// Same content again: nothing to write.
	if w = tagRequest(ft.s, roleFull, "conv-a-0001", `{"tags":["planning","trip"]}`); w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"changed":true`) {
		t.Fatalf("unchanged put: %d %s", w.Code, w.Body)
	}
	// An empty list removes the record entirely.
	if w = tagRequest(ft.s, roleFull, "conv-a-0001", `{"tags":[]}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"changed":true`) {
		t.Fatalf("clear put: %d %s", w.Code, w.Body)
	}
	if records, _ = ft.s.conversationTagsRecords(); records["conv-a-0001"] != nil {
		t.Fatalf("record not dropped: %+v", records["conv-a-0001"])
	}
	if row = rowByID(ft.s.decorated("mac"), "conv-a-0001"); row.Tags != nil {
		t.Fatalf("cleared row still tagged: %+v", row.Tags)
	}
}

// Route guards: only a full role writes, the id must be a session id, bodies must decode, and the limits return 400.
func TestConversationTagsRouteGuards(t *testing.T) {
	ft := tagsFleet(t)
	if w := tagRequest(ft.s, "answer", "conv-a-0001", `{"tags":["x"]}`); w.Code != http.StatusForbidden {
		t.Fatalf("role guard: %d", w.Code)
	}
	if w := tagRequest(ft.s, roleFull, "bad!id", `{"tags":["x"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("id guard: %d", w.Code)
	}
	if w := tagRequest(ft.s, roleFull, "conv-a-0001", `{not json`); w.Code != http.StatusBadRequest {
		t.Fatalf("body guard: %d", w.Code)
	}
	if w := tagRequest(ft.s, roleFull, "conv-a-0001", `{"tags":["`+strings.Repeat("好", conversationTagMaxRunes+1)+`"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("length guard: %d %s", w.Code, w.Body)
	}
	eleven := `{"tags":["t01","t02","t03","t04","t05","t06","t07","t08","t09","t10","t11"]}`
	if w := tagRequest(ft.s, roleFull, "conv-a-0001", eleven); w.Code != http.StatusBadRequest {
		t.Fatalf("count guard: %d %s", w.Code, w.Body)
	}
}

// Deleting a conversation takes its tag record with it (search_deleted.go's finishDeletedConversation).
func TestConversationTagsDeletedWithConversation(t *testing.T) {
	ctx := context.Background()
	ft := tagsFleet(t)
	if changed, err := ft.s.setConversationTags(ctx, "mac", "conv-b-0002", []string{"trip"}, titleByUser); err != nil || !changed {
		t.Fatalf("tag before delete: changed=%v err=%v", changed, err)
	}
	if err := ft.s.deleteConversation(ctx, "mac", "conv-b-0002"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	records, _ := ft.s.conversationTagsRecords()
	if rec := records["conv-b-0002"]; rec != nil {
		t.Fatalf("tags outlived the conversation: %+v", rec)
	}
}

// GET /api/v1/assistant?tag= keeps only the conversations carrying that exact tag.
func TestProfileTagFilter(t *testing.T) {
	ctx := context.Background()
	ft := tagsFleet(t)
	if _, err := ft.s.setConversationTags(ctx, "mac", "conv-b-0002", []string{"trip", "planning"}, titleByUser); err != nil {
		t.Fatal(err)
	}
	ids := func(query string) []string {
		w := serve(ft.s, http.MethodGet, "/api/v1/assistant"+query, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", query, w.Code, w.Body)
		}
		var view struct {
			Conversations []proto.Session `json:"conversations"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range view.Conversations {
			out = append(out, c.SessionID)
		}
		return out
	}
	if got := strings.Join(ids(""), ","); got != "conv-a-0001,conv-b-0002" {
		t.Fatalf("unfiltered: %s", got)
	}
	if got := strings.Join(ids("?tag=trip"), ","); got != "conv-b-0002" {
		t.Fatalf("tag filter: %s", got)
	}
	if got := ids("?tag=missing"); len(got) != 0 {
		t.Fatalf("unknown tag: %v", got)
	}
}

// The MCP tool replaces the whole set, resolves an 8-character prefix, can tag the calling conversation itself, and
// conversations_list filters by tag and prints each row's tags.
func TestConversationTagsToolAndListFilter(t *testing.T) {
	ctx := context.Background()
	ft := tagsFleet(t)
	tool := func(self, name, args string) proto.ToolResult {
		return ft.s.Tool(ctx, ToolCall{MachineID: "mac", SessionID: self, Name: name, Arguments: json.RawMessage(args)})
	}
	// Full replacement through the 8-character prefix.
	res := tool("conv-a-0001", "conversation_tags_set", `{"id":"conv-b-0","tags":["x","y"]}`)
	if res.IsError {
		t.Fatalf("tags_set: %+v", res)
	}
	if row := rowByID(ft.s.decorated("mac"), "conv-b-0002"); row.Tags == nil || strings.Join(*row.Tags, ",") != "x,y" {
		t.Fatalf("tool write: %+v", row.Tags)
	}
	// The calling conversation itself is a valid target.
	res = tool("conv-a-0001", "conversation_tags_set", `{"id":"conv-a-0001","tags":["self"]}`)
	if res.IsError || !strings.Contains(res.Content[0].Text, "Tagged this conversation") {
		t.Fatalf("self tag: %+v", res)
	}
	// Limits are refused before any write.
	if res = tool("conv-a-0001", "conversation_tags_set", `{"id":"conv-a-0001","tags":["t01","t02","t03","t04","t05","t06","t07","t08","t09","t10","t11"]}`); !res.IsError || !strings.Contains(res.Content[0].Text, "10") {
		t.Fatalf("count limit: %+v", res)
	}

	list := func(args string) string {
		r := tool("conv-a-0001", "conversations_list", args)
		if r.IsError {
			t.Fatalf("conversations_list %s: %+v", args, r)
		}
		return r.Content[0].Text
	}
	out := list(`{"tag":"x"}`)
	if !strings.Contains(out, "conv-b-0002") || strings.Contains(out, "conv-a-0001") {
		t.Fatalf("tag-filtered list: %s", out)
	}
	if !strings.Contains(out, "tags: x, y") {
		t.Fatalf("row tags not shown: %s", out)
	}
	if out = list(`{"tag":"missing"}`); !strings.Contains(out, "No conversation matches") {
		t.Fatalf("empty filter: %s", out)
	}
	out = list(`{}`)
	if !strings.Contains(out, "conv-a-0001") || !strings.Contains(out, "tags: self") {
		t.Fatalf("full list loses tags: %s", out)
	}
}

// A publication keeps the records it read while later writes replace or add tags.
func TestConversationTagsRecordsStayStableAcrossWrites(t *testing.T) {
	ctx := context.Background()
	ft := tagsFleet(t)
	set := func(id, tag string) {
		t.Helper()
		if _, err := ft.s.setConversationTags(ctx, "mac", id, []string{tag}, titleByUser); err != nil {
			t.Fatal(err)
		}
	}
	set("conv-a-0001", "first")
	records, ok := ft.s.conversationTagsRecords()
	if !ok || len(records) != 1 {
		t.Fatalf("initial records: %v, loaded %v", records, ok)
	}
	set("conv-a-0001", "second")
	set("conv-b-0002", "other")
	if len(records) != 1 || strings.Join(records["conv-a-0001"].Tags, ",") != "first" {
		t.Fatalf("a later write changed the published snapshot: %v", records)
	}
	delete(records, "conv-a-0001")
	current, ok := ft.s.conversationTagsRecords()
	if !ok || len(current) != 2 || strings.Join(current["conv-a-0001"].Tags, ",") != "second" {
		t.Fatalf("editing a returned map changed the book: %v, loaded %v", current, ok)
	}
}

// The health space and the assistant's own space cannot tag each other's conversations; within one space it works.
func TestConversationTagsToolSpaceIsolation(t *testing.T) {
	f := healthHub()
	// The fixture's rows omit MachineID; a real node always sends it, and a tag record stores it.
	for i := range f.sessions["server"] {
		f.sessions["server"][i].MachineID = "server"
	}
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })
	tool := func(self, args string) proto.ToolResult {
		return s.Tool(context.Background(), ToolCall{MachineID: "server", SessionID: self, Name: "conversation_tags_set", Arguments: json.RawMessage(args)})
	}
	if res := tool("main-0000-0000", `{"id":"sleep-000-0000","tags":["x"]}`); !res.IsError {
		t.Fatalf("main tagged a health conversation: %+v", res)
	}
	if res := tool("sleep-000-0000", `{"id":"work-0000-0000","tags":["x"]}`); !res.IsError {
		t.Fatalf("health tagged the assistant's conversation: %+v", res)
	}
	if res := tool("sleep-000-0000", `{"id":"steps-000-0000","tags":["sleep"]}`); res.IsError {
		t.Fatalf("same-space tag refused: %+v", res)
	}
	records, _ := s.conversationTagsRecords()
	if rec := records["steps-000-0000"]; rec == nil || strings.Join(rec.Tags, ",") != "sleep" {
		t.Fatalf("health-space write: %+v", rec)
	}
}

// conversation_new applies the initial tags to the conversation it opened, and an invalid tag set is refused before
// any conversation starts.
func TestConversationNewWithTags(t *testing.T) {
	h := newSpawnHarness(t)
	res := h.s.Tool(context.Background(), ToolCall{MachineID: "server", SessionID: "parent-1", Name: "conversation_new",
		Arguments: json.RawMessage(`{"text":"go research","tags":[" alpha ","alpha","beta"]}`)})
	if res.IsError {
		t.Fatalf("conversation_new: %+v", res)
	}
	m := startedConvRe.FindStringSubmatch(res.Content[0].Text)
	if m == nil {
		t.Fatalf("no id in %q", res.Content[0].Text)
	}
	id := m[1]
	records, _ := h.s.conversationTagsRecords()
	rec := records[id]
	if rec == nil || strings.Join(rec.Tags, ",") != "alpha,beta" || rec.By != titleByAssistant {
		t.Fatalf("initial tags: %+v", rec)
	}
	if !strings.Contains(res.Content[0].Text, "tags: alpha, beta") {
		t.Fatalf("answer omits the tags: %q", res.Content[0].Text)
	}

	// Over the count limit: nothing starts.
	before := len(h.f.sessions["server"])
	res = h.s.Tool(context.Background(), ToolCall{MachineID: "server", SessionID: "parent-1", Name: "conversation_new",
		Arguments: json.RawMessage(`{"text":"go research","tags":["t01","t02","t03","t04","t05","t06","t07","t08","t09","t10","t11"]}`)})
	if !res.IsError || !strings.Contains(res.Content[0].Text, "10") {
		t.Fatalf("limit refused: %+v", res)
	}
	if len(h.f.sessions["server"]) != before {
		t.Fatal("a refused call started a conversation")
	}
}
