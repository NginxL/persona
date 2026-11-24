package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/proto"
)

// quoteHub is a paired bot whose owner replies to om_pic, a picture they sent before; resource answers the download.
func quoteHub(t *testing.T, resource appAnswer) (*fakeHub, *Service) {
	t.Helper()
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.setSessions("server", proto.Session{SessionID: "m1", Assistant: true})
	f.answerApp(AppFeishu, FeishuReact, func(json.RawMessage) (any, error) { return map[string]any{"reaction_id": "r1"}, nil })
	f.answerApp(AppFeishu, FeishuMessages, func(json.RawMessage) (any, error) {
		return map[string]any{"items": []map[string]any{{"message_id": "om_pic", "msg_type": "image",
			"sender": map[string]any{"id": "ou_a", "sender_type": "user"}, "body": map[string]any{"content": `{"image_key":"img_q"}`}}}}, nil
	})
	f.answerApp(AppFeishu, FeishuUserName, func(json.RawMessage) (any, error) { return map[string]any{"name": "我"}, nil })
	f.answerApp(AppFeishu, FeishuResource, resource)
	return f, newTestService(t, f)
}

func quoteReply() *botcard.MessageEvent {
	var ev botcard.MessageEvent
	ev.Message.MessageID, ev.Message.ParentID, ev.Message.RootID = "om_ask", "om_pic", "om_pic"
	ev.Message.ChatType, ev.Message.MessageType, ev.Message.Content = "p2p", "text", `{"text":"这张图里写的什么？"}`
	return &ev
}

// sentImages are the images of the conversation's session.send_once call.
func sentImages(f *fakeHub) []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.Method == DeviceSessionSendOnce {
			var p map[string]any
			_ = json.Unmarshal(c.Params, &p)
			imgs, _ := p["images"].([]any)
			return imgs
		}
	}
	return nil
}

// Replying to a picture to ask about it: the picture goes to the model with the question, downloaded with the
// message it is in, not only the "[image]" line in the quote.
func TestFeishuReplyToAPictureCarriesThePicture(t *testing.T) {
	var asked []string
	f, s := quoteHub(t, func(p json.RawMessage) (any, error) {
		var a map[string]any
		_ = json.Unmarshal(p, &a)
		asked = append(asked, a["message_id"].(string)+"/"+a["key"].(string))
		return map[string]any{"data": []byte("png"), "mime": "image/png"}, nil
	})
	ev := quoteReply()
	content := botcard.ParseContent(ev.Message.MessageType, ev.Message.Content, nil)
	if err := s.botSend(context.Background(), ev, content, botTarget{Kind: targetMain}); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != "om_pic/img_q" {
		t.Fatalf("downloads %v", asked)
	}
	if imgs := sentImages(f); len(imgs) != 1 {
		t.Fatalf("images sent %v", imgs)
	}
	if text := sendOnceText(f); !strings.Contains(text, "引用的消息") || !strings.Contains(text, "这张图里写的什么？") {
		t.Fatalf("text %q", text)
	}
}

// A quoted picture that can't be had does not hold the question back: it goes with the quote's words.
func TestFeishuQuotedPictureGoneStillSends(t *testing.T) {
	f, s := quoteHub(t, func(json.RawMessage) (any, error) { return nil, errors.New("resource gone") })
	ev := quoteReply()
	content := botcard.ParseContent(ev.Message.MessageType, ev.Message.Content, nil)
	if err := s.botSend(context.Background(), ev, content, botTarget{Kind: targetMain}); err != nil {
		t.Fatalf("held back: %v", err)
	}
	if text := sendOnceText(f); !strings.Contains(text, "这张图里写的什么？") {
		t.Fatalf("text %q", text)
	}
	if imgs := sentImages(f); len(imgs) != 0 {
		t.Fatalf("images %v", imgs)
	}
}

// A quote reply to a notify_user copy returns to the conversation that made the notification and carries the copied
// card's identity and text. Two nearby notices make a default-conversation fallback visible.
func TestFeishuQuoteReplyToOwnNotificationKeepsSourceAndQuote(t *testing.T) {
	e := newThreadEnv(t, nil)
	e.features.Pushes = true
	e.s.setBotCurrent(botTarget{Kind: targetPersona, MachineID: "server", SessionID: "m1"})
	ids := []string{"om_notice_main", "om_notice_work"}
	e.f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) {
		if len(ids) == 0 {
			return nil, errors.New("unexpected extra notification card")
		}
		id := ids[0]
		ids = ids[1:]
		return map[string]any{"message_id": id}, nil
	})
	if err := e.s.feishuPushTold(context.Background(), "server", "m1", "First notification", "First original body", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.s.feishuPushTold(context.Background(), "server", "c1x1x1", "Second notification", "Second original body", "", nil); err != nil {
		t.Fatal(err)
	}
	target := e.s.botTargetFor("", "om_notice_work")
	if target.Kind != targetPersona || target.MachineID != "server" || target.SessionID != "c1x1x1" {
		t.Fatalf("notification reply target %+v", target)
	}
	e.f.answerApp(AppFeishu, FeishuMessages, func(json.RawMessage) (any, error) {
		return map[string]any{"items": []map[string]any{{
			"message_id": "om_notice_work", "msg_type": "interactive", "sender": map[string]any{"sender_type": "app"},
			"body": map[string]any{"content": `{"schema":"2.0","header":{"title":{"tag":"plain_text","content":"Second notification"}},"body":{"elements":[{"tag":"markdown","content":"Second original body"}]}}`},
		}}}, nil
	})
	ev := privateEvent("om_notice_answer", "Which notice is this?")
	ev.Message.ParentID, ev.Message.RootID = "om_notice_work", "om_notice_work"
	e.deliver(ev)
	h := onlyHandoff(t, e.f)
	if h.machine != "server" || h.session != "c1x1x1" {
		t.Fatalf("reply went to %+v", h)
	}
	for _, want := range []string{"om_notice_work", "Second notification", "Second original body", "Which notice is this?"} {
		if !strings.Contains(h.text, want) {
			t.Errorf("reply lacks %q:\n%s", want, h.text)
		}
	}
}

// An ordinary private reply does not read or invent a quote.
func TestFeishuPlainPrivateReplySkipsQuoteLookup(t *testing.T) {
	e := newThreadEnv(t, nil)
	e.deliver(privateEvent("om_plain", "Plain private words"))
	h := onlyHandoff(t, e.f)
	if h.machine != "server" || h.session != "m1" || !strings.Contains(h.text, "Plain private words") || strings.Contains(h.text, "引用的消息") {
		t.Fatalf("plain reply changed: %+v", h)
	}
	if got := e.f.appParams(AppFeishu, FeishuMessages); len(got) != 0 {
		t.Fatalf("plain reply read a quote: %v", got)
	}
}

// An unknown source keeps the existing current-conversation fallback, but the quoted message still has its ID.
func TestFeishuQuoteReplyUnknownSourceKeepsFallbackAndQuoteID(t *testing.T) {
	e := newThreadEnv(t, nil)
	e.s.setBotCurrent(botTarget{Kind: targetPersona, MachineID: "server", SessionID: "c1x1x1"})
	e.f.answerApp(AppFeishu, FeishuMessages, func(json.RawMessage) (any, error) {
		return map[string]any{"items": []map[string]any{{
			"message_id": "om_unknown", "msg_type": "text", "sender": map[string]any{"id": "ou_a", "sender_type": "user"},
			"body": map[string]any{"content": `{"text":"Original external words"}`},
		}}}, nil
	})
	ev := privateEvent("om_unknown_answer", "Answer the quote")
	ev.Message.ParentID, ev.Message.RootID = "om_unknown", "om_unknown"
	e.deliver(ev)
	h := onlyHandoff(t, e.f)
	if h.machine != "server" || h.session != "c1x1x1" {
		t.Fatalf("reply went to %+v", h)
	}
	for _, want := range []string{"om_unknown", "Original external words", "Answer the quote"} {
		if !strings.Contains(h.text, want) {
			t.Errorf("reply lacks %q:\n%s", want, h.text)
		}
	}
}

// A quoted or forwarded message read back carries its own mentions: an @ in it reaches the model as the person's name,
// not as the message's @_user_N key.
func TestFeishuQuoteAndForwardNameTheirMentions(t *testing.T) {
	mentions := []map[string]any{{"key": "@_user_1", "name": "Alice"}}
	t.Run("quote", func(t *testing.T) {
		f, s := quoteHub(t, func(json.RawMessage) (any, error) { return nil, errors.New("no media") })
		f.answerApp(AppFeishu, FeishuMessages, func(json.RawMessage) (any, error) {
			return map[string]any{"items": []map[string]any{{"message_id": "om_pic", "msg_type": "text", "mentions": mentions,
				"sender": map[string]any{"id": "ou_a", "sender_type": "user"}, "body": map[string]any{"content": `{"text":"@_user_1 please check"}`}}}}, nil
		})
		ev := quoteReply()
		content := botcard.ParseContent(ev.Message.MessageType, ev.Message.Content, nil)
		if err := s.botSend(context.Background(), ev, content, botTarget{Kind: targetMain}); err != nil {
			t.Fatal(err)
		}
		if text := sendOnceText(f); !strings.Contains(text, "@Alice please check") || strings.Contains(text, "@_user_1") {
			t.Fatalf("text %q", text)
		}
	})
	t.Run("forward", func(t *testing.T) {
		f, s := quoteHub(t, func(json.RawMessage) (any, error) { return nil, errors.New("no media") })
		f.answerApp(AppFeishu, FeishuMessages, func(json.RawMessage) (any, error) {
			return map[string]any{"items": []map[string]any{
				{"message_id": "om_fwd", "msg_type": "merge_forward", "body": map[string]any{"content": "Merged and Forwarded Message"}},
				{"message_id": "om_in", "upper_message_id": "om_fwd", "msg_type": "text", "create_time": "100000", "mentions": mentions,
					"sender": map[string]any{"id": "ou_a", "sender_type": "user"}, "body": map[string]any{"content": `{"text":"@_user_1 please check"}`}},
			}}, nil
		})
		var ev botcard.MessageEvent
		ev.Message.MessageID, ev.Message.ChatType, ev.Message.MessageType = "om_fwd", "p2p", "merge_forward"
		ev.Message.Content = `{"content":"Merged and Forwarded Message"}`
		content := botcard.ParseContent(ev.Message.MessageType, ev.Message.Content, nil)
		if err := s.botSend(context.Background(), &ev, content, botTarget{Kind: targetMain}); err != nil {
			t.Fatal(err)
		}
		if text := sendOnceText(f); !strings.Contains(text, "@Alice please check") || strings.Contains(text, "@_user_1") {
			t.Fatalf("text %q", text)
		}
	})
}
