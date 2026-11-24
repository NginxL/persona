package larkcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/connectorwire"

	adapter "github.com/nwverse/persona/server/internal/modules/lark/larkcli"
	"github.com/nwverse/persona/server/internal/prompts"
)

func TestInboxPollFollowupAfterOwnReply(t *testing.T) {
	for _, topic := range []string{"", "omt_example_topic"} {
		t.Run("topic="+topic, func(t *testing.T) {
			request := adapter.Message{MessageID: "om_z_request", MessagePosition: "10", ChatID: "oc_example",
				ChatType: "p2p", ThreadID: topic, CreateTime: "2030-01-01T00:00:00Z",
				Sender: adapter.Sender{ID: "ou_example_peer", Name: "Example peer"}, Content: json.RawMessage(`"Request"`)}
			if topic != "" {
				request.ChatType = "group"
				request.Mentions = []adapter.Mention{{ID: "ou_example_user"}}
			}
			reply := request
			reply.MessageID, reply.MessagePosition = "om_y_reply", "11"
			reply.Sender = adapter.Sender{ID: "ou_example_user", Name: "Example user"}
			reply.Mentions = nil
			reply.Content = json.RawMessage(`"Acknowledged"`)
			followup := request
			followup.MessageID, followup.MessagePosition = "om_a_followup", "12"
			followup.Content = json.RawMessage(`"Still need help"`)
			earlier := request // the chat's background, an hour before
			earlier.MessageID, earlier.MessagePosition, earlier.CreateTime = "om_b_earlier", "5", "2029-12-31T23:00:00Z"
			earlier.Content = json.RawMessage(`"Earlier question"`)
			for _, onlyRequest := range []bool{false, true} {
				name := "all_search_results"
				search := []adapter.Message{followup, reply, request}
				if onlyRequest {
					name, search = "only_request_is_indexed", []adapter.Message{request}
				}
				t.Run(name, func(t *testing.T) {
					history := []adapter.Message{followup, reply, request, earlier}
					for i := range history {
						history[i].ChatType = "" // Chat reads omit the type supplied by search.
					}
					c := followupConnector(t, search, history)
					if moved, err := c.Poll(context.Background()); err != nil || !moved {
						t.Fatalf("first poll: moved=%v err=%v", moved, err)
					}
					thread := "larkcli:chat:oc_example"
					if topic != "" {
						thread += ":thread:" + topic
					}
					got := inboxThings(t, c)[thread]
					if got.Last != followup.MessageID || got.Done != "" { // Last marks the follow-up; ReplyTo says where a reply goes (#5779)
						t.Fatalf("unanswered follow-up was lost or settled: %+v", got)
					}
					if topic == "" && got.Kind != "dm" {
						t.Fatalf("history lost direct chat metadata: %+v", got)
					}
					// The unindexed follow-up is current. The owner's reply and the earlier question are background.
					want := prompts.RenderLarkCLIInboxContext(prompts.LarkCLIInboxContext{
						Current: contextLines([]adapter.Message{request, followup}, "ou_example_user", 0),
						History: contextLines([]adapter.Message{earlier, reply}, "ou_example_user", 0),
					})
					if got.Context != want {
						t.Fatalf("context:\n%s\nwant\n%s", got.Context, want)
					}
					if moved, err := c.Poll(context.Background()); err != nil || moved {
						t.Fatalf("unchanged poll duplicated the item: moved=%v err=%v", moved, err)
					}
				})
			}
		})
	}
}

func TestInboxOwnReplyOnlySettlesEarlierIncoming(t *testing.T) {
	for _, positioned := range []bool{true, false} {
		t.Run(map[bool]string{true: "source_order_known", false: "same_minute_order_unknown"}[positioned], func(t *testing.T) {
			request := adapter.Message{MessageID: "om_a_request", MessagePosition: "10", ChatID: "oc_example",
				ChatType: "p2p", CreateTime: "2030-01-01T00:00:00Z",
				Sender: adapter.Sender{ID: "ou_example_peer"}, Content: json.RawMessage(`"Request"`)}
			reply := request
			reply.MessageID, reply.MessagePosition, reply.Sender.ID = "om_z_reply", "11", "ou_example_user"
			want := connectorwire.DoneReplied
			if !positioned {
				request.MessagePosition, reply.MessagePosition = "", ""
				want = "" // Unknown order must not automatically hide an unanswered request.
			}
			c := followupConnector(t, []adapter.Message{reply, request}, []adapter.Message{reply, request})
			if _, err := c.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := inboxThings(t, c)["larkcli:chat:oc_example"]; got.Done != want {
				t.Fatalf("reply classification=%q, want %q: %+v", got.Done, want, got)
			}
		})
	}
}

func TestInboxContextDoesNotPromoteUnrelatedGroupMessages(t *testing.T) {
	mention := adapter.Message{MessageID: "om_request", MessagePosition: "10", ChatID: "oc_example",
		ChatType: "group", CreateTime: "2030-01-01T00:00:00Z", Mentions: []adapter.Mention{{ID: "ou_example_user"}},
		Sender: adapter.Sender{ID: "ou_example_peer"}, Content: json.RawMessage(`"Request"`)}
	other := mention
	other.MessageID, other.MessagePosition, other.Mentions = "om_other", "11", nil
	c := followupConnector(t, []adapter.Message{mention}, []adapter.Message{other, mention})
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := inboxThings(t, c)["larkcli:chat:oc_example"]; got.Last != mention.MessageID {
		t.Fatalf("an ordinary group message replaced the mention: %+v", got)
	}
}

func TestInboxSweepReachesEleventhDirectChat(t *testing.T) {
	message := adapter.Message{MessageID: "om_followup", MessagePosition: "20", ChatID: "oc_target",
		CreateTime: "2029-12-31T22:59:32Z", Sender: adapter.Sender{ID: "ou_example_peer"}, Content: json.RawMessage(`"Need help"`)}
	c := followupConnector(t, nil, []adapter.Message{message})
	var chats []adapter.Chat
	for i := 0; i < inboxDirectChats; i++ {
		chats = append(chats, adapter.Chat{ChatID: fmt.Sprintf("oc_other_%d", i), ChatType: "p2p"})
	}
	chats = append(chats, adapter.Chat{ChatID: "oc_target", ChatType: "p2p", Name: "Example peer"})
	raw, err := json.Marshal(map[string]any{"ok": true, "data": adapter.ChatList{Chats: chats}})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "chats.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LARKCLI_FAKE_CHATS", file)
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := inboxThings(t, c); len(got) != 0 {
		t.Fatalf("first round read beyond the ten-chat bound: %+v", got)
	}
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := inboxThings(t, c)["larkcli:chat:oc_target"]; got.Last != message.MessageID || got.Done != "" {
		t.Fatalf("eleventh chat was skipped again: %+v", got)
	}
}

// lark-cli lists only people in a message's mentions: an @all is in the content alone, as "@_all" in a text message
// and as <at user_id="all"> in a post.
func TestInboxKeepsAtAllWrittenOnlyInContent(t *testing.T) {
	for name, content := range map[string]string{
		"text": `"@_all Please update your weekly notes"`,
		"post": `"<at user_id=\"all\"></at> Service is degraded"`,
	} {
		t.Run(name, func(t *testing.T) {
			message := adapter.Message{MessageID: "om_all", MessagePosition: "10", ChatID: "oc_example",
				ChatType: "group", CreateTime: "2030-01-01T00:00:00Z",
				Sender: adapter.Sender{ID: "ou_example_peer"}, Content: json.RawMessage(content)}
			c := followupConnector(t, []adapter.Message{message}, []adapter.Message{message})
			if _, err := c.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := inboxThings(t, c)["larkcli:chat:oc_example"]; got.Kind != "mention" || got.Last != message.MessageID {
				t.Fatalf("an @all in the content was not kept as a mention: %+v", got)
			}
		})
	}
	for name, content := range map[string]string{
		"address": `"mail me@_allhands.example"`,
		"other":   `"<at user_id=\"ou_example_other\"></at> hi"`,
	} {
		t.Run("not/"+name, func(t *testing.T) {
			if messageMentions(adapter.Message{Content: json.RawMessage(content)}, "ou_example_user") {
				t.Fatalf("%s read as an @all", content)
			}
		})
	}
}

func followupConnector(t *testing.T, search, history []adapter.Message) *Connector {
	t.Helper()
	c := inboxTestConnector(t, "inbox_followup")
	for _, fixture := range []struct {
		name, env string
		messages  []adapter.Message
	}{{"search.json", "LARKCLI_FAKE_SEARCH", search}, {"context.json", "LARKCLI_FAKE_CONTEXT", history}} {
		file := filepath.Join(t.TempDir(), fixture.name)
		raw, err := json.Marshal(map[string]any{"ok": true, "data": adapter.MessagePage{Messages: fixture.messages}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(fixture.env, file)
	}
	return c
}
