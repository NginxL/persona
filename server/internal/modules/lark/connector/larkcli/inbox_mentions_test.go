package larkcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	adapter "github.com/nwverse/persona/server/internal/modules/lark/larkcli"
	"github.com/nwverse/persona/server/internal/prompts"
)

// The context marks whom each message @'s from its structured mentions, as the tier criteria read them
// (prompts/inbox/tiers.md): a Lark group message without a mark did not @ the owner (#9124).
func TestContextMarksWhomEachMessageMentions(t *testing.T) {
	const me = "ou_example_user"
	message := func(text string, mentions ...adapter.Mention) adapter.Message {
		raw, _ := json.Marshal(text)
		return adapter.Message{ChatType: "group", Sender: adapter.Sender{ID: "ou_example_peer", Name: "Example peer"},
			Content: raw, Mentions: mentions}
	}
	owner := adapter.Mention{ID: me, Key: "@_user_1", Name: "Example user"}
	all := adapter.Mention{ID: "all", Key: "@_all", Name: "everyone"}
	for _, tc := range []struct {
		name    string
		message adapter.Message
		want    string
	}{
		{"owner by id", message("@Example user please review the plan today", owner), "Example peer（@了我）: @Example user please review the plan today"},
		{"another person with the owner's name", message("@Example user please review", adapter.Mention{ID: "ou_example_namesake", Name: "Example user"}),
			"Example peer: @Example user please review"},
		{"another person", message("@Example other please review", adapter.Mention{ID: "ou_example_other", Name: "Example other"}),
			"Example peer: @Example other please review"},
		{"a bot", message("@Example bot deploy it", adapter.Mention{ID: "ou_example_bot", Name: "Example bot"}), "Example peer: @Example bot deploy it"},
		{"everyone", message("@_all fill in the weekly report", all), "Example peer（@所有人）: @_all fill in the weekly report"},
		{"everyone written in the text", message("@_all fill in the weekly report"), "Example peer（@所有人）: @_all fill in the weekly report"},
		{"owner and everyone", message("@_all @Example user please confirm", all, owner), "Example peer（@了我）（@所有人）: @_all @Example user please confirm"},
		{"no one", message("the release went out"), "Example peer: the release went out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := contextLines([]adapter.Message{tc.message}, me, 0); got != tc.want {
				t.Fatalf("context = %q, want %q", got, tc.want)
			}
		})
	}
	// Without an owner id nothing is marked as the owner's; a sender known only by id still gets its marks.
	if got := contextLines([]adapter.Message{message("@Example user hi", owner)}, "", 0); got != "Example peer: @Example user hi" {
		t.Fatalf("no owner id: %q", got)
	}
	bare := message("@Example user hi", owner)
	bare.Sender = adapter.Sender{}
	if got := contextLines([]adapter.Message{bare}, me, 0); got != "（@了我）: @Example user hi" {
		t.Fatalf("no sender: %q", got)
	}
}

// The marks are spelled exactly as the tier criteria name them.
func TestContextMarksMatchTheTierCriteria(t *testing.T) {
	criteria := prompts.RenderInboxTriage(prompts.InboxTriage{})
	for _, mark := range []string{markOwner, markAll} {
		if !strings.Contains(criteria, "「"+mark+"」") {
			t.Errorf("the tier criteria do not name the mark %q", mark)
		}
	}
}

// A collected group item marks the incoming owner mention and keeps the other message in background history.
func TestInboxItemContextMarksTheOwnerMention(t *testing.T) {
	request := adapter.Message{MessageID: "om_request", MessagePosition: "10", ChatID: "oc_example", ChatType: "group",
		CreateTime: "2030-01-01T00:00:00Z", Mentions: []adapter.Mention{{ID: "ou_example_user", Name: "Example user"}},
		Sender: adapter.Sender{ID: "ou_example_peer", Name: "Example peer"}, Content: json.RawMessage(`"@Example user can you approve the budget today?"`)}
	other := request
	other.MessageID, other.MessagePosition = "om_other", "9"
	other.Mentions = []adapter.Mention{{ID: "ou_example_other", Name: "Example other"}}
	other.Content = json.RawMessage(`"@Example other please send the numbers"`)
	c := followupConnector(t, []adapter.Message{request}, []adapter.Message{other, request})
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := inboxThings(t, c)["larkcli:chat:oc_example"]
	if got.Kind != "mention" {
		t.Fatalf("kind = %q: %+v", got.Kind, got)
	}
	current, history, ok := strings.Cut(got.Context, "Background history (context only, not new requests):")
	if !ok || !strings.Contains(current, "Example peer（@了我）: @Example user can you approve the budget today?") ||
		!strings.Contains(history, "Example peer: @Example other please send the numbers") {
		t.Fatalf("mention marks or the history boundary were lost: %q", got.Context)
	}
}

func TestContextKeepsRecentBackgroundAndBoundsMessages(t *testing.T) {
	position := 0
	message := func(day, hour int, text string) adapter.Message {
		position++
		raw, _ := json.Marshal(text)
		return adapter.Message{MessageID: fmt.Sprintf("om_%d", position), MessagePosition: fmt.Sprint(position), ChatID: "oc_example",
			ChatType: "p2p", CreateTime: fmt.Sprintf("2030-01-%02d %02d:00", day, hour), // the CLI's wall clock, as the hub's
			Sender: adapter.Sender{ID: "ou_example_peer", Name: "Example peer"}, Content: raw}
	}
	wantContext := func(current, history []adapter.Message) string {
		return prompts.RenderLarkCLIInboxContext(prompts.LarkCLIInboxContext{
			Current: contextLines(current, "", 0),
			History: contextLines(history, "", 0),
		})
	}

	// A busy chat: of the twelve earlier messages, those of the day before the first new one (09:03 on), not the older ones.
	var chat []adapter.Message
	for i := 0; i < 4; i++ {
		chat = append(chat, message(1, 10+i, fmt.Sprintf("old %d", i)))
	}
	for hour := 1; hour <= 8; hour++ {
		chat = append(chat, message(9, hour, fmt.Sprintf("yesterday %d", hour)))
	}
	fresh := []adapter.Message{message(10, 3, "can you approve it today?"), message(10, 4, "the numbers are in the sheet")}
	if got, want := contextFor(fresh, chat, ""), wantContext(fresh, chat[6:]); got != want {
		t.Fatalf("busy chat:\n%s\nwant\n%s", got, want)
	}

	// A quiet chat: its last three messages, though weeks old, so a short answer keeps its question.
	quiet := []adapter.Message{message(1, 9, "first"), message(1, 10, "which branch?"), message(1, 11, "main or release?"), message(1, 12, "either")}
	answer := []adapter.Message{message(20, 9, "ok, main")}
	if got, want := contextFor(answer, quiet, ""), wantContext(answer, quiet[1:]); got != want {
		t.Fatalf("quiet chat:\n%s\nwant\n%s", got, want)
	}

	if got, want := contextFor(fresh, nil, ""), wantContext(fresh, nil); got != want {
		t.Fatalf("only new: %q, want %q", got, want)
	}

	// A card's thousands of characters: the background keeps contextEarlierRunes, a new message contextNewRunes.
	long := message(10, 5, strings.Repeat("长", 3000))
	newer := message(10, 6, strings.Repeat("新", 3000))
	got := contextFor([]adapter.Message{newer}, []adapter.Message{long}, "")
	current, history, ok := strings.Cut(got, "Background history (context only, not new requests):")
	if !ok || !strings.HasSuffix(strings.TrimSpace(current), strings.Repeat("新", contextNewRunes)+"…") ||
		!strings.HasSuffix(history, strings.Repeat("长", contextEarlierRunes)+"…") ||
		strings.Contains(current, strings.Repeat("新", contextNewRunes+1)) || strings.Contains(history, strings.Repeat("长", contextEarlierRunes+1)) {
		t.Fatal("current or background message was not cut to its rune limit")
	}
}

func TestInboxContextOrdersPositionlessSearchResults(t *testing.T) {
	request := adapter.Message{MessageID: "om_z_request", MessagePosition: "10", ChatID: "oc_example",
		ChatType: "p2p", CreateTime: "2030-01-01T00:00:00Z",
		Sender: adapter.Sender{ID: "ou_example_peer"}, Content: json.RawMessage(`"Can you review it?"`)}
	ack := request
	ack.MessageID, ack.MessagePosition = "om_ack", "11"
	ack.Sender = adapter.Sender{ID: "ou_example_user"}
	ack.Content = json.RawMessage(`"Looking"`)
	followup := request
	followup.MessageID, followup.MessagePosition = "om_a_followup", "12"
	followup.Content = json.RawMessage(`"Still need it"`)
	search := []adapter.Message{followup, request}
	for i := range search {
		search[i].MessagePosition = ""
	}
	c := followupConnector(t, search, []adapter.Message{request, ack, followup})
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := inboxThings(t, c)["larkcli:chat:oc_example"].Context
	want := prompts.RenderLarkCLIInboxContext(prompts.LarkCLIInboxContext{
		Current: contextLines([]adapter.Message{request, followup}, "", 0),
		History: contextLines([]adapter.Message{ack}, "", 0),
	})
	if got != want {
		t.Fatalf("search without positions:\n%s\nwant\n%s", got, want)
	}
}

func TestContextSectionsMatchThePrompt(t *testing.T) {
	context := prompts.RenderLarkCLIInboxContext(prompts.LarkCLIInboxContext{Current: "New", History: "Earlier"})
	for _, kind := range []string{"dm", "bot", "mention", "group"} {
		p := prompts.RenderInboxItem(prompts.InboxItem{Source: ID, Kind: kind})
		for _, section := range []string{"Current messages (new activity):", "Background history (context only, not new requests):"} {
			if !strings.Contains(context, section) || !strings.Contains(p, section) {
				t.Errorf("%s: the context and prompt must agree on %q", kind, section)
			}
		}
	}
}
