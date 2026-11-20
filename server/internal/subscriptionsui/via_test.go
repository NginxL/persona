package subscriptionsui

import (
	"context"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

// withMessages is the fakes with a built-in Messages that has a default subscription (#6345), and that subscription.
func withMessages(migrated bool) (*fake, *fakeBuiltins) {
	f, b := newFake(), newBuiltins()
	yes := true
	b.list = append(b.list, Builtin{ID: "imessage", Name: "Messages", Icon: "chat", Status: "ok", Inbox: &yes, InboxEvery: 120,
		Mutable: true, Via: &Via{On: migrated, Subscription: subscriptions.MigrationID("imessage")}})
	sub := subscriptions.Subscription{ID: subscriptions.MigrationID("imessage"), Connector: "imessage", Source: "imessage",
		Label: "Messages", State: subscriptions.StateOK, IntervalSeconds: 900, Revision: 2, RecipeOrigin: subscriptions.OriginPersona,
		Recipe: subscriptions.Recipe{Mode: wire.ConnectorSubscriptionModeThings, Tool: "inbox_things", Kind: "sub_message", GroupBy: "thread"}}
	if migrated {
		sub.Migrated = &now
	}
	f.subs[sub.ID] = sub
	return f, b
}

// A built-in source with a default subscription has a switch to read it through it, asked once when turned on and at
// once back (#6345); while on, the Inbox line says so and a row opens the subscription. One without a default has none.
func TestABuiltinIsReadThroughItsSubscription(t *testing.T) {
	f, b := withMessages(false)
	env := sourcesEnv(i18n.En, true, b)
	v := draw(t, f, env, state{P: pageBuiltin, C: "imessage"})
	sw := item(v, "v.via")
	if sw == nil || sw.Control == nil || sw.Control.On || sw.Control.Action.ID != "builtin_via" || !strings.Contains(sw.Control.Action.Confirm, "Messages") {
		t.Fatalf("the switch, off: %+v", sw)
	}
	if item(v, "v.open") != nil || item(v, "d.to_inbox").Control == nil {
		t.Fatal("off: a row to the subscription, or no Inbox switch")
	}
	if st := section(v, "status"); st == nil || st.Footer == "" {
		t.Fatalf("off: the status footer (reads since the hub started): %+v", st)
	}
	if gh := draw(t, f, env, state{P: pageBuiltin, C: "github"}); section(gh, "via") != nil {
		t.Fatal("a source without a default subscription has the switch")
	}
	res := handle(t, f, env, state{P: pageBuiltin, C: "imessage"}, "builtin_via", map[string]string{"via": "true"})
	if strings.Join(b.migrated, ",") != "imessage=on" || res.View == nil || !strings.Contains(res.Toast, "through its subscription") {
		t.Fatalf("switched on: %v %+v", b.migrated, res)
	}
	v = *res.View
	if sw := item(v, "v.via"); !sw.Control.On || sw.Control.Action.Confirm != "" {
		t.Fatalf("the switch, on (back asks nothing): %+v", sw.Control)
	}
	if o := item(v, "v.open"); o == nil || o.Action == nil || !strings.Contains(o.Action.Push.State, subscriptions.MigrationID("imessage")) {
		t.Fatalf("the row to the subscription: %+v", o)
	}
	if in := item(v, "d.to_inbox"); in.Control != nil || in.Value != "Through its subscription" || item(v, "f.inbox") != nil {
		t.Fatalf("the Inbox while on: %+v", in)
	}
	if st := section(v, "status"); st == nil || st.Footer != "" { // its reads are the subscription's, kept (#11762)
		t.Fatalf("on: the status footer says reads count from the hub's start: %+v", st)
	}
	handle(t, f, env, state{P: pageBuiltin, C: "imessage"}, "builtin_via", map[string]string{"via": "false"})
	if strings.Join(b.migrated, ",") != "imessage=on,imessage=off" {
		t.Fatalf("switched back: %v", b.migrated)
	}
}

// A built-in's default subscription is shown on its built-in (#6345): not a row of the list, not a source of the
// relationship view, which reads the built-in as through its subscription; its own page says where it is switched.
func TestADefaultSubscriptionIsItsBuiltin(t *testing.T) {
	f, b := withMessages(true)
	env := sourcesEnv(i18n.En, true, b)
	v := draw(t, f, env, state{P: pageList})
	if item(v, "s."+subscriptions.MigrationID("imessage")) != nil || item(v, "b.imessage") == nil {
		t.Fatal("the list shows the source twice, or not as its built-in")
	}
	g, err := DrawGraph(context.Background(), f, env)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range g.Sources {
		if s.ID == "s."+subscriptions.MigrationID("imessage") {
			t.Fatal("the relationship view shows the default subscription")
		}
		if s.ID == "b.imessage" && s.Read != "v."+GraphPersona { // reads are an extension's: Persona's own here
			t.Fatalf("the built-in's read: %q", s.Read)
		}
	}
	v = draw(t, f, env, state{P: pageSource, ID: subscriptions.MigrationID("imessage")})
	if in := item(v, "d.to_inbox"); in == nil || in.Control != nil || in.Value != "As its source's rows" || item(v, "d.to_kb") != nil {
		t.Fatalf("its page's Inbox: %+v", in)
	}
	if r := item(v, "h.recipe"); r == nil || r.Subtitle != "Provided by the connector · Comes with Persona" {
		t.Fatalf("its recipe's row, no tool name: %+v", r)
	}
	// Its recipe stays the manager's, so its rows stay the built-in's: tried, not changed.
	v = draw(t, f, env, state{P: pageRecipe, ID: subscriptions.MigrationID("imessage")})
	if item(v, "b.dry") == nil || item(v, "x.tools") != nil || item(v, "x.raw") != nil || item(v, "x.tool") != nil {
		t.Fatal("a default subscription's recipe can be changed")
	}
	if p := item(v, "plain"); p == nil || strings.Contains(p.Text, "inbox_things") {
		t.Fatalf("what it does names the tool: %+v", p)
	}
}
