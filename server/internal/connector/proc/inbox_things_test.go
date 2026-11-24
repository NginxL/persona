package proc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

// chatProgram is a chat connector taking over one of the hub's own sources (MigratableConnectors), as Lark's
// extension does, on made-up data: two signed-in accounts, chats, a document's comment, and a group chat whose new
// messages wait to be related to its events (CapGroup) while two of its events were read and replied to elsewhere.
type chatProgram struct{}

func (chatProgram) ID() string   { return "lark" }
func (chatProgram) Name() string { return "Lark" }
func (chatProgram) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapStatus, connector.CapInbox, connector.CapGroup,
		connector.CapMeta}, MetaDescription: "messages: the event's messages, oldest first"}
}
func (chatProgram) Status(context.Context) connector.Status {
	return connector.Status{State: connector.StateOK}
}
func (chatProgram) Reply(context.Context, connector.InboxReply) error { return nil }
func (chatProgram) GroupTool(context.Context, connector.GroupToolCall) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (chatProgram) GroupAppend(_ context.Context, a connector.GroupAppend) (connector.InboxThing, error) {
	return connector.InboxThing{ID: a.Event, Thread: a.Thread, Last: "7"}, nil
}
func (chatProgram) GroupFallback(context.Context, connector.GroupFallback) (connector.GroupFallbackResult, error) {
	return connector.GroupFallbackResult{}, nil
}

// chatThings are the program's Inbox rows, the same at every read.
func chatThings() []connector.InboxThing {
	at := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	extra := func(account, name, block string) *connector.ThingExtra {
		return &connector.ThingExtra{Source: "lark", Account: account, AccountName: name, LegacyID: "lark:" + account + ":" + block,
			RuleTier: string(connector.TierFYI), Lark: json.RawMessage(`{"chat":"` + block + `"}`)}
	}
	return []connector.InboxThing{
		{Thread: "chat:a1", Kind: "lark_p2p", Label: "Direct message", Title: "Example Person", Sender: "Example Person",
			Preview: "Can you look at the draft?", Context: "Example Person: Can you look at the draft?", At: at, Last: "m12",
			URL: "https://example.com/chat/a1", ReplyTo: "Reply to Example Person", CanReply: true, Avatar: "https://example.com/a1.png",
			Tier: connector.TierAction, Why: "Rule: a direct message", Rules: "Direct messages need me.",
			Tags:  []connector.Tag{{Key: connector.TagChat, Value: "a1", Name: "Example Person"}, {Key: connector.TagSender, Value: "u1", Name: "Example Person"}},
			Extra: extra("acct-1", "Work", "a1")},
		{Thread: "chat:b1", Kind: "lark_p2p", Title: "Example Friend", Sender: "Example Friend", Preview: "Thanks!", At: at.Add(-time.Hour),
			Last: "m3", Read: true, Done: connector.DoneRead, Extra: extra("acct-2", "Personal", "b1")},
		{Thread: "doc:d1", Kind: "doc_comment", Label: "Comment", Title: "Example plan", Sender: "Example Person", Preview: "Looks good",
			At: at.Add(-2 * time.Hour), Last: "c4", URL: "https://example.com/doc/d1", Parent: &connector.ThingParent{Kind: "todo", ID: "todo-1"},
			Extra: &connector.ThingExtra{Source: "lark", Account: "acct-1", Doc: json.RawMessage(`{"doc":"d1"}`)}},
		// one group chat: the new messages to relate, then two of its events' state, all under the chat's thread
		{Thread: "chat:g1", Kind: "lark_group", Group: &connector.ThingGroup{Batch: "chat:g1@7", New: []string{"m6", "m7"}},
			Context: "Example Person: m6\nExample Friend: m7", Rules: "Mentions need me.", At: at.Add(-3 * time.Hour), Last: "7"},
		{Thread: "chat:g1", Kind: "lark_group", ID: "lark:acct-1:g1:e1", Group: &connector.ThingGroup{Batch: "", New: []string{}},
			Read: true, At: at.Add(-4 * time.Hour), Last: "5"},
		{Thread: "chat:g1", Kind: "lark_group", ID: "lark:acct-1:g1:e2", Group: &connector.ThingGroup{Batch: "", New: []string{}},
			Done: connector.DoneReplied, At: at.Add(-5 * time.Hour), Last: "4"},
	}
}

func (chatProgram) Collect(context.Context) ([]connector.InboxThing, error) { return chatThings(), nil }

// oneSource is the subscriptions' registry with only c in it.
type oneSource struct{ c *Conn }

func (o oneSource) Find(_ context.Context, id string) (subscriptions.ConnectorInfo, bool) {
	if id != o.c.ID() {
		return subscriptions.ConnectorInfo{}, false
	}
	return subscriptions.ConnectorInfo{ID: id, Conn: o.c, Enabled: true, Status: connector.StateOK}, true
}

func (o oneSource) All(ctx context.Context) []subscriptions.ConnectorInfo {
	info, _ := o.Find(ctx, o.c.ID())
	return []subscriptions.ConnectorInfo{info}
}

// An extension's chat source switched to its default subscription hands the Inbox, under the source's own id, exactly
// the rows its Collect does (Lark's, M3 of the Inbox Sources migration, G1, #13672): every field, a document's parent,
// two accounts, and a group chat's batch with its events' state under one thread (#13637). Through the extension
// protocol and the real subscription service, with the generic inbox_things answering Collect and Lark's default
// recipe (extmgr's migrations["lark"], which TestABuiltinSwitchesToItsDefaultSubscription pins to this one).
func TestASwitchedChatSourceHandsCollectsRows(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := program(t, &memSecrets{}, "PERSONA_PROC_KIND=chat")
	cfg.ID, cfg.Name = "lark", "Lark"
	c := New(cfg)
	go c.Run(ctx)
	waitUntil(t, "the program", func() bool { return c.Status(ctx).State == connector.StateOK })
	want, err := c.Collect(ctx)
	if err != nil || len(want) != len(chatThings()) {
		t.Fatalf("Collect: %d rows, %v", len(want), err)
	}
	svc := subscriptions.New(subscriptions.Options{Store: subscriptions.NewMemStore(), Connectors: oneSource{c},
		Location: func() *time.Location { return time.UTC }})
	recipe := subscriptions.Recipe{Mode: wire.ConnectorSubscriptionModeThings, Tool: subscriptions.InboxThingsTool, ItemsPath: "$.things",
		Kind: wire.ConnectorSubscriptionKindSubMessage, GroupBy: wire.ConnectorSubscriptionGroupThread}
	if _, err := svc.Migrate(ctx, subscriptions.Migration{Connector: c.ID(), Source: c.ID(), Label: "Lark", Recipe: recipe}, true,
		subscriptions.Actor{ID: "device:phone"}); err != nil {
		t.Fatal(err)
	}
	svc.RunDue(ctx)
	srcs, err := svc.InboxSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []connector.InboxThing
	for _, s := range srcs {
		if s.ID != c.ID() {
			continue
		}
		for _, th := range s.Things {
			if th.Kind != subscriptions.KindFailing {
				got = append(got, th)
			}
		}
	}
	if rowKeys(got) != rowKeys(want) {
		t.Fatalf("the switched feed's rows:\n got %s\nwant %s", rowKeys(got), rowKeys(want))
	}
	for i := range want {
		g, _ := json.Marshal(got[i])
		w, _ := json.Marshal(want[i])
		if string(g) != string(w) {
			t.Fatalf("the switched feed's row %d:\n got %s\nwant %s", i, g, w)
		}
	}
}

// rowKeys names each row by its thread, event and batch, in order.
func rowKeys(rows []connector.InboxThing) string {
	var out []string
	for _, r := range rows {
		k := r.Thread
		if r.ID != "" {
			k += " event " + r.ID
		}
		if r.Group != nil && r.Group.Batch != "" {
			k += " batch " + r.Group.Batch
		}
		out = append(out, k)
	}
	b, _ := json.Marshal(out)
	return string(b)
}
