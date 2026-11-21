package subscriptions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// InboxThingsTool reads a connector's own Inbox rows for a things recipe (#6381, P4b of
// docs/design/proposals/inbox-sources-migration.md): the connector's own tool when its catalog declares one
// (iMessage's, #6317), else the subscriptions' generic one, which answers the connector's Collect, the very rows the
// Inbox's own round gets. It is the Inbox's own rows, for a built-in's default subscription only: not listed for
// people, and no subscription is made with it.
const InboxThingsTool = "inbox_things"

// inboxThings is a connector as the subscriptions read and call it: its catalog, and the generic inbox_things in its
// own source when it has an Inbox and declares none.
type inboxThings struct {
	connector.Connector
	declared bool // its own catalog has inbox_things: the call is its own
}

// withInboxThings is c as the subscriptions read its catalog: itself when it has no Inbox.
func withInboxThings(c connector.Connector) connector.Connector {
	if _, ok := connector.As[connector.Inbox](c); !ok || c == nil {
		return c
	}
	return &inboxThings{Connector: c}
}

func (w *inboxThings) Manifest() connector.Manifest {
	m := w.Connector.Manifest()
	if !m.Has(connector.CapCatalog) {
		m.Capabilities = append(slices.Clone(m.Capabilities), connector.CapCatalog)
	}
	return m
}

func (w *inboxThings) Catalog(ctx context.Context) ([]connector.CatalogSource, error) {
	sources, err := connector.ReadCatalog(ctx, w.Connector)
	if err != nil {
		return nil, err
	}
	at := slices.IndexFunc(sources, func(s connector.CatalogSource) bool { return s.ID == w.ID() })
	if at >= 0 && slices.ContainsFunc(sources[at].Tools, func(t connector.CatalogTool) bool { return t.Name == InboxThingsTool }) {
		w.declared = true
		return sources, nil
	}
	w.declared = false
	if at < 0 {
		sources = append(sources, connector.CatalogSource{ID: w.ID(), Name: w.Name(), Tools: []connector.CatalogTool{}})
		at = len(sources) - 1
	}
	t := prompts.Tool("connector_inbox_things", string(i18n.From(ctx)))
	sources[at].Tools = append(sources[at].Tools, connector.CatalogTool{Name: InboxThingsTool, Description: t.Description,
		InputSchema: t.InputSchema, ReadOnly: true})
	return sources, nil
}

// CatalogCall answers the generic inbox_things with the connector's Collect; every other call is the connector's.
func (w *inboxThings) CatalogCall(ctx context.Context, q connector.CatalogQuery) (string, error) {
	if q.Tool != InboxThingsTool || q.Source != w.ID() || w.declared {
		return connector.CallCatalog(ctx, w.Connector, q)
	}
	in, ok := connector.As[connector.Inbox](w.Connector)
	if !ok {
		return "", fmt.Errorf("%s has no inbox", w.ID())
	}
	things, err := in.Collect(ctx)
	if err != nil {
		return "", err
	}
	if things == nil {
		things = []connector.InboxThing{}
	}
	out, err := json.Marshal(map[string]any{"things": things})
	return string(out), err
}

// peopleTools are a catalog source's tools as people see them: inbox_things left out, which only a built-in's default
// subscription reads.
func peopleTools(tools []connector.CatalogTool) []connector.CatalogTool {
	return slices.DeleteFunc(slices.Clone(tools), func(t connector.CatalogTool) bool { return t.Name == InboxThingsTool })
}

// inboxThingsOnly refuses a subscription people make or change to read inbox_things, or a change of a default
// subscription's recipe: those rows are a built-in's, switched on its page.
func inboxThingsOnly() *Error {
	return errorf(http.StatusBadRequest, CodeBadRecipe,
		"inbox_things is the Inbox's own rows: a built-in source's default subscription reads them, switched on its page")
}
