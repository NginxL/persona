package subscriptions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Delivery to the Inbox and the knowledge base (#2039, docs/extensions/subscriptions.md "Destinations"). Every
// subscription is an internal source of the manager. The Inbox reads its things every collection round
// (InboxSources), made from the held items the way a connector answers Collect from its memory; the knowledge base
// pages through the held items from a cursor it keeps itself (Changes). Nothing here records what a destination
// took: the Inbox knows that by thread and last, the knowledge base by its cursor and message-id receipts, so a
// replay or a restarted manager hands them the same things again and they add nothing.

const (
	catchupBack     = 24 * time.Hour     // the Inbox catches up on a day before it was turned on; the knowledge base starts there too
	digestOver      = 3                  // more catch-up items than this come in as one digest
	digestLinesMost = 50                 // lines of a digest; the rest are counted
	catchupReadMost = 500                // catch-up items read for a digest
	inboxReadMost   = 300                // the newest items a round reads of one subscription
	inboxWindow     = 7 * 24 * time.Hour // the Inbox's: an older item starts nothing
	kbPageMost      = 100                // items a knowledge-base call takes
	previewMost     = 200
	contextBodyMost = 3000
	threadLinesMost = 10 // a row's earlier items listed above its newest
	failingNotice   = 24 * time.Hour
)

// KindFailing is the kind of a subscription's failure notice in the Inbox, beside the recipes' kinds.
const KindFailing = "sub_failing"

// itemKinds are the kinds a subscription's Inbox items can have, each described for the models.
var itemKinds = append(slices.Clone(wire.ConnectorSubscriptionKinds), KindFailing)

// InboxSource is one subscription as a source of an Inbox collection round.
type InboxSource struct {
	ID     string            // the subscription's id: its items' source
	Name   string            // its label
	Kinds  map[string]string // what its kinds of items are, for the models
	Things []connector.InboxThing
	// ReadError says it is behind while its Inbox is on (readErrorOf): the short code a built-in's failed read has.
	ReadError string
	// Connector and SourceName are what it reads, for the AI twin; Chats the knowledge-base chat of each row's thread
	// (deliverer.chatOf) while its knowledge base is on (#6222).
	Connector  string
	SourceName string
	Chats      map[string]string
	// Switched: it is a built-in's Inbox feed now (MigratedFrom), under the built-in's id (#6265), which the manager
	// gives the built-in's abilities in the round (#6381). ConnectorRows: its rows are the connector's own (a things
	// recipe), whose counts are the connector's; otherwise the subscription makes them, with its counts (#5671).
	Switched, ConnectorRows bool
}

// InboxSources are the subscriptions as Inbox sources: one's items while its Inbox is on, its read error while it is
// behind (#6189), a notice once it has failed for a day, and the notice's end once it pulls again.
func (s *Service) InboxSources(ctx context.Context) ([]InboxSource, error) {
	all, err := s.o.Store.ListSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := []InboxSource{}
	replying := s.replying(ctx)
	for _, sub := range all {
		src := InboxSource{ID: sub.ID, Name: sub.Label, Kinds: kindsOf(sub.Label, s.o.Lang()), Things: []connector.InboxThing{}}
		builtin, migrated := MigratedFrom(sub)
		if migrated {
			src.ID, src.Switched = builtin, true // the built-in's Inbox feed now: under its own source id (#6265)
		}
		src.ConnectorRows = sub.Recipe.Mode == wire.ConnectorSubscriptionModeThings
		catchup, live, err := s.inboxHeld(ctx, sub, now)
		if err != nil {
			return nil, err
		}
		d := s.deliverer(sub, now)
		d.replies = replying[sub.Connector] && sub.RunState != StateNotConnected
		d.migrated = migrated
		if kbFrom(sub) != nil {
			d.chats = map[string]string{}
		}
		src.Things = append(src.Things, d.inboxThings(catchup, live)...)
		if !migrated { // a built-in's feed reads as the built-in's to the AI twin
			src.Connector, src.SourceName, src.Chats = sub.Connector, cmpOr(sub.SourceName, sub.Source), d.chats
		}
		src.Things = append(src.Things, d.failing())
		if inboxFrom(sub) != nil {
			src.ReadError = readErrorOf(sub.RunState)
		}
		out = append(out, src)
	}
	return out, nil
}

// replying are the connectors that are on and answer an inbox reply (connector.Inbox), by id. Their status is not
// asked on every round (it may be a call to the extension): a subscription that could not reach its connector says so
// itself (StateNotConnected), and ReplyTarget asks before a reply goes.
func (s *Service) replying(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	if s.o.Connectors == nil { // a service that only delivers (the assistant's tests): no connector to reply through
		return out
	}
	for _, info := range s.o.Connectors.All(ctx) {
		if _, ok := connector.As[connector.Inbox](info.Conn); ok && info.Enabled {
			out[info.ID] = true
		}
	}
	return out
}

// ReplyTarget is where a reply to a row of subscription id goes (#6198): its connector, and the thread the connector
// knows the row's newest item by (the recipe's thread, else the item's id). row is the row's thread and last its newest
// item (the thing's Last, the item's id in the source). A things recipe's row is the connector's own (#6296): its thread
// as it is. err says why there is none (#9194): connector.ErrCannotReply when the row cannot be answered (no newest
// item, or none with an id of the source's; the item or its reply_to is gone; the connector does not reply), so it is
// refused as such a row; errReplyUnavailable when the subscription is gone or its connector cannot be called now.
func (s *Service) ReplyTarget(ctx context.Context, id, row, last string) (conn connector.Inbox, thread string, err error) {
	sub, err := s.o.Store.GetSubscription(ctx, id)
	if err != nil || sub == nil || s.o.Connectors == nil {
		return nil, "", errReplyUnavailable
	}
	if last == "" || strings.HasPrefix(last, weakPrefix) {
		return nil, "", fmt.Errorf("%w: the row has no item of the source's to answer", connector.ErrCannotReply)
	}
	thread = row
	if sub.Recipe.Mode != wire.ConnectorSubscriptionModeThings {
		h, err := s.o.Store.HeldItem(ctx, id, last)
		if err != nil {
			return nil, "", errReplyUnavailable
		}
		if h == nil || h.ReplyTo == "" {
			return nil, "", fmt.Errorf("%w: the row's item is gone or says nowhere to reply", connector.ErrCannotReply)
		}
		thread = cmpOr(h.Thread, h.UID)
	}
	info, found := s.o.Connectors.Find(ctx, sub.Connector)
	if !found || !info.ready() {
		return nil, "", errReplyUnavailable
	}
	in, ok := connector.As[connector.Inbox](info.Conn)
	if !ok {
		return nil, "", fmt.Errorf("%w: the connector does not reply", connector.ErrCannotReply)
	}
	return in, thread, nil
}

// errReplyUnavailable is ReplyTarget's answer for a subscription that is gone or whose connector cannot be called now:
// the reply's source is not here, whatever the row (#9194).
var errReplyUnavailable = errors.New("subscription: no connector to reply through")

// readErrorOf is how a subscription in state says it is behind, as a built-in's failed read does (#6189): its last run
// failed, or nothing is read until its recipe is fixed, or the connector asked it to wait. A connector that cannot be
// called now is no failure (a built-in's is off), and the others read.
func readErrorOf(state string) string {
	switch state {
	case StateFailing, StateNeedsRecipe:
		return assistantwire.ReadFailed
	case StateRateLimited:
		return assistantwire.ReadRateLimited
	}
	return ""
}

func kindsOf(label string, lang i18n.Lang) map[string]string {
	language := ""
	if lang != i18n.Default && i18n.Valid(lang) {
		language = lang.Name()
	}
	out := make(map[string]string, len(itemKinds))
	for _, k := range itemKinds {
		out[k] = prompts.RenderSubscriptionKind(prompts.SubscriptionKind{Language: language, Kind: k, Label: label})
	}
	return out
}

// inboxFrom is when sub's Inbox was turned on; nil while it is off. One turned on before the mark was kept counts
// from when it was made.
func inboxFrom(sub Subscription) *time.Time {
	switch {
	case sub.Migrated != nil:
		return sub.Migrated // a built-in's Inbox feed since then (#6265)
	case !sub.ToInbox:
		return nil
	case sub.InboxFrom != nil:
		return sub.InboxFrom
	}
	return &sub.CreatedAt
}

// kbFrom is when sub's knowledge base was turned on; nil while it is off.
func kbFrom(sub Subscription) *time.Time {
	switch {
	case !sub.ToKB, sub.Recipe.Mode == wire.ConnectorSubscriptionModeThings: // a things one's rows are the Inbox's only
		return nil
	case sub.KBFrom != nil:
		return sub.KBFrom
	}
	return &sub.CreatedAt
}

// inboxHeld are the held items the Inbox gets of sub, once the first run since it was turned on has read the
// source: the catch-up (stored from a day before it was turned on up to that run) and the ones stored later, the
// newest of the Inbox's window.
func (s *Service) inboxHeld(ctx context.Context, sub Subscription, now time.Time) (catchup, live []Held, err error) {
	from, mark := inboxFrom(sub), sub.InboxCatchup
	if from == nil || mark == nil || mark.Before(*from) {
		return nil, nil, nil
	}
	start := from.Add(-catchupBack)
	if c := sub.CatchupFrom; c != nil && c.After(start) {
		start = *c // a recipe changed with existing digest: the catch-up is what the new recipe found
	}
	// The catch-up is read while it is in the Inbox's window, which ends before its first items can be pruned (a
	// digest of fewer items would read as a new move).
	if now.Sub(start) < inboxWindow {
		held, err := s.o.Store.HeldItems(ctx, sub.ID, ItemQuery{Since: start, Limit: catchupReadMost})
		if err != nil {
			return nil, nil, err
		}
		for _, h := range held {
			if !h.Stored.After(*mark) {
				catchup = append(catchup, h)
			}
		}
	}
	since := now.Add(-inboxKeep)
	if since.Before(start) {
		since = start
	}
	held, err := s.o.Store.HeldItems(ctx, sub.ID, ItemQuery{Since: since, Limit: inboxReadMost, Newest: true})
	if err != nil {
		return nil, nil, err
	}
	for _, h := range held {
		if h.Stored.After(*mark) {
			live = append(live, h)
		}
	}
	return catchup, live, nil
}

// deliverer makes one subscription's things and batches for the destinations.
type deliverer struct {
	sub   Subscription
	now   time.Time
	lang  i18n.Lang
	loc   *time.Location
	rules string
	// replies: the subscription's connector is on and answers an inbox reply (Service.replying), so a row whose newest
	// item has reply_to can be replied to (#6198).
	replies bool
	// chats, while the subscription's knowledge base is on, gets each row's thread → its knowledge-base chat (#6222).
	chats map[string]string
	// migrated: the subscription is a built-in's Inbox feed (MigratedFrom, #6265): its rows have the items' own threads,
	// and its catch-up comes as rows, the ones the built-in already brought known by their ids.
	migrated bool
}

func (s *Service) deliverer(sub Subscription, now time.Time) deliverer {
	rules := strings.TrimSpace(sub.TriageRules)
	if rules == "" && sub.Extension != "" {
		rules = strings.TrimSpace(s.o.ExtensionRules(sub.Extension))
	}
	return deliverer{sub: sub, now: now, lang: s.o.Lang(), loc: s.o.Location(), rules: clip(rules, 4000)}
}

// heldAt is when an item happened as the destinations see it: its time, never later than when it was stored.
func heldAt(h Held) time.Time {
	if h.Time != nil && h.Time.Before(h.Stored) {
		return *h.Time
	}
	return h.Stored
}

// inboxThings are the Inbox's things of the catch-up and later items: a snapshot's newest state; the catch-up's
// items as rows, or past digestOver as one digest with the ones still due kept as rows; then the rows, one a thread,
// or one for the whole subscription when its recipe groups by source (#5671), of the items stored since the rows'
// grouping started (rowsFrom).
func (d deliverer) inboxThings(catchup, live []Held) []connector.InboxThing {
	if d.sub.Recipe.Mode == wire.ConnectorSubscriptionModeThings {
		return thingRows(append(slices.Clone(catchup), live...))
	}
	var snaps, rows []Held
	var digest []Held
	for _, h := range catchup {
		switch {
		case h.Snapshot:
			snaps = append(snaps, h)
		case h.DueDate != nil && h.DueDate.After(*d.sub.InboxCatchup):
			rows = append(rows, h) // still due when it came in: never buried in the digest
		default:
			digest = append(digest, h)
		}
	}
	if len(digest) <= digestOver || d.migrated {
		rows, digest = append(rows, digest...), nil
	}
	for _, h := range live {
		if h.Snapshot {
			snaps = append(snaps, h)
		} else {
			rows = append(rows, h)
		}
	}
	var out []connector.InboxThing
	if len(snaps) > 0 {
		out = append(out, d.snapshotThing(slices.MaxFunc(snaps, func(a, b Held) int { return int(a.Seq - b.Seq) })))
	}
	if len(digest) > 0 {
		out = append(out, d.digestThing(digest))
	}
	from, grouped := rowsFrom(d.sub)
	byThread := groupsByThread(d.sub.Recipe)
	threads := map[string][]Held{}
	var order []string
	for _, h := range rows {
		if !grouped || from != nil && h.Stored.Before(*from) {
			continue // came in as a row grouped otherwise; or the next run says from when the new grouping starts
		}
		key := rowAll
		if byThread {
			key = cmpOr(h.Thread, h.UID)
		}
		if _, ok := threads[key]; !ok {
			order = append(order, key)
		}
		threads[key] = append(threads[key], h)
	}
	for _, key := range order {
		t := d.threadThing(key, threads[key])
		if !byThread {
			// The subscription's one row reads as a group chat's: titled by its name, with no tag saying it again.
			t.Title, t.Label = d.sub.Label, ""
		}
		out = append(out, t)
	}
	return out
}

// thingRows are a things recipe's rows (#6296): each thread's newest kept row, exactly as the connector made it (no
// prefix, tag, tier or count added, no digest), in the order they were kept.
func thingRows(held []Held) []connector.InboxThing {
	slices.SortStableFunc(held, func(a, b Held) int { return int(a.Seq - b.Seq) })
	newest := map[string]int{}
	var rows []connector.InboxThing
	for _, h := range held {
		var t connector.InboxThing
		if len(h.Thing) == 0 || json.Unmarshal(h.Thing, &t) != nil {
			continue // kept by another recipe, or a row this build cannot read
		}
		if i, ok := newest[t.Thread]; ok {
			rows[i] = t
			continue
		}
		newest[t.Thread] = len(rows)
		rows = append(rows, t)
	}
	return rows
}

// rowAll is the key of a subscription's one row when its recipe groups by source, as its knowledge-base chat is "all".
const rowAll = "all"

// groupsByThread says whether a recipe's items are grouped a thread each, in the Inbox and the knowledge base alike;
// otherwise (source) the whole subscription is one row and one chat.
func groupsByThread(r Recipe) bool { return r.GroupBy == wire.ConnectorSubscriptionGroupThread }

// groupOf is the grouping r's items get: thread or source.
func groupOf(r Recipe) string {
	if groupsByThread(r) {
		return wire.ConnectorSubscriptionGroupThread
	}
	return wire.ConnectorSubscriptionGroupSource
}

// rowsGroup is how sub's Inbox rows were made: its InboxGroup, or for rows from before #5671 a thread each (an item
// without a thread its own), whatever the recipe said.
func rowsGroup(sub Subscription) string {
	return cmpOr(sub.InboxGroup, wire.ConnectorSubscriptionGroupThread)
}

// rowsFrom is when sub's rows start: they take the items stored from then on (nil: all of them). ok is false while the
// recipe groups otherwise than the rows were made, until its next run that succeeds marks the change (markRows): the
// items before it came in as rows of their own, and handing them in again regrouped would bring them in twice.
func rowsFrom(sub Subscription) (from *time.Time, ok bool) {
	if rowsGroup(sub) != groupOf(sub.Recipe) {
		return nil, false
	}
	return sub.InboxGroupFrom, true
}

// markRows is next after a run that succeeded at start: the run that ends the Inbox's catch-up starts its rows with
// the recipe's grouping (the catch-up holds no item delivered before), and one after the grouping changed (a recipe
// changed without a new catch-up, or rows from before #5671) starts the new grouping with this run's items. The mark is
// to the microsecond, as the database keeps the items' stored time: to the nanosecond, this run's own would read back
// as stored before it.
func markRows(next Subscription, caughtUp bool, start time.Time) Subscription {
	switch g := groupOf(next.Recipe); {
	case caughtUp:
		next.InboxGroup, next.InboxGroupFrom = g, nil
	case inboxFrom(next) != nil && next.InboxCatchup != nil && rowsGroup(next) != g:
		at := start.Truncate(time.Microsecond)
		next.InboxGroup, next.InboxGroupFrom = g, &at
	}
	return next
}

func (d deliverer) thread(key string) string { return threadPrefix(d.sub.ID) + key }

// rowThread is a row's thread: the items' own (a built-in's, as its connector brought them) for a built-in's feed,
// else under the subscription's prefix.
func (d deliverer) rowThread(key string) string {
	if d.migrated {
		return key
	}
	return d.thread(key)
}

func threadPrefix(sub string) string { return "sub:" + sub + ":" }

// OwnsThread says whether thread is one subscription id delivered to the Inbox (the item's connector thread, under
// the subscription's id as its source), deleted since or not: it has no conversation of its own to open.
func OwnsThread(id, thread string) bool {
	return id != "" && strings.HasPrefix(thread, threadPrefix(id))
}

// tags are a thing's: the subscription it came from, and the sender and chat of a row's newest item, which the Inbox
// can mute as it mutes a connector's (#5306). The sender matches by the recipe's sender_id when it maps one, as a
// built-in's mutes do (#6165), else by name, lowercased, as people are told apart by name here; the chat only by its
// id. A digest, a snapshot and the failure notice have no one sender (nil).
func (d deliverer) tags(it *Item) []connector.Tag {
	out := []connector.Tag{{Key: "source", Value: d.sub.ID, Name: d.sub.Label}}
	if it == nil {
		return out
	}
	name := strings.TrimSpace(it.Sender)
	switch {
	case it.SenderID != "":
		out = append(out, connector.Tag{Key: connector.TagSender, Value: it.SenderID, Name: cmpOr(name, it.SenderID)})
	case name != "":
		out = append(out, connector.Tag{Key: connector.TagSender, Value: strings.ToLower(name), Name: name})
	}
	if it.Chat != "" {
		out = append(out, connector.Tag{Key: connector.TagChat, Value: it.Chat, Name: cmpOr(it.ChatName, it.Chat)})
	}
	return out
}

// fallback is the tier an item gets when no model can triage it (the subscription's fallback_tier): by default FYI,
// and Needs you for one with a due date; "wait" none, so it waits for the model.
func (d deliverer) fallback(due bool) *connector.ThingExtra {
	tier := connector.TierFYI
	switch d.sub.FallbackTier {
	case "wait":
		return nil
	case "action":
		tier = connector.TierAction
	case "fyi":
	default:
		if due {
			tier = connector.TierAction
		}
	}
	return &connector.ThingExtra{RuleTier: string(tier)}
}

// threadThing is one row for a thread's items (or one item's, or the whole subscription's): its newest is the row,
// the earlier ones are listed above it in the context. Its count (extra.count) is how many items the newest pull
// brought (pulled together with its newest, or the catch-up's when the Inbox took them at once): the Inbox adds it as a
// move's messages, as a chat row counts its unread ones (#5671).
func (d deliverer) threadThing(key string, items []Held) connector.InboxThing {
	slices.SortStableFunc(items, func(a, b Held) int {
		if c := heldAt(a).Compare(heldAt(b)); c != 0 {
			return c
		}
		return int(a.Seq - b.Seq)
	})
	last := items[len(items)-1]
	due := false
	var b strings.Builder
	earlier := items[:len(items)-1]
	if len(earlier) > threadLinesMost {
		earlier = earlier[len(earlier)-threadLinesMost:]
	}
	for _, h := range items {
		due = due || h.DueDate != nil || h.Kind == wire.ConnectorSubscriptionKindSubDeadline
	}
	for _, h := range earlier {
		b.WriteString(d.line(h))
		b.WriteString("\n")
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString(d.block(last.Item, heldAt(last)))
	t := connector.InboxThing{Thread: d.rowThread(key), Kind: cmpOr(last.Kind, d.sub.Recipe.Kind), Label: d.sub.Label,
		Title: last.Title, Sender: last.Sender, Preview: clip(oneLine(last.Body), previewMost), Context: b.String(),
		At: heldAt(last), Last: last.UID, Rules: d.rules, Extra: d.fallback(due), Tags: d.tags(&last.Item),
		Read: last.Seen} // read in the source up to its newest item: read here (#6176)
	if strings.HasPrefix(last.Link, "https://") {
		t.URL = last.Link
	}
	if last.ReplyTo != "" && d.replies && !strings.HasPrefix(last.UID, weakPrefix) {
		t.ReplyTo, t.CanReply = last.ReplyTo, true // the reply goes to its connector with this item (Service.ReplyTarget)
	}
	if d.chats != nil {
		d.chats[t.Thread] = d.chatOf(last).ID
	}
	if n := d.newestPull(items); n > 1 {
		if t.Extra == nil {
			t.Extra = &connector.ThingExtra{}
		}
		t.Extra.Count = n
	}
	return t
}

// newestPull is how many of items the Inbox got at the latest time it got any: those stored by the newest run, or
// the catch-up's, which come in together when it ends.
func (d deliverer) newestPull(items []Held) int {
	var newest time.Time
	n := 0
	for _, h := range items {
		got := h.Stored
		if m := d.sub.InboxCatchup; m != nil && !got.After(*m) {
			got = *m
		}
		switch {
		case got.After(newest):
			newest, n = got, 1
		case got.Equal(newest):
			n++
		}
	}
	return n
}

// block is an item for the Inbox's context: title, who and when, its due date, its body (cut; left out when it only
// says the title again, as a chat message read as both does), its link.
func (d deliverer) block(it Item, when time.Time) string {
	var lines []string
	if it.Title != "" {
		lines = append(lines, it.Title)
	}
	who := d.when(when)
	if it.Sender != "" {
		who = it.Sender + " · " + who
	}
	lines = append(lines, who)
	if it.DueDate != nil {
		lines = append(lines, i18n.T(d.lang, "subscriptions-deliver.due", i18n.Args{"when": d.when(*it.DueDate)}))
	}
	if body := strings.TrimSpace(it.Body); body != "" && body != strings.TrimSpace(it.Title) {
		lines = append(lines, "", clip(body, contextBodyMost))
	}
	if it.Link != "" {
		lines = append(lines, "", it.Link)
	}
	return strings.Join(lines, "\n")
}

// line is an item in one line: when, who, what, its link.
func (d deliverer) line(h Held) string {
	parts := []string{d.when(heldAt(h))}
	if h.Sender != "" {
		parts = append(parts, h.Sender)
	}
	parts = append(parts, cmpOr(h.Title, clip(oneLine(h.Body), 80)))
	s := strings.Join(parts, " · ")
	if h.Link != "" {
		s += " " + h.Link
	}
	return s
}

func (d deliverer) when(t time.Time) string { return t.In(d.loc).Format("01-02 15:04") }

// digestThing is the catch-up as one row: a line an item, at most digestLinesMost, triaged once.
func (d deliverer) digestThing(items []Held) connector.InboxThing {
	slices.SortStableFunc(items, func(a, b Held) int {
		if c := heldAt(b).Compare(heldAt(a)); c != 0 {
			return c
		}
		return int(b.Seq - a.Seq)
	})
	var lines, uids []string
	for i, h := range items {
		uids = append(uids, h.UID)
		if i < digestLinesMost {
			lines = append(lines, d.line(h))
		}
	}
	if more := len(items) - digestLinesMost; more > 0 {
		lines = append(lines, i18n.T(d.lang, "subscriptions-deliver.digest_more", i18n.Args{"count": more}))
	}
	slices.Sort(uids)
	title := i18n.T(d.lang, "subscriptions-deliver.digest_title", i18n.Args{"label": d.sub.Label, "count": len(items)})
	thread := d.thread("catchup")
	if c := d.sub.CatchupFrom; c != nil {
		// What a changed recipe found at once: a row of its own, not the first catch-up's again.
		title = i18n.T(d.lang, "subscriptions-deliver.digest_title_recipe", i18n.Args{"label": d.sub.Label, "count": len(items)})
		thread = d.thread("catchup-" + strconv.FormatInt(c.Unix(), 10))
	}
	return connector.InboxThing{Thread: thread, Kind: wire.ConnectorSubscriptionKindSubDigest, Label: d.sub.Label,
		Title: title, Preview: clip(oneLine(cmpOr(items[0].Title, items[0].Body)), previewMost), Context: strings.Join(lines, "\n"),
		At: heldAt(items[0]), Last: "digest:" + hashOf(uids...)[:24], Rules: d.rules, Extra: d.fallback(false), Tags: d.tags(nil),
		Read: !slices.ContainsFunc(items, func(h Held) bool { return !h.Seen })} // every item read in the source (#6176)
}

// snapshotThing is a snapshot that changed: the state as the source answered it.
func (d deliverer) snapshotThing(h Held) connector.InboxThing {
	t := connector.InboxThing{Thread: d.thread("snapshot"), Kind: cmpOr(h.Kind, wire.ConnectorSubscriptionKindSubUpdate),
		Label: d.sub.Label, Title: cmpOr(h.Title, d.sub.Label), Preview: clip(oneLine(h.Body), previewMost),
		Context: clip(h.Body, contextBodyMost), At: h.Stored, Last: h.UID, Rules: d.rules, Extra: d.fallback(false), Tags: d.tags(nil)}
	if d.chats != nil {
		d.chats[t.Thread] = d.chatOf(h).ID
	}
	return t
}

// failing is the subscription's failure notice: FYI by rule once it has not pulled for failingNotice (one a
// failure, while it is on), else the end of one that may be open.
func (d deliverer) failing() connector.InboxThing {
	since := d.sub.FailingSince
	if !on(d.sub) || since == nil || d.now.Sub(*since) < failingNotice {
		done := connector.InboxThing{Thread: d.thread("failing"), Kind: KindFailing, Done: connector.DoneRead}
		if on(d.sub) {
			done.Why = i18n.T(d.lang, "subscriptions-deliver.recovered")
		}
		return done
	}
	reason := i18n.T(d.lang, "subscriptions-deliver.reason.other")
	if d.sub.Error != nil {
		switch d.sub.Error.Code {
		case CodeCallFailed, CodeNotReadOnly, CodeNoList, CodeShapeChanged, CodeToolChanged:
			reason = i18n.T(d.lang, "subscriptions-deliver.reason."+d.sub.Error.Code)
		}
	}
	return connector.InboxThing{Thread: d.thread("failing"), Kind: KindFailing, Label: d.sub.Label,
		Title:   i18n.T(d.lang, "subscriptions-deliver.failing_title", i18n.Args{"label": d.sub.Label, "since": d.when(*since)}),
		Preview: reason, Context: reason, At: since.Add(failingNotice), Last: "failing:" + strconv.FormatInt(since.Unix(), 10),
		Tier: connector.TierFYI, Why: i18n.T(d.lang, "subscriptions-deliver.failing_why"), Tags: d.tags(nil)}
}

// ---- the knowledge base ----

// Archives are the subscriptions the knowledge base reads now: those with it on.
func (s *Service) Archives(ctx context.Context) ([]string, error) {
	all, err := s.o.Store.ListSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, sub := range all {
		if kbFrom(sub) != nil {
			out = append(out, sub.ID)
		}
	}
	return out, nil
}

// IsSubscription says whether id names a subscription (an internal source, not a connector).
func (s *Service) IsSubscription(ctx context.Context, id string) bool {
	sub, err := s.o.Store.GetSubscription(ctx, id)
	return err == nil && sub != nil
}

// ErrArchiveOff answers Changes for a subscription whose knowledge base is off (or that is gone).
var ErrArchiveOff = errorf(http.StatusConflict, "archive_off", "the subscription does not go to the knowledge base")

// Changes are subscription id's held items after cursor (a stored position; "" the first time) as archive batches:
// a chat a thread or one for the whole subscription (the recipe's group_by), a message an item under its uid. It
// starts a day before the knowledge base was turned on, also with a cursor from before it was last off. The
// knowledge base moves its cursor once it has written them.
func (s *Service) Changes(ctx context.Context, id, cursor string, _ int) ([]connector.ChatBatch, string, bool, error) {
	sub, err := s.o.Store.GetSubscription(ctx, id)
	if err != nil {
		return nil, cursor, false, err
	}
	if sub == nil || kbFrom(*sub) == nil {
		return nil, cursor, false, ErrArchiveOff
	}
	from := kbFrom(*sub)
	after, _ := strconv.ParseInt(cursor, 10, 64)
	held, err := s.o.Store.HeldItems(ctx, id, ItemQuery{After: after, Since: from.Add(-catchupBack), Limit: kbPageMost})
	if err != nil {
		return nil, cursor, false, err
	}
	if len(held) == 0 {
		return nil, cursor, false, nil
	}
	d := s.deliverer(*sub, s.now())
	var out []connector.ChatBatch
	index := map[string]int{}
	for _, h := range held {
		chat := d.chatOf(h)
		i, ok := index[chat.ID]
		if !ok {
			i = len(out)
			index[chat.ID] = i
			out = append(out, connector.ChatBatch{Chat: chat})
		}
		out[i].Messages = append(out[i].Messages, d.message(h))
	}
	return out, strconv.FormatInt(held[len(held)-1].Seq, 10), len(held) == kbPageMost, nil
}

// chatOf is the knowledge-base chat an item goes to: its thread's (named by the thread's first item the knowledge base
// sees), or the subscription's own; a snapshot always the subscription's.
func (d deliverer) chatOf(h Held) kbwire.SourceChat {
	if h.Snapshot || !groupsByThread(d.sub.Recipe) {
		return kbwire.SourceChat{ID: rowAll, Name: d.sub.Label, Kind: "subscription"}
	}
	key := cmpOr(h.Thread, h.UID)
	return kbwire.SourceChat{ID: "t" + hashOf(key)[:16], Name: cmpOr(h.Title, d.sub.Label), Kind: "subscription"}
}

// message is an item as a knowledge-base message: 【title】 and the body, its due date and link on lines of their own.
func (d deliverer) message(h Held) kbwire.SourceMessage {
	var b strings.Builder
	if h.Title != "" && (!h.Snapshot || h.Title != d.sub.Label) { // a snapshot's default title is the chat's name
		b.WriteString("【" + h.Title + "】")
	}
	b.WriteString(strings.TrimSpace(h.Body))
	if h.DueDate != nil {
		b.WriteString("\n" + i18n.T(d.lang, "subscriptions-deliver.due", i18n.Args{"when": d.when(*h.DueDate)}))
	}
	if h.Link != "" {
		b.WriteString("\n" + h.Link)
	}
	return kbwire.SourceMessage{ID: h.UID, SenderName: h.Sender, Type: "TEXT", Text: strings.TrimSpace(b.String()), At: heldAt(h)}
}
