package extmgr

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Asking from any group (bots_topics.go botAIElsewhere, #6146): the owner's "@bot /ai …" in a group that is not opted
// in is handed to the assistant, marked; nothing else from such a group is; and the bot may reply under that message
// and write nothing else in the group. All ids are placeholders.

const askChat = "oc_ask" // a group the bot is in and the owner did not opt in

var atBot = []map[string]any{{"key": "@_user_1", "id": map[string]any{"open_id": "ou_bot"}, "name": "Persona"}}

// askEvent is a message in askChat; mentions says who is @-ed (nil: nobody), msgType the type of the message.
func askEvent(id, from, text string, mentions []map[string]any, extra map[string]any) []byte {
	e := map[string]any{"chat_type": "group", "chat_id": askChat}
	if mentions != nil {
		e["mentions"] = mentions
	}
	for k, v := range extra {
		e[k] = v
	}
	return botMessageEvent(id, from, "text", textContent(text), e)
}

// takenFrom is the last message handed over, and its marker.
func (w *botWorld) takenFrom(t *testing.T) (id, from string) {
	t.Helper()
	got := w.a.got(assistantwire.MethodFeishuMessage)
	if len(got) == 0 {
		t.Fatal("nothing was handed to the assistant")
	}
	var m struct {
		Event struct {
			Message struct {
				ID string `json:"message_id"`
			} `json:"message"`
		} `json:"event"`
		From string `json:"from"`
	}
	if err := json.Unmarshal(got[len(got)-1], &m); err != nil {
		t.Fatal(err)
	}
	return m.Event.Message.ID, m.From
}

// 1. The owner's "@bot /ai …" in a group that is not opted in goes to the assistant marked as from elsewhere, without an
// owner action, is kept as a source and audited (never the words), and the group does not become a chat the bot may send
// into or one that is read.
func TestAIElsewhereOwnerCommandIsHandedMarked(t *testing.T) {
	needsLarkModule(t)
	for _, c := range []struct{ name, text string }{
		{"plain", "@_user_1 /ai 查一下发版说明"},
		{"upper case", "@_user_1 /AI 查一下发版说明"},
		{"full-width slash", "@_user_1 ／ai 查一下发版说明"},
		{"no words", "@_user_1 /ai"},
		{"the @ after the words", "/ai 查一下发版说明 @_user_1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newBotWorld(t)
			w.pair(t)
			w.send(t, askEvent("om_ask1", "ou_me", c.text, atBot, nil))
			eventually(t, 5*time.Second, "handed", func() bool { return len(w.handed()) == 1 })
			id, from := w.takenFrom(t)
			var m assistantwire.FeishuMessage
			_ = json.Unmarshal(w.a.got(assistantwire.MethodFeishuMessage)[0], &m)
			if id != "om_ask1" || from != assistantwire.FeishuFromElsewhere || m.Action != "" {
				t.Fatalf("handed %q from %q with action %q", id, from, m.Action)
			}
			set := w.settings()
			if len(set.AISources) != 1 || set.AISources[0].ChatID != askChat || set.AISources[0].MessageID != "om_ask1" {
				t.Fatalf("sources %+v", set.AISources)
			}
			if botChatAllowed(set, askChat) || set.topicChat(askChat) || len(set.TopicChats) != 0 {
				t.Fatalf("the group became one the bot uses: %+v", set)
			}
			if set.Owner.ChatID != "oc_p2p" {
				t.Fatalf("it moved the owner's private chat to %q", set.Owner.ChatID)
			}
			// Kept with the settings: still a source after a restart.
			w.h.bots.mu.Lock()
			w.h.bots.loaded = false
			w.h.bots.mu.Unlock()
			if _, set := w.h.botAPI(); set.aiSource("om_ask1", time.Now()) == nil {
				t.Fatalf("not kept: %+v", set.AISources)
			}
			// Audited, without the words.
			w.audited(t, auditBotAITaken, func(detail string) {
				if !strings.Contains(detail, askChat) || strings.Contains(detail, "发版") {
					t.Errorf("audit detail %s", detail)
				}
			})
		})
	}
}

// audited finds the audit record action and checks its detail.
func (w *botWorld) audited(t *testing.T, action string, check func(detail string)) {
	t.Helper()
	st := w.h.store.(*botStore)
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, r := range st.audit {
		if r.Action == action {
			d, _ := json.Marshal(r.Detail)
			check(string(d))
			return
		}
	}
	t.Fatalf("no audit record %s", action)
}

// 2. Anything short of "the owner, @-ing the bot, starting with /ai" does nothing and says nothing: no reply, no
// reaction, nothing handed over, nothing kept.
func TestAIElsewhereEverythingElseIsDroppedSilently(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	w.botInfoRead(t)
	total := w.api.total()
	otherAt := []map[string]any{{"key": "@_user_1", "id": map[string]any{"open_id": "ou_someone"}, "name": "Someone"}}
	for _, c := range []struct {
		name     string
		from     string
		text     string
		mentions []map[string]any
		typ      string
	}{
		{"the owner's ordinary message that @s the bot", "ou_me", "@_user_1 帮我看看", atBot, ""},
		{"the owner's ordinary message", "ou_me", "大家好", nil, ""},
		{"someone else's /ai that @s the bot", "ou_stranger", "@_user_1 /ai 查一下", atBot, ""},
		{"the owner's /ai with no @", "ou_me", "/ai 查一下", nil, ""},
		{"the owner's /ai @-ing someone else", "ou_me", "@_user_1 /ai 查一下", otherAt, ""},
		{"the owner's /aim", "ou_me", "@_user_1 /aim 查一下", atBot, ""},
		{"/ai not at the start", "ou_me", "@_user_1 请 /ai 查一下", atBot, ""},
		{"the owner's /ai as a merged forward", "ou_me", "@_user_1 /ai 查一下", atBot, "merge_forward"},
		{"the owner's /ai as a picture", "ou_me", "@_user_1 /ai 查一下", atBot, "image"},
	} {
		typ := c.typ
		if typ == "" {
			typ = "text"
		}
		content := textContent(c.text)
		if typ == "merge_forward" || typ == "image" {
			content = `{}`
		}
		extra := map[string]any{"chat_type": "group", "chat_id": askChat}
		if c.mentions != nil {
			extra["mentions"] = c.mentions
		}
		w.send(t, botMessageEvent("om_x_"+strings.ReplaceAll(c.name, " ", "_"), c.from, typ, content, extra))
	}
	w.quiet(t, total, "om_marker_ask")
	if got := w.handed(); len(got) != 1 {
		t.Fatalf("handed %v: only the marker should have been", got)
	}
	if set := w.settings(); len(set.AISources) != 0 {
		t.Fatalf("sources kept for messages that were dropped: %+v", set.AISources)
	}
}

// 3. The bot's own id is read when start-up did not get it, so its @ is still told from anyone else's.
func TestAIElsewhereReadsTheBotIDWhenStartUpDidNot(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.h.bots.mu.Lock()
	w.h.bots.info.OpenID = ""
	w.h.bots.mu.Unlock()
	w.send(t, askEvent("om_late", "ou_me", "@_user_1 /ai 查", atBot, nil))
	eventually(t, 5*time.Second, "handed", func() bool { return len(w.handed()) == 1 })
	if w.api.count("/open-apis/bot/v3/info") < 1 {
		t.Fatal("the bot's id was not read")
	}
}

// The send rule: the group stays closed to the bot. Only a text reply under the message that was taken, in that chat,
// goes; a send into the group, a reply under any other message in it, a card, a reply that names another chat, and a
// long one are all refused, and a reply whose original is gone is not turned into a message of the group.
func TestAIElsewhereTheGroupStaysClosedExceptForAReplyUnderTheSource(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	w.send(t, askEvent("om_src", "ou_me", "@_user_1 /ai 查一下", atBot, nil))
	eventually(t, 5*time.Second, "taken", func() bool { return len(w.handed()) == 1 })
	w.api.inChat("om_src", askChat)
	w.api.inChat("om_other", askChat)
	card := botcard.NewCard("t", "green", "a").Markdown("a card").JSON()

	// Refused, and nothing reaches Feishu for any of them.
	before := w.api.total()
	for name, c := range map[string]struct {
		method string
		params map[string]any
	}{
		"a send into the group":                    {assistantwire.FeishuText, map[string]any{"text": "你好", "chat_id": askChat}},
		"a send into the group with a reply_to":    {assistantwire.FeishuText, map[string]any{"text": "你好", "chat_id": askChat, "reply_to": ""}},
		"a reply under another message of it":      {assistantwire.FeishuText, map[string]any{"text": "你好", "chat_id": askChat, "reply_to": "om_other"}},
		"a reply under another message, no chat":   {assistantwire.FeishuText, map[string]any{"text": "你好", "reply_to": "om_other"}},
		"a card into the group":                    {assistantwire.FeishuPost, map[string]any{"card": card, "chat_id": askChat}},
		"a card under the source":                  {assistantwire.FeishuPost, map[string]any{"card": card, "chat_id": askChat, "reply_to": "om_src"}},
		"a card under the source, no chat":         {assistantwire.FeishuPost, map[string]any{"card": card, "reply_to": "om_src"}},
		"a reply that names another chat":          {assistantwire.FeishuText, map[string]any{"text": "你好", "chat_id": topicOther, "reply_to": "om_src"}},
		"a reply that names a chat the bot uses":   {assistantwire.FeishuText, map[string]any{"text": "你好", "chat_id": topicChat, "reply_to": "om_src"}},
		"a reply that is not one paragraph (long)": {assistantwire.FeishuText, map[string]any{"text": strings.Repeat("长", botSourceTextMax+1), "chat_id": askChat, "reply_to": "om_src"}},
		"an agent message under the source":        {assistantwire.FeishuCotCreate, map[string]any{"reply_to": "om_src"}},
	} {
		err := w.sendErr(c.method, c.params)
		var re *rpc.Error
		if err == nil || !errors.As(err, &re) || re.Code != assistantwire.CodeChatDenied {
			t.Errorf("%s: %v, want a refusal of the chat", name, err)
		}
	}
	if sent := w.api.since(before); strings.Contains(sent, "POST /open-apis/im/v1/messages") {
		t.Fatalf("something was sent for a refused call:\n%s", sent)
	}
	if botChatAllowed(w.settings(), askChat) {
		t.Fatal("the group is a chat the bot may send into")
	}

	// A text reply under the source goes, in that chat; with or without its chat named.
	for _, p := range []map[string]any{
		{"text": "结论一", "reply_to": "om_src"},
		{"text": "结论二", "reply_to": "om_src", "chat_id": askChat, "uuid": "sr:abc"},
	} {
		if err := w.sendErr(assistantwire.FeishuText, p); err != nil {
			t.Fatalf("a reply under the source: %v", err)
		}
	}
	w.api.find(t, "POST /open-apis/im/v1/messages/om_src/reply", "结论一")
	w.api.find(t, "POST /open-apis/im/v1/messages/om_src/reply", "结论二", `"uuid":"sr:abc"`)

	// The original gone: the reply is lost, not sent into the group in place of it.
	w.send(t, askEvent("om_src_gone", "ou_me", "@_user_1 /ai 另一件", atBot, nil)) // a source of its own: om_src has had its replies
	eventually(t, 5*time.Second, "taken", func() bool { return len(w.handed()) == 2 })
	w.api.mu.Lock()
	w.api.rejectReply = 230011
	w.api.mu.Unlock()
	before = w.api.total()
	if err := w.sendErr(assistantwire.FeishuText, map[string]any{"text": "结论三", "reply_to": "om_src_gone", "chat_id": askChat}); !botcard.MessageGone(err) {
		t.Fatalf("a reply whose original is gone: %v", err)
	}
	if sent := w.api.since(before); strings.Contains(sent, "receive_id_type=chat_id") || strings.Contains(sent, askChat) {
		t.Fatalf("it was sent into the group instead:\n%s", sent)
	}
}

// The hint a group gets when nothing is opted in, the reaction and the like are the assistant's replies; here only that
// a source is bounded and that it goes with the owner.
func TestAISourcesAreBoundedAndGoWithTheOwner(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	for i := range botAISourcesKeep + 5 {
		w.h.takeAISource(askChat, fmt.Sprintf("om_s%d", i), "")
	}
	set := w.settings()
	if len(set.AISources) != botAISourcesKeep || set.aiSource("om_s0", time.Now()) != nil || set.aiSource(fmt.Sprintf("om_s%d", botAISourcesKeep+4), time.Now()) == nil {
		t.Fatalf("%d sources kept; first one still there: %v", len(set.AISources), set.aiSource("om_s0", time.Now()) != nil)
	}
	w.h.takeAISource(askChat, fmt.Sprintf("om_s%d", botAISourcesKeep+4), "") // again: nothing changes
	if got := len(w.settings().AISources); got != botAISourcesKeep {
		t.Fatalf("%d after taking one twice", got)
	}
	// Old ones go.
	w.h.bots.mu.Lock()
	w.h.bots.set.AISources[0].At = time.Now().Add(-botAISourcesKeepFor - time.Hour)
	w.h.bots.mu.Unlock()
	w.h.takeAISource(askChat, "om_new", "")
	if set := w.settings(); set.aiSource("om_new", time.Now()) == nil || len(set.AISources) != botAISourcesKeep {
		t.Fatalf("old sources were not dropped: %d", len(set.AISources))
	}
	if code, _ := w.do(t, "DELETE", "/api/v1/bots/feishu/owner", ""); code != 200 {
		t.Fatalf("unpair: %d", code)
	}
	var saved FeishuBotSettings
	_, _ = w.h.store.Setting(w.ctx, botSettingsKey, &saved)
	if len(w.settings().AISources) != 0 || len(saved.AISources) != 0 {
		t.Fatalf("unpaired, the sources stay: %d / %d", len(w.settings().AISources), len(saved.AISources))
	}
	// And the settings can never carry a reply rule for a source with no owner.
	if _, found, _ := aiSourceReply(FeishuBotSettings{AISources: []BotAISource{{ChatID: askChat, MessageID: "om_a"}}}, "", "om_a", "x", time.Now(), false); found {
		t.Fatal("a source counts with no owner")
	}
}

// The home topic group: the one marked, else the first opted in; /topics home marks (opting in when need be) and moves
// it; /topics off takes the mark with the group; the app's list keeps it unless it says otherwise; two homes are refused.
func TestHomeTopicGroup(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	if got := w.settings().homeChat(); got != "" {
		t.Fatalf("home with nothing on: %q", got)
	}
	w.send(t, groupEvent("om_h1", "ou_me", "/topics", topicChat, ""))
	w.api.find(t, "/messages/om_h1/reply", "已开启")
	w.send(t, groupEvent("om_h2", "ou_me", "/topics", topicOther, ""))
	w.api.find(t, "/messages/om_h2/reply", "已开启")
	if got := w.settings().homeChat(); got != topicChat {
		t.Fatalf("home = %q, want the first opted in", got)
	}
	// /topics home in a group that is not on: on, and home.
	w.send(t, groupEvent("om_h3", "ou_me", "/topics home", askChat, ""))
	w.api.find(t, "/messages/om_h3/reply", "主话题群")
	set := w.settings()
	if set.homeChat() != askChat || !set.topicChat(askChat) || len(set.TopicChats) != 3 {
		t.Fatalf("after /topics home in a new group: home %q, %+v", set.homeChat(), set.TopicChats)
	}
	// In one that is on: moved, and said again when it is home already. Stranger's is nothing.
	w.send(t, groupEvent("om_h4", "ou_stranger", "/topics home", topicOther, ""))
	w.send(t, groupEvent("om_h5", "ou_me", "/话题群 主群", topicOther, ""))
	w.api.find(t, "/messages/om_h5/reply", "主话题群")
	if got := w.settings().homeChat(); got != topicOther {
		t.Fatalf("home = %q after the move", got)
	}
	w.send(t, groupEvent("om_h6", "ou_me", "/topics home", topicOther, ""))
	w.api.find(t, "/messages/om_h6/reply", "已经是主话题群")
	marked := 0
	for _, c := range w.settings().TopicChats {
		if c.Home {
			marked++
		}
	}
	if marked != 1 {
		t.Fatalf("%d groups marked home: %+v", marked, w.settings().TopicChats)
	}
	// The view says which is home; the status the assistant reads says so too.
	_, view := w.do(t, "GET", "/api/v1/bots/feishu", "")
	homes := 0
	for _, c := range view["topic_chats"].([]any) {
		if c.(map[string]any)["home"] == true {
			homes++
			if c.(map[string]any)["chat_id"] != topicOther {
				t.Fatalf("the view marks %v", c)
			}
		}
	}
	if homes != 1 {
		t.Fatalf("the view marks %d homes: %v", homes, view["topic_chats"])
	}
	if st := w.feishu(t, assistantwire.FeishuStatusGet, nil); st["home_chat"] != topicOther {
		t.Fatalf("status home_chat %v", st["home_chat"])
	}
	// Off takes the mark with the group: the first one left is home.
	w.send(t, groupEvent("om_h7", "ou_me", "/topics off", topicOther, ""))
	w.api.find(t, "/messages/om_h7/reply", "已关闭")
	if got := w.settings().homeChat(); got != topicChat {
		t.Fatalf("home = %q after the home group went", got)
	}
	// The app's list: with no word about home the mark stays; with one it is as said; two are refused whole.
	w.send(t, groupEvent("om_h8", "ou_me", "/topics home", askChat, ""))
	w.api.find(t, "/messages/om_h8/reply", "已设为主话题群")
	if code, out := w.do(t, "PUT", "/api/v1/bots/feishu", fmt.Sprintf(`{"topic_chats":[{"chat_id":%q},{"chat_id":%q,"name":"x"}]}`, topicChat, askChat)); code != 200 {
		t.Fatalf("put: %d %v", code, out)
	}
	if got := w.settings().homeChat(); got != askChat {
		t.Fatalf("a list that says nothing of home moved it to %q", got)
	}
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", fmt.Sprintf(`{"topic_chats":[{"chat_id":%q,"home":true},{"chat_id":%q}]}`, topicChat, askChat)); code != 200 {
		t.Fatalf("put with home")
	}
	if got := w.settings().homeChat(); got != topicChat {
		t.Fatalf("home = %q, want the one the list marks", got)
	}
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", fmt.Sprintf(`{"topic_chats":[{"chat_id":%q,"home":true},{"chat_id":%q,"home":true}]}`, topicChat, askChat)); code != 400 {
		t.Fatalf("two homes: %d, want 400", code)
	}
}

// A rich-text "@bot /ai …" (a post message: the @ is a node of its own) is taken like a text one.
func TestAIElsewhereTakesARichTextCommand(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	post := `{"title":"","content":[[{"tag":"at","user_id":"@_user_1","user_name":"Persona"},{"tag":"text","text":" /ai 查一下发版说明"}]]}`
	w.send(t, botMessageEvent("om_post1", "ou_me", "post", post, map[string]any{"chat_type": "group", "chat_id": askChat, "mentions": atBot}))
	eventually(t, 5*time.Second, "handed", func() bool { return len(w.handed()) == 1 })
	if id, from := w.takenFrom(t); id != "om_post1" || from != assistantwire.FeishuFromElsewhere {
		t.Fatalf("handed %q from %q", id, from)
	}
	// The same without the command: nothing.
	other := `{"title":"","content":[[{"tag":"at","user_id":"@_user_1","user_name":"Persona"},{"tag":"text","text":" 帮我看看"}]]}`
	total := w.api.total()
	w.send(t, botMessageEvent("om_post2", "ou_me", "post", other, map[string]any{"chat_type": "group", "chat_id": askChat, "mentions": atBot}))
	time.Sleep(200 * time.Millisecond)
	if len(w.handed()) != 1 || w.api.total() != total {
		t.Fatalf("handed %v, calls %d (was %d)", w.handed(), w.api.total(), total)
	}
}

// What is not an "/ai" is not asked about the bot at all: a group's ordinary messages cost no call to Feishu.
func TestAIElsewhereAsksNothingForOrdinaryMessages(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.h.bots.mu.Lock()
	w.h.bots.info.OpenID = ""
	w.h.bots.mu.Unlock()
	time.Sleep(300 * time.Millisecond) // the bot's own start-up calls are done
	total, infos := w.api.total(), w.api.count("/open-apis/bot/v3/info")
	w.send(t, askEvent("om_chat1", "ou_me", "@_user_1 帮我看看", atBot, nil))
	w.send(t, askEvent("om_chat2", "ou_me", "大家早", nil, nil))
	time.Sleep(300 * time.Millisecond)
	if n := w.api.count("/open-apis/bot/v3/info"); n != infos {
		t.Fatalf("%d reads of the bot's id for messages that are not an /ai", n-infos)
	}
	if w.api.total() != total {
		t.Fatalf("calls: %s", w.api.since(total))
	}
}

// A new App ID (or any restart of the connection) forgets the bot's own id: the old one must not tell the new bot's @.
func TestBotRestartForgetsItsOwnID(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.h.bots.mu.Lock()
	w.h.bots.info.OpenID = "ou_stale"
	w.h.bots.mu.Unlock()
	w.api.mu.Lock()
	w.api.botInfoFail = true
	w.api.mu.Unlock()
	before := w.api.count("/open-apis/bot/v3/info")
	w.h.restartBot()
	eventually(t, 5*time.Second, "the id read again", func() bool { return w.api.count("/open-apis/bot/v3/info") > before })
	w.h.bots.mu.Lock()
	got := w.h.bots.info.OpenID
	w.h.bots.mu.Unlock()
	if got != "" {
		t.Fatalf("the bot's id after a restart: %q", got)
	}
	// With the id unknown (and unreadable) nothing is taken, even for an @ of the old id.
	w.send(t, askEvent("om_stale", "ou_me", "@_user_1 /ai 查", []map[string]any{{"key": "@_user_1", "id": map[string]any{"open_id": "ou_stale"}, "name": "Old"}}, nil))
	time.Sleep(300 * time.Millisecond)
	if len(w.handed()) != 0 {
		t.Fatalf("handed %v", w.handed())
	}
}

// A source takes at most botSourceReplies replies, however they end, and no reply once it is older than botAISourcesKeepFor
// (the usual rules refuse it then); the count is kept with the settings.
func TestAISourceReplyCapAndExpiry(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.send(t, askEvent("om_cap", "ou_me", "@_user_1 /ai 查一下", atBot, nil))
	eventually(t, 5*time.Second, "taken", func() bool { return len(w.handed()) == 1 })
	w.api.inChat("om_cap", askChat)
	reply := func(text string) error {
		return w.sendErr(assistantwire.FeishuText, map[string]any{"text": text, "reply_to": "om_cap", "chat_id": askChat})
	}
	for i := 1; i <= botSourceReplies; i++ {
		if err := reply(fmt.Sprintf("第%d句", i)); err != nil {
			t.Fatalf("reply %d: %v", i, err)
		}
	}
	posts := w.api.posts()
	if err := reply("第三句"); !errors.Is(err, errBotSourceReply) {
		t.Fatalf("a reply past the cap: %v", err)
	}
	if w.api.posts() != posts {
		t.Fatal("a reply past the cap was sent")
	}
	// Kept: after a reload the source has used them.
	w.h.bots.mu.Lock()
	w.h.bots.loaded = false
	w.h.bots.mu.Unlock()
	if _, set := w.h.botAPI(); set.aiSource("om_cap", time.Now()).Replies != botSourceReplies {
		t.Fatalf("replies not kept: %+v", set.AISources)
	}
	if err := reply("重启后"); !errors.Is(err, errBotSourceReply) {
		t.Fatalf("after a reload: %v", err)
	}
	// Old: no longer a source.
	w.send(t, askEvent("om_old", "ou_me", "@_user_1 /ai 再查", atBot, nil))
	eventually(t, 5*time.Second, "taken", func() bool { return len(w.handed()) == 2 })
	w.api.inChat("om_old", askChat)
	w.h.bots.mu.Lock()
	for i := range w.h.bots.set.AISources {
		if w.h.bots.set.AISources[i].MessageID == "om_old" {
			w.h.bots.set.AISources[i].At = time.Now().Add(-botAISourcesKeepFor - time.Hour)
		}
	}
	w.h.bots.mu.Unlock()
	before := w.api.total()
	err := w.sendErr(assistantwire.FeishuText, map[string]any{"text": "太晚了", "reply_to": "om_old", "chat_id": askChat})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != assistantwire.CodeChatDenied || strings.Contains(w.api.since(before), "太晚了") {
		t.Fatalf("a reply under an expired source: %v", err)
	}
}

// An emoji goes only on a message of the owner's private chat, of a group opted in, or one an "/ai" was taken from; on
// a message of any other chat it is refused (and so is taking one away), and nothing reaches Feishu for it.
func TestReactionsOnlyWhereTheBotMayWrite(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	w.send(t, askEvent("om_src_react", "ou_me", "@_user_1 /ai 查", atBot, nil))
	eventually(t, 5*time.Second, "taken", func() bool { return len(w.handed()) == 1 })
	w.send(t, botMessageEvent("om_dm_react", "ou_me", "text", textContent("你好"), nil))
	w.send(t, groupEvent("om_topic_react", "ou_me", "在", topicChat, "omt_1"))
	eventually(t, 5*time.Second, "handed", func() bool { return len(w.handed()) == 3 })
	// Messages the bot does not know are asked about.
	w.api.inChat("om_foreign", askChat)
	w.api.inChat("om_foreign_topicless", topicOther)

	for _, id := range []string{"om_src_react", "om_dm_react", "om_topic_react"} {
		before := w.api.total()
		if err := w.sendErr(assistantwire.FeishuReact, map[string]any{"message_id": id, "emoji": "OnIt"}); err != nil {
			t.Fatalf("react on %s: %v", id, err)
		}
		w.api.find(t, "/messages/"+id+"/reactions", "OnIt")
		if err := w.sendErr(assistantwire.FeishuUnreact, map[string]any{"message_id": id, "reaction_id": "r1"}); err != nil {
			t.Fatalf("unreact on %s: %v", id, err)
		}
		_ = before
	}
	before := w.api.total()
	for _, id := range []string{"om_foreign", "om_foreign_topicless"} {
		for method, p := range map[string]map[string]any{
			assistantwire.FeishuReact:   {"message_id": id, "emoji": "OnIt"},
			assistantwire.FeishuUnreact: {"message_id": id, "reaction_id": "r1"},
		} {
			var re *rpc.Error
			if err := w.sendErr(method, p); !errors.As(err, &re) || re.Code != assistantwire.CodeChatDenied {
				t.Fatalf("%s on %s: %v, want a refusal of the chat", method, id, err)
			}
		}
	}
	if strings.Contains(w.api.since(before), "/reactions") {
		t.Fatalf("a reaction reached Feishu:\n%s", w.api.since(before))
	}
	if err := w.sendErr(assistantwire.FeishuReact, map[string]any{"emoji": "OnIt"}); err == nil {
		t.Fatal("a reaction with no message")
	}
}

func TestSourceAnswerTargetsOnlyTheOriginalRequest(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.send(t, askEvent("om_answer_source", "ou_me", "@_user_1 /ai Check the release notes", atBot, nil))
	eventually(t, 5*time.Second, "taken", func() bool { return len(w.handed()) == 1 })
	text := "Final answer\n\n- Item one\n- Item two"
	params := map[string]any{"chat_id": askChat, "reply_to": "om_answer_source", "text": text, "full_answer": true, "uuid": "sr:answer"}
	before := w.api.total()
	assertDenied := func(p map[string]any) {
		t.Helper()
		var re *rpc.Error
		if err := w.sendErr(assistantwire.FeishuText, p); !errors.As(err, &re) || re.Code != assistantwire.CodeChatDenied {
			t.Fatalf("expected a refused send, got %v", err)
		}
	}
	assertDenied(params)
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"source_report_full_reply":true}`); code != 200 {
		t.Fatal(code)
	}
	assertDenied(map[string]any{"chat_id": askChat, "text": text, "full_answer": true})
	assertDenied(map[string]any{"chat_id": askChat, "reply_to": "om_other", "text": text, "full_answer": true})
	assertDenied(map[string]any{"chat_id": topicOther, "reply_to": "om_answer_source", "text": text, "full_answer": true})
	if sent := w.api.since(before); strings.Contains(sent, "POST /open-apis/im/v1/messages") {
		t.Fatalf("sent an unauthorized answer: %s", sent)
	}
	if err := w.sendErr(assistantwire.FeishuText, params); err != nil {
		t.Fatal(err)
	}
	w.api.find(t, "POST /open-apis/im/v1/messages/om_answer_source/reply", `"msg_type":"post"`, `"uuid":"sr:answer"`, "- Item two")
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"source_report_full_reply":false}`); code != 200 {
		t.Fatal(code)
	}
	assertDenied(params)
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"source_report_full_reply":true}`); code != 200 {
		t.Fatal(code)
	}

	w.api.mu.Lock()
	w.api.rejectReply = 230011 // the original was withdrawn
	w.api.mu.Unlock()
	before = w.api.total()
	if err := w.sendErr(assistantwire.FeishuText, params); !botcard.MessageGone(err) {
		t.Fatalf("expected a missing parent error, got %v", err)
	}
	if sent := w.api.since(before); strings.Contains(sent, "receive_id_type=chat_id") {
		t.Fatalf("the full answer detoured to the group: %s", sent)
	}
}
