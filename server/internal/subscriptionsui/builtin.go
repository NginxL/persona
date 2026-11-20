package subscriptionsui

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

// Built-in sources (#5278, docs/extensions/subscriptions.md "Pages"): the connectors and mail that bring things into the
// Inbox or the knowledge base themselves, beside the subscriptions. The pages list them with the same controls as a
// subscription; what they change is the connector's own setting (the manager answers BuiltinSources), so a switch
// turned here shows on the connector's page and the other way round.

// Builtin is one built-in source as the pages show it.
type Builtin struct {
	// ID is its connector's ("mail" for every mail account); Name and Icon (a NavIcon) as people know it.
	ID, Name, Icon string
	// Extension is the extension that brings it, "" for the hub's own.
	Extension string
	// Status is its connector's state (connector.StateOK, StateNotSignedIn, StateDisabled…).
	Status string
	// Inbox and Archive say whether Add to Inbox and Add to Knowledge Base are on; nil: it has no such switch.
	Inbox, Archive *bool
	// InboxAlways: it brings things into the Inbox with no switch for it (mail).
	InboxAlways bool
	// InboxEvery and ArchiveEvery are how often the Inbox and the knowledge base read it, in seconds (0: unknown).
	InboxEvery, ArchiveEvery int
	// Read is the Inbox's latest read of it since the manager started; nil before the first.
	Read *Read
	// Accounts are what it reads one by one (mail accounts), each with its latest read.
	Accounts []Account
	// Rules are its own triage rules when it can have them (RulesOwn); RulesLocked: set where the pages can't change
	// them (manager.json). Without its own, its items follow GeneralRules (the Inbox's).
	Rules        string
	RulesOwn     bool
	RulesLocked  bool
	GeneralRules string
	// Mutable: its items can be muted by chat, sender or repository (the Inbox's menu).
	Mutable bool
	// Via is its default subscription (#6345); nil when it has none.
	Via *Via
}

// Via is a built-in source's default subscription: On while its Inbox feed is read through it (P5a), and Subscription
// its id once it was made.
type Via struct {
	On           bool
	Subscription string
}

// Read is one read of a source by the Inbox: when, and the short code of its failure ("" read; ReadTimeout…).
type Read struct {
	At    time.Time
	Error string
	// OK is the latest read that worked, when there was one.
	OK *time.Time
}

// Account is one account of a built-in source, read on its own.
type Account struct {
	ID, Name string
	Read     *Read
}

// Mute is a chat, sender or repository of a source muted in the Inbox.
type Mute struct {
	ID, Source, Tag, Name string
	At                    time.Time
}

// BuiltinSources is what the pages use of the manager's built-in sources.
type BuiltinSources interface {
	List(ctx context.Context) []Builtin
	// SetFeed turns Add to Inbox (inbox) or Add to Knowledge Base on or off: the connector's own switch.
	SetFeed(ctx context.Context, id string, inbox, on bool, by subscriptions.Actor) error
	// SetRules sets the source's own triage rules ("" clears them).
	SetRules(ctx context.Context, id, rules string, by subscriptions.Actor) error
	// Mutes are a source's mutes ("" every source's); ErrInboxAway while the Inbox can't say.
	Mutes(ctx context.Context, source string) ([]Mute, error)
	Unmute(ctx context.Context, id string) error
	// Native is the built-in source that reads a catalog service ("mail:example.com") and where it reads it
	// ("me@example.com", a connector's name); "" when none does.
	Native(service string) (id, where string)
	// SetMigrated reads the source's Inbox feed through its default subscription (on) or as before (#6345).
	SetMigrated(ctx context.Context, id string, on bool, by subscriptions.Actor) error
}

// builtins are the manager's built-in sources, by name; none without BuiltinSources.
func (p *pages) builtins() []Builtin {
	if p.env.Builtin == nil {
		return nil
	}
	if p.listed == nil {
		list := slices.Clone(p.env.Builtin.List(p.ctx)) // sorted here, not in the caller's slice
		slices.SortStableFunc(list, func(a, b Builtin) int { return cmp.Compare(a.Name, b.Name) })
		p.listed = &list
	}
	return *p.listed
}

func (p *pages) builtin(id string) (Builtin, bool) {
	for _, b := range p.builtins() {
		if b.ID == id {
			return b, true
		}
	}
	return Builtin{}, false
}

// native is where Persona already reads a catalog service itself, and the built-in source that does ("" when none
// is one of the pages', such as a host only).
func (p *pages) native(service string) (id, where string) {
	if service == "" {
		return "", ""
	}
	if p.env.Builtin != nil {
		if id, where = p.env.Builtin.Native(service); where != "" {
			return id, where
		}
	}
	return "", p.env.Native(service)
}

// duplicates are the subscriptions that read a service built-in source id already reads.
func (p *pages) duplicates(id string, subs []subscriptions.Subscription) []subscriptions.Subscription {
	var out []subscriptions.Subscription
	for _, s := range subs {
		if b, _ := p.native(s.Service); b == id && s.State != subscriptions.StateOff {
			out = append(out, s)
		}
	}
	return out
}

// trouble says what keeps a built-in source from reading: "" when nothing does. Off is not trouble.
func (p *pages) builtinTrouble(b Builtin) string {
	switch b.Status {
	case "ok", "rate_limited", "disabled":
	case "not_signed_in", "not_configured":
		return p.t("builtin.signed_out")
	case "no_permission":
		return p.t("builtin.no_permission")
	default:
		return p.t("builtin.status_error")
	}
	if b.Read != nil && b.Read.Error != "" {
		return p.readProblem(b.Read)
	}
	for _, a := range b.Accounts {
		if a.Read != nil && a.Read.Error != "" {
			return p.t("builtin.account_failed", i18n.Args{"account": a.Name, "problem": p.readProblem(a.Read)})
		}
	}
	return ""
}

// readProblem is a failed read as people read it.
func (p *pages) readProblem(r *Read) string {
	key := "read." + r.Error
	if !i18n.Has("subscriptions-pages." + key) {
		key = "read.failed"
	}
	return p.t(key, i18n.Args{"time": p.clock(&r.At)})
}

// builtinDest is where a built-in source's things go, in a word or two.
func (p *pages) builtinDest(b Builtin) string {
	inbox := b.InboxAlways || b.Inbox != nil && *b.Inbox
	kb := b.Archive != nil && *b.Archive
	switch {
	case b.Status == "disabled":
		return p.t("builtin.disabled")
	case inbox && kb:
		return p.t("dest.both")
	case inbox:
		return p.t("dest.inbox")
	case kb:
		return p.t("dest.kb")
	}
	return p.t("dest.none")
}

// builtinLine is a built-in source's row: where its things go, how often, and its latest read.
func (p *pages) builtinLine(b Builtin) string {
	if t := p.builtinTrouble(b); t != "" {
		return t
	}
	parts := []string{p.builtinDest(b)}
	if b.Status == "disabled" {
		return parts[0]
	}
	if b.InboxEvery > 0 && (b.InboxAlways || b.Inbox != nil && *b.Inbox) {
		parts = append(parts, p.every(b.InboxEvery, ""))
	}
	if r := lastRead(b); r != nil {
		parts = append(parts, p.t("builtin.read_at", i18n.Args{"when": p.ago(r)}))
	}
	return join(parts)
}

// lastRead is the latest read of b that worked, its accounts' included.
func lastRead(b Builtin) *time.Time {
	var at *time.Time
	consider := func(r *Read) {
		if r != nil && r.OK != nil && (at == nil || r.OK.After(*at)) {
			at = r.OK
		}
	}
	consider(b.Read)
	for _, a := range b.Accounts {
		consider(a.Read)
	}
	return at
}

// builtinRow is a built-in source in the list.
func (p *pages) builtinRow(b Builtin, subs []subscriptions.Subscription) wire.Component {
	r := row("b."+b.ID, b.Name)
	r.Icon, r.IconColor = p.icon(cmp.Or(b.Icon, "plugin")), colorFor(b.ID)
	r.Subtitle = p.builtinLine(b)
	switch {
	case p.builtinTrouble(b) != "":
		r.Badge = p.builtinBadge(b)
	case len(p.duplicates(b.ID, subs)) > 0:
		r.Badge = badge(p.t("badge.maybe_duplicate"), "caution")
	}
	r.Action = open(state{P: pageBuiltin, C: b.ID}, b.Name)
	return r
}

// ⑩ A built-in source: the same sections as a subscription's, with what it can't change here said in plain words.
func (p *pages) builtinPage() wire.View {
	b, ok := p.builtin(p.st.C)
	if !ok {
		return p.gone()
	}
	v := wire.View{Title: b.Name, State: state{P: pageBuiltin, C: b.ID}.String()}
	subs, _, _ := p.svc.List(p.ctx, subscriptions.Filter{})
	if dups := p.duplicates(b.ID, subs); len(dups) > 0 {
		sec := wire.Section{Key: "dup", Title: p.t("builtin.dup_title"), Footer: p.t("builtin.dup_footer")}
		for _, s := range dups {
			r := row("dup."+s.ID, s.Label)
			r.Subtitle, r.Icon, r.IconColor = p.t("builtin.dup_line"), "warning", "orange"
			r.Action = open(state{P: pageSource, ID: s.ID}, s.Label)
			sec.Items = append(sec.Items, r)
		}
		v.Sections = append(v.Sections, sec)
	}
	if t := p.builtinTrouble(b); t != "" {
		r := row("e.now", t)
		r.Icon, r.IconColor = "warning", "orange"
		v.Sections = append(v.Sections, wire.Section{Key: "trouble", Title: p.t("builtin.trouble_title"),
			Items: []wire.Component{r, text("e.why", p.t("builtin.trouble_why"), "caption")}})
	}
	v.Sections = append(v.Sections, p.builtinSwitches(b))
	if b.Via != nil {
		v.Sections = append(v.Sections, p.builtinVia(b))
	}
	via := b.Via != nil && b.Via.On
	freq := wire.Section{Key: "freq", Title: p.t("builtin.freq"), Footer: p.t("builtin.freq_footer")}
	if b.InboxEvery > 0 && (b.InboxAlways || b.Inbox != nil) && !via { // through its subscription: on its page
		r := row("f.inbox", p.t("builtin.freq_inbox"))
		r.Value = p.every(b.InboxEvery, "")
		freq.Items = append(freq.Items, r)
	}
	if b.ArchiveEvery > 0 && b.Archive != nil {
		r := row("f.kb", p.t("builtin.freq_kb"))
		r.Value = p.every(b.ArchiveEvery, "")
		freq.Items = append(freq.Items, r)
	}
	if len(freq.Items) > 0 {
		v.Sections = append(v.Sections, freq)
	}
	v.Sections = append(v.Sections, p.builtinStatus(b))
	how := wire.Section{Key: "how", Title: p.t("builtin.how")}
	rules := row("h.rules", p.t("source.rules"))
	switch {
	case b.RulesOwn && b.Rules != "":
		rules.Subtitle = b.Rules
	case b.RulesOwn:
		rules.Subtitle = p.t("builtin.rules_general")
	default:
		rules.Subtitle = p.t("builtin.rules_none")
	}
	if b.RulesOwn && !b.RulesLocked {
		rules.Action = open(state{P: pageRules, C: b.ID}, p.t("source.rules"))
	}
	how.Items = append(how.Items, rules)
	if b.Mutable {
		how.Items = append(how.Items, p.mutesRow(b.ID))
	}
	v.Sections = append(v.Sections, how)
	return v
}

// builtinSwitches are a built-in source's two destinations: its connector's own switches, asking first as a
// subscription's do; one it always uses is a line, one it doesn't have is left out.
func (p *pages) builtinSwitches(b Builtin) wire.Section {
	sec := wire.Section{Key: "dest", Title: p.t("source.dest")}
	if b.Status == "disabled" {
		sec.Footer = p.t("builtin.disabled_footer")
	}
	sw := func(name string, on bool, subtitle string) wire.Component {
		r := row("d."+name, p.t("dest."+name))
		r.Subtitle = subtitle
		act := wire.Action{ID: "builtin_dest", Values: map[string]string{"to": name}}
		if !on {
			act.Confirm = p.builtinConfirm(name, b)
		}
		r.Control = &wire.RowControl{Kind: "switch", Name: name, On: on, Action: act}
		return r
	}
	switch {
	case b.Via != nil && b.Via.On && b.Inbox != nil: // its rows come through its default subscription (#6345)
		r := row("d.to_inbox", p.t("dest.to_inbox"))
		r.Value, r.Subtitle = p.t("builtin.via_inbox"), p.t("builtin.via_inbox_detail")
		sec.Items = append(sec.Items, r)
	case b.InboxAlways:
		r := row("d.to_inbox", p.t("dest.to_inbox"))
		r.Value, r.Subtitle = p.t("builtin.always"), p.t("builtin.always_detail")
		sec.Items = append(sec.Items, r)
	case b.Inbox != nil && b.ID == "mail": // every account's unread mail (#5307)
		sec.Items = append(sec.Items, sw("to_inbox", *b.Inbox, p.t("builtin.always_detail")))
	case b.Inbox != nil:
		sec.Items = append(sec.Items, sw("to_inbox", *b.Inbox, p.t("builtin.inbox_detail")))
	}
	if b.Archive != nil {
		sec.Items = append(sec.Items, sw("to_kb", *b.Archive, p.t("source.kb_detail")))
	}
	return sec
}

// builtinVia is a built-in source's switch to read it through its default subscription (#6345): asked once when
// turned on (the source stays on, decision 2 of the migration), at once back; while on, a row to the subscription.
func (p *pages) builtinVia(b Builtin) wire.Section {
	args := i18n.Args{"source": b.Name}
	r := row("v.via", p.t("builtin.via"))
	r.Subtitle = p.t("builtin.via_detail", args)
	act := wire.Action{ID: "builtin_via"}
	if !b.Via.On {
		act.Confirm = p.t("builtin.via_confirm", args)
	}
	r.Control = &wire.RowControl{Kind: "switch", Name: "via", On: b.Via.On, Action: act}
	sec := wire.Section{Key: "via", Title: p.t("builtin.via_title"), Footer: p.t("builtin.via_footer", args), Items: []wire.Component{r}}
	if b.Via.On && b.Via.Subscription != "" {
		o := row("v.open", p.t("builtin.via_open"))
		o.Subtitle = p.t("builtin.via_open_detail")
		o.Action = open(state{P: pageSource, ID: b.Via.Subscription}, p.t("builtin.via_open"))
		sec.Items = append(sec.Items, o)
	}
	return sec
}

// builtinSwitchVia switches a built-in source's Inbox feed to its default subscription or back, and says so.
func (p *pages) builtinSwitchVia(values map[string]string) (wire.ActionResult, error) {
	b, ok := p.builtin(p.st.C)
	if p.env.Builtin == nil || !ok || b.Via == nil {
		return wire.ActionResult{}, errBadValues
	}
	on := values["via"] == "true"
	if err := p.env.Builtin.SetMigrated(p.ctx, b.ID, on, p.env.By); err != nil {
		return p.builtinFailed(err)
	}
	p.listed = nil // read again with the change
	v := p.draw()
	toast := "toast.via_off"
	if on {
		toast = "toast.via_on"
	}
	return wire.ActionResult{View: &v, Toast: p.t(toast, i18n.Args{"source": b.Name})}, nil
}

// builtinConfirm is what turning a built-in source's destination on asks: as a subscription's, what it reads and
// which model sorts it; nothing is pulled on a schedule the pages set.
func (p *pages) builtinConfirm(dest string, b Builtin) string {
	purpose, every := "inbox.triage", b.InboxEvery
	if dest == "to_kb" {
		purpose, every = "kb", b.ArchiveEvery
	}
	provider := ""
	if m := p.modelOf(purpose); m.OK && m.Provider != "" {
		provider = p.t("confirm.provider", i18n.Args{"name": modelName(m)})
	}
	often := p.t("builtin.often_own")
	if every > 0 {
		often = p.every(every, "")
	}
	return p.t("builtin.confirm."+dest, i18n.Args{"source": b.Name, "every": often, "provider": provider})
}

// builtinStatus is how a built-in source is doing: its connection, its latest read, and each account's.
func (p *pages) builtinStatus(b Builtin) wire.Section {
	sec := wire.Section{Key: "status", Title: p.t("source.status"), Footer: p.t("builtin.status_footer")}
	if b.Via != nil && b.Via.On {
		sec.Footer = "" // its reads are its subscription's runs, which a restart keeps (#11762)
	}
	conn := row("st.conn", p.t("builtin.connection"))
	conn.Value = p.t("builtin.status." + statusKey(b.Status))
	sec.Items = append(sec.Items, conn)
	readRow := func(key, title string, r *Read) wire.Component {
		c := row(key, title)
		switch {
		case r == nil:
			c.Value = p.t("builtin.not_read")
		case r.Error != "":
			c.Subtitle, c.Icon, c.IconColor = p.readProblem(r), "warning", "orange"
			if r.OK != nil {
				c.Value = p.ago(r.OK)
			}
		default:
			c.Value = p.ago(r.OK)
		}
		return c
	}
	if len(b.Accounts) == 0 {
		sec.Items = append(sec.Items, readRow("st.last", p.t("builtin.last_read"), b.Read))
	}
	for _, a := range b.Accounts {
		sec.Items = append(sec.Items, readRow("st.a."+a.ID, a.Name, a.Read))
	}
	return sec
}

func statusKey(s string) string {
	switch s {
	case "ok", "disabled", "not_signed_in", "not_configured", "no_permission", "rate_limited":
		return s
	}
	return "error"
}

// mutesRow opens what is muted of a source in the Inbox.
func (p *pages) mutesRow(source string) wire.Component {
	r := row("h.mutes", p.t("mutes.row"))
	mutes, err := p.env.Builtin.Mutes(p.ctx, source)
	switch {
	case err != nil:
		r.Value = p.t("mutes.unknown")
	case len(mutes) == 0:
		r.Value = p.t("mutes.none")
	default:
		r.Value = p.t("mutes.count", i18n.Args{"count": len(mutes)})
	}
	r.Action = open(state{P: pageMutes, C: source}, p.t("mutes.row"))
	return r
}

// ⑪ What is muted of a source: each chat, sender or repository with Unmute; what a mute filed stays done.
func (p *pages) mutes() wire.View {
	v := wire.View{Title: p.t("mutes.row")}
	if p.env.Builtin == nil {
		return p.gone()
	}
	mutes, err := p.env.Builtin.Mutes(p.ctx, p.st.C)
	if err != nil {
		return p.failed(v.Title, err)
	}
	if len(mutes) == 0 {
		v.Sections = []wire.Section{{Items: []wire.Component{{Type: "empty", Key: "none", Title: p.t("mutes.none_title"),
			Detail: p.t("mutes.none_detail"), Icon: "bell"}}}}
		return v
	}
	sec := wire.Section{Key: "mutes", Footer: p.t("mutes.footer")}
	slices.SortFunc(mutes, func(a, b Mute) int { return b.At.Compare(a.At) })
	for _, m := range mutes {
		r := row("m."+m.ID, m.Name)
		r.Subtitle = join([]string{p.t("mutes.tag." + cmp.Or(m.Tag, "sender")), p.when(m.At)})
		r.Value = p.t("mutes.unmute")
		r.Action = &wire.Action{ID: "unmute", Values: map[string]string{"mute": m.ID},
			Confirm: p.t("mutes.unmute_confirm", i18n.Args{"name": m.Name})}
		sec.Items = append(sec.Items, r)
	}
	v.Sections = []wire.Section{sec}
	return v
}

// builtinDestination turns a built-in source's Add to Inbox or Add to Knowledge Base on or off: its connector's switch.
func (p *pages) builtinDestination(values map[string]string) (wire.ActionResult, error) {
	to := values["to"]
	if p.env.Builtin == nil || to != "to_inbox" && to != "to_kb" {
		return wire.ActionResult{}, errBadValues
	}
	if err := p.env.Builtin.SetFeed(p.ctx, p.st.C, to == "to_inbox", values[to] == "true", p.env.By); err != nil {
		return p.builtinFailed(err)
	}
	p.listed = nil // read again with the change
	v := p.draw()
	return wire.ActionResult{View: &v}, nil
}

// builtinRules saves a built-in source's own triage rules and goes back a page.
func (p *pages) builtinRules(rules string) (wire.ActionResult, error) {
	if p.env.Builtin == nil {
		return wire.ActionResult{}, errBadValues
	}
	if err := p.env.Builtin.SetRules(p.ctx, p.st.C, rules, p.env.By); err != nil {
		return p.builtinFailed(err)
	}
	return wire.ActionResult{Pop: true, Toast: p.t("toast.saved")}, nil
}

// unmute takes a mute away; what it filed stays done.
func (p *pages) unmute(id string) (wire.ActionResult, error) {
	if p.env.Builtin == nil || id == "" {
		return wire.ActionResult{}, errBadValues
	}
	if err := p.env.Builtin.Unmute(p.ctx, id); err != nil {
		return p.builtinFailed(err)
	}
	v := p.draw()
	return wire.ActionResult{View: &v, Toast: p.t("toast.unmuted")}, nil
}

// builtinFailed is a built-in source's change that didn't go through: the page as it is, and a toast.
func (p *pages) builtinFailed(err error) (wire.ActionResult, error) {
	if se := (*subscriptions.Error)(nil); errors.As(err, &se) {
		return p.errorResult(err)
	}
	p.listed = nil
	v := p.draw()
	return wire.ActionResult{View: &v, Toast: p.t("toast.builtin_failed")}, nil
}

// builtinRulesPage is a built-in source's own triage rules: free text, its items only.
func (p *pages) builtinRulesPage() wire.View {
	b, ok := p.builtin(p.st.C)
	if !ok || !b.RulesOwn {
		return p.gone()
	}
	field := wire.Component{Type: "field", Key: "r.text", Name: "triage_rules", Label: p.t("rules.label"), Kind: "multiline",
		Value: b.Rules, Placeholder: p.t("rules.placeholder")}
	save := button("b.save", p.t("rules.save"), "rules")
	save.Action.Submit = true
	return wire.View{Title: p.t("source.rules"), Sections: []wire.Section{
		{Key: "rules", Footer: p.t("builtin.rules_footer"), Items: []wire.Component{field}},
		{Key: "save", Items: []wire.Component{save}},
	}}
}

// builtinBadge names what keeps a built-in source from reading, for its row: its connection, else a failed read.
func (p *pages) builtinBadge(b Builtin) *wire.Badge {
	switch b.Status {
	case "ok", "rate_limited", "disabled":
		return badge(p.t("badge.read_failed"), "caution")
	}
	return badge(p.t("builtin.status."+statusKey(b.Status)), "caution")
}
