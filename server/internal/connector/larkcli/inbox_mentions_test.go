package larkcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	adapter "github.com/nwverse/persona/server/internal/larkcli"
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
			if got := contextText([]adapter.Message{tc.message}, me); got != tc.want {
				t.Fatalf("context = %q, want %q", got, tc.want)
			}
		})
	}
	// Without an owner id nothing is marked as the owner's; a sender known only by id still gets its marks.
	if got := contextText([]adapter.Message{message("@Example user hi", owner)}, ""); got != "Example peer: @Example user hi" {
		t.Fatalf("no owner id: %q", got)
	}
	bare := message("@Example user hi", owner)
	bare.Sender = adapter.Sender{}
	if got := contextText([]adapter.Message{bare}, me); got != "（@了我）: @Example user hi" {
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

// A collected group item carries the marks per message: the request that @'s the owner is marked, the message in the
// same chat that @'s someone else is not, and comes before the divider as the background.
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
	at := messageTime(request).In(cliLocation()).Format("01-02 15:04") // the hub's wall clock, whatever its zone
	want := at + " Example peer: @Example other please send the numbers\n" + markNew + "\n" +
		at + " Example peer（@了我）: @Example user can you approve the budget today?"
	if got.Context != want {
		t.Fatalf("context = %q, want %q", got.Context, want)
	}
}

// The context keeps a little background before the new messages and marks where they start (contextFor): the earlier
// messages of the day before the first new one, at most contextEarlier, and at least the last contextEarlierKeep
// however old; each line with its time, the background cut shorter than the new messages.
func TestContextKeepsRecentBackgroundBeforeTheNewMessages(t *testing.T) {
	position := 0
	message := func(day, hour int, text string) adapter.Message {
		position++
		raw, _ := json.Marshal(text)
		return adapter.Message{MessageID: fmt.Sprintf("om_%d", position), MessagePosition: fmt.Sprint(position), ChatID: "oc_example",
			ChatType: "group", CreateTime: fmt.Sprintf("2030-01-%02d %02d:00", day, hour), // the CLI's wall clock, as the hub's
			Sender: adapter.Sender{ID: "ou_example_peer", Name: "Example peer"}, Content: raw}
	}
	line := func(m adapter.Message) string {
		return messageTime(m).In(cliLocation()).Format("01-02 15:04") + " Example peer: " + strings.TrimSpace(messageText(m))
	}
	lines := func(ms ...adapter.Message) string {
		out := make([]string, len(ms))
		for i, m := range ms {
			out[i] = line(m)
		}
		return strings.Join(out, "\n")
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
	want := lines(chat[6:]...) + "\n" + markNew + "\n" + lines(fresh...)
	if got := contextFor(append(append([]adapter.Message(nil), chat...), fresh...), fresh, ""); got != want {
		t.Fatalf("busy chat:\n%s\nwant\n%s", got, want)
	}

	// A quiet chat: its last three messages, though weeks old, so a short answer keeps its question.
	quiet := []adapter.Message{message(1, 9, "first"), message(1, 10, "which branch?"), message(1, 11, "main or release?"), message(1, 12, "either")}
	answer := message(20, 9, "ok, main")
	if got, want := contextFor(append(append([]adapter.Message(nil), quiet...), answer), []adapter.Message{answer}, ""),
		lines(quiet[1:]...)+"\n"+markNew+"\n"+line(answer); got != want {
		t.Fatalf("quiet chat:\n%s\nwant\n%s", got, want)
	}

	// Search may print no positions: its copies of one minute's messages then order by id, the follow-up ("om_a…")
	// before the request ("om_z…"), not as the chat has them. The new messages still start at the request.
	request := message(10, 3, "can you review it?")
	request.MessageID = "om_z_request"
	ack := message(10, 3, "looking")
	ack.Sender = adapter.Sender{ID: "ou_example_user", Name: "Example user"}
	followup := message(10, 3, "still need it")
	followup.MessageID = "om_a_followup"
	unpositioned := func(m adapter.Message) adapter.Message { m.MessagePosition = ""; return m }
	background := message(9, 20, "earlier question")
	in := []adapter.Message{background, request, ack, followup}
	if got, want := contextFor(in, []adapter.Message{unpositioned(followup), unpositioned(request)}, ""),
		line(background)+"\n"+markNew+"\n"+line(request)+"\n"+contextText([]adapter.Message{ack}, "")+"\n"+line(followup); got != want {
		t.Fatalf("search without positions:\n%s\nwant\n%s", got, want)
	}

	// Nothing earlier: no divider; and the new messages without fresh ones named are all new.
	if got, want := contextFor(fresh, fresh, ""), lines(fresh...); got != want {
		t.Fatalf("only new: %q, want %q", got, want)
	}
	if got, want := contextFor(fresh, nil, ""), lines(fresh...); got != want {
		t.Fatalf("no fresh: %q, want %q", got, want)
	}

	// A card's thousands of characters: the background keeps contextEarlierRunes, a new message contextNewRunes.
	long := message(10, 5, strings.Repeat("长", 3000))
	newer := message(10, 6, strings.Repeat("新", 3000))
	got := strings.Split(contextFor([]adapter.Message{long, newer}, []adapter.Message{newer}, ""), "\n")
	if len(got) != 3 || got[1] != markNew || !strings.HasSuffix(got[0], strings.Repeat("长", contextEarlierRunes)+"…") ||
		!strings.HasSuffix(got[2], strings.Repeat("新", contextNewRunes)+"…") || strings.Contains(got[2], strings.Repeat("新", contextNewRunes+1)) {
		t.Fatalf("long messages: %d lines, %d and %d runes", len(got), len([]rune(got[0])), len([]rune(got[len(got)-1])))
	}
}

// The divider is spelled as the larkcli kinds' prompt names it.
func TestContextDividerMatchesThePrompt(t *testing.T) {
	for _, kind := range []string{"dm", "bot", "mention", "group"} {
		if p := prompts.RenderInboxItem(prompts.InboxItem{Source: ID, Kind: kind}); !strings.Contains(p, "「"+markNew+"」") {
			t.Errorf("%s: the prompt does not name the divider %q", kind, markNew)
		}
	}
}
