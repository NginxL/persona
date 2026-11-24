package extmgr

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// AC1: the transport creates the native agent message under the origin, in the same opted-in chat and topic.
func TestCotCreateRepliesInOriginTopic(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	w.send(t, groupEvent("om_cot_native_topic", "ou_me", "在", topicChat, "omt_1"))
	eventually(t, 5*time.Second, "origin handed to the assistant", func() bool { return len(w.handed()) == 1 })

	if err := w.sendErr(assistantwire.FeishuCotCreate, map[string]any{"reply_to": "om_cot_native_topic", "chat_id": topicChat, "in_thread": true}); err != nil {
		t.Fatalf("create a native agent message in the origin topic: %v", err)
	}
	w.api.find(t, "POST /open-apis/im/v1/message_cot", `"receive_id":"`+topicChat+`"`, `"origin_message_id":"om_cot_native_topic"`, `"reply_in_thread":true`)
}

func topicCotBotFeatures(t *testing.T, w *botWorld) map[string]any {
	t.Helper()
	code, out := w.do(t, "GET", "/api/v1/bots/feishu", "")
	if code != 200 {
		t.Fatalf("GET bot settings: %d, %v", code, out)
	}
	features, _ := out["features"].(map[string]any)
	return features
}

// AC5: the existing bot-settings API exposes the behavior-changing switch as on by default.
func TestBotTopicCotSettingDefaultsOn(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	if features := topicCotBotFeatures(t, w); features["topic_cot"] != true {
		t.Fatalf("default bot features = %v, want topic_cot on", features)
	}
}

// AC5: the setting reverses through the existing API without disturbing the other feature switches.
func TestBotTopicCotSettingRoundTrip(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	for _, tc := range []struct {
		body string
		want bool
	}{{`{"features":{"topic_cot":false}}`, false}, {`{"features":{"topic_cot":true}}`, true}} {
		if code, out := w.do(t, "PUT", "/api/v1/bots/feishu", tc.body); code != 200 {
			t.Fatalf("PUT %s: %d, %v", tc.body, code, out)
		}
		features := topicCotBotFeatures(t, w)
		if got, ok := features["topic_cot"].(bool); !ok || got != tc.want || features["main"] != true {
			t.Fatalf("after PUT %s: features = %v, want topic_cot %v and main unchanged", tc.body, features, tc.want)
		}
	}
}

// AC5: Settings' existing channel metadata exposes a labelled switch, not a hidden/chat-only control.
func TestChannelListIncludesTopicCotSetting(t *testing.T) {
	needsLarkModule(t)
	w := newChannelWorld(t)
	out, err := w.channels(t, assistantwire.ChannelsList, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range out["channels"].([]any) {
		m := c.(map[string]any)
		if m["id"] != "feishu" {
			continue
		}
		for _, f := range m["features"].([]any) {
			fm := f.(map[string]any)
			if fm["id"] == "topic_cot" {
				if fm["on"] != true || fm["title"] == "" || fm["description"] == "" {
					t.Fatalf("topic_cot setting = %v, want visible, labelled and on", fm)
				}
				return
			}
		}
		t.Fatalf("no topic_cot setting in Lark channel features: %v", m["features"])
	}
	t.Fatal("no Lark bot channel")
}
