package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

func TestTopicOpeningUsesReadableTitle(t *testing.T) {
	const link = "https://docs.example.com/wiki/garden-notes-2026"
	post := func(body string) botcard.MessageEvent {
		ev := rootEvent("om_first", "")
		ev.Message.MessageType, ev.Message.Content = "post", body
		return ev
	}
	withBot := func(ev botcard.MessageEvent) botcard.MessageEvent {
		ev.Message.Mentions = []botcard.Mention{{Key: "@_user_1", Name: "Garden Bot"}}
		return ev
	}
	withBotOpenID := func(ev botcard.MessageEvent) botcard.MessageEvent {
		ev.Message.Mentions = []botcard.Mention{{Key: "@_user_1", Name: "Garden Bot", ID: botcard.UserID{OpenID: "ou_bot"}}}
		return ev
	}
	cases := []struct {
		name, locale string
		event        botcard.MessageEvent
		title, notes string
	}{
		{"ordinary words", "en", rootEvent("om_first", "Review the seed order today"), "Review the seed order today", "Review the seed order today"},
		{"filename is part of the request", "en", rootEvent("om_first", "slides.pdf needs updated totals"), "slides.pdf needs updated totals", "slides.pdf needs updated totals"},
		{"words after a URL and bot", "en", withBot(rootEvent("om_first", link+" @_user_1 Compare the April totals")), "Compare the April totals", link + " @Garden Bot Compare the April totals"},
		{"file and URL followed by a generic request", "en", withBot(rootEvent("om_first", "slides.pdf 🔗 "+link+" @_user_1 have a look")), "New to-do", "slides.pdf 🔗 " + link + " @Garden Bot have a look"},
		{"URL alone", "ja", rootEvent("om_first", link), "新しい ToDo", link},
		{"opening mention touching a URL", "zh-Hans", withBot(rootEvent("om_first", "@_user_1，"+link+" 有结果吗")), "有结果吗", "@Garden Bot，" + link + " 有结果吗"},
		{"opening mention touching a named rich link", "en",
			withBot(post(`{"content":[[{"tag":"at","user_id":"@_user_1"},{"tag":"text","text":"："},{"tag":"a","href":"` + link + `","text":"Garden [Q3] (draft)"}]]}`)),
			"Garden [Q3] (draft)", "@Garden Bot：[Garden [Q3] (draft)](" + link + ")"},
		{"opening mention directly touching a named rich link", "en",
			withBot(post(`{"content":[[{"tag":"at","user_id":"@_user_1"},{"tag":"a","href":"` + link + `","text":"Garden [Q3] (draft)"}]]}`)),
			"Garden [Q3] (draft)", "@Garden Bot[Garden [Q3] (draft)](" + link + ")"},
		{"opening open-id mention touching punctuation and a named rich link", "en",
			withBotOpenID(post(`{"content":[[{"tag":"at","user_id":"ou_bot","user_name":"Garden Bot"},{"tag":"text","text":"："},{"tag":"a","href":"` + link + `","text":"Garden [Q3] (draft)"}]]}`)),
			"Garden [Q3] (draft)", "@Garden Bot：[Garden [Q3] (draft)](" + link + ")"},
		{"opening mention with space before punctuation and a named rich link", "en",
			withBot(post(`{"content":[[{"tag":"at","user_id":"@_user_1"},{"tag":"text","text":" ："},{"tag":"a","href":"` + link + `","text":"Garden [Q3] (draft)"}]]}`)),
			"Garden [Q3] (draft)", "：[Garden [Q3] (draft)](" + link + ")"},
		{"punctuation after a shared URL and mention", "zh-Hans", withBot(rootEvent("om_first", link+"，@_user_1：核对春季花种")), "核对春季花种", link + "，@Garden Bot：核对春季花种"},
		{"generic words alone", "en", rootEvent("om_first", "Have a look!"), "New to-do", "Have a look!"},
		{"similar words with a subject", "en", rootEvent("om_first", "Have a look at the seed order"), "Have a look at the seed order", "Have a look at the seed order"},
		{"named rich link after media", "en",
			withBot(post(`{"title":"","content":[[{"tag":"media","file_key":"file_example"},{"tag":"a","href":"` + link + `","text":"Garden [Q3] (draft)"},{"tag":"text","text":" "},{"tag":"at","user_id":"@_user_1"},{"tag":"text","text":" have a look"}]]}`)),
			"Garden [Q3] (draft)", "[视频][Garden [Q3] (draft)](" + link + ") @Garden Bot have a look"},
		{"rich link labeled with its own URL", "zh-Hans",
			post(`{"zh_cn":{"content":[[{"tag":"a","href":"` + link + `","text":"` + link + `"},{"tag":"text","text":" 请看一下。"}]]}}`),
			"新建待办", "[" + link + "](" + link + ") 请看一下。"},
		{"rich link labeled with an attachment marker", "en", post(`{"content":[[{"tag":"a","href":"` + link + `","text":"[File]"},{"tag":"text","text":", have a look"}]]}`), "New to-do", "[[File]](" + link + "), have a look"},
		{"punctuation as a rich link label", "en", post(`{"content":[[{"tag":"a","href":"` + link + `","text":"…"}]]}`), "New to-do", "[…](" + link + ")"},
		{"unlabeled rich link followed by words", "ja",
			post(`{"ja_jp":{"content":[[{"tag":"a","href":"` + link + `","text":""},{"tag":"text","text":" 予算表を更新する"}]]}}`),
			"予算表を更新する", "[](" + link + ") 予算表を更新する"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verify := func(t *testing.T, database *db.DB) {
				t.Helper()
				var store Store
				if database != nil {
					store = database
				}
				e := newThreadEnv(t, store)
				e.f.answerApp(AppFeishu, FeishuSave, func(json.RawMessage) (any, error) {
					return map[string]string{"path": "/tmp/example-topic-file.mp4"}, nil
				})
				e.f.mu.Lock()
				e.f.hello.Locale = tc.locale
				e.publicURL = "https://hub.example.com"
				e.f.mu.Unlock()
				e.deliver(tc.event)
				todo := onlyTodo(e.s)
				if todo.ID == "" || todo.Title != tc.title || todo.Notes != tc.notes || todo.Thread == nil || todo.Thread.Title != tc.title {
					t.Errorf("initial to-do title %q, notes %q, topic %+v; want %q, %q", todo.Title, todo.Notes, todo.Thread, tc.title, tc.notes)
				}
				notes := topicNotes(e.f)
				want := e.said("thread_open_done", i18n.Args{"title": tc.title}) + "\n" + e.publicURL + "/todos/" + todo.ID
				if len(notes) != 1 || notes[0]["text"] != want || notes[0]["chat_id"] != tc.event.Message.ChatID ||
					notes[0]["in_thread"] != true || notes[0]["reply_to"] != tc.event.Message.MessageID {
					t.Errorf("initial topic reply %v, want %q", notes, want)
				}
				if tc.name == "named rich link after media" {
					if saved := e.f.appParams(AppFeishu, FeishuSave); len(saved) != 1 || saved[0]["key"] != "file_example" {
						t.Errorf("original attachment: %+v", saved)
					}
				}
				if h := onlyHandoff(t, e.f); !strings.Contains(h.text, tc.notes) {
					t.Errorf("main conversation lost original input: %q", h.text)
				}
				if database != nil {
					row, err := database.GetTodo(context.Background(), todo.ID)
					if err != nil || row == nil || row.Title != tc.title || row.Notes != tc.notes {
						t.Errorf("stored row %+v, %v", row, err)
					}
				}
			}
			t.Run("memory", func(t *testing.T) { verify(t, nil) })
			t.Run("database", func(t *testing.T) { verify(t, todoTestDB(t)) })
		})
	}
}

func TestTopicTitleRenamesFromTheOriginalLink(t *testing.T) {
	database := todoTestDB(t)
	e := newThreadEnv(t, database)
	e.f.fakeModel(t)
	e.f.mu.Lock()
	e.f.hello.Locale = "en"
	e.f.todoTitleReply = "Review the garden map"
	e.f.mu.Unlock()
	const link = "https://docs.example.com/wiki/garden-map-2026"
	e.deliver(rootEvent("om_shared", link))

	todo := onlyTodo(e.s)
	initial := e.said("thread_open_done", i18n.Args{"title": "New to-do"})
	if notes := topicNotes(e.f); len(notes) != 1 || notes[0]["text"] != initial {
		t.Fatalf("first topic reply %+v, want %q", notes, initial)
	}
	row, err := database.GetTodo(t.Context(), todo.ID)
	if err != nil || row == nil || row.Title != "Review the garden map" || row.Notes != link {
		t.Fatalf("renamed to-do %+v, %v", row, err)
	}
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	if len(e.f.todoTitleAsked) != 1 || !strings.Contains(e.f.todoTitleAsked[0].User, link) {
		t.Fatalf("model did not receive the original URL: %+v", e.f.todoTitleAsked)
	}
}

func TestRequestFromAnotherChatKeepsItsOpeningWords(t *testing.T) {
	for _, home := range []string{askHome, ""} {
		t.Run(home, func(t *testing.T) {
			e := newAskEnv(t, nil)
			e.f.mu.Lock()
			e.home, e.f.hello.Locale = home, "en"
			e.f.mu.Unlock()
			e.ask(ownerAsk("om_request", "/ai Have a look!"))
			todo := onlyTodo(e.s)
			if todo.Title != "Have a look!" || todo.Notes != "Have a look!" || todo.Source == nil {
				t.Fatalf("original external request %+v", todo)
			}
		})
	}
}
