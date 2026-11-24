package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

// A source subscription's things through the Inbox (#2039): the manager hands them in as a plugin's connector's
// (triaged before they enter), so a catch-up digest is one row triaged once, and without a model the fallback tiers
// let everything in.

// subscriptionSource is a subscription as the manager's round names it (extmgr readSubscriptions).
type subscriptionSource struct {
	svc *subscriptions.Service
	id  string
}

func (s subscriptionSource) ID() string   { return s.id }
func (s subscriptionSource) Name() string { return "Notices" }
func (subscriptionSource) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapInbox}}
}
func (s subscriptionSource) Collect(ctx context.Context) ([]connector.InboxThing, error) {
	srcs, err := s.svc.InboxSources(ctx)
	for _, src := range srcs {
		if src.ID == s.id {
			return src.Things, err
		}
	}
	return nil, err
}
func (subscriptionSource) Reply(context.Context, connector.InboxReply) error {
	return errors.New("subscriptions are read-only")
}

// caughtUp is a subscription whose Inbox was just turned on, with 30 items its first run since caught up on: one of
// them due tomorrow. It is grouped by source, so that one is the subscription's one row (#5671), as the run that ended
// the catch-up marked.
func caughtUp(t *testing.T) subscriptionSource {
	t.Helper()
	ctx, now := context.Background(), time.Now().UTC()
	store := subscriptions.NewMemStore()
	from, mark := now.Add(-time.Minute), now.Add(-30*time.Second)
	sub := subscriptions.Subscription{ID: "sub_example", Connector: "example", Source: "notices", Label: "Notices",
		Recipe: subscriptions.Recipe{Mode: "items", Kind: "sub_notice", GroupBy: "source"}, ToInbox: true, InboxFrom: &from,
		InboxCatchup: &mark, InboxGroup: "source", Revision: 1, CreatedAt: now.Add(-time.Hour)}
	if err := store.CreateSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	var items []subscriptions.Item
	for i := range 30 {
		at := now.Add(-time.Duration(i+2) * time.Minute)
		items = append(items, subscriptions.Item{UID: fmt.Sprint("n", i), Title: fmt.Sprint("示例通知 ", i), Sender: "Office", Time: &at,
			Body: "示例正文", Kind: "sub_notice"})
	}
	due := now.Add(24 * time.Hour)
	items[3].DueDate = &due
	if _, err := store.CommitRun(ctx, subscriptions.RunCommit{Sub: sub, Revision: 1, Items: items, Now: mark}); err != nil {
		t.Fatal(err)
	}
	svc := subscriptions.New(subscriptions.Options{Store: store, Lang: func() i18n.Lang { return i18n.En }})
	return subscriptionSource{svc: svc, id: sub.ID}
}

func subscriptionItems(h *inboxHub, source string) []*InboxItem {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	var out []*InboxItem
	for _, it := range h.inbox.items {
		if it.Source == source {
			out = append(out, it)
		}
	}
	return out
}

func TestASubscriptionCatchUpIsOneDigestTriagedOnce(t *testing.T) {
	model, client := newTriageModel(t, "ok", `{"tier":"fyi","summary":"示例摘要","reason":"示例"}`)
	src := caughtUp(t)
	h, _ := admitHub(t, client, src)
	collectAndAdmit(h)
	items := subscriptionItems(h, src.id)
	if len(items) != 2 {
		t.Fatalf("30 caught-up items: %d rows, want the digest and the deadline", len(items))
	}
	if n := len(model.asked()); n != 2 {
		t.Fatalf("%d triage calls, want one for the digest and one for the deadline", n)
	}
	digest := itemOf(h, src.id, "sub:"+src.id+":catchup")
	if digest == nil || digest.Kind != "sub_digest" || digest.Tier != TierFYI || digest.Summary != "示例摘要" ||
		!strings.Contains(digest.Title, "29 items") {
		t.Fatalf("the digest: %+v", digest)
	}
	asked := ""
	for _, c := range model.asked() {
		asked += c.User
	}
	if !strings.Contains(asked, "示例通知 0") || !strings.Contains(asked, "示例通知 29") {
		t.Fatal("the digest's lines did not reach the model")
	}
	// The same things next round: nothing new, nobody asked again.
	collectAndAdmit(h)
	if n := len(model.asked()); n != 2 || len(subscriptionItems(h, src.id)) != 2 {
		t.Fatalf("a second round: %d calls, %d rows", n, len(subscriptionItems(h, src.id)))
	}
}

func TestASubscriptionWithoutATriageModelComesInByItsFallback(t *testing.T) {
	_, client := newTriageModel(t, "fail", "")
	src := caughtUp(t)
	h, _ := admitHub(t, client, src)
	collectAndAdmit(h)
	digest := itemOf(h, src.id, "sub:"+src.id+":catchup")
	deadline := itemOf(h, src.id, "sub:"+src.id+":all")
	if digest == nil || digest.Tier != TierFYI || deadline == nil || deadline.Tier != TierAction {
		t.Fatalf("without a model: the digest %+v, the deadline %+v", digest, deadline)
	}
	if _, status := inboxTriageOf(t, h); status.Waiting != 0 {
		t.Fatalf("items wait though they have a fallback: %+v", status)
	}
}
