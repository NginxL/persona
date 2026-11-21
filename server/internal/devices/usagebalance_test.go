package devices

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func balance(name, amount string, sampled time.Time) proto.UsageAccount {
	return proto.UsageAccount{Provider: "deepseek", Name: name, Ref: "balance:" + name, SampledAt: sampled,
		Balance: &proto.UsageBalance{Amount: amount, Currency: "CNY"}}
}

// A balance row lives beside window rows: it never hides one, and its own amount is neither a percent nor a window.
func TestMergeKeepsBalanceAndWindowAccountsTogether(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	in := []proto.UsageAccount{
		balance("DeepSeek", "12.34", now.Add(-time.Minute)),
		local("codex", "Codex Pro", now.Add(-time.Minute), uw("5h", 300, 20, now.Add(3*time.Hour)), uw("7d", 10080, 40, now.Add(3*24*time.Hour))),
		local("claude", "Claude Max", now.Add(-time.Minute), uw("7d", 10080, 70, now.Add(2*24*time.Hour))),
	}
	got := mergeUsageAccounts(in, now)
	if len(got) != 3 {
		t.Fatalf("want 3 accounts, got %+v", got)
	}
	var ds, codex, claude *proto.UsageAccount
	for i := range got {
		switch got[i].Name {
		case "DeepSeek":
			ds = &got[i]
		case "Codex Pro":
			codex = &got[i]
		case "Claude Max":
			claude = &got[i]
		}
	}
	if ds == nil || codex == nil || claude == nil {
		t.Fatalf("accounts: %+v", got)
	}
	if ds.Balance == nil || ds.Balance.Amount != "12.34" || ds.Balance.Currency != "CNY" || len(ds.Windows) != 0 || !strings.HasPrefix(ds.ID, "deepseek-") {
		t.Errorf("balance row: %+v", ds)
	}
	if len(codex.Windows) != 2 || codex.Windows[0].UsedPercent != 20 || len(claude.Windows) != 1 || claude.Windows[0].UsedPercent != 70 {
		t.Errorf("window rows changed: codex=%+v claude=%+v", codex, claude)
	}
}

// A genuine zero is kept; a failed read before any success is kept without an amount, so it cannot read as zero.
func TestMergeDistinguishesZeroFromAFailedBalance(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	zero := balance("DeepSeek", "0.00", now)
	failed := proto.UsageAccount{Provider: "deepseek", Name: "DeepSeek failed", Ref: "balance:DeepSeek failed", SampledAt: now, Problem: "unreachable"}
	got := mergeUsageAccounts([]proto.UsageAccount{zero, failed}, now)
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	for _, a := range got {
		switch a.Name {
		case "DeepSeek":
			if a.Balance == nil || a.Balance.Amount != "0.00" || a.Problem != "" {
				t.Errorf("zero: %+v", a)
			}
		case "DeepSeek failed":
			if a.Balance != nil || a.Problem != "unreachable" || !a.Stale {
				t.Errorf("failed: %+v", a)
			}
		default:
			t.Errorf("unexpected %+v", a)
		}
	}
}

// The row keeps its id while the amount moves: the launcher identifies it, not the number.
func TestBalanceRowIDsAreStable(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	a := mergeUsageAccounts([]proto.UsageAccount{balance("DeepSeek", "12.34", now)}, now)
	b := mergeUsageAccounts([]proto.UsageAccount{balance("DeepSeek", "0.11", now.Add(time.Minute))}, now.Add(time.Minute))
	if len(a) != 1 || len(b) != 1 || a[0].ID != b[0].ID {
		t.Fatalf("ids: %q vs %q", a[0].ID, b[0].ID)
	}
}

// The poller keeps the last good amount through a failed read, and a zero replaces it when the provider says zero.
func TestBalancePollerKeepsTheLastAmountAndMarksFailures(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	step := 0
	var b usageBalances
	b.init(func(context.Context) ([]proto.UsageAccount, bool) {
		switch step {
		case 0:
			return []proto.UsageAccount{balance("DeepSeek", "12.34", time.Time{})}, true
		case 1:
			return []proto.UsageAccount{{Provider: "deepseek", Name: "DeepSeek", Ref: "balance:DeepSeek", Problem: "unreachable"}}, true
		default:
			return []proto.UsageAccount{balance("DeepSeek", "0.00", time.Time{})}, true
		}
	}, nil, nil)

	b.pollDue(ctx, now)
	first := b.accounts(now)
	if len(first) != 1 || first[0].Balance == nil || first[0].Balance.Amount != "12.34" || first[0].Problem != "" {
		t.Fatalf("first read: %+v", first)
	}
	sampled := first[0].SampledAt

	step = 1
	later := now.Add(usageBalancePoll)
	b.pollDue(ctx, later)
	kept := b.accounts(later)
	if len(kept) != 1 || kept[0].Balance == nil || kept[0].Balance.Amount != "12.34" || !kept[0].SampledAt.Equal(sampled) {
		t.Fatalf("a failed read must keep the last amount and its time: %+v", kept)
	}
	if kept[0].Problem != "unreachable" || !kept[0].Stale {
		t.Fatalf("a failed read must say so: %+v", kept)
	}

	step = 2
	then := later.Add(usageBalancePoll)
	b.pollDue(ctx, then)
	zero := b.accounts(then)
	if len(zero) != 1 || zero[0].Balance == nil || zero[0].Balance.Amount != "0.00" || zero[0].Problem != "" || zero[0].Stale {
		t.Fatalf("a zero read replaces the old amount: %+v", zero)
	}
}

// An unreadable launcher list is not "every launcher was removed": the last samples stay.
func TestBalancePollerKeepsSamplesWhenTheLauncherListIsUnknown(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	known := true
	var b usageBalances
	b.init(func(context.Context) ([]proto.UsageAccount, bool) {
		if !known {
			return nil, false
		}
		return []proto.UsageAccount{balance("DeepSeek", "12.34", time.Time{})}, true
	}, nil, nil)
	b.pollDue(ctx, now)
	known = false
	b.pollDue(ctx, now.Add(usageBalancePoll))
	if got := b.accounts(now.Add(usageBalancePoll)); len(got) != 1 || got[0].Balance == nil || got[0].Balance.Amount != "12.34" {
		t.Fatalf("kept: %+v", got)
	}
}

// The route carries the amount and currency, and no field of the account holds a key.
func TestUsageWindowsRouteIncludesBalanceAccounts(t *testing.T) {
	now := time.Now()
	h := newTestDevices(t)
	h.usageBal.init(func(context.Context) ([]proto.UsageAccount, bool) {
		return []proto.UsageAccount{balance("DeepSeek", "12.34", time.Time{})}, true
	}, h.usageWin.invalidate, h.logf)
	h.usageBal.pollDue(context.Background(), now)
	mux := h.Handler(http.NotFoundHandler())
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/usage/windows", nil))
	body := w.Body.String()
	var out struct {
		Accounts []proto.UsageAccount `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if len(out.Accounts) != 1 || out.Accounts[0].Balance == nil || out.Accounts[0].Balance.Amount != "12.34" || out.Accounts[0].Balance.Currency != "CNY" {
		t.Fatalf("route: %s", body)
	}
	if !strings.Contains(body, `"windows":[]`) {
		t.Errorf("a balance account has no windows: %s", body)
	}
}
