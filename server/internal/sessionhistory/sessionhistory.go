// Package sessionhistory pages Agent sessions active in a time window, including inactive ones (docs/protocol/protocol.md
// "Session history"): the window, the order, the cursor and the merge of several sources that the node (its own
// Claude Code / Codex records and its runtimes) and the hub (its machines) share.
//
// Order is newest first by updated_at in milliseconds, ties by a key that names the row within everything merged
// (the node's: source, runtime and session id; the hub's: the machine before that). Milliseconds, because that is
// what a runtime written in JavaScript can tell apart (the SDK's rt.history orders the same way).
package sessionhistory

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const (
	// DefaultLimit is a page's size when the caller gives none; MaxLimit the largest it may ask.
	DefaultLimit = 200
	MaxLimit     = 500
	// collectPages bounds how many pages Collect reads from one source for one page of its own.
	collectPages = 20
)

// Source names (Row.Source, SourceState.Source).
const (
	SourceClaude  = "claude"  // Claude Code transcripts the node reads itself
	SourceCodex   = "codex"   // Codex rollouts the node reads itself (codex_history and Codex app-server threads)
	SourceRuntime = "runtime" // a connected runtime's listHistory (Row.Runtime / SourceState.Runtime: its id)
)

// Reasons a source was not honored (SourceState.Reason).
const (
	ReasonUnsupported     = "unsupported"      // the runtime does not declare caps.history
	ReasonFailed          = "failed"           // asking it failed or timed out
	ReasonNodeUnsupported = "node_unsupported" // the machine's node predates sessions.history
	ReasonOffline         = "offline"          // the machine is not connected
)

// Query is a window and the page wanted of it.
type Query struct {
	Since, Until time.Time
	Cursor       string
	Limit        int
}

// ErrInvalid wraps every error Parse returns.
var ErrInvalid = errors.New("invalid history query")

func invalid(msg string) error { return errors.Join(ErrInvalid, errors.New(msg)) }

// Parse checks a window as callers give it: since required (RFC 3339), until defaulting to now, limit 0 for the
// default and at most MaxLimit.
func Parse(since, until, cursor string, limit int, now time.Time) (Query, error) {
	q := Query{Cursor: cursor, Limit: DefaultLimit, Until: now}
	var err error
	if since == "" {
		return Query{}, invalid("since is required (RFC 3339)")
	}
	if q.Since, err = time.Parse(time.RFC3339Nano, since); err != nil {
		return Query{}, invalid("since must be an RFC 3339 time")
	}
	if until != "" {
		if q.Until, err = time.Parse(time.RFC3339Nano, until); err != nil {
			return Query{}, invalid("until must be an RFC 3339 time")
		}
	}
	if limit < 0 {
		return Query{}, invalid("limit must be positive")
	}
	if limit > 0 {
		q.Limit = min(limit, MaxLimit)
	}
	if _, err := DecodeCursor(cursor); err != nil {
		return Query{}, err
	}
	return q, nil
}

// Params is the query as sessions.history carries it.
func (q Query) Params() map[string]any {
	p := map[string]any{"since": q.Since.UTC().Format(time.RFC3339Nano), "until": q.Until.UTC().Format(time.RFC3339Nano), "limit": q.Limit}
	if q.Cursor != "" {
		p["cursor"] = q.Cursor
	}
	return p
}

// In reports whether a time is in the window [Since, Until), at the order's precision.
func (q Query) In(t time.Time) bool {
	ms := t.UnixMilli()
	return !t.IsZero() && ms >= q.Since.UnixMilli() && ms < q.Until.UnixMilli()
}

// Key is a row's place in the order.
type Key struct {
	T int64  `json:"t"` // updated_at, Unix milliseconds
	K string `json:"k"`
}

// KeyOf is the key of a row updated at t named k.
func KeyOf(t time.Time, k string) Key { return Key{T: t.UnixMilli(), K: k} }

// Before reports whether a comes before b: newer, or as new with the smaller name.
func (a Key) Before(b Key) bool {
	if a.T != b.T {
		return a.T > b.T
	}
	return a.K < b.K
}

func compare(a, b Key) int {
	if c := cmp.Compare(b.T, a.T); c != 0 {
		return c
	}
	return strings.Compare(a.K, b.K)
}

// EncodeCursor is the opaque cursor that continues after k.
func EncodeCursor(k Key) string {
	b, _ := json.Marshal(k)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor reads a cursor EncodeCursor made; nil for none.
func DecodeCursor(c string) (*Key, error) {
	if c == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	var k Key
	if err != nil || json.Unmarshal(b, &k) != nil || k.T == 0 && k.K == "" {
		return nil, invalid("malformed cursor")
	}
	return &k, nil
}

// Item is a row and its key.
type Item[T any] struct {
	Key Key
	V   T
}

// Part is what one source gave towards a page: its rows after the cursor in order, and whether it has more beyond
// the last of them. Reached, when set, is how far the source read: rows it left out (a blocked folder's) can lie past
// its last item, and a page goes on from there rather than from that item.
type Part[T any] struct {
	Items   []Item[T]
	More    bool
	Reached *Key
}

// Sorted puts items in order.
func Sorted[T any](items []Item[T]) []Item[T] {
	slices.SortStableFunc(items, func(a, b Item[T]) int { return compare(a.Key, b.Key) })
	return items
}

// After keeps the items that come after the cursor's key (all of them for none), in place.
func After[T any](items []Item[T], after *Key) []Item[T] {
	if after == nil {
		return items
	}
	out := items[:0]
	for _, it := range items {
		if after.Before(it.Key) {
			out = append(out, it)
		}
	}
	return out
}

// Merge takes one page of up to limit items from the parts, and the key the next page continues after (nil: this is
// the last). A part with more stops the page at its last item: what it has beyond may come before another part's
// later items.
func Merge[T any](parts []Part[T], limit int) ([]Item[T], *Key) {
	var all []Item[T]
	var stop *Key
	more := false
	for _, p := range parts {
		all = append(all, p.Items...)
		if p.More {
			more = true
			last := p.Reached
			if last == nil && len(p.Items) > 0 {
				last = &p.Items[len(p.Items)-1].Key
			}
			if last != nil && (stop == nil || last.Before(*stop)) {
				stop = last
			}
		}
	}
	all = Sorted(all)
	if stop != nil {
		n := 0
		for n < len(all) && !stop.Before(all[n].Key) {
			n++
		}
		all = all[:n]
	}
	if len(all) > limit {
		all, more = all[:limit], true
	}
	if !more {
		return all, nil
	}
	if len(all) == 0 {
		// Every row read up to the stop was left out: an empty page that goes on after it, so the rows past it are
		// neither skipped nor taken for the end. No stop known: the last page, as before.
		if stop == nil {
			return all, nil
		}
		next := *stop
		return all, &next
	}
	next := all[len(all)-1].Key
	return all, &next
}

// Page is one answer of a source that pages with its own cursor. Reached is the key of the last row the page read,
// those the fetch left out included; nil when it left none out past its last item.
type Page[T any] struct {
	Items      []Item[T]
	NextCursor string
	Complete   bool
	Reached    *Key
}

// Fetch asks a source for one page of q.
type Fetch[T any] func(ctx context.Context, q Query) (Page[T], error)

// Collect reads a source until it has want items after the key (or the source has no more), paging with the
// source's own cursor. The window's end is brought down to the key's time, so a source is not paged from its newest
// row for every page of the merge. complete: every page read said so.
func Collect[T any](ctx context.Context, fetch Fetch[T], q Query, after *Key, want int) (Part[T], bool, error) {
	sub := Query{Since: q.Since, Until: q.Until, Limit: min(MaxLimit, want+50)}
	if after != nil {
		if end := time.UnixMilli(after.T + 1); end.Before(sub.Until) {
			sub.Until = end
		}
	}
	var part Part[T]
	complete := true
	in := func(k Key) bool {
		return k.T >= q.Since.UnixMilli() && k.T < sub.Until.UnixMilli() && (after == nil || after.Before(k))
	}
	reach := func(k Key) {
		if part.Reached == nil || part.Reached.Before(k) {
			part.Reached = &k
		}
	}
	for i := 0; i < collectPages; i++ {
		page, err := fetch(ctx, sub)
		if err != nil {
			return Part[T]{}, false, err
		}
		complete = complete && page.Complete
		for _, it := range page.Items {
			if in(it.Key) {
				part.Items = append(part.Items, it)
				reach(it.Key)
			}
		}
		if page.Reached != nil && in(*page.Reached) {
			reach(*page.Reached)
		}
		if page.NextCursor == "" || page.NextCursor == sub.Cursor {
			part.Items, part.Reached = Sorted(part.Items), nil
			return part, complete, nil
		}
		if len(part.Items) >= want {
			break
		}
		sub.Cursor = page.NextCursor
	}
	part.Items, part.More = Sorted(part.Items), true
	return part, complete, nil
}

// SourceState says how one source answered.
type SourceState struct {
	Machine  string `json:"machine,omitempty"` // the hub's; a node leaves it out
	Source   string `json:"source"`
	Runtime  string `json:"runtime,omitempty"`
	Honored  bool   `json:"honored"`
	Complete bool   `json:"complete"`
	Reason   string `json:"reason,omitempty"`
}

// Row is a session row of a history page: a session as listed, and the source it came from. The session is a named
// field, not embedded: an embedded one would lend Row its own JSON methods, and the source would not be written.
//
// ActiveAt is when the session was last active (its last message), which archiving does not move as it may move
// updated_at; the window and the order go by it, else updated_at, else StartedAt. Origin is "user" or "automation"
// (a scheduled run) when the source can tell; UserMessages how many messages the user wrote in it, when it can count.
type Row struct {
	Session      proto.Session
	Source       string
	Runtime      string
	ActiveAt     time.Time
	StartedAt    time.Time
	Origin       string
	UserMessages *int
}

// Origin values (Row.Origin).
const (
	OriginUser       = "user"
	OriginAutomation = "automation"
)

// rowExtra is what a history row says beyond a listed session.
type rowExtra struct {
	Source       string    `json:"history_source"`
	Runtime      string    `json:"history_runtime,omitempty"`
	ActiveAt     time.Time `json:"active_at,omitzero"`
	StartedAt    time.Time `json:"started_at,omitzero"`
	Origin       string    `json:"origin,omitempty"`
	UserMessages *int      `json:"user_messages,omitempty"`
}

// At is the row's time in the window and the order: ActiveAt, else updated_at, else StartedAt.
func (r *Row) At() time.Time {
	switch {
	case !r.ActiveAt.IsZero():
		return r.ActiveAt
	case !r.Session.UpdatedAt.IsZero():
		return r.Session.UpdatedAt
	}
	return r.StartedAt
}

// RuntimeRow reads a row as a runtime's listHistory gives it: the session, and the history fields beyond it. deleted:
// the runtime says the session was deleted (status "deleted", or deleted: true), which no history lists.
func RuntimeRow(raw []byte) (r Row, deleted bool, err error) {
	if err = json.Unmarshal(raw, &r.Session); err != nil {
		return Row{}, false, err
	}
	var x struct {
		rowExtra
		Status  string `json:"status"`
		Deleted bool   `json:"deleted"`
	}
	if err = json.Unmarshal(raw, &x); err != nil {
		return Row{}, false, err
	}
	r.ActiveAt, r.StartedAt, r.UserMessages = x.ActiveAt, x.StartedAt, x.UserMessages
	if x.Origin == OriginUser || x.Origin == OriginAutomation {
		r.Origin = x.Origin
	}
	if r.UserMessages != nil && *r.UserMessages < 0 {
		r.UserMessages = nil
	}
	return r, x.Status == "deleted" || x.Deleted, nil
}

// MarshalJSON writes the session's own JSON with the history fields added (history_source, history_runtime,
// active_at, started_at, origin, user_messages; the empty ones left out).
func (r Row) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(r.Session)
	if err != nil {
		return nil, err
	}
	extra, err := json.Marshal(rowExtra{r.Source, r.Runtime, r.ActiveAt, r.StartedAt, r.Origin, r.UserMessages})
	if err != nil {
		return nil, err
	}
	b = bytes.TrimRight(b, " \n")
	if len(b) < 2 || b[len(b)-1] != '}' {
		return nil, errors.New("session is not a JSON object")
	}
	out := append(b[:len(b)-1:len(b)-1], ',')
	if len(b) == 2 { // {}
		out = out[:1]
	}
	return append(out, extra[1:]...), nil
}

// UnmarshalJSON reads what MarshalJSON writes.
func (r *Row) UnmarshalJSON(b []byte) error {
	var x rowExtra
	if err := json.Unmarshal(b, &x); err != nil {
		return err
	}
	if err := json.Unmarshal(b, &r.Session); err != nil {
		return err
	}
	r.Source, r.Runtime, r.ActiveAt, r.StartedAt, r.Origin, r.UserMessages = x.Source, x.Runtime, x.ActiveAt, x.StartedAt, x.Origin, x.UserMessages
	return nil
}

// NodeKey is a row's name within one machine.
func (r *Row) NodeKey() string { return r.Source + "\x00" + r.Runtime + "\x00" + r.Session.SessionID }

// Answer is sessions.history's result.
type Answer struct {
	Rows       []Row         `json:"rows"`
	NextCursor string        `json:"next_cursor"`
	Complete   bool          `json:"complete"`
	Sources    []SourceState `json:"sources,omitempty"`
}
