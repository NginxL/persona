package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The processing conversation names an item (title and summary through inbox_suggest, docs/assistant/inbox.md
// "Following up"): a chat item takes both, the title clipped and on one line as grouping writes it, and title_by says
// the assistant wrote them, so grouping leaves them alone; a mail, a document or a company-feed item keeps its own
// title and takes the summary only; blanks written nothing.

// A chat item's conversation names it: both on one line, the title and the summary clipped as grouping clips them,
// and title_by says the assistant wrote them.
func TestInboxSuggestWritesTitleAndSummary(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.mu.Lock()
	before := it.Title
	h.inbox.mu.Unlock()
	out, err := suggestTool(t, h, `{"kind":"reply","text":"可以，周四给你","title":"  周五前给报价，\n等张三确认金额  ","summary":"张三问报价\n等他说能不能周四给"}`)
	if err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	title, summary, by := it.Title, it.Summary, it.TitleBy
	h.inbox.mu.Unlock()
	if title != "周五前给报价， 等张三确认金额" || strings.Contains(title, "\n") {
		t.Fatalf("title %q, want the subtitle on one line", title)
	}
	if title == before {
		t.Fatalf("the title did not change from %q", before)
	}
	if summary != "张三问报价 等他说能不能周四给" || strings.Contains(summary, "\n") {
		t.Fatalf("summary %q", summary)
	}
	if by != TitleByAssistant {
		t.Fatalf("title_by %q, want %q", by, TitleByAssistant)
	}
	if out == "" {
		t.Fatal("the tool answered nothing")
	}
}

// Long ones are clipped to the lengths grouping writes (40 and 80 runes) rather than kept whole.
func TestInboxSuggestClipsTitleAndSummary(t *testing.T) {
	h, _, it := handledHub(t)
	long := strings.Repeat("长", 200)
	if _, err := suggestTool(t, h, `{"recap":"r","title":"`+long+`","summary":"`+long+`"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	title, summary := it.Title, it.Summary
	h.inbox.mu.Unlock()
	if title != clip(long, 40) || title != clip(oneLine(long), 40) {
		t.Fatalf("title runs %d, want 41 (40 and the ellipsis): %q", len([]rune(title)), title)
	}
	if summary != clip(long, 80) {
		t.Fatalf("summary runs %d, want 81: %q", len([]rune(summary)), summary)
	}
}

// Whitespace is not a name: neither field changes and title_by stays as grouping left it.
func TestInboxSuggestBlankTitleChangesNothing(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.mu.Lock()
	it.TitleBy, it.Title, it.Summary = TitleByModel, "张三", "张三问报价"
	h.inbox.mu.Unlock()
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"好的","title":"   ","summary":"\n\t "}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Title != "张三" || it.Summary != "张三问报价" {
		t.Fatalf("blank fields changed the item: title %q, summary %q", it.Title, it.Summary)
	}
	if it.TitleBy != TitleByModel {
		t.Fatalf("title_by %q: blank fields were recorded as a name", it.TitleBy)
	}
}

// Writing only the summary still says the assistant wrote the item's words: grouping leaves the summary alone.
func TestInboxSuggestSummaryOnlyRecordsAssistant(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.mu.Lock()
	it.TitleBy, it.Title = TitleByModel, "张三"
	h.inbox.mu.Unlock()
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"好的","summary":"等张三确认周四能不能给"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Summary != "等张三确认周四能不能给" || it.TitleBy != TitleByAssistant || it.Title != "张三" {
		t.Fatalf("summary %q, title_by %q, title %q", it.Summary, it.TitleBy, it.Title)
	}
}

// A recap-only call may name it too: that is the turn that updates the summary after a send.
func TestInboxSuggestRecapAloneWritesTitle(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"recap":"已发出报价，等张三回","title":"报价已发，等张三回","summary":"等张三答复"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Title != "报价已发，等张三回" || it.Summary != "等张三答复" || it.TitleBy != TitleByAssistant || it.Recap != "已发出报价，等张三回" {
		t.Fatalf("title %q, summary %q, title_by %q, recap %q", it.Title, it.Summary, it.TitleBy, it.Recap)
	}
}

// An item whose title is the source's own keeps it (a mail's subject, a document, a post, a pull request or issue, a
// connector thread not grouped into a chat event); the summary is still written, and the tool says why the title was
// left. A chat event is one the hub's grouping made (Group) or a chat's own (Lark); a connector thread alone is not.
func TestInboxSuggestKeepsSourceTitles(t *testing.T) {
	for name, tc := range map[string]struct {
		kind string
		set  func(*InboxItem)
	}{
		"mail": {KindMail, func(it *InboxItem) { it.Mail = &InboxMail{AccountID: "acc1", Mailbox: "INBOX"} }},
		// A document and a company-feed row of a chat carry its chat block too: the block is not what decides.
		"doc": {KindDoc, func(it *InboxItem) {
			it.Doc, it.Lark = &InboxDoc{URL: "https://example.feishu.cn/docx/AbC"}, &InboxLark{FeedID: "f9"}
		}},
		"moment": {KindMoment, func(it *InboxItem) {
			it.Moment, it.Lark = &InboxMoment{PostID: "p1", Notice: "comment"}, &InboxLark{FeedID: "f9"}
		}},
		"github":  {"review_requested", func(it *InboxItem) { it.Connector = &InboxConnector{Thread: "example-org/example-repo#12"} }},
		"release": {"release", func(it *InboxItem) { it.Connector = &InboxConnector{Thread: "example-org/example-repo@v1"} }},
		// GitHub's @me rows carry the chat kinds too, but their title is the pull request's or issue's.
		"github mention": {KindMention, func(it *InboxItem) {
			it.Connector = &InboxConnector{Thread: "example-org/example-repo#12"}
			it.Source = "github"
		}},
		"github dm": {KindDM, func(it *InboxItem) {
			it.Connector = &InboxConnector{Thread: "example-org/example-repo#12"}
			it.Source = "github"
		}},
		// A chat kind on a connector's thread the hub did not group into an event: not a chat event, its title stays.
		"imessage dm": {KindDM, func(it *InboxItem) {
			it.Connector = &InboxConnector{Thread: "imessage-0123456789ab"}
			it.Source = "imessage"
		}},
		"subscription": {"sub_notice", func(it *InboxItem) {
			it.Connector = &InboxConnector{Thread: "sub:sub_x:t1"}
			it.Source = "sub_x"
		}},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, it := handledHub(t)
			h.inbox.mu.Lock()
			it.Kind, it.Title, it.Source = tc.kind, "来源自己的标题", "example"
			it.Lark, it.Group = nil, nil
			tc.set(it)
			h.inbox.mu.Unlock()
			out, err := suggestTool(t, h, `{"kind":"reply","text":"好的","title":"助手想写的新标题","summary":"等对方回复"}`)
			if err != nil {
				t.Fatal(err)
			}
			h.inbox.mu.Lock()
			title, summary, by := it.Title, it.Summary, it.TitleBy
			h.inbox.mu.Unlock()
			if title != "来源自己的标题" {
				t.Fatalf("title %q: the source's own title was overwritten", title)
			}
			if by == TitleByAssistant && summary == "" {
				t.Fatalf("title_by %q yet no summary was written", by)
			}
			if summary != "等对方回复" {
				t.Fatalf("summary %q: the summary was not written", summary)
			}
			if !strings.Contains(strings.ToLower(out), "title") {
				t.Fatalf("the tool does not say the title was left: %q", out)
			}
		})
	}
}

// A grouped chat event (one the hub grouped, its Connector beside its Group) is a chat event to name: the assistant's
// title stands, whatever the source is.
func TestInboxSuggestNamesGroupedChat(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.mu.Lock()
	it.Kind, it.Source, it.Title = KindGroup, "example", "示例群"
	it.Lark = nil
	it.Connector = &InboxConnector{Thread: "example-thread"}
	it.Group = &InboxGroup{Connector: "example", Thread: "example-thread"}
	h.inbox.mu.Unlock()
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"好的","title":"发布改到周三","summary":"等对方确认时间"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Title != "发布改到周三" || it.TitleBy != TitleByAssistant || it.Summary != "等对方确认时间" {
		t.Fatalf("title %q, title_by %q, summary %q", it.Title, it.TitleBy, it.Summary)
	}
}

// A filed item refuses a kind and nothing it carried is kept; a recap-only call on it is kept, and names it.
func TestInboxSuggestFiledItemTitle(t *testing.T) {
	// A kind is refused on a filed item: no title, no summary, no title_by.
	h, _, it := handledHub(t)
	done := time.Now().Add(-time.Minute).UTC()
	h.inbox.mu.Lock()
	h.inbox.file(it, done, "user")
	it.TitleBy, it.Title, it.Summary = TitleByModel, "张三", "张三问报价"
	before := string(itemState(it))
	h.inbox.mu.Unlock()
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"好的","title":"不该写进去","summary":"也不该"}`); !errors.Is(err, errSuggestFiled) {
		t.Fatalf("a filed item accepted a kind: %v", err)
	}
	h.inbox.mu.Lock()
	after := string(itemState(it))
	h.inbox.mu.Unlock()
	if after != before {
		t.Fatalf("a refused kind changed the filed item:\nbefore: %s\nafter:  %s", before, after)
	}
	// Only a recap is kept, and may name it.
	if _, err := suggestTool(t, h, `{"recap":"对方只说了谢谢","title":"对方道谢","summary":"不用再回"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Title != "对方道谢" || it.Summary != "不用再回" || it.TitleBy != TitleByAssistant || it.DoneAt == nil {
		t.Fatalf("title %q, summary %q, title_by %q, done %v", it.Title, it.Summary, it.TitleBy, it.DoneAt)
	}
}

// The conversation an item was handed to names it too, recorded as the assistant's.
func TestInboxSuggestHandoffWritesTitle(t *testing.T) {
	h, _, it := handoffHub(t)
	setSettings(t, h.Service, func(v *Settings) { v.Main = "m1" })
	h.f.setSessions("server", proto.Session{SessionID: "m1", Assistant: true, Status: proto.StatusIdle})
	handOff(t, h, it)
	if _, err := mainSuggest(h, "m1", `{"item":"lark:f1:100","kind":"reply","text":"好的","title":"张三的报价请求","summary":"等张三确认金额"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Title != "张三的报价请求" || it.Summary != "等张三确认金额" || it.TitleBy != TitleByAssistant {
		t.Fatalf("title %q, summary %q, title_by %q", it.Title, it.Summary, it.TitleBy)
	}
}

// title_by is bookkeeping for the hub: an app is never sent it, neither in the list nor on the item.
func TestInboxTitleByNotListed(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"好的","title":"等张三定时间","summary":"等张三答复"}`); err != nil {
		t.Fatal(err)
	}
	mux := h.routes()
	if code, out := inboxCall(t, mux, "GET", "/api/v1/inbox/"+it.ID, ""); code != 200 {
		t.Fatalf("read the item: %d", code)
	} else {
		raw, _ := json.Marshal(out)
		if strings.Contains(string(raw), "title_by") {
			t.Fatalf("the item sends title_by: %s", raw)
		}
		if out["title"] != "等张三定时间" {
			t.Fatalf("the item does not send the title it now has: %v", out["title"])
		}
	}
	if _, list := inboxCall(t, mux, "GET", "/api/v1/inbox", ""); true {
		raw, _ := json.Marshal(list)
		if strings.Contains(string(raw), "title_by") {
			t.Fatalf("the list sends title_by: %s", raw)
		}
	}
}

// What the conversation named is kept: a manager started again has it.
func TestInboxSuggestedTitleSurvivesRestart(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"好的","title":"等张三定时间","summary":"等张三答复"}`); err != nil {
		t.Fatal(err)
	}
	h2 := New(Options{Hub: h.f, Store: func() Store { return h.st }, Logf: testLogf(t)})
	if err := h2.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	h2.inbox.mu.Lock()
	back := h2.inbox.items[it.ID]
	h2.inbox.mu.Unlock()
	if back == nil {
		t.Fatal("the item was not stored")
	}
	if back.Title != "等张三定时间" || back.Summary != "等张三答复" || back.TitleBy != TitleByAssistant {
		t.Fatalf("reloaded title %q, summary %q, title_by %q", back.Title, back.Summary, back.TitleBy)
	}
}
