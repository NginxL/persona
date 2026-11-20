package hub

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestWithInjected(t *testing.T) {
	h := &Hub{}
	h.takeTodoReport(assistantwire.AssistantID, &assistantwire.TodoReport{Links: []assistantwire.TodoLink{
		{MachineID: "m1", SessionID: "a", TodoID: "td_1", Title: "跑测试", Task: true},
		{MachineID: "m1", SessionID: "b", TodoID: "td_1", Title: "跑测试", Task: true}, // one task, two agents
		{MachineID: "m1", SessionID: "c", TodoID: "td_2", Title: "重名", Task: true},
		{MachineID: "m1", SessionID: "d", TodoID: "td_3", Title: "重名", Task: true},
	}})
	notice := func(title string) string {
		return prompts.RenderTaskNotice(prompts.TaskNotice{Title: title, Machine: "电脑", State: "done", Text: "好了"})
	}
	items := []proto.Item{
		{ID: "1", Role: "user", Kind: proto.KindText, Text: notice("跑测试")},
		{ID: "2", Role: "user", Kind: proto.KindText, Text: notice("重名")},
		{ID: "3", Role: "user", Kind: proto.KindText, Text: notice("跑测试"), ClientMsgID: "c1"}, // the app sent it: the user's
		{ID: "4", Role: "assistant", Kind: proto.KindText, Text: notice("跑测试")},
		{ID: "5", Role: "user", Kind: proto.KindText, Text: "帮我看看"},
	}
	want := items[0].Text
	got := h.withInjected(items)
	if in := got[0].Injected; in == nil || in.Kind != proto.InjectedTaskNotice || in.TodoID != "td_1" || in.Line != "任务「跑测试」做完了" {
		t.Fatalf("notice: %+v", in)
	}
	if got[0].Text != want {
		t.Fatal("the text the model read changed")
	}
	if in := got[1].Injected; in == nil || in.TodoID != "" {
		t.Fatalf("two tasks with the title: %+v", in)
	}
	for _, i := range []int{2, 3, 4} {
		if got[i].Injected != nil {
			t.Fatalf("item %s marked: %+v", got[i].ID, got[i].Injected)
		}
	}
}

// The manager's note to a conversation an inbox item was handed to (#1194) is one line in the user's language; the
// same words sent by an app (client_msg_id) stay the user's.
func TestWithInjectedInboxNote(t *testing.T) {
	h := &Hub{}
	note := prompts.RenderInboxHandoffNote(prompts.InboxHandoffNote{ID: "lark:f1:100", Title: "报价", Note: "示例用户驳回了你的建议。"})
	got := h.withInjected([]proto.Item{
		{ID: "1", Role: "user", Kind: proto.KindText, Text: note},
		{ID: "2", Role: "user", Kind: proto.KindText, Text: note, ClientMsgID: "c1"},
	})
	if in := got[0].Injected; in == nil || in.Kind != proto.InjectedSystem || in.State != "inbox" || in.Title != "报价" || in.Line != "收件箱「报价」有了下文" {
		t.Fatalf("note: %+v", in)
	}
	if got[0].Text != note {
		t.Fatal("the text the model read changed")
	}
	if got[1].Injected != nil {
		t.Fatalf("an app's line marked: %+v", got[1].Injected)
	}
	for l, want := range map[i18n.Lang]string{i18n.En: "Inbox: an update on “报价”", i18n.Ja: "受信箱：「报价」に動きがありました"} {
		if line := injectedLine(l, got[0].Injected); line != want {
			t.Errorf("%s: %q, want %q", l, line, want)
		}
	}
}

func TestWithLocalizedWakeKeepsTextAndUserIdentity(t *testing.T) {
	h := &Hub{}
	text := prompts.RenderAgentWake(prompts.AgentWake{Language: "English", Attempt: 1, Wait: "sample build"})
	got := h.withInjected([]proto.Item{
		{Role: "user", Kind: proto.KindText, Text: text},
		{Role: "user", Kind: proto.KindText, Text: text, ClientMsgID: "app-input"},
		{Role: "user", Kind: proto.KindText, Text: prompts.ProtectUserMessage(text)},
	})
	if got[0].Injected == nil || got[0].Injected.State != "wake" || got[0].Text != text {
		t.Fatalf("wake text or metadata changed: %+v", got[0])
	}
	for _, it := range got[1:] {
		if it.Injected != nil || it.Source != nil {
			t.Fatal("a user's pasted wake became automated input")
		}
	}
	for _, l := range []i18n.Lang{i18n.En, i18n.Ja, i18n.ZhHans} {
		want := i18n.T(l, "injected.wake", i18n.Args{"title": "sample build"})
		if line := injectedLine(l, got[0].Injected); line != want {
			t.Fatalf("%s: %q, want %q", l, line, want)
		}
	}
}

func TestWithInjectedAskLater(t *testing.T) {
	h := &Hub{}
	later := prompts.RenderAskLater(prompts.AskLater{When: "11-13 12:48", Questions: []string{"什么时候提醒？"},
		Answers: []prompts.AskTodoChoice{{Question: "什么时候提醒？", Choice: "到家时"}}})
	got := h.withInjected([]proto.Item{{ID: "1", Role: "user", Kind: proto.KindText, Text: later}})
	in := got[0].Injected
	if in == nil || in.Kind != proto.InjectedSystem || in.State != "ask_answered" || in.Title != "到家时" || in.Line != "你回答了：到家时" {
		t.Fatalf("ask_later: %+v", in)
	}
	if got[0].Text != later {
		t.Fatal("the text the model read changed")
	}
	for l, want := range map[i18n.Lang]string{i18n.En: "You answered: 到家时", i18n.Ja: "回答しました：到家时"} {
		if line := injectedLine(l, in); line != want {
			t.Errorf("%s: %q, want %q", l, line, want)
		}
	}
	for state, want := range map[string]string{"ask_expired": "The question timed out unanswered", "ask_closed": "The question was closed without an answer"} {
		if line := injectedLine(i18n.En, &proto.Injected{Kind: proto.InjectedSystem, State: state}); line != want {
			t.Errorf("%s: %q, want %q", state, line, want)
		}
	}
}

func TestWithLocalizedInboxKeepsTextAndUserIdentity(t *testing.T) {
	h := &Hub{}
	text := prompts.RenderInboxHandoffNote(prompts.InboxHandoffNote{Language: "English", ID: "event_1", Title: "sample build", Note: "Original notice"})
	got := h.withInjected([]proto.Item{
		{Role: "user", Kind: proto.KindText, Text: text},
		{Role: "user", Kind: proto.KindText, Text: text, ClientMsgID: "app-input"},
		{Role: "user", Kind: proto.KindText, Text: prompts.ProtectUserMessage(text)},
	})
	if got[0].Injected == nil || got[0].Injected.State != "inbox" || got[0].Text != text {
		t.Fatalf("Inbox text or metadata changed: %+v", got[0])
	}
	for _, it := range got[1:] {
		if it.Injected != nil || it.Source != nil {
			t.Fatal("a user's pasted Inbox notice became automated input")
		}
	}
	for _, l := range []i18n.Lang{i18n.En, i18n.Ja, i18n.ZhHans} {
		want := i18n.T(l, "injected.inbox_update", i18n.Args{"title": "sample build"})
		if line := injectedLine(l, got[0].Injected); line != want {
			t.Fatalf("%s: %q, want %q", l, line, want)
		}
	}
}
