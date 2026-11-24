package imessage

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

// sameRows says whether two lists of Inbox rows are the same field for field, as the Inbox reads them (their JSON).
func sameRows(t *testing.T, what string, got, want []connectorwire.InboxThing) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if len(want) == 0 || string(g) != string(w) {
		t.Fatalf("%s:\n got %s\nwant %s", what, g, w)
	}
}

// inboxThingsRecipe is iMessage's default subscription's recipe (#6317): its rows, ready-made.
func inboxThingsRecipe() subscriptions.Recipe {
	return subscriptions.Recipe{Mode: wire.ConnectorSubscriptionModeThings, Tool: inboxThings, ItemsPath: "$.things",
		Kind: wire.ConnectorSubscriptionKindSubMessage, GroupBy: wire.ConnectorSubscriptionGroupThread}
}

// On the same Mac, inbox_things answers exactly the rows Collect has (#6317): one computation. Through the catalog
// (its read-only check) as a subscription calls it, the existing read tools are still in it.
func TestInboxThingsAreCollectsRows(t *testing.T) {
	ctx := context.Background()
	c, _ := personaRemote(t, map[string]string{"connector.chats": chatsPage(time.Now()), "connector.older": olderPage})
	c.look(ctx, func(connectorwire.Scope) {})
	want, _ := c.Collect(ctx)
	out, err := connector.CallCatalog(ctx, c, connectorwire.CatalogQuery{Source: ID, Tool: inboxThings, Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Things []connectorwire.InboxThing `json:"things"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	sameRows(t, "inbox_things", got.Things, want)
	sources, err := connector.ReadCatalog(ctx, c)
	if err != nil || len(sources) != 1 {
		t.Fatalf("catalog: %+v %v", sources, err)
	}
	var names []string
	for _, tool := range sources[0].Tools {
		names = append(names, tool.Name)
	}
	if got := json.RawMessage(mustJSON(names)); string(got) != `["chats","read","search","inbox_things"]` {
		t.Fatalf("the catalog's tools: %s", got)
	}
	for _, tool := range c.Tools() {
		if tool.Name == "imessage_inbox_things" {
			t.Fatal("inbox_things is one of the assistant's tools")
		}
	}
}

// oneConnector is the subscriptions' registry with only this Mac's iMessage in it.
type oneConnector struct{ c *Remote }

func (o oneConnector) Find(_ context.Context, id string) (subscriptions.ConnectorInfo, bool) {
	if id != ID {
		return subscriptions.ConnectorInfo{}, false
	}
	return subscriptions.ConnectorInfo{ID: ID, Conn: o.c, Enabled: true, Status: connectorwire.StateOK}, true
}

func (o oneConnector) All(ctx context.Context) []subscriptions.ConnectorInfo {
	info, _ := o.Find(ctx, ID)
	return []subscriptions.ConnectorInfo{info}
}

// iMessage's Inbox feed switched to its default things subscription hands the Inbox, under iMessage's own id, exactly
// the rows its Collect does (#6317, G1 of the migration): the same fixtures, through the real subscription service.
func TestTheSwitchedFeedHandsCollectsRows(t *testing.T) {
	ctx := context.Background()
	c, _ := personaRemote(t, map[string]string{"connector.chats": chatsPage(time.Now()), "connector.older": olderPage})
	c.look(ctx, func(connectorwire.Scope) {})
	want, _ := c.Collect(ctx)
	svc := subscriptions.New(subscriptions.Options{Store: subscriptions.NewMemStore(), Connectors: oneConnector{c},
		Location: func() *time.Location { return time.UTC }})
	if _, err := svc.Migrate(ctx, subscriptions.Migration{Connector: ID, Source: ID, Label: c.Name(), Recipe: inboxThingsRecipe()}, true,
		subscriptions.Actor{ID: "device:phone"}); err != nil {
		t.Fatal(err)
	}
	svc.RunDue(ctx)
	srcs, err := svc.InboxSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []connectorwire.InboxThing
	for _, s := range srcs {
		if s.ID != ID {
			continue
		}
		for _, th := range s.Things {
			if th.Kind != subscriptions.KindFailing {
				got = append(got, th)
			}
		}
	}
	sameRows(t, "the switched feed", got, want)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// stepClock moves only when the runner sleeps or a test steps it.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *stepClock) Sleep(ctx context.Context, d time.Duration) error {
	c.step(d)
	return ctx.Err()
}
func (c *stepClock) step(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// switchedRows are the rows the switched feed hands the Inbox under iMessage's id.
func switchedRows(t *testing.T, svc *subscriptions.Service) []connectorwire.InboxThing {
	t.Helper()
	srcs, err := svc.InboxSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []connectorwire.InboxThing
	for _, s := range srcs {
		if s.ID != ID {
			continue
		}
		for _, th := range s.Things {
			if th.Kind != subscriptions.KindFailing {
				got = append(got, th)
			}
		}
	}
	return got
}

// The latest answer kept (#13637) changes nothing iMessage's switched feed hands the Inbox, which owners already read
// through it: with one row a chat, it is each chat's newest stored, what the feed handed before, after the first run
// and after another.
func TestTheKeptAnswerHandsWhatIMessagesFeedHandedBefore(t *testing.T) {
	ctx := context.Background()
	c, _ := personaRemote(t, map[string]string{"connector.chats": chatsPage(time.Now()), "connector.older": olderPage})
	c.look(ctx, func(connectorwire.Scope) {})
	store, clock := subscriptions.NewMemStore(), &stepClock{t: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)}
	svc := subscriptions.New(subscriptions.Options{Store: store, Connectors: oneConnector{c}, Clock: clock,
		Location: func() *time.Location { return time.UTC }})
	sub, err := svc.Migrate(ctx, subscriptions.Migration{Connector: ID, Source: ID, Label: c.Name(), Recipe: inboxThingsRecipe()}, true,
		subscriptions.Actor{ID: "device:phone"})
	if err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		clock.step(time.Hour)
		svc.RunDue(ctx)
		after := switchedRows(t, svc)
		cur, _ := store.GetSubscription(ctx, sub.ID)
		if cur == nil || cur.AnswerAt == nil || !cur.AnswerAt.Equal(clock.Now()) {
			t.Fatalf("run %d kept no answer: %+v", run, cur)
		}
		old := *cur
		old.Answer, old.AnswerAt = nil, nil // as a build before #13637 hands it
		if ok, err := store.UpdateSubscription(ctx, old, cur.Revision); !ok || err != nil {
			t.Fatal(ok, err)
		}
		sameRows(t, "the kept answer against each chat's newest stored", after, switchedRows(t, svc))
	}
}
