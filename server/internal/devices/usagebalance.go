package devices

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Prepaid balances as a source of the sidebar's usage strip (#11825, docs/server/usage-windows.md "Prepaid
// balances"). Some providers have no rate-limit window but do report how much money is left (the official DeepSeek
// launcher's GET /user/balance). The assistant reads it, since it owns the launchers and their sealed keys; this keeps
// the last good amount of each launcher, replaces it when a read works, and marks a failed read instead of turning the
// old amount into a zero or hiding it.
//
// The key never reaches here: LauncherBalances hands over an account with an amount or a problem code, no credential.

const (
	usageBalancePoll      = 5 * time.Minute  // how often the launchers are asked
	usageBalanceTick      = 15 * time.Second // how often the loop checks whether they are due
	usageBalanceFetchMost = 20 * time.Second // one whole read, launchers one after another
)

// balanceState is what is known of one launcher.
type balanceState struct {
	account proto.UsageAccount // the last sample; its Balance is nil before the first successful read
	errCode string             // why the last attempt failed; "" when it worked
}

// usageBalances polls the launchers' balances and holds their last samples.
type usageBalances struct {
	mu      sync.Mutex
	fetch   func(ctx context.Context) (accounts []proto.UsageAccount, known bool)
	state   map[string]*balanceState
	lastTry time.Time
	// changed is called when the samples changed (the sidebar's merged list is stale).
	changed func()
	logf    func(string, ...any)
}

func (b *usageBalances) init(fetch func(context.Context) ([]proto.UsageAccount, bool), changed func(), logf func(string, ...any)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fetch, b.changed, b.logf = fetch, changed, logf
	if b.state == nil {
		b.state = map[string]*balanceState{}
	}
}

// run asks the launchers that are due until ctx ends. One read covers every launcher: the assistant knows the list.
func (b *usageBalances) run(ctx context.Context) {
	b.mu.Lock()
	on := b.fetch != nil
	b.mu.Unlock()
	if !on {
		return
	}
	tick := time.NewTicker(usageBalanceTick)
	defer tick.Stop()
	for {
		b.pollDue(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// pollDue reads once when the interval has passed (and the first time). A launcher list that cannot be read keeps
// everything as it was and waits for the next interval.
func (b *usageBalances) pollDue(ctx context.Context, now time.Time) {
	b.mu.Lock()
	if b.fetch == nil {
		b.mu.Unlock()
		return
	}
	if !b.lastTry.IsZero() && now.Sub(b.lastTry) < usageBalancePoll {
		b.mu.Unlock()
		return
	}
	b.lastTry = now
	fetch := b.fetch
	b.mu.Unlock()

	c, cancel := context.WithTimeout(ctx, usageBalanceFetchMost)
	samples, known := fetch(c)
	cancel()

	b.mu.Lock()
	if known {
		b.applyLocked(samples, now)
	}
	changed := b.changed
	b.mu.Unlock()
	if known && changed != nil {
		changed()
	}
}

// applyLocked takes one read's samples. A launcher that still exists keeps its last good amount while a read fails;
// one that is gone is forgotten. A read that failed before the first success keeps an account with no Balance, so the
// sidebar can say the balance is unavailable instead of showing nothing (which would read as zero).
func (b *usageBalances) applyLocked(samples []proto.UsageAccount, now time.Time) {
	seen := map[string]bool{}
	for _, a := range samples {
		if a.Ref == "" {
			continue
		}
		seen[a.Ref] = true
		st := b.state[a.Ref]
		if st == nil {
			st = &balanceState{}
			b.state[a.Ref] = st
		}
		switch {
		case a.Balance != nil:
			a.SampledAt = now.UTC()
			a.Problem = ""
			a.Stale = false
			st.account, st.errCode = a, ""
		case st.account.Balance != nil:
			// keep the last amount and when it was sampled; only the failure is news
			st.account.Name, st.account.Problem = a.Name, a.Problem
			if st.account.Problem == "" {
				st.account.Problem = "failed"
			}
			st.errCode = st.account.Problem
		default:
			if a.Problem == "" {
				a.Problem = "failed"
			}
			a.SampledAt = now.UTC()
			st.account, st.errCode = a, a.Problem
		}
	}
	for ref := range b.state {
		if !seen[ref] {
			delete(b.state, ref)
		}
	}
}

// accounts are the launchers' samples as the sidebar's merge takes them. A failed last read is marked and fades like a
// gateway's; a stale sample is marked stale but never replaced by a made-up number.
func (b *usageBalances) accounts(now time.Time) []proto.UsageAccount {
	b.mu.Lock()
	defer b.mu.Unlock()
	refs := make([]string, 0, len(b.state))
	for ref := range b.state {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	out := make([]proto.UsageAccount, 0, len(refs))
	for _, ref := range refs {
		st := b.state[ref]
		a := st.account
		if now.Sub(a.SampledAt) > usageHubMaxAge {
			continue
		}
		if st.errCode != "" {
			a.Problem = balanceProblem(st.errCode)
			a.Stale = true
		}
		if now.Sub(a.SampledAt) > usageHubStaleAfter {
			a.Stale = true
		}
		out = append(out, a)
	}
	return out
}

// balanceProblem is what the usage panel says about a failed balance read: the provider turned the key away, could not
// be reached, or answered with something else. The words for each code live in the web's catalog.
func balanceProblem(code string) string {
	switch code {
	case "rejected", "unreachable", "failed":
		return code
	}
	return "failed"
}
