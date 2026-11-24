package github

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

// sameRows stops the test unless two non-empty lists of Inbox rows are the same field for field, as the Inbox reads
// them (their JSON).
func sameRows(t *testing.T, what string, got, want []connectorwire.InboxThing) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if len(want) == 0 || string(g) != string(w) {
		t.Fatalf("%s:\n got %s\nwant %s", what, g, w)
	}
}

// oneConnector is the subscriptions' registry with only this GitHub in it.
type oneConnector struct{ c *Connector }

func (o oneConnector) Find(_ context.Context, id string) (subscriptions.ConnectorInfo, bool) {
	if id != o.c.ID() {
		return subscriptions.ConnectorInfo{}, false
	}
	return subscriptions.ConnectorInfo{ID: id, Conn: o.c, Enabled: true, Status: connectorwire.StateOK}, true
}

func (o oneConnector) All(ctx context.Context) []subscriptions.ConnectorInfo {
	info, _ := o.Find(ctx, o.c.ID())
	return []subscriptions.ConnectorInfo{info}
}

// GitHub's Inbox feed switched to its default subscription hands the Inbox, under GitHub's own id, exactly the rows its
// Collect does (#6404, G1 of the migration): the same fixtures, through the real subscription service and the generic
// inbox_things (#6381), which answers GitHub's Collect. Collect has no side effects: read again, before and after the
// subscription read it, it has the same rows.
func TestTheSwitchedFeedHandsCollectsRows(t *testing.T) {
	ctx := context.Background()
	c := notificationsFixture().server(t)
	if _, _, err := c.poll(ctx); err != nil {
		t.Fatal(err)
	}
	want, _ := c.Collect(ctx)
	again, _ := c.Collect(ctx)
	sameRows(t, "Collect read again", again, want)
	svc := subscriptions.New(subscriptions.Options{Store: subscriptions.NewMemStore(), Connectors: oneConnector{c},
		Location: func() *time.Location { return time.UTC }})
	recipe := subscriptions.Recipe{Mode: wire.ConnectorSubscriptionModeThings, Tool: subscriptions.InboxThingsTool, ItemsPath: "$.things",
		Kind: wire.ConnectorSubscriptionKindSubNotice, GroupBy: wire.ConnectorSubscriptionGroupThread}
	if _, err := svc.Migrate(ctx, subscriptions.Migration{Connector: c.ID(), Source: c.ID(), Label: "GitHub", Recipe: recipe}, true,
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
		if s.ID != c.ID() {
			continue
		}
		for _, th := range s.Things {
			if th.Kind != subscriptions.KindFailing {
				got = append(got, th)
			}
		}
	}
	sameRows(t, "the switched feed", got, want)
	after, _ := c.Collect(ctx)
	sameRows(t, "Collect after the subscription read it", after, want)
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

// switchedRows are the rows the switched feed hands the Inbox under source id.
func switchedRows(t *testing.T, svc *subscriptions.Service, id string) []connectorwire.InboxThing {
	t.Helper()
	srcs, err := svc.InboxSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []connectorwire.InboxThing
	for _, s := range srcs {
		if s.ID != id {
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

// The latest answer kept (#13637) changes nothing GitHub's switched feed hands the Inbox: with one row a thread, it
// is each thread's newest stored, what the feed handed before, after the first run and after another.
func TestTheKeptAnswerHandsWhatGitHubsFeedHandedBefore(t *testing.T) {
	ctx := context.Background()
	c := notificationsFixture().server(t)
	if _, _, err := c.poll(ctx); err != nil {
		t.Fatal(err)
	}
	store, clock := subscriptions.NewMemStore(), &stepClock{t: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)}
	svc := subscriptions.New(subscriptions.Options{Store: store, Connectors: oneConnector{c}, Clock: clock,
		Location: func() *time.Location { return time.UTC }})
	recipe := subscriptions.Recipe{Mode: wire.ConnectorSubscriptionModeThings, Tool: subscriptions.InboxThingsTool, ItemsPath: "$.things",
		Kind: wire.ConnectorSubscriptionKindSubNotice, GroupBy: wire.ConnectorSubscriptionGroupThread}
	sub, err := svc.Migrate(ctx, subscriptions.Migration{Connector: c.ID(), Source: c.ID(), Label: "GitHub", Recipe: recipe}, true,
		subscriptions.Actor{ID: "device:phone"})
	if err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		clock.step(time.Hour)
		svc.RunDue(ctx)
		after := switchedRows(t, svc, c.ID())
		cur, _ := store.GetSubscription(ctx, sub.ID)
		if cur == nil || cur.AnswerAt == nil || !cur.AnswerAt.Equal(clock.Now()) {
			t.Fatalf("run %d kept no answer: %+v", run, cur)
		}
		old := *cur
		old.Answer, old.AnswerAt = nil, nil // as a build before #13637 hands it
		if ok, err := store.UpdateSubscription(ctx, old, cur.Revision); !ok || err != nil {
			t.Fatal(ok, err)
		}
		sameRows(t, "the kept answer against each thread's newest stored", after, switchedRows(t, svc, c.ID()))
	}
}
