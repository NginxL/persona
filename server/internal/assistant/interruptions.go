package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Interrupted receipts after the fact (docs/assistant/unified-entry.md "Interrupted receipts"): a receipt for a turn the
// model service cut off stays in the chat, but once its task was dealt with it says how (cancelled, rerouted,
// resumed, accepted, done) and the apps draw it grey. Receipts with the same cause in one conversation within
// interruptBatchWindow form one batch: the first is its head and carries the batch, the others name the head in
// `group` and are drawn only inside it. The hub works out both and sends them; the apps only draw.

// interruptBatchWindow is how long after a batch's first interruption another with the same cause still joins it.
const interruptBatchWindow = 10 * time.Minute

// interruptTrackFor is how long the hub keeps re-sending an interrupted receipt when its task changes.
const interruptTrackFor = 48 * time.Hour

// interruptRefreshDelay gathers a burst of changes (a plan's tasks cancelled together) into one refresh.
var interruptRefreshDelay = time.Second

// Resolution values of an interrupted receipt.
const (
	resolutionPending   = "pending"
	resolutionCancelled = "cancelled"
	resolutionRerouted  = "rerouted"
	resolutionResumed   = "resumed"
	resolutionAccepted  = "accepted"
	resolutionDone      = "done"
)

// Cause values of an interrupted receipt: what kind of error cut the turn off.
const (
	causeModelError  = "model_error"  // the model service failed (5xx, overloaded, connection lost…)
	causeRateLimited = "rate_limited" // too many requests or a usage limit
	causeAuth        = "auth"         // not signed in, or the key was refused
	causeModelRoute  = "model_route"  // the model name is not one the service knows
	causeNoOutput    = "no_output"    // the turn answered nothing (#2349)
	causeStopped     = "stopped"      // Persona's Stop interrupted it before it had any output, with its conversation (#10187)
)

// interruptCause is the cause of a cut-off turn by its error text (silent: the turn had no output at all).
func interruptCause(errText string, silent bool) string {
	if silent {
		return causeNoOutput
	}
	switch turnErrorCode(errText) {
	case proto.TurnErrorRateLimited:
		return causeRateLimited
	case proto.TurnErrorNotLoggedIn, proto.TurnErrorAuthFailed:
		return causeAuth
	case proto.TurnErrorModelRoute, proto.TurnErrorModelFallbackUnavailable:
		return causeModelRoute
	}
	return causeModelError
}

// receiptBatch is what a batch's head says of the whole batch.
type receiptBatch struct {
	Cause string `json:"cause"`
	// Machine: the computer every task of the batch ran on; left out when they ran on several.
	Machine  string       `json:"machine,omitempty"`
	Total    int          `json:"total"`
	Pending  int          `json:"pending"`
	Resolved int          `json:"resolved"`
	Tasks    []batchEntry `json:"tasks"`
}

// batchEntry is one interrupted task of a batch, in the order they were cut off.
type batchEntry struct {
	Receipt    string `json:"receipt"`
	TodoID     string `json:"todo_id"`
	Title      string `json:"title"`
	Resolution string `json:"resolution"`
	At         string `json:"at"`
}

// interruptBook remembers the interrupted receipts the hub still re-sends when their tasks change, and what it last
// sent for each, so that only a change goes out.
type interruptBook struct {
	mu sync.Mutex
	// appendMu: one interrupted receipt at a time picks its batch, so that two cut off at once do not both start one.
	appendMu sync.Mutex
	// convs: conversation → the earliest interrupted receipt still followed there.
	convs map[string]time.Time
	sent  map[string]map[string]string // conversation → receipt id → the body last sent (let go with the conversation)
	armed bool
}

func (b *interruptBook) init() {
	if b.convs == nil {
		b.convs = map[string]time.Time{}
		b.sent = map[string]map[string]string{}
	}
}

// remember records body as what the apps last got of receipt id in conv, and returns what they had before ("" for nothing).
func (b *interruptBook) remember(conv, id, body string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	if b.sent[conv] == nil {
		b.sent[conv] = map[string]string{}
	}
	last := b.sent[conv][id]
	b.sent[conv][id] = body
	return last
}

// normalJSON is an object as json.Marshal writes a map of it (keys sorted), to compare a stored body with a derived one.
func normalJSON(raw json.RawMessage) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return string(raw)
	}
	out, _ := json.Marshal(m)
	return string(out)
}

// follow has the refreshes look at conversation conv's interrupted receipts from at on.
func (b *interruptBook) follow(conv string, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	if old, ok := b.convs[conv]; !ok || at.Before(old) {
		b.convs[conv] = at
	}
}

// appendInterrupted writes an interrupted receipt for task t's turn on run, which its body names (#10465): its cause
// (one the caller knows, such as causeStopped, stands), and the batch it joins when another receipt in the same
// conversation with the same cause began one less than interruptBatchWindow ago.
func (s *Service) appendInterrupted(ctx context.Context, t Todo, run SessionRef, body receiptBody, silent bool) {
	body.MachineID, body.SessionID = run.MachineID, run.SessionID
	if body.Cause == "" { // one the caller knows (causeStopped) stands
		body.Cause = interruptCause(body.Error, silent)
	}
	body.Resolution = resolutionPending
	b := &s.interrupts
	b.appendMu.Lock()
	defer b.appendMu.Unlock()
	now := time.Now().UTC()
	if head, ok := s.interruptHead(ctx, t.Origin, body.Cause, now); ok {
		body.Group = head.ID
	}
	item, err := s.appendFeed(ctx, t.Origin, feedReceipt, t.ID, body)
	if err != nil {
		s.logf("task %s: receipt: %v", t.ID, err)
		return
	}
	at := item.At
	if body.Group != "" {
		if head, err := s.feedItem(ctx, body.Group); err == nil && head != nil {
			at = head.At
		}
	}
	b.follow(t.Origin, at)
	s.interruptsSoon()
}

// interruptHead is the head of the batch an interruption with cause in conv at now joins: the latest receipt there
// with that cause that heads a batch (names none) and began at most interruptBatchWindow before now.
func (s *Service) interruptHead(ctx context.Context, conv, cause string, now time.Time) (FeedItem, bool) {
	items, err := s.rawFeed(ctx, conv, now.Add(-interruptBatchWindow))
	if err != nil {
		return FeedItem{}, false
	}
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		rb, ok := interruptedBody(it)
		if !ok || rb.Group != "" || rb.Cause != cause || now.Sub(it.At) > interruptBatchWindow {
			continue
		}
		return it, true
	}
	return FeedItem{}, false
}

// interruptedBody is item's receipt body when it is an interrupted receipt.
func interruptedBody(it FeedItem) (receiptBody, bool) {
	if it.Kind != feedReceipt {
		return receiptBody{}, false
	}
	var rb receiptBody
	if json.Unmarshal(it.Body, &rb) != nil || rb.State != "interrupted" {
		return receiptBody{}, false
	}
	return rb, true
}

// resolveInterruptions writes into the interrupted receipts among items how each was dealt with (resolution) and, on
// each batch's head, the batch. A member whose head is not among items brings the head along (its body changed), so
// a page read after the head still updates it. The page's other receipts tell what went on after an interruption.
func (s *Service) resolveInterruptions(ctx context.Context, conversation string, items []FeedItem) []FeedItem {
	have := map[string]int{}
	var earliest time.Time
	found := false
	for i, it := range items {
		have[it.ID] = i
		if _, ok := interruptedBody(it); ok {
			found = true
			if earliest.IsZero() || it.At.Before(earliest) {
				earliest = it.At
			}
		}
	}
	if !found {
		return items
	}
	// The heads that are not on this page, and with them every receipt from the earliest head on: the members a head
	// counts and the receipts that came after its tasks were cut off.
	from := earliest
	for _, it := range items {
		rb, ok := interruptedBody(it)
		if !ok || rb.Group == "" {
			continue
		}
		if _, on := have[rb.Group]; on {
			continue
		}
		if head, err := s.feedItem(ctx, rb.Group); err == nil && head != nil && head.Conversation == it.Conversation {
			have[head.ID] = len(items)
			items = append(items, *head)
			if head.At.Before(from) {
				from = head.At
			}
		}
	}
	// Everything from the earliest head on, not only this page (one read with since): a batch's other receipts and the
	// receipts that came after its tasks were cut off.
	around := items
	if all, err := s.feedFrom(ctx, conversation, from); err == nil {
		around = mergeFeedByID(items, all)
	}
	derived := s.deriveInterruptions(around)
	for i, it := range items {
		if body, ok := derived[it.ID]; ok {
			items[i].Body = body
		}
	}
	slices.SortStableFunc(items, func(a, b FeedItem) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	s.interrupts.follow(conversation, from)
	return items
}

// interruptReadPages caps how many feed pages feedFrom reads after an interruption.
const interruptReadPages = 20

// feedFrom is conversation conv's feed from at on, page after page (one page is feedPage items): the receipts that
// show a task went on after it was cut off can come long after it in a busy chat. At most interruptReadPages pages.
func (s *Service) feedFrom(ctx context.Context, conv string, at time.Time) ([]FeedItem, error) {
	var out []FeedItem
	since := at.Add(-time.Microsecond)
	for range interruptReadPages {
		page, err := s.rawFeed(ctx, conv, since)
		if err != nil {
			return out, err
		}
		out = append(out, page...)
		if len(page) < feedPage {
			break
		}
		since = page[len(page)-1].At
	}
	return out, nil
}

// mergeFeedByID is a with b's items it lacks, by id.
func mergeFeedByID(a, b []FeedItem) []FeedItem {
	seen := make(map[string]bool, len(a))
	out := append([]FeedItem(nil), a...)
	for _, it := range a {
		seen[it.ID] = true
	}
	for _, it := range b {
		if !seen[it.ID] {
			out = append(out, it)
		}
	}
	return out
}

// deriveInterruptions is the new body of every interrupted receipt among items, by id: resolution on all, batch on
// the heads of batches with more than one receipt. items are one conversation's, in any order.
func (s *Service) deriveInterruptions(items []FeedItem) map[string]json.RawMessage {
	var cuts []*cut
	later := map[string]time.Time{} // todo id → its latest receipt of any kind
	for _, it := range items {
		if it.Kind == feedReceipt && it.Ref != "" && it.At.After(later[it.Ref]) {
			later[it.Ref] = it.At
		}
		if rb, ok := interruptedBody(it); ok {
			cuts = append(cuts, &cut{item: it, body: rb})
		}
	}
	if len(cuts) == 0 {
		return nil
	}
	slices.SortFunc(cuts, func(a, b *cut) int {
		if c := a.item.At.Compare(b.item.At); c != 0 {
			return c
		}
		return strings.Compare(a.item.ID, b.item.ID)
	})
	loaded := s.todosLoaded()
	for _, c := range cuts {
		c.res = s.receiptResolution(c.item, loaded, later[c.item.Ref].After(c.item.At))
	}
	members := map[string][]*cut{}
	for _, c := range cuts {
		head := c.body.Group
		if head == "" {
			head = c.item.ID
		}
		members[head] = append(members[head], c)
	}
	out := make(map[string]json.RawMessage, len(cuts))
	for _, c := range cuts {
		var raw map[string]json.RawMessage
		_ = json.Unmarshal(c.item.Body, &raw)
		if raw == nil {
			raw = map[string]json.RawMessage{}
		}
		raw["resolution"], _ = json.Marshal(c.res)
		if c.body.Cause == "" {
			raw["cause"], _ = json.Marshal(interruptCause(c.body.Error, false))
		}
		delete(raw, "batch")
		if c.body.Group != "" && distinctTasks(members[c.body.Group]) < 2 {
			delete(raw, "group") // its "batch" is one task cut off twice: no card to be drawn in, so it stands on its own
		}
		if group := members[c.item.ID]; c.body.Group == "" && len(group) > 1 && distinctTasks(group) > 1 {
			raw["batch"], _ = json.Marshal(batchOf(group))
		}
		out[c.item.ID], _ = json.Marshal(raw)
	}
	return out
}

// cut is an interrupted receipt being resolved: the item, its body and how it was dealt with.
type cut struct {
	item FeedItem
	body receiptBody
	res  string
}

// distinctTasks is how many tasks group's receipts are of.
func distinctTasks(group []*cut) int {
	seen := map[string]bool{}
	for _, c := range group {
		seen[c.item.Ref] = true
	}
	return len(seen)
}

// batchOf is the batch of group (its receipts in the order they were cut off, the head first).
func batchOf(all []*cut) receiptBatch {
	// One entry per task: a task cut off twice within the window (Continue, then cut off again) counts once, as its
	// latest receipt says; the earlier one reads resumed anyway (a later receipt exists).
	latest := map[string]int{}
	for i, c := range all {
		latest[c.item.Ref] = i
	}
	group := make([]*cut, 0, len(all))
	for i, c := range all {
		if latest[c.item.Ref] == i {
			group = append(group, c)
		}
	}
	cause := all[0].body.Cause
	if cause == "" {
		cause = interruptCause(all[0].body.Error, false) // a receipt from before causes were written: read off its error
	}
	b := receiptBatch{Cause: cause, Total: len(group), Tasks: make([]batchEntry, 0, len(group))}
	same := true
	for _, c := range group {
		b.Tasks = append(b.Tasks, batchEntry{Receipt: c.item.ID, TodoID: c.item.Ref, Title: c.body.Title, Resolution: c.res,
			At: c.item.At.UTC().Format(time.RFC3339Nano)})
		if c.res == resolutionPending {
			b.Pending++
		} else {
			b.Resolved++
		}
		if c.body.Machine != group[0].body.Machine {
			same = false
		}
	}
	if same {
		b.Machine = group[0].body.Machine
	}
	return b
}

// receiptResolution is how the task of interrupted receipt it was dealt with since; pending while it was not. wentOn:
// a later receipt of the same task exists (its agent went on after the interruption). Without the todos read, every
// one is pending.
func (s *Service) receiptResolution(it FeedItem, loaded, wentOn bool) string {
	if !loaded || it.Ref == "" {
		return resolutionPending
	}
	t, ok := s.todoNow(it.Ref)
	if !ok {
		return resolutionCancelled // the task is gone: removed by hand
	}
	at := it.At
	if t.Cancelled() {
		if s.rerouted(t, at) {
			return resolutionRerouted
		}
		return resolutionCancelled
	}
	if t.DoneAt != nil {
		if t.DoneBy == db.DoneByReview || t.Supervision != nil && t.Supervision.state(t) == reviewAccepted {
			return resolutionAccepted
		}
		return resolutionDone
	}
	for _, a := range t.Agents {
		if a.LinkedAt.After(at) && a.ReleasedAt == nil && a.DismissedAt == nil {
			return resolutionRerouted // another agent took the task over
		}
	}
	if wentOn {
		return resolutionResumed
	}
	s.tasks.mu.Lock()
	working := s.tasks.working[t.ID]
	s.tasks.mu.Unlock()
	if working.After(at) {
		return resolutionResumed
	}
	return resolutionPending
}

// rerouted: cancelled task t was dispatched again after at, as a new task with the same title from the same
// conversation (the old one cancelled, a new one started).
func (s *Service) rerouted(t Todo, at time.Time) bool {
	title := strings.TrimSpace(t.Title)
	if title == "" || t.Origin == "" {
		return false
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for _, o := range s.todos.todos {
		if o.ID != t.ID && o.Origin == t.Origin && strings.TrimSpace(o.Title) == title && o.CreatedAt.After(at) {
			return true
		}
	}
	return false
}

// interruptsSoon has the followed interrupted receipts looked at again shortly: a task changed (cancelled, closed,
// reopened, an agent linked), one of its agents started a turn, or a receipt was written.
func (s *Service) interruptsSoon() {
	b := &s.interrupts
	b.mu.Lock()
	b.init()
	if b.armed || len(b.convs) == 0 {
		b.mu.Unlock()
		return
	}
	b.armed = true
	b.mu.Unlock()
	time.AfterFunc(interruptRefreshDelay, func() {
		b.mu.Lock()
		b.armed = false
		b.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.refreshInterruptions(ctx)
	})
}

// refreshInterruptions sends the apps every followed interrupted receipt whose body changed since it was last sent
// (an assistant_feed event with the same id: the apps take the new body). Conversations whose receipts are all older
// than interruptTrackFor are let go.
func (s *Service) refreshInterruptions(ctx context.Context) {
	b := &s.interrupts
	b.mu.Lock()
	b.init()
	convs := make(map[string]time.Time, len(b.convs))
	for c, at := range b.convs {
		if time.Since(at) > interruptTrackFor {
			delete(b.convs, c)
			delete(b.sent, c)
			continue
		}
		convs[c] = at
	}
	b.mu.Unlock()
	for conv, from := range convs {
		items, err := s.feedFrom(ctx, conv, from)
		if err != nil {
			continue
		}
		derived := s.deriveInterruptions(items)
		for _, it := range items {
			body, ok := derived[it.ID]
			if !ok {
				continue
			}
			last := b.remember(conv, it.ID, string(body))
			if last == string(body) || last == "" && string(body) == normalJSON(it.Body) {
				continue // the apps have it: sent before, or as stored
			}
			it.Body = body
			s.event(ctx, map[string]any{"type": "assistant_feed", "conversation": conv, "item": it})
		}
	}
}
