package extmgr

import (
	"context"
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/mailwire"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

// Mail's Add to Inbox (#5307): on until turned off, kept like the other connector switches; off, the mail rounds read
// no account (the manager keeps the mail it has); on again, the next round reads mail at once.
func TestMailInboxSwitch(t *testing.T) {
	ctx := context.Background()
	m := &slowMail{accounts: []mailwire.MailAccount{{ID: "a", Email: "a@example.com"}}}
	h := newSlowMailHub(t, m)
	h.store = newKeepStore()
	if h.readMail(ctx) == nil {
		t.Fatal("on by default: the round has mail")
	}
	b := builtinSources{h}
	if l := connectorSources(b.List(ctx)); len(l) != 1 || l[0].Inbox == nil || !*l[0].Inbox || l[0].InboxAlways || l[0].InboxEvery != 180 {
		t.Fatalf("Inbox Sources shows mail's switch: %+v", l)
	}
	mux := http.NewServeMux() // Settings' route (the fake mail serves no pages of its own)
	h.connectorRoutes(mux)
	code, out := call(t, mux, "PUT", "/api/v1/connectors/mail/settings", h.cfg.Token, `{"inbox":false}`)
	if code != 200 || out["inbox"] != false {
		t.Fatalf("Settings' switch: %d %v", code, out)
	}
	if h.readMail(ctx) != nil {
		t.Error("off, a mail round still read the accounts")
	}
	if l := b.List(ctx); *l[0].Inbox {
		t.Error("Inbox Sources still shows it on")
	}
	var cs connectorSettings
	if _, err := h.store.Setting(ctx, connectorSettingsKey, &cs); err != nil || (*cs.feeds(connector.FeedInbox))["mail"] {
		t.Fatalf("kept for the next start: %+v %v", cs, err)
	}
	h2 := newSlowMailHub(t, m) // a restart: the choice comes back before the connectors start
	h2.store = h.store
	h2.restoreConnectorSwitches(ctx)
	if h2.connectors.Feeds("mail", connector.FeedInbox) {
		t.Error("the switch didn't come back after a restart")
	}
	h.inbox.owed.Store(false)
	if err := b.SetFeed(ctx, "mail", true, true, subscriptions.Actor{}); err != nil {
		t.Fatal(err)
	}
	if !h.inbox.owed.Load() || h.readMail(ctx) == nil {
		t.Error("turned on again: the next round is a full one, with mail")
	}
}

// A connector without an inbox still can't take the switch; one with InboxSwitch can, and lists it.
func TestInboxSwitchNeedsAnInbox(t *testing.T) {
	r := connector.NewRegistry(nil)
	if err := r.Add(archiveOnly{}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetFeed("archiveonly", connector.FeedInbox, false); err == nil {
		t.Error("an archive-only connector took the Inbox switch")
	}
	h := newSlowMailHub(t, &slowMail{})
	for _, info := range h.connectors.Infos(context.Background()) {
		if info.ID == "mail" && (info.Inbox == nil || !*info.Inbox) {
			t.Errorf("mail's Inbox switch isn't listed: %+v", info)
		}
	}
}

// archiveOnly has an archive and nothing for the Inbox.
type archiveOnly struct{}

func (archiveOnly) ID() string   { return "archiveonly" }
func (archiveOnly) Name() string { return "Archive only" }
func (archiveOnly) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapArchive}}
}
func (archiveOnly) Changes(_ context.Context, cursor string, _ int) ([]connector.ChatBatch, string, bool, error) {
	return nil, cursor, false, nil
}
