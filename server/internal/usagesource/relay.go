package usagesource

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// An account-pool relay pools provider accounts behind one gateway and stores each account's quota
// windows, harvested from the headers upstream attaches to ordinary traffic. Its management API, all under
// /api/admin with "Authorization: Bearer <management key>":
//
//	GET  /accounts?view=compact&limit=   → {"accounts": [{id, name, provider, status, schedulable, last_error,
//	                                        quota_summary, …}], next_after_id?, next_after_priority?}  (no credentials,
//	                                        no email; paged with after_id and after_priority)
//	POST /accounts/usage {ids, refresh}  → {"accounts": [{account_id, plan_display_name, windows: [...], checked_at, …}]}
//
// refresh is always false here: the gateway answers from its database and never calls an upstream (a refresh would
// spend the pool's probe budget, and every probe leaves from the gateway's one address). No other route is used.
// A scoped key (an "onboarder" or "operator" group key, gateway/docs/admin-access.md) lists and reads only its group's accounts,
// so a dedicated group key is the recommended one.
const (
	relayAccountsPath = "/api/admin/accounts?view=compact"
	relayUsagePath    = "/api/admin/accounts/usage"
	// relayBatch is how many accounts one usage call names (the gateway accepts 500; smaller calls keep each read short).
	relayBatch = 100
	// relayPage is how many accounts one page of the list asks for (the gateway's most); a gateway before paging
	// ignores it and sends the whole list.
	relayPage = 500
)

type relayAccount struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
	// Schedulable is nil from a gateway that does not say (taken as schedulable).
	Schedulable   *bool          `json:"schedulable"`
	CooldownUntil string         `json:"cooldown_until"`
	LastError     string         `json:"last_error"`
	QuotaSummary  []relaySummary `json:"quota_summary"`
}

// relaySummary is the compact list's short form of one stored window: what a disabled account (never in the usage
// call) still shows.
type relaySummary struct {
	Kind        string     `json:"kind"`
	ScopedModel string     `json:"scoped_model"`
	UsedRatio   *float64   `json:"used_ratio"`
	ResetsAt    *time.Time `json:"resets_at"`
}

type relayWindow struct {
	Kind        string     `json:"kind"`
	Scope       string     `json:"scope"`
	ScopedModel string     `json:"scoped_model"`
	UsedRatio   *float64   `json:"used_ratio"`
	Remaining   *float64   `json:"remaining"`
	Limit       *float64   `json:"limit"`
	ResetsAt    *time.Time `json:"resets_at"`
	CheckedAt   time.Time  `json:"checked_at"`
}

type relayEntry struct {
	AccountID       string        `json:"account_id"`
	Plan            string        `json:"plan"`
	PlanRaw         string        `json:"plan_raw"`
	PlanDisplayName string        `json:"plan_display_name"`
	Windows         []relayWindow `json:"windows"`
	CheckedAt       *time.Time    `json:"checked_at"`
	Cached          bool          `json:"cached"`
	Error           string        `json:"error"`
	Unsupported     bool          `json:"unsupported"`
	Stale           bool          `json:"stale"`
}

// fetchRelay reads the gateway once: the account list (paged), then the stored usage of the accounts that are not
// disabled. One pass gives both the samples the sidebar's strip draws (Accounts, mapRelay) and the account list
// the extensions read (Inventory, docs/server/usage-windows.md"Gateway account inventory"), disabled accounts included.
func fetchRelay(ctx context.Context, c *client, now time.Time) (Result, error) {
	list, complete, err := relayList(ctx, c)
	if err != nil {
		return Result{}, err
	}
	res := Result{Seen: Seen{Providers: map[string]int{}}, Complete: complete}
	ids := make([]string, 0, min(len(list), AccountsMost))
	byID := map[string]relayAccount{}
	var order []string // the inventory, in the gateway's order
	for _, a := range list {
		a.ID = strings.TrimSpace(a.ID)
		if a.ID == "" {
			// The sidebar can use the other rows. A scheduler cannot use this
			// roster as proof that it has seen every available account.
			res.Complete = false
			continue
		}
		if _, dup := byID[a.ID]; dup {
			continue
		}
		byID[a.ID] = a
		order = append(order, a.ID)
		if a.Status == "disabled" || len(ids) >= AccountsMost { // switched off on purpose: nothing to watch
			continue
		}
		ids = append(ids, a.ID)
	}
	entries := map[string]relayEntry{}
	for start := 0; start < len(ids); start += relayBatch {
		batch := ids[start:min(start+relayBatch, len(ids))]
		body, _ := json.Marshal(map[string]any{"ids": batch, "refresh": false})
		var out struct {
			Accounts []relayEntry `json:"accounts"`
		}
		if err := c.do(ctx, "POST", relayUsagePath, bytes.NewReader(body), &out); err != nil {
			return Result{}, err
		}
		for _, e := range out.Accounts {
			a, ok := byID[e.AccountID]
			if !ok { // an answer for an account we did not ask about is not used
				continue
			}
			if _, dup := entries[e.AccountID]; dup {
				continue
			}
			entries[e.AccountID] = e
			if acct, windows, ok := mapRelay(a, e, now); ok {
				res.Accounts = append(res.Accounts, acct)
				res.Seen.Accounts++
				res.Seen.Windows += windows
				res.Seen.Providers[acct.Provider]++
			}
		}
	}
	res.Seen.Listed = len(order) // distinct ids: a page read twice does not count its accounts twice
	sort.SliceStable(res.Accounts, func(i, j int) bool { return res.Accounts[i].Ref < res.Accounts[j].Ref })
	res.Inventory = make([]proto.GatewayAccount, 0, len(order))
	for _, id := range order {
		e, read := entries[id]
		res.Inventory = append(res.Inventory, inventoryAccount(byID[id], e, read))
	}
	return res, nil
}

// relayList reads the account list, page by page: {"accounts": [...], "next_after_id", "next_after_priority"}
// from a gateway that pages the compact view, {"accounts": [...]} or a bare array (the fields used are the same) from
// one before it. complete is false when the list stopped short: InventoryMost accounts, a cursor given again, or a page
// after the first that could not be read (the first one failing is the error).
func relayList(ctx context.Context, c *client) (list []relayAccount, complete bool, err error) {
	path := relayAccountsPath + "&limit=" + strconv.Itoa(relayPage)
	seen := map[string]bool{}
	for {
		page, next, err := relayPageOf(ctx, c, path)
		if err != nil {
			if len(seen) == 0 {
				return nil, false, err
			}
			return list, false, nil
		}
		for _, a := range page {
			if len(list) >= InventoryMost {
				return list, false, nil
			}
			list = append(list, a)
		}
		if next == "" || len(page) == 0 {
			return list, true, nil
		}
		if seen[next] { // the gateway gave a cursor again: its paging went wrong, the list is not known to be whole
			return list, false, nil
		}
		if len(list) >= InventoryMost {
			return list, false, nil
		}
		seen[next] = true
		path = relayAccountsPath + "&limit=" + strconv.Itoa(relayPage) + "&" + next
	}
}

// relayPageOf reads one page; next is the query of the page after it ("" when this is the last).
func relayPageOf(ctx context.Context, c *client, path string) (page []relayAccount, next string, err error) {
	var raw json.RawMessage
	if err := c.do(ctx, "GET", path, nil, &raw); err != nil {
		return nil, "", err
	}
	if t := bytes.TrimSpace(raw); len(t) > 0 && t[0] == '[' {
		if err := json.Unmarshal(t, &page); err != nil {
			return nil, "", &Error{Code: CodeBadResponse, Detail: "account list"}
		}
		return page, "", nil
	}
	var wrapped struct {
		Accounts     []relayAccount  `json:"accounts"`
		NextID       json.RawMessage `json:"next_after_id"`
		NextPriority json.RawMessage `json:"next_after_priority"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, "", &Error{Code: CodeBadResponse, Detail: "account list"}
	}
	id, priority := cursorOf(wrapped.NextID), cursorOf(wrapped.NextPriority)
	if id == "" {
		return wrapped.Accounts, "", nil
	}
	q := url.Values{"after_id": {id}}
	if priority != "" {
		q.Set("after_priority", priority)
	}
	return wrapped.Accounts, q.Encode(), nil
}

// cursorOf is a page cursor as the query carries it: a string as it is, a number as written ("" when absent or null).
func cursorOf(raw json.RawMessage) string {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(t, &s) == nil {
		return clip(s, 256)
	}
	var n json.Number
	if json.Unmarshal(t, &n) == nil {
		return n.String()
	}
	return ""
}

// reasonPattern is what of an account's last error is passed on: its head, a short lowercase token. The rest can be
// an upstream's free text (an address, a message naming the account) and is dropped.
var reasonPattern = lazyre.New(`^[a-z_]{1,32}$`)

func reasonOf(lastError string) string {
	head, _, _ := strings.Cut(strings.TrimSpace(lastError), ":")
	if head = strings.TrimSpace(head); reasonPattern.MatchString(head) {
		return head
	}
	return ""
}

// inventoryAccount is one account of the list as usage.gateway_accounts gives it: the usage call's reading when there
// is one (read), else the compact list's summary of its windows, marked stale (a disabled account, one past
// AccountsMost, one the gateway did not answer for).
func inventoryAccount(a relayAccount, e relayEntry, read bool) proto.GatewayAccount {
	provider := clip(strings.ToLower(strings.TrimSpace(a.Provider)), 32)
	out := proto.GatewayAccount{
		AccountID: clip(a.ID, 128), Name: clip(strings.TrimSpace(a.Name), 80), Provider: provider,
		Status: clip(strings.ToLower(strings.TrimSpace(a.Status)), 32), Schedulable: a.Schedulable == nil || *a.Schedulable,
		Reason: reasonOf(a.LastError), Windows: []proto.GatewayWindow{},
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(a.CooldownUntil)); err == nil {
		t = t.UTC()
		out.CooldownUntil = &t
	}
	var newest time.Time
	if read {
		out.Plan, out.PlanRaw, out.PlanDisplayName = clip(strings.TrimSpace(e.Plan), 40), clip(strings.TrimSpace(e.PlanRaw), 40), clip(strings.TrimSpace(e.PlanDisplayName), 40)
		out.Cached, out.Stale = e.Cached, e.Stale || e.Error != ""
		for _, w := range e.Windows {
			if len(out.Windows) >= WindowsMost {
				break
			}
			kind := clip(strings.TrimSpace(w.Kind), 64)
			if kind == "" {
				continue
			}
			out.Windows = append(out.Windows, proto.GatewayWindow{Kind: kind, Scope: clip(strings.TrimSpace(w.Scope), 16),
				ScopedModel: clip(strings.TrimSpace(w.ScopedModel), 80), UsedRatio: finite(w.UsedRatio), Limit: finite(w.Limit),
				Remaining: finite(w.Remaining), ResetsAt: utc(w.ResetsAt)})
			if w.CheckedAt.After(newest) {
				newest = w.CheckedAt
			}
		}
		if e.CheckedAt != nil && e.CheckedAt.After(newest) {
			newest = *e.CheckedAt
		}
	}
	if !read || len(e.Windows) == 0 {
		summary := false
		for _, w := range a.QuotaSummary {
			if len(out.Windows) >= WindowsMost {
				break
			}
			kind := clip(strings.TrimSpace(w.Kind), 64)
			if kind == "" {
				continue
			}
			scope := "account"
			model := clip(strings.TrimSpace(w.ScopedModel), 80)
			if model != "" {
				scope = "model"
			}
			out.Windows = append(out.Windows, proto.GatewayWindow{Kind: kind, Scope: scope, ScopedModel: model,
				UsedRatio: finite(w.UsedRatio), ResetsAt: utc(w.ResetsAt)})
			summary = true
		}
		if summary || !read {
			out.Stale = true
		}
	}
	if !newest.IsZero() {
		t := newest.UTC()
		out.CheckedAt = &t
	}
	return out
}

// finite is v when it is a number JSON can carry (nil for none, NaN or an infinity).
func finite(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return nil
	}
	f := *v
	return &f
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// mapRelay turns one account's stored quota into a usage account; ok is false when there is nothing to draw (the
// provider publishes no usage, or no window has a length and a number). windows counts the windows kept.
func mapRelay(a relayAccount, e relayEntry, now time.Time) (acct proto.UsageAccount, windows int, ok bool) {
	if e.Unsupported {
		return acct, 0, false
	}
	var list []proto.UsageWindow
	checkedAt := map[int]time.Time{}
	var newest time.Time
	for _, w := range e.Windows {
		if len(list) >= WindowsMost {
			break
		}
		minutes, rest, isWindow := splitKind(w.Kind)
		if !isWindow {
			continue
		}
		var used float64
		switch {
		case w.UsedRatio != nil:
			p, good := percent(*w.UsedRatio)
			if !good {
				continue
			}
			used = p
		case w.Limit != nil && w.Remaining != nil && *w.Limit > 0:
			p, good := percent(1 - *w.Remaining / *w.Limit)
			if !good {
				continue
			}
			used = p
		default:
			continue
		}
		model := strings.TrimSpace(w.ScopedModel)
		if model == "" && w.Scope == "model" {
			model = rest
		}
		model = clip(model, 40) // the same bound the generic source puts on a model
		out := proto.UsageWindow{Label: proto.WindowLabel(minutes), WindowMinutes: minutes, UsedPercent: used, ResetsAt: utc(w.ResetsAt), Model: model}
		list = addWindow(list, checkedAt, out, w.CheckedAt)
		if w.CheckedAt.After(newest) {
			newest = w.CheckedAt
		}
	}
	if len(list) == 0 {
		return acct, 0, false
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].WindowMinutes != list[j].WindowMinutes {
			return list[i].WindowMinutes < list[j].WindowMinutes
		}
		return list[i].Model < list[j].Model
	})
	sampled := newest
	if e.CheckedAt != nil && e.CheckedAt.After(sampled) {
		sampled = *e.CheckedAt
	}
	if sampled.IsZero() {
		sampled = now
	}
	provider := clip(strings.ToLower(strings.TrimSpace(a.Provider)), 32)
	name := clip(strings.TrimSpace(a.Name), 80)
	if name == "" {
		name = provider
	}
	plan := clip(firstNonEmpty(e.PlanDisplayName, e.Plan, e.PlanRaw), 40)
	acct = proto.UsageAccount{
		Provider: provider, Name: name, Plan: plan, Windows: list, SampledAt: notAfter(sampled.UTC(), now),
		Stale: e.Stale || e.Error != "", Ref: clip(a.ID, 128),
	}
	if e.Error != "" { // the gateway could not read the numbers just now and kept the stored ones; its text stays there
		acct.Problem = "error"
	}
	return acct, len(list), true
}

func firstNonEmpty(list ...string) string {
	for _, s := range list {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// clip cuts s to at most n runes (a name from outside should not be able to fill the sidebar).
var clip = textfmt.Cut
