package usagesource

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// A relay also answers an ordinary API key (the kind a launcher uses) about the accounts that key can use, with no
// management key: its key self-service usage document (#12311, docs/server/usage-windows.md "What is read and how it maps").
//
//	GET /me/usage  → {"account_quota_shared": true, "accounts": [{account_id, provider, plan, plan_raw, state,
//	                   windows: [{kind, scope, scoped_model, used_ratio, remaining, limit, resets_at, checked_at}],
//	                   checked_at, cached}], …}
//
// The windows are the stored ones the management API's usage call returns, in the same vocabulary, so they go through
// mapRelay. The relay answers from its stored snapshot and never asks upstream for it. The document carries no
// account names (nor emails or credentials): an account is named after its provider and plan. The key's own request
// counts and spending in the same document are not read; nor are the seats behind another relay ("upstream"). The
// relay refuses query parameters it does not know, so none are sent.
const relayKeyPath = "/me/usage"

type relayKeyDoc struct {
	// Shared false: the relay shares no account quota with this key (accounts is then empty). An older relay leaves it
	// out, which means shared.
	Shared *bool `json:"account_quota_shared"`
	// Accounts is a pointer so that a document without the list (not this kind of answer) can be told from an empty one.
	Accounts *[]relayKeyAccount `json:"accounts"`
}

type relayKeyAccount struct {
	AccountID string        `json:"account_id"`
	Provider  string        `json:"provider"`
	Plan      string        `json:"plan"`
	PlanRaw   string        `json:"plan_raw"`
	Windows   []relayWindow `json:"windows"`
	CheckedAt *time.Time    `json:"checked_at"`
}

// fetchRelayKey reads the document once. A trailing /v1 on the address is dropped, so that the base URL a launcher
// uses can be given as it is (the relay serves the document at its root).
func fetchRelayKey(ctx context.Context, c *client, now time.Time) (Result, error) {
	root := *c
	root.base = strings.TrimSuffix(root.base, "/v1")
	var doc relayKeyDoc
	if err := root.do(ctx, "GET", relayKeyPath, nil, &doc); err != nil {
		return Result{}, err
	}
	res := Result{Seen: Seen{Providers: map[string]int{}}}
	if doc.Shared != nil && !*doc.Shared {
		res.Seen.QuotaHidden = true
		return res, nil
	}
	if doc.Accounts == nil {
		return Result{}, &Error{Code: CodeBadResponse, Detail: "no account list"}
	}
	seen := map[string]bool{}
	for _, a := range *doc.Accounts {
		id := strings.TrimSpace(a.AccountID)
		if id == "" || seen[id] { // an account listed twice: the first listing wins, as in the other sources
			continue
		}
		seen[id] = true
		res.Seen.Listed++
		provider := clip(strings.ToLower(strings.TrimSpace(a.Provider)), 32)
		if provider == "" || len(res.Accounts) >= AccountsMost {
			continue
		}
		acct, windows, ok := mapRelay(relayAccount{ID: id, Name: keyAccountName(provider, a.Plan), Provider: provider},
			relayEntry{AccountID: id, Plan: a.Plan, PlanRaw: a.PlanRaw, Windows: a.Windows, CheckedAt: a.CheckedAt}, now)
		if !ok {
			continue
		}
		res.Accounts = append(res.Accounts, acct)
		res.Seen.Accounts++
		res.Seen.Windows += windows
		res.Seen.Providers[acct.Provider]++
	}
	sort.SliceStable(res.Accounts, func(i, j int) bool { return res.Accounts[i].Ref < res.Accounts[j].Ref })
	return res, nil
}

// keyAccountName is what an account of the document is called, for want of a name somebody gave it: its provider and
// plan, as the computers name the accounts they read ("Codex Pro"); two of the same are numbered by the merge.
func keyAccountName(provider, plan string) string {
	name := capitalized(provider)
	if p := capitalized(clip(strings.TrimSpace(plan), 40)); p != "" {
		name += " " + p
	}
	return name
}

// capitalized is s with its first letter upper-cased ("claude" → "Claude", "max" → "Max").
func capitalized(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 || r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}
