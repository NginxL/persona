package larkcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"

	"github.com/nwverse/persona/server/internal/i18n"
	adapter "github.com/nwverse/persona/server/internal/modules/lark/larkcli"
)

func TestInboxIgnoresNonConversationActivity(t *testing.T) {
	for _, source := range []string{"search", "direct_sweep"} {
		for _, activity := range []string{"empty_system", "system_with_sender", "senderless"} {
			t.Run(source+"/"+activity, func(t *testing.T) {
				message := adapter.Message{MessageID: "om_activity", ChatID: "oc_example", ChatType: "p2p",
					MsgType: "system", CreateTime: "2030-01-01T00:00:00Z"}
				switch activity {
				case "system_with_sender":
					message.Sender = adapter.Sender{ID: "ou_example_peer"}
					message.Content = json.RawMessage(`"Conversation settings changed"`)
				case "senderless":
					message.MsgType, message.Content = "text", json.RawMessage(`"No identified sender"`)
				}
				search := []adapter.Message{message}
				if source == "direct_sweep" {
					search = nil
				}
				old := adapter.Message{MessageID: "om_old_request", ChatID: "oc_example", ChatType: "p2p",
					CreateTime: "2029-07-01T00:00:00Z", Sender: adapter.Sender{ID: "ou_example_peer"},
					Content: json.RawMessage(`"Old feedback awaiting a reply"`)}
				c := followupConnector(t, search, []adapter.Message{message, old})
				if source == "direct_sweep" {
					file := filepath.Join(t.TempDir(), "chats.json")
					if err := os.WriteFile(file, []byte(`{"ok":true,"data":{"chats":[{"chat_id":"oc_example","chat_type":"p2p"}]}}`), 0600); err != nil {
						t.Fatal(err)
					}
					t.Setenv("LARKCLI_FAKE_CHATS", file)
				}
				store := &inboxStateStore{}
				c.SetStateStore(store)
				if moved, err := c.Poll(context.Background()); err != nil || moved {
					t.Fatalf("non-conversation activity changed Inbox: moved=%v err=%v", moved, err)
				}
				if got := inboxThings(t, c); len(got) != 0 {
					t.Fatalf("non-conversation activity created an item: %+v", got)
				}
				state := c.inboxStateForTests()
				if !slices.Contains(state.Seen, message.MessageID) {
					t.Fatal("ignored activity was not consumed by the seen ledger")
				}
				if source == "search" && state.Cursor != "2029-12-31T23:50:00Z" {
					t.Fatalf("ignored activity did not advance the search cursor: %q", state.Cursor)
				}
				if moved, err := c.Poll(context.Background()); err != nil || moved {
					t.Fatalf("ignored activity was replayed: moved=%v err=%v", moved, err)
				}
			})
		}
	}
}

func TestInboxSystemActivityDoesNotWakeExistingThing(t *testing.T) {
	const thread = "larkcli:chat:oc_example"
	for _, done := range []string{"", connectorwire.DoneReplied} {
		t.Run("done="+done, func(t *testing.T) {
			system := adapter.Message{MessageID: "om_system", ChatID: "oc_example", ChatType: "p2p",
				MsgType: "system", CreateTime: "2030-01-01T00:00:00Z"}
			c := followupConnector(t, []adapter.Message{system}, []adapter.Message{system})
			previous := connectorwire.InboxThing{Thread: thread, Kind: "dm", Last: "om_old_request",
				At: time.Date(2029, 7, 1, 0, 0, 0, 0, time.UTC), Context: "Previously processed request", Done: done}
			c.inboxThings = map[string]connectorwire.InboxThing{thread: previous}
			if moved, err := c.Poll(context.Background()); err != nil || moved {
				t.Fatalf("system activity woke existing thing: moved=%v err=%v", moved, err)
			}
			if got := inboxThings(t, c)[thread]; !reflect.DeepEqual(got, previous) {
				t.Fatalf("system activity rewrote existing thing: %+v", got)
			}
		})
	}
}

func TestInboxHistoryDoesNotPromoteSystemActivity(t *testing.T) {
	request := adapter.Message{MessageID: "om_request", MessagePosition: "10", ChatID: "oc_example",
		ChatType: "p2p", CreateTime: "2030-01-01T00:00:00Z",
		Sender: adapter.Sender{ID: "ou_example_peer"}, Content: json.RawMessage(`"Current request"`)}
	system := adapter.Message{MessageID: "om_system", MessagePosition: "11", ChatID: "oc_example",
		MsgType: "system", CreateTime: "2030-01-01T00:01:00Z",
		Content: json.RawMessage(`"Conversation settings changed"`)}
	c := followupConnector(t, []adapter.Message{request}, []adapter.Message{system, request})
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := inboxThings(t, c)["larkcli:chat:oc_example"]
	if got.Last != request.MessageID || !got.At.Equal(messageTime(request)) {
		t.Fatalf("history promoted a system event: %+v", got)
	}
	if strings.Contains(got.Context, "Conversation settings changed") {
		t.Fatalf("system activity entered triage context: %q", got.Context)
	}
}

func TestInboxContextSeparatesCurrentMessagesFromHistory(t *testing.T) {
	old := adapter.Message{MessageID: "om_old", MessagePosition: "1", ChatID: "oc_example",
		ChatType: "p2p", CreateTime: "2029-07-01T00:00:00Z",
		Sender: adapter.Sender{ID: "ou_example_peer", Name: "Example peer"}, Content: json.RawMessage(`"Old approval request"`)}
	current := old
	current.MessageID, current.MessagePosition, current.CreateTime = "om_current", "2", "2030-01-01T00:00:00Z"
	current.Content = json.RawMessage(`"Thanks, no action needed"`)
	c := followupConnector(t, []adapter.Message{current}, []adapter.Message{current, old})
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := inboxThings(t, c)["larkcli:chat:oc_example"]
	parts := strings.Split(got.Context, "Background history (context only, not new requests):")
	if len(parts) != 2 || !strings.Contains(parts[0], "Current messages (new activity):") ||
		!strings.Contains(parts[0], "Thanks, no action needed") || strings.Contains(parts[0], "Old approval request") ||
		!strings.Contains(parts[1], "Old approval request") || strings.Contains(parts[1], "Thanks, no action needed") {
		t.Fatalf("current activity and old requests were mixed: %q", got.Context)
	}
	if !strings.Contains(parts[0], "[2030-01-01T00:00:00Z]") || !strings.Contains(parts[1], "[2029-07-01T00:00:00Z]") {
		t.Fatalf("message timestamps are missing: %q", got.Context)
	}
	golden, err := os.ReadFile("testdata/inbox_context.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got.Context+"\n" != string(golden) {
		t.Fatalf("triage context changed:\n%s", got.Context)
	}
}

func TestInboxCurrentIncludesOnlyUnseenHistoryFollowups(t *testing.T) {
	request := adapter.Message{MessageID: "om_request", MessagePosition: "10", ChatID: "oc_example",
		ChatType: "p2p", CreateTime: "2030-01-01T00:00:00Z",
		Sender: adapter.Sender{ID: "ou_example_peer"}, Content: json.RawMessage(`"New request"`)}
	followup := request
	followup.MessageID, followup.MessagePosition, followup.CreateTime = "om_followup", "11", "2030-01-01T00:01:00Z"
	followup.Content = json.RawMessage(`"Follow-up found in history"`)
	for _, seen := range []bool{false, true} {
		t.Run(map[bool]string{false: "unseen", true: "already_processed"}[seen], func(t *testing.T) {
			c := followupConnector(t, []adapter.Message{request}, []adapter.Message{followup, request})
			if seen {
				c.markSeen(followup.MessageID)
			}
			if _, err := c.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := inboxThings(t, c)["larkcli:chat:oc_example"]
			wantLast := followup.MessageID
			if seen {
				wantLast = request.MessageID
			}
			current := strings.Split(got.Context, "Background history (context only, not new requests):")[0]
			if got.Last != wantLast || strings.Contains(current, "Follow-up found in history") == seen {
				t.Fatalf("history freshness was lost (seen=%v): %+v", seen, got)
			}
		})
	}
}

func TestInboxCurrentAttachmentIsNotReplacedByOldText(t *testing.T) {
	old := adapter.Message{MessageID: "om_old", MessagePosition: "1", ChatID: "oc_example",
		ChatType: "p2p", CreateTime: "2029-07-01T00:00:00Z",
		Sender: adapter.Sender{ID: "ou_example_peer"}, Content: json.RawMessage(`"Old request"`)}
	image := old
	image.MessageID, image.MessagePosition, image.CreateTime = "om_image", "2", "2030-01-01T00:00:00Z"
	image.MsgType, image.Content = "image", json.RawMessage(`{"image_key":"img_example"}`)
	c := followupConnector(t, []adapter.Message{image}, []adapter.Message{image, old})
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := inboxThings(t, c)["larkcli:chat:oc_example"]
	current := strings.Split(got.Context, "Background history (context only, not new requests):")[0]
	if got.Last != image.MessageID || !strings.Contains(current, "[image]") || strings.Contains(current, "Old request") {
		t.Fatalf("new attachment disappeared behind old text: %+v", got)
	}
}

func TestInboxHistoryPromotionStaysStableAcrossGroups(t *testing.T) {
	first := adapter.Message{MessageID: "om_first", MessagePosition: "10", ChatID: "oc_example",
		ChatType: "p2p", CreateTime: "2030-01-01T00:00:00Z",
		Sender: adapter.Sender{ID: "ou_example_peer"}, Content: json.RawMessage(`"First request"`)}
	other := first
	other.MessageID, other.ChatID, other.CreateTime = "om_other", "oc_other", "2030-01-01T00:01:00Z"
	second := first
	second.MessageID, second.MessagePosition, second.CreateTime = "om_second", "12", "2030-01-01T00:02:00Z"
	second.Content = json.RawMessage(`"Second request"`)
	followup := first
	followup.MessageID, followup.MessagePosition, followup.CreateTime = "om_followup", "13", "2030-01-01T00:03:00Z"
	followup.Content = json.RawMessage(`"Unindexed follow-up"`)
	c := followupConnector(t, []adapter.Message{first, other, second}, []adapter.Message{followup, second, other, first})
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := inboxThings(t, c)["larkcli:chat:oc_example"]
	if got.Last != followup.MessageID || !strings.Contains(got.Preview, "Unindexed follow-up") {
		t.Fatalf("a later group rewound the history-discovered follow-up: %+v", got)
	}
	current, history, _ := strings.Cut(got.Context, "Background history (context only, not new requests):")
	if !strings.Contains(current, "Current messages (new activity):") {
		t.Fatalf("missing current messages: %q", got.Context)
	}
	for _, text := range []string{"First request", "Second request", "Unindexed follow-up"} {
		if strings.Count(current, text) != 1 || strings.Contains(history, text) {
			t.Fatalf("incoming activity %q was lost or moved into history: %q", text, got.Context)
		}
	}
}

func TestInboxInterleavedIncomingMetadataStaysCurrent(t *testing.T) {
	for _, topic := range []string{"", "omt_example_topic"} {
		for _, historyKind := range []string{"complete", "missing_metadata", "partial", "unavailable"} {
			t.Run("topic="+topic+"/"+historyKind, func(t *testing.T) {
				old := adapter.Message{MessageID: "om_old", MessagePosition: "1", ChatID: "oc_example",
					ChatType: "p2p", ThreadID: topic, CreateTime: "2029-07-01T00:00:00Z",
					ChatPartner: json.RawMessage(`{"id":"ou_example_peer","name":"Example peer"}`),
					Sender:      adapter.Sender{ID: "ou_example_peer"}, Content: json.RawMessage(`"Old background"`)}
				first := old
				first.MessageID, first.MessagePosition, first.CreateTime = "om_first", "10", "2030-01-01T00:00:00Z"
				first.Content = json.RawMessage(`"First request"`)
				second := first
				second.MessageID, second.MessagePosition, second.CreateTime = "om_second", "12", "2030-01-01T00:00:20Z"
				second.Content = json.RawMessage(`"Second request"`)
				if topic != "" {
					first.ChatType, second.ChatType = "group", "group"
					first.Mentions, second.Mentions = []adapter.Mention{{ID: "ou_example_user"}}, []adapter.Mention{{ID: "ou_example_user"}}
				}
				other := first
				other.MessageID, other.ChatID, other.ThreadID = "om_other", "oc_other", ""
				other.ChatType, other.Mentions, other.CreateTime = "p2p", nil, "2030-01-01T00:00:10Z"
				other.Content = json.RawMessage(`"Unrelated request"`)
				history := []adapter.Message{second, other, first, old}
				switch historyKind {
				case "missing_metadata":
					// lark-cli prints wall-clock minutes in the machine's zone (cliLocation): the minute of 00:00 UTC
					// there, not "2030-01-01 00:00", which west of UTC is hours after the incoming messages.
					minute := messageTime(first).In(cliLocation()).Format("2006-01-02 15:04")
					for i := range history {
						history[i].ChatType, history[i].ChatPartner, history[i].ThreadID = "", nil, ""
						history[i].MessagePosition, history[i].CreateTime = "", minute
					}
				case "partial":
					history = []adapter.Message{first, old}
				}
				c := followupConnector(t, []adapter.Message{first, other, second}, history)
				if historyKind == "unavailable" {
					t.Setenv("LARKCLI_FAKE_CONTEXT", filepath.Join(t.TempDir(), "missing.json"))
				}
				if _, err := c.Poll(context.Background()); err != nil {
					t.Fatal(err)
				}
				thread := "larkcli:chat:oc_example"
				if topic != "" {
					thread += ":thread:" + topic
				}
				got := inboxThings(t, c)[thread]
				if got.Last != second.MessageID || got.Preview != "Second request" || !got.At.Equal(messageTime(second)) {
					t.Fatalf("incoming reply target or preview changed: %+v", got)
				}
				if topic == "" && (got.Kind != "dm" || got.Title != "Example peer") {
					t.Fatalf("history replaced incoming direct-chat metadata: %+v", got)
				}
				current, background, _ := strings.Cut(got.Context, "Background history (context only, not new requests):")
				if !strings.Contains(current, "Current messages (new activity):") {
					t.Fatalf("missing current messages: %q", got.Context)
				}
				for _, text := range []string{"First request", "Second request"} {
					if strings.Count(current, text) != 1 || strings.Contains(background, text) {
						t.Fatalf("incoming activity %q was lost or moved into history: %q", text, got.Context)
					}
				}
				if strings.Contains(current, "Old background") || strings.Contains(got.Context, "Unrelated request") {
					t.Fatalf("unrelated activity entered current context: %q", got.Context)
				}
			})
		}
	}
}

func TestInboxIncomingIDsAreNotPromotedFromHistory(t *testing.T) {
	first := adapter.Message{MessageID: "om_first", MessagePosition: "10", ChatID: "oc_example",
		ChatType: "p2p", CreateTime: "2030-01-01T00:00:00Z",
		Sender: adapter.Sender{ID: "ou_example_peer"}, Content: json.RawMessage(`"First request"`)}
	second := first
	second.MessageID, second.MessagePosition, second.CreateTime = "om_second", "12", "2030-01-01T00:02:00Z"
	second.Content = json.RawMessage(`"Second request"`)
	historyCopy := first
	historyCopy.MessagePosition, historyCopy.CreateTime, historyCopy.ChatType = "99", "2030-01-01T00:10:00Z", ""
	historyCopy.Content = json.RawMessage(`"History copy of first"`)
	c := followupConnector(t, nil, []adapter.Message{historyCopy, second})
	group := &inboxGroup{key: messageKey(first), chatID: first.ChatID,
		messages: []adapter.Message{first, second}, latest: second}
	got := c.thingForGroup(context.Background(), group, "ou_example_user")
	if got.Last != second.MessageID || got.Preview != "Second request" {
		t.Fatalf("a history copy superseded an incoming message: %+v", got)
	}
	if strings.Contains(got.Context, "History copy of first") || strings.Count(got.Context, "First request") != 1 {
		t.Fatalf("history metadata replaced or duplicated incoming content: %q", got.Context)
	}
}

func TestInboxAttachmentPreviewUsesNormalizedLabels(t *testing.T) {
	for _, tc := range []struct {
		name, messageType, label string
	}{
		{"image", "image", "image"},
		{"file", "file", "file"},
		{"audio", "audio", "audio"},
		{"video", "media", "video"},
		{"sticker", "sticker", "sticker"},
		{"card", "interactive", "card"},
		{"normalized", " IMAGE ", "image"},
		{"unknown", strings.Repeat("unknown\n", 100), "message"},
	} {
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			t.Run(tc.name+"/"+string(lang), func(t *testing.T) {
				message := adapter.Message{MessageID: "om_attachment", ChatID: "oc_example", ChatType: "p2p",
					MsgType: tc.messageType, CreateTime: "2030-01-01T00:00:00Z",
					Sender: adapter.Sender{ID: "ou_example_peer"}, Content: json.RawMessage(`{"image_key":"img_example"}`)}
				c := followupConnector(t, []adapter.Message{message}, []adapter.Message{message})
				ctx := i18n.With(context.Background(), lang)
				if _, err := c.Poll(ctx); err != nil {
					t.Fatal(err)
				}
				got := inboxThings(t, c)["larkcli:chat:oc_example"]
				want := "[" + i18n.T(lang, "hub-connectors.larkcli_placeholder_"+tc.label) + "]"
				if got.Last != message.MessageID || got.Preview != want {
					t.Fatalf("attachment preview=%q, want %q (last=%q)", got.Preview, want, got.Last)
				}
				if !strings.Contains(got.Context, "["+tc.label+"]") || tc.name == "unknown" && strings.Contains(got.Context, "unknown") {
					t.Fatalf("attachment context exposed an unnormalized type: %q", got.Context)
				}
			})
		}
	}
}
