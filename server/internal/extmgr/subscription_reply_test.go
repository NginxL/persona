package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

// A reply to a subscription's row goes to the connector the subscription reads, by the thread and id that connector
// knows the item by (#6198); a row whose item is gone can't be replied to (inbox_cannot_reply, #9194), and a
// connector's own rows are as before.
func TestASubscriptionsRowIsRepliedToThroughItsConnector(t *testing.T) {
	h := newTestManager(t)
	ctx := context.Background()
	h.connectors = connector.NewRegistry(nil)
	c := &inboxConnector{}
	_ = h.addConnector(c)
	store := subscriptions.NewMemStore()
	now := time.Now().UTC()
	sub := subscriptions.Subscription{ID: "sub_example", Connector: c.ID(), Source: "notices", Label: "Notices",
		Recipe: subscriptions.Recipe{Mode: "items", Kind: "sub_notice", GroupBy: "thread"}, ToInbox: true, Revision: 1, CreatedAt: now}
	if err := store.CreateSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	items := []subscriptions.Item{{UID: "c2", Thread: "example-org/example-repo#12", Title: "A comment", Kind: "sub_notice",
		ReplyTo: "Comment on example-org/example-repo#12 (anyone who can see the repo)"}}
	if _, err := store.CommitRun(ctx, subscriptions.RunCommit{Sub: sub, Revision: 1, Items: items, Now: now}); err != nil {
		t.Fatal(err)
	}
	h.subs = subscriptions.New(h.subscriptionOptions(store))
	reply := func(source string, r connector.InboxReply) error {
		raw, _ := json.Marshal(map[string]any{"source": source, "reply": r})
		_, handled, err := h.AppCall(ctx, assistantwire.AppCall{AppID: assistantwire.AppInbox, Method: assistantwire.InboxReply, Params: raw})
		if !handled {
			t.Fatal("inbox.reply is not the manager's")
		}
		return err
	}
	err := reply(sub.ID, connector.InboxReply{Thread: "sub:sub_example:example-org/example-repo#12", ItemID: "sub_example:1a2b", Last: "c2", Text: "Thanks"})
	if err != nil || len(c.replies) != 1 {
		t.Fatalf("the reply: %v %+v", err, c.replies)
	}
	if got := c.replies[0]; got.Thread != "example-org/example-repo#12" || got.Last != "c2" || got.ItemID != "sub_example:1a2b" || got.Text != "Thanks" {
		t.Fatalf("the connector got %+v", got)
	}
	// A row whose item is gone is one that cannot be answered, not a source that is not here (#9194); a subscription that
	// is gone is that.
	var re *rpc.Error
	if err := reply(sub.ID, connector.InboxReply{Thread: "sub:sub_example:x", Last: "gone", Text: "Thanks"}); !errors.As(err, &re) || re.Code != assistantwire.CodeInboxCannotReply {
		t.Fatalf("a row whose item is gone: %v", err)
	}
	if err := reply("sub_gone", connector.InboxReply{Thread: "sub:sub_gone:x", Last: "c2", Text: "Thanks"}); !errors.As(err, &re) || re.Code != assistantwire.CodeInboxSourceUnavailable {
		t.Fatalf("a row of a subscription that is gone: %v", err)
	}
	if err := reply(c.ID(), connector.InboxReply{Thread: "chat:C1", Text: "Own"}); err != nil || len(c.replies) != 2 || c.replies[1].Thread != "chat:C1" {
		t.Fatalf("the connector's own row: %v %+v", err, c.replies)
	}
}
