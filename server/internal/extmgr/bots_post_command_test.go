package extmgr

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The event content follows the documented received post shape, not the language-keyed send shape.
func TestPostAdjacentMentionCommandHandoff(t *testing.T) {
	needsLarkModule(t)
	for _, command := range []string{"/read", "/ai"} {
		t.Run(command, func(t *testing.T) {
			w := newBotWorld(t)
			w.pair(t)
			w.topicsOn(t, topicChat)
			w.readOn(t)
			content := `{"title":"","content":[[{"tag":"at","user_id":"@_user_1","user_name":""},{"tag":"text","text":"` + command + ` summarize"}]]}`
			w.send(t, botMessageEvent("om_adjacent", "ou_me", "post", content, map[string]any{"chat_type": "group", "chat_id": askChat, "mentions": atBot}))
			w.send(t, groupEvent("om_barrier", "ou_me", "marker", topicChat, "omt_marker"))
			eventually(t, 5*time.Second, "barrier handed", func() bool { got := w.handed(); return len(got) > 0 && got[len(got)-1] == "om_barrier" })
			got := w.handed()
			if len(got) != 2 || got[0] != "om_adjacent" {
				t.Fatalf("handoffs %v; want [om_adjacent om_barrier]", got)
			}
			expected := assistantwire.FeishuFromElsewhere
			if command == "/read" {
				expected = assistantwire.FeishuFromRead
			}
			messages := w.a.got(assistantwire.MethodFeishuMessage)
			if !containsFrom(messages[0], expected) {
				t.Fatalf("handoff %s; want from %q", messages[0], expected)
			}
		})
	}
}

func containsFrom(raw json.RawMessage, want string) bool {
	var message assistantwire.FeishuMessage
	_ = json.Unmarshal(raw, &message)
	return message.From == want
}

func TestPostCommandAdmissionPolicies(t *testing.T) {
	needsLarkModule(t)
	otherAt := []map[string]any{{"key": "@_user_10", "name": "Persona", "id": map[string]any{"open_id": "ou_other"}}}
	bothAt := append(append([]map[string]any{}, otherAt...), atBot...)
	for _, command := range []string{"/read", "/ai"} {
		for _, tc := range []struct {
			name, prefix, words, sender, chat, thread, senderType string
			mentions                                              []map[string]any
			readOff, accepted, ordinaryTopic, plain               bool
		}{
			{name: "plain text spaced", words: "@_user_1 " + command + " summarize", mentions: atBot, accepted: true, plain: true},
			{name: "plain text adjacent", words: "@_user_1" + command + " summarize", mentions: atBot, plain: true},
			{name: "spaced", prefix: `{"tag":"at","user_id":"@_user_1"},`, words: " " + command + " summarize", mentions: atBot, accepted: true},
			{name: "other sender", prefix: `{"tag":"at","user_id":"@_user_1"},`, words: command + " summarize", sender: "ou_stranger", mentions: atBot},
			{name: "app sender", prefix: `{"tag":"at","user_id":"@_user_1"},`, words: command + " summarize", senderType: "app", mentions: atBot},
			{name: "same name other ID", prefix: `{"tag":"at","user_id":"@_user_10"},`, words: command + " summarize", mentions: otherAt},
			{name: "same name with actual bot later", prefix: `{"tag":"at","user_id":"@_user_10"},`, words: command + " summarize", mentions: bothAt},
			{name: "literal name plus real mention elsewhere", words: "@Persona " + command + " summarize", mentions: atBot},
			{name: "no bot mention", words: command + " summarize"},
			{name: "ordinary words", prefix: `{"tag":"at","user_id":"@_user_1"},`, words: "please " + command + " summarize", mentions: atBot},
			{name: "command lookalike", prefix: `{"tag":"at","user_id":"@_user_1"},`, words: command + "more summarize", mentions: atBot},
			{name: "read toggle off", prefix: `{"tag":"at","user_id":"@_user_1"},`, words: command + " summarize", mentions: atBot, readOff: true, accepted: command == "/ai"},
			{name: "strict topic allowed", prefix: `{"tag":"at","user_id":"@_user_1"},`, words: command + " summarize", mentions: atBot, chat: "oc_strict", thread: "omt_post", accepted: true, ordinaryTopic: command == "/ai"},
			{name: "strict topic denied", prefix: `{"tag":"at","user_id":"@_user_10"},`, words: command + " summarize", mentions: otherAt, chat: "oc_strict", thread: "omt_post"},
		} {
			t.Run(command+"/"+tc.name, func(t *testing.T) {
				w := newBotWorld(t)
				w.pair(t)
				w.topicsOn(t, topicChat)
				if !tc.readOff {
					w.readOn(t)
				}
				if tc.chat == "oc_strict" {
					if code, out := w.do(t, "PUT", "/api/v1/bots/feishu", `{"topic_chats":[{"chat_id":"oc_topic"},{"chat_id":"oc_strict","require_owner_mention":true}]}`); code != 200 {
						t.Fatalf("strict policy: %d %v", code, out)
					}
				}
				sender := tc.sender
				if sender == "" {
					sender = "ou_me"
				}
				chat := tc.chat
				if chat == "" {
					chat = askChat
				}
				content := `{"content":[[` + tc.prefix + `{"tag":"text","text":` + textJSON(tc.words) + `}`
				if tc.name == "same name with actual bot later" || tc.name == "literal name plus real mention elsewhere" {
					content += `,{"tag":"at","user_id":"@_user_1"}`
				}
				content += `]]}`
				kind := "post"
				if tc.plain {
					kind, content = "text", textContent(tc.words)
				}
				event := botMessageEvent("om_policy", sender, kind, content, map[string]any{"chat_type": "group", "chat_id": chat, "thread_id": tc.thread, "mentions": tc.mentions})
				if tc.senderType != "" {
					var envelope map[string]any
					_ = json.Unmarshal(event, &envelope)
					envelope["event"].(map[string]any)["sender"].(map[string]any)["sender_type"] = tc.senderType
					event, _ = json.Marshal(envelope)
				}
				// Count only what this message makes the bot do: the bot's start-up read of its own identity (after the
				// pairing and each settings change above) is in the background and may come after the count (#14219).
				w.botInfoRead(t)
				total := w.api.total()
				w.send(t, event)
				// The accepted /read is handed without an API send; ordinary topic and /ai paths likewise.
				w.quiet(t, total, "om_policy_barrier")
				got := w.handed()
				want := 1
				if tc.accepted {
					want = 2
				}
				if len(got) != want || tc.accepted && got[0] != "om_policy" {
					t.Fatalf("handoffs %v; accepted=%v", got, tc.accepted)
				}
				sources := w.settings().AISources
				if tc.accepted {
					expected := assistantwire.FeishuFromElsewhere
					if command == "/read" {
						expected = assistantwire.FeishuFromRead
					}
					if tc.ordinaryTopic {
						expected = ""
					}
					raw := w.a.got(assistantwire.MethodFeishuMessage)[0]
					if !containsFrom(raw, expected) {
						t.Fatalf("handoff %s; want from %q", raw, expected)
					}
					var handoff assistantwire.FeishuMessage
					_ = json.Unmarshal(raw, &handoff)
					var received struct {
						Message struct {
							Content string `json:"content"`
						}
					}
					_ = json.Unmarshal(handoff.Event, &received)
					if received.Message.Content != content {
						t.Fatalf("handoff changed the original post: %q", received.Message.Content)
					}
					if !tc.ordinaryTopic && (len(sources) != 1 || sources[0].MessageID != "om_policy" || sources[0].ChatID != chat) {
						t.Fatalf("sources %+v", sources)
					}
				} else if len(sources) != 0 {
					t.Fatalf("denied request kept a source: %+v", sources)
				}
				if botChatAllowed(w.settings(), askChat) {
					t.Fatal("command granted general group send permission")
				}
			})
		}
	}
}

func textJSON(s string) string { raw, _ := json.Marshal(s); return string(raw) }

func TestPostCommandUnknownBotIdentityIsDenied(t *testing.T) {
	needsLarkModule(t)
	for _, command := range []string{"/read", "/ai"} {
		t.Run(command, func(t *testing.T) {
			w := newBotWorld(t)
			w.pair(t)
			w.topicsOn(t, topicChat)
			w.readOn(t)
			const within = 30 * time.Second
			// Wait for restartBotLocked's lookup to fill the cache. Clearing
			// it earlier lets the in-flight response restore the identity.
			eventually(t, within, "the startup bot identity", func() bool {
				w.h.bots.mu.Lock()
				defer w.h.bots.mu.Unlock()
				return w.h.bots.info.OpenID == "ou_bot"
			})
			w.api.mu.Lock()
			w.api.botInfoFail = true
			w.api.mu.Unlock()
			w.h.bots.mu.Lock()
			w.h.bots.info.OpenID = ""
			previous := w.h.bots.line
			w.h.bots.mu.Unlock()
			before := w.api.total()
			content := `{"content":[[{"tag":"at","user_id":"@_user_1"},{"tag":"text","text":"` + command + ` summarize"}]]}`
			w.send(t, botMessageEvent("om_unknown_post", "ou_me", "post", content, map[string]any{"chat_type": "group", "chat_id": askChat, "mentions": atBot}))
			w.h.bots.mu.Lock()
			done := w.h.bots.line
			w.h.bots.mu.Unlock()
			if done == previous {
				t.Fatal("post was not queued for processing")
			}
			select {
			case <-done:
			case <-time.After(within):
				t.Fatal("post processing did not finish")
			}
			if got := w.handed(); len(got) != 0 || len(w.settings().AISources) != 0 {
				t.Fatalf("unknown identity acted: %v", got)
			}
			calls := w.api.since(before)
			if !strings.Contains(calls, "GET /open-apis/bot/v3/info") || strings.Contains(calls, "/open-apis/im/") {
				t.Fatalf("unexpected calls: %s", calls)
			}
		})
	}
}
