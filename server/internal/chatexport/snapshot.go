package chatexport

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/hubclient"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	pageSize      = 80
	maxPages      = 400
	maxItems      = 20_000
	maxFullReads  = 512
	maxStretches  = 64
	maxReplyBytes = 16 << 20
	maxReadBytes  = 96 << 20
)

var errMissing = errors.New("conversation not found")

type sessionRow struct {
	Machine   string            `json:"machine_id"`
	ID        string            `json:"session_id"`
	Manager   string            `json:"manager"`
	Title     string            `json:"title"`
	Folder    string            `json:"cwd"`
	Agent     string            `json:"agent"`
	Assistant bool              `json:"assistant"`
	History   []proto.EngineLeg `json:"engine_history"`
}

type conversation struct {
	row       sessionRow
	stretches []stretch
}

// A switched Agent has one (after, until] window per engine switch and one
// open-ended window for the current session. The same session can run twice.
type stretch struct {
	session, agent string
	after, until   time.Time
	items          []proto.Item
}

type snapshotReader struct {
	hub       *hubclient.Client
	machine   string
	remaining int64
	pages     int
	items     int
	fullReads int
}

type itemPage struct {
	Items   []proto.Item `json:"items"`
	Strict  bool         `json:"strict_read"`
	From    *int         `json:"from_seq"`
	More    *bool        `json:"has_more"`
	History string       `json:"history"`
	To      *int         `json:"to_seq"`
}

type snapshotSource struct {
	head  itemPage
	items []proto.Item
}

func (r *snapshotReader) read(ctx context.Context, session string, work bool) (conversation, error) {
	row, err := r.session(ctx, session)
	if err != nil {
		return conversation{}, err
	}
	stretches, err := row.windows()
	if err != nil {
		return conversation{}, err
	}

	// Establish the current session's upper bound first. Each distinct earlier
	// source gets its own bound, even when its session supplies several legs.
	sources := make(map[string]*snapshotSource)
	sourceIDs := []string{session}
	for _, leg := range row.History {
		if !slices.Contains(sourceIDs, leg.SessionID) {
			sourceIDs = append(sourceIDs, leg.SessionID)
		}
	}
	for _, id := range sourceIDs {
		head, err := r.page(ctx, id, 0, pageSize)
		if err != nil {
			return conversation{}, err
		}
		sources[id] = &snapshotSource{head: head}
	}

	for _, id := range sourceIDs {
		source := sources[id]
		items, err := r.collect(ctx, id, source.head)
		if err != nil {
			return conversation{}, err
		}
		source.items = items
	}
	for _, id := range sourceIDs {
		if err := placeItems(stretches, id, sources[id].items); err != nil {
			return conversation{}, err
		}
	}
	for i := range stretches {
		s := &stretches[i]
		s.items = visibleItems(s.items, work, i == len(stretches)-1)
		for j := range s.items {
			if err := r.expand(ctx, s.session, &s.items[j]); err != nil {
				// A rewrite can remove the exact item that the clipped page named.
				// Retain the original failure unless a strict reread proves a change.
				if !errors.Is(err, errBudget) && ctx.Err() == nil {
					end, checkErr := r.page(ctx, s.session, 0, 1)
					if checkErr == nil && end.validate(0) == nil && changed(sources[s.session].head, end) {
						return conversation{}, errChanged
					}
				}
				return conversation{}, err
			}
		}
	}
	for _, id := range sourceIDs {
		end, err := r.page(ctx, id, 0, 1)
		if err != nil {
			return conversation{}, err
		}
		head := sources[id].head
		if changed(head, end) {
			return conversation{}, errChanged
		}
		if err := end.validate(0); err != nil {
			return conversation{}, err
		}
	}
	final, err := r.session(ctx, session)
	if errors.Is(err, errMissing) {
		return conversation{}, errChanged
	}
	if err != nil {
		return conversation{}, err
	}
	if row.Manager != final.Manager || row.Assistant != final.Assistant ||
		!proto.SameEngineHistory(row.History, final.History) {
		return conversation{}, errChanged
	}
	return conversation{row, stretches}, nil
}

func (r *snapshotReader) session(ctx context.Context, id string) (sessionRow, error) {
	var page struct {
		Sessions []sessionRow `json:"sessions"`
		More     *bool        `json:"has_more"`
		Total    *int         `json:"total"`
		Revision string       `json:"revision"`
	}
	query := url.Values{"machine_id": {r.machine}, "session_id": {id}, "summary": {"0"}, "limit": {"1"}}
	if err := r.get(ctx, "/api/v1/sessions", query, &page); err != nil {
		return sessionRow{}, err
	}
	if page.Revision == "" || page.More == nil || *page.More || page.Sessions == nil ||
		page.Total == nil || *page.Total != len(page.Sessions) || len(page.Sessions) > 1 {
		return sessionRow{}, errInvalid
	}
	if len(page.Sessions) == 0 {
		return sessionRow{}, errMissing
	}
	row := page.Sessions[0]
	if row.Machine != r.machine || row.ID != id {
		return sessionRow{}, errInvalid
	}
	return row, nil
}

func (r *snapshotReader) itemPath(session string) string {
	return "/api/v1/sessions/" + url.PathEscape(r.machine) + "/" + url.PathEscape(session) + "/items"
}

func (r *snapshotReader) page(ctx context.Context, id string, before, limit int) (itemPage, error) {
	if r.pages >= maxPages {
		return itemPage{}, errBudget
	}
	r.pages++
	query := url.Values{"limit": {strconv.Itoa(limit)}, "lazy_images": {"1"}, "strict": {"1"}}
	if before > 0 {
		query.Set("before_seq", strconv.Itoa(before))
	}
	var page itemPage
	err := r.get(ctx, r.itemPath(id), query, &page)
	return page, err
}

func (p itemPage) watermark() int {
	if p.To != nil {
		return *p.To
	}
	return 0
}

func (p itemPage) validate(before int) error {
	if !p.Strict {
		return errStrict
	}
	if p.Items == nil || p.More == nil || p.From == nil || p.To == nil || p.watermark() < 0 {
		return errInvalid
	}
	if before > 0 && p.watermark() != before-1 {
		return errInvalid
	}
	// An empty source can lack a token. Any written line needs a stable span.
	if (len(p.Items) != 0 || *p.More || p.watermark() > 0) && p.History == "" {
		return errInvalid
	}
	if len(p.Items) == 0 && (before > 0 || *p.More) {
		return errInvalid
	}
	from := 0
	if *p.More {
		from = p.Items[0].Seq - 1
	}
	if *p.From != from {
		return errInvalid
	}
	for i, it := range p.Items {
		if !hubclient.ValidID(it.ID) || it.Seq < 1 || it.Seq > p.watermark() || it.FullSize < 0 ||
			(i > 0 && p.Items[i-1].Seq > it.Seq) {
			return errInvalid
		}
	}
	return nil
}

func changed(head, end itemPage) bool {
	return head.History != end.History || end.watermark() < head.watermark()
}

func (r *snapshotReader) collect(ctx context.Context, id string, head itemPage) ([]proto.Item, error) {
	var pages [][]proto.Item
	ids := make(map[string]bool)
	page, before := head, 0
	for {
		if page.Strict && page.History != head.History {
			return nil, errChanged
		}
		if err := page.validate(before); err != nil {
			return nil, err
		}
		r.items += len(page.Items)
		if r.items > maxItems {
			return nil, errBudget
		}
		for _, item := range page.Items {
			if ids[item.ID] {
				return nil, errInvalid
			}
			ids[item.ID] = true
		}
		pages = append(pages, page.Items)
		if !*page.More {
			break
		}
		before = page.Items[0].Seq
		next, err := r.page(ctx, id, before, pageSize)
		if err != nil {
			return nil, err
		}
		page = next
	}
	items := make([]proto.Item, 0, len(ids))
	for _, p := range slices.Backward(pages) {
		items = append(items, p...)
	}
	return items, nil
}

func (r *snapshotReader) expand(ctx context.Context, session string, item *proto.Item) error {
	if item.FullSize == 0 {
		return nil
	}
	if r.fullReads >= maxFullReads || item.FullSize > maxReplyBytes {
		return errBudget
	}
	r.fullReads++
	var full struct {
		proto.Item
		Text            *string `json:"text"`
		Strict          bool    `json:"strict_read"`
		SourceTextBytes *int    `json:"source_text_bytes"`
	}
	if err := r.get(ctx, r.itemPath(session)+"/"+url.PathEscape(item.ID)+"/full", url.Values{"strict": {"1"}}, &full); err != nil {
		return err
	}
	if !full.Strict {
		return errStrict
	}
	if full.SourceTextBytes == nil || *full.SourceTextBytes != item.FullSize || full.ID != item.ID || full.Seq != item.Seq || full.Kind != item.Kind || full.Role != item.Role ||
		full.FullSize != 0 || full.Text == nil || *full.Text == "" {
		return errInvalid
	}
	item.Text, item.Injected, item.FullSize = *full.Text, full.Injected, 0
	return nil
}

func (row sessionRow) windows() ([]stretch, error) {
	if len(row.History) >= maxStretches {
		return nil, errBudget
	}
	var after time.Time
	windows := make([]stretch, 0, len(row.History)+1)
	for _, leg := range row.History {
		if !hubclient.ValidID(leg.SessionID) || leg.Until.IsZero() || !leg.Until.After(after) {
			return nil, errInvalid
		}
		windows = append(windows, stretch{session: leg.SessionID, agent: leg.Agent, after: after, until: leg.Until})
		after = leg.Until
	}
	return append(windows, stretch{session: row.ID, agent: row.Agent, after: after}), nil
}

func placeItems(stretches []stretch, id string, items []proto.Item) error {
	if len(stretches) == 1 {
		stretches[0].items = items
		return nil
	}
	if len(items) == 0 {
		return nil
	}
	first := slices.IndexFunc(items, func(item proto.Item) bool { return !item.TS.IsZero() })
	if first < 0 {
		return errInvalid
	}
	// Like the chat, a row without a timestamp follows its previous timed
	// item. Leading untimed rows follow the first timestamp in their source.
	at := items[first].TS
	for _, item := range items {
		if !item.TS.IsZero() {
			at = item.TS
		}
		for i := range stretches {
			s := &stretches[i]
			if s.session == id && (s.after.IsZero() || at.After(s.after)) && (s.until.IsZero() || !at.After(s.until)) {
				s.items = append(s.items, item)
				break
			}
		}
	}
	return nil
}
