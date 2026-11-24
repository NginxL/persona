package larkcli

import (
	"context"
	"net/url"
	"testing"

	adapter "github.com/nwverse/persona/server/internal/modules/lark/larkcli"
)

func TestInboxSourceAppLink(t *testing.T) {
	withCredentials := (&url.URL{Scheme: "https", Host: "applink.feishu.cn", Path: "/client/chat/open", User: url.User("example")}).String()
	for _, tc := range []struct {
		name, link, topic string
		want              bool
	}{
		{"private message", "https://applink.feishu.cn/client/chat/open?openChatId=oc_example&position=4", "", true},
		{"topic message", "https://applink.feishu.cn/client/thread/open?open_chat_id=oc_example&open_thread_id=omt_example&thread_position=8", "omt_example", true},
		{"Lark", "https://applink.larksuite.com/client/chat/open?openChatId=oc_example&position=4", "", true},
		{"missing", "", "", false},
		{"script", "javascript:alert(1)", "", false},
		{"plain HTTP", "http://applink.feishu.cn/client/chat/open?openChatId=oc_example", "", false},
		{"another host", "https://example.com/client/chat/open?openChatId=oc_example", "", false},
		{"host suffix", "https://applink.feishu.cn.example.com/client/chat/open", "", false},
		{"credentials", withCredentials, "", false},
		{"another target", "https://applink.feishu.cn/client/mini_program/open", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConnector(t, "inbox_bot_loop") // no fresh context: the source link is already on the item
			message := adapter.Message{MessageID: "om_example", ChatID: "oc_example", ChatType: "p2p", ThreadID: tc.topic,
				Sender: adapter.Sender{ID: "ou_example_peer"}, MessageAppLink: tc.link}
			group := &inboxGroup{key: messageKey(message), chatID: message.ChatID, threadID: tc.topic, latest: message, messages: []adapter.Message{message}}
			item := c.thingForGroup(context.Background(), group, "ou_example_user")
			want := ""
			if tc.want {
				want = tc.link
			}
			if item.OpenURL != want {
				t.Fatalf("OpenURL = %q, want %q", item.OpenURL, want)
			}
		})
	}
}

func TestInboxRefreshObtainsSourceAppLinkForOlderCollectedMessage(t *testing.T) {
	c := testConnector(t, "success")
	message := adapter.Message{MessageID: "om_example_newer", ChatID: "oc_example_group", ThreadID: "omt_example_thread",
		Sender: adapter.Sender{ID: "ou_example_peer"}}
	group := &inboxGroup{key: messageKey(message), chatID: message.ChatID, threadID: message.ThreadID, latest: message, messages: []adapter.Message{message}}
	item := c.thingForGroup(context.Background(), group, "ou_example_user")
	want := "https://applink.feishu.cn/client/thread/open?open_chat_id=oc_example_group&open_thread_id=omt_example_thread&thread_position=4"
	if item.OpenURL != want {
		t.Fatalf("fresh source link = %q, want %q", item.OpenURL, want)
	}
	// A different loaded message is never substituted for the original item.
	group.latest.MessageID = "om_example_missing"
	group.messages = []adapter.Message{group.latest}
	if item = c.thingForGroup(context.Background(), group, "ou_example_user"); item.OpenURL != "" {
		t.Fatalf("linked another message: %q", item.OpenURL)
	}
}
