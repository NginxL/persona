package connector

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

type capability struct {
	cap    Capability
	iface  reflect.Type
	listed bool
}

// capabilities are every capability with its interface, in the order GET /api/v1/connectors lists them ("hosts"
// goes after "events": it is listed when Manifest.Hosts has any).
var capabilities = []capability{
	{CapStatus, reflect.TypeFor[Accounts](), true},
	{CapTools, reflect.TypeFor[ReadTools](), true},
	{CapCatalog, reflect.TypeFor[Catalog](), true},
	{CapActions, reflect.TypeFor[Actions](), true},
	{CapEvents, reflect.TypeFor[Events](), true},
	{CapInbox, reflect.TypeFor[Inbox](), true},
	{CapSettle, reflect.TypeFor[InboxSettler](), false},
	{CapArchive, reflect.TypeFor[Archive](), true},
	{CapChats, reflect.TypeFor[Chats](), true},
	{CapItems, reflect.TypeFor[Items](), true},
	{CapLogin, reflect.TypeFor[Login](), true},
	{CapOptions, reflect.TypeFor[Options](), true},
	{CapThreads, reflect.TypeFor[Threads](), true},
	{CapSnapshot, reflect.TypeFor[Snapshot](), true},
	{CapGroup, reflect.TypeFor[Grouper](), false},
	{CapMeta, reflect.TypeFor[EventWriter](), false},
	{CapFallback, nil, false},
	{CapMachines, reflect.TypeFor[Machines](), false},
	{CapRecall, nil, false},
	{CapHistory, reflect.TypeFor[History](), true},
	{CapHistoryAnchors, reflect.TypeFor[HistoryAnchors](), true},
}

// Known says whether this hub has capability cp (Check refuses a connector declaring one it does not).
func Known(cp Capability) bool {
	return slices.ContainsFunc(capabilities, func(x capability) bool { return x.cap == cp })
}

// As is c as the capability interface T, when its manifest declares that capability: the one way the hub's pipes
// find a capability (a proxy implements every interface). T must be one of the capabilities' interfaces.
func As[T any](c Connector) (T, bool) {
	var zero T
	t := reflect.TypeFor[T]()
	i := slices.IndexFunc(capabilities, func(x capability) bool {
		return x.iface == t
	})
	if i < 0 {
		panic("connector.As: " + t.String() + " is not a capability")
	}
	if c == nil || !c.Manifest().Has(capabilities[i].cap) {
		return zero, false
	}
	v, ok := c.(T)
	return v, ok
}

// Check is what Registry.Add holds a connector's manifest to: every capability known and declared once, each
// declared one implemented, and (unless it is a proxy) each implemented one declared; kinds only with an inbox.
func Check(c Connector) error {
	m := c.Manifest()
	var errs []error
	for i, cp := range m.Capabilities {
		if !Known(cp) {
			errs = append(errs, fmt.Errorf("unknown capability %q", cp))
		} else if slices.Index(m.Capabilities, cp) != i {
			errs = append(errs, fmt.Errorf("capability %q declared twice", cp))
		}
	}
	impl := reflect.TypeOf(c)
	for _, x := range capabilities {
		if x.iface == nil { // a promise: nothing to implement
			continue
		}
		has, declared := impl.Implements(x.iface), m.Has(x.cap)
		switch {
		case declared && !has:
			errs = append(errs, fmt.Errorf("declares %q but is no %s", x.cap, ifaceName(x.iface)))
		case has && !declared && !m.Proxy:
			errs = append(errs, fmt.Errorf("is a %s but does not declare %q", ifaceName(x.iface), x.cap))
		}
	}
	if len(m.Kinds) > 0 && !m.Has(CapInbox) {
		errs = append(errs, errors.New("declares inbox kinds without an inbox"))
	}
	if m.Has(CapGroup) && !m.Has(CapInbox) {
		errs = append(errs, errors.New("declares group without an inbox"))
	}
	if m.Has(CapMeta) && !m.Has(CapGroup) {
		errs = append(errs, errors.New("declares meta without group"))
	}
	if m.Has(CapMeta) && strings.TrimSpace(m.MetaDescription) == "" {
		errs = append(errs, errors.New("declares meta without a meta_description"))
	}
	if !m.Has(CapMeta) && m.MetaDescription != "" {
		errs = append(errs, errors.New("has a meta_description without declaring meta"))
	}
	if m.Has(CapFallback) && !m.Has(CapInbox) {
		errs = append(errs, errors.New("declares fallback without an inbox"))
	}
	if m.Has(CapHistory) && !m.Has(CapArchive) {
		errs = append(errs, errors.New("declares history without an archive"))
	}
	if m.Has(CapHistoryAnchors) && !m.Has(CapHistory) {
		errs = append(errs, errors.New("declares history_anchors without history"))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("connector %s: %w", c.ID(), err)
	}
	return nil
}

// ifaceName is a capability interface's name in this package: connectorwire's HistoryAnchorsReader is HistoryAnchors.
func ifaceName(t reflect.Type) string {
	if t == reflect.TypeFor[HistoryAnchors]() {
		return "HistoryAnchors"
	}
	return t.Name()
}

// Capabilities are the names GET /api/v1/connectors lists for c: what its manifest declares, in a fixed order, with
// "hosts" when it has any.
func Capabilities(c Connector) []string {
	m := c.Manifest()
	out := []string{}
	for _, x := range capabilities {
		if x.listed && m.Has(x.cap) {
			out = append(out, string(x.cap))
		}
		if x.cap == CapEvents && len(m.Hosts) > 0 {
			out = append(out, "hosts")
		}
	}
	return out
}
