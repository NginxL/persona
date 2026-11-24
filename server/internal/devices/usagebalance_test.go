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

// A key replaced after a good read does not lend its amount to the next credential: a failed read after the change
// shows no amount rather than another account's last known balance (#11825).
func TestBalancePollerDropsTheAmountWhenTheCredentialChanges(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	step := 0
	var b usageBalances
	b.init(func(context.Context) ([]proto.UsageAccount, bool) {
		switch step {
		case 0:
			a := balance("DeepSeek", "12.34", time.Time{})
			a.BalanceCredential = "old"
			return []proto.UsageAccount{a}, true
		default:
			return []proto.UsageAccount{{Provider: "deepseek", Name: "DeepSeek", Ref: "balance:DeepSeek",
				Problem: "rejected", BalanceCredential: "new"}}, true
		}
	}, nil, nil)

	b.pollDue(ctx, now)
	if got := b.accounts(now); len(got) != 1 || got[0].Balance == nil || got[0].Balance.Amount != "12.34" {
		t.Fatalf("first read: %+v", got)
	}

	step = 1
	later := now.Add(usageBalancePoll)
	b.pollDue(ctx, later)
	got := b.accounts(later)
	if len(got) != 1 || got[0].Balance != nil || got[0].Problem != "rejected" {
		t.Fatalf("another credential's amount must not be kept: %+v", got)
	}
	if now.Sub(got[0].SampledAt) > usageBalancePoll {
		t.Fatalf("the failed read must be sampled now: %+v", got[0])
	}
}

// A failure on the same credential still keeps its last known amount (#11825).
func TestBalancePollerKeepsTheAmountForTheSameCredential(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	step := 0
	var b usageBalances
	b.init(func(context.Context) ([]proto.UsageAccount, bool) {
		if step == 0 {
			a := balance("DeepSeek", "12.34", time.Time{})
			a.BalanceCredential = "same"
			return []proto.UsageAccount{a}, true
		}
		return []proto.UsageAccount{{Provider: "deepseek", Name: "DeepSeek", Ref: "balance:DeepSeek",
			Problem: "unreachable", BalanceCredential: "same"}}, true
	}, nil, nil)
	b.pollDue(ctx, now)
	step = 1
	later := now.Add(usageBalancePoll)
	b.pollDue(ctx, later)
	got := b.accounts(later)
	if len(got) != 1 || got[0].Balance == nil || got[0].Balance.Amount != "12.34" || got[0].Problem != "unreachable" {
		t.Fatalf("same credential keeps the amount: %+v", got)
	}
}

// A launcher skipped because the poll's overall context expired keeps its previous state without being marked
// unreachable or removed, and a never-read skipped launcher is not turned into a bogus failed row (#11825).
func TestBalancePollerKeepsPreviousStateWhenSampleIsSkipped(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	step := 0
	var b usageBalances
	b.init(func(context.Context) ([]proto.UsageAccount, bool) {
		if step == 0 {
			return []proto.UsageAccount{
				balance("ds-a", "12.34", time.Time{}),
				balance("ds-b", "5.00", time.Time{}),
			}, true
		}
		return []proto.UsageAccount{
			balance("ds-a", "11.00", time.Time{}),
			{Provider: "deepseek", Name: "ds-b", Ref: "balance:ds-b", BalanceSkipped: true},
			{Provider: "deepseek", Name: "ds-c", Ref: "balance:ds-c", BalanceSkipped: true},
		}, true
	}, nil, nil)

	b.pollDue(ctx, now)
	first := b.accounts(now)
	if len(first) != 2 || first[1].Balance == nil || first[1].Balance.Amount != "5.00" {
		t.Fatalf("first read: %+v", first)
	}
	bSampled := first[1].SampledAt

	step = 1
	later := now.Add(usageBalancePoll)
	b.pollDue(ctx, later)
	got := b.accounts(later)
	if len(got) != 2 {
		t.Fatalf("skipped never-read launcher must not fabricate a row, and existing one must stay: %+v", got)
	}
	if got[0].Name != "ds-a" || got[0].Balance == nil || got[0].Balance.Amount != "11.00" || got[0].Problem != "" {
		t.Fatalf("healthy launcher should still update: %+v", got[0])
	}
	if got[1].Name != "ds-b" || got[1].Balance == nil || got[1].Balance.Amount != "5.00" ||
		got[1].Problem != "" || got[1].Stale || !got[1].SampledAt.Equal(bSampled) {
		t.Fatalf("skipped launcher must keep its previous state unchanged: %+v", got[1])
	}
}

// A temporary open/resolution failure of unchanged sealed material (such as a locked vault behind an op:// reference)
// keeps the last known amount marked stale with problem "failed" and preserves the prior resolved-key identity, so a
// subsequent rotation to a different failing key still drops the old amount (#11825).
func TestBalancePollerRetainsAmountOnTemporaryOpenFailureOfUnchangedSeal(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	step := 0
	var b usageBalances
	b.init(func(context.Context) ([]proto.UsageAccount, bool) {
		switch step {
		case 0:
			a := balance("DeepSeek", "12.34", time.Time{})
			a.BalanceCredential = "resolved-old"
			a.BalanceSealedCredential = "sealed:ref-1"
			return []proto.UsageAccount{a}, true
		case 1:
			// Vault locked / extension temporarily unavailable: same sealed reference, no resolved key.
			return []proto.UsageAccount{{
				Provider:                "deepseek",
				Name:                    "DeepSeek",
				Ref:                     "balance:DeepSeek",
				Problem:                 "failed",
				BalanceSealedCredential: "sealed:ref-1",
			}}, true
		default:
			// Vault unlocked, but the secret behind the same reference was rotated to a different failing key.
			return []proto.UsageAccount{{
				Provider:                "deepseek",
				Name:                    "DeepSeek",
				Ref:                     "balance:DeepSeek",
				Problem:                 "rejected",
				BalanceCredential:       "resolved-new",
				BalanceSealedCredential: "sealed:ref-1",
			}}, true
		}
	}, nil, nil)

	b.pollDue(ctx, now)
	first := b.accounts(now)
	if len(first) != 1 || first[0].Balance == nil || first[0].Balance.Amount != "12.34" || first[0].Stale || first[0].Problem != "" {
		t.Fatalf("first read: %+v", first)
	}
	sampled := first[0].SampledAt

	step = 1
	later := now.Add(usageBalancePoll)
	b.pollDue(ctx, later)
	kept := b.accounts(later)
	if len(kept) != 1 || kept[0].Balance == nil || kept[0].Balance.Amount != "12.34" || kept[0].Balance.Currency != "CNY" || !kept[0].SampledAt.Equal(sampled) {
		t.Fatalf("temporary open failure of unchanged sealed ref must keep last amount and time: %+v", kept)
	}
	if kept[0].Problem != "failed" || !kept[0].Stale {
		t.Fatalf("temporary open failure must mark stale with problem failed: %+v", kept)
	}

	step = 2
	afterRotate := later.Add(usageBalancePoll)
	b.pollDue(ctx, afterRotate)
	dropped := b.accounts(afterRotate)
	if len(dropped) != 1 || dropped[0].Balance != nil || dropped[0].Problem != "rejected" || !dropped[0].Stale {
		t.Fatalf("rotating resolved key after temporary open failure must drop old amount: %+v", dropped)
	}
	if !dropped[0].SampledAt.Equal(afterRotate.UTC()) {
		t.Fatalf("dropped read must be sampled now: got %v want %v", dropped[0].SampledAt, afterRotate.UTC())
	}
}

// Replacing the sealed reference itself with one that fails to open clears the old amount instead of keeping the
// previous credential's balance (#11825).
func TestBalancePollerDropsAmountWhenSealedReferenceIsReplacedAndFailsToOpen(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	step := 0
	var b usageBalances
	b.init(func(context.Context) ([]proto.UsageAccount, bool) {
		if step == 0 {
			a := balance("DeepSeek", "12.34", time.Time{})
			a.BalanceCredential = "resolved-1"
			a.BalanceSealedCredential = "sealed:ref-1"
			return []proto.UsageAccount{a}, true
		}
		return []proto.UsageAccount{{
			Provider:                "deepseek",
			Name:                    "DeepSeek",
			Ref:                     "balance:DeepSeek",
			Problem:                 "failed",
			BalanceSealedCredential: "sealed:ref-2",
		}}, true
	}, nil, nil)

	b.pollDue(ctx, now)
	if first := b.accounts(now); len(first) != 1 || first[0].Balance == nil || first[0].Balance.Amount != "12.34" {
		t.Fatalf("first read: %+v", first)
	}

	step = 1
	later := now.Add(usageBalancePoll)
	b.pollDue(ctx, later)
	got := b.accounts(later)
	if len(got) != 1 || got[0].Balance != nil || got[0].Problem != "failed" || !got[0].Stale {
		t.Fatalf("replaced unopenable seal must drop previous amount: %+v", got)
	}
	if !got[0].SampledAt.Equal(later.UTC()) {
		t.Fatalf("failed read must be sampled now: got %v want %v", got[0].SampledAt, later.UTC())
	}
}

// Re-sealing the same resolved key (e.g. saving the launcher again) keeps the last amount across a network failure and
// updates the tracked sealed digest so a later temporary open failure on the new seal still retains the amount (#11825).
func TestBalancePollerKeepsAmountWhenSameKeyIsResealedAndLaterFailsToOpen(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	step := 0
	var b usageBalances
	b.init(func(context.Context) ([]proto.UsageAccount, bool) {
		switch step {
		case 0:
			a := balance("DeepSeek", "12.34", time.Time{})
			a.BalanceCredential = "resolved-same"
			a.BalanceSealedCredential = "sealed:v1"
			return []proto.UsageAccount{a}, true
		case 1:
			return []proto.UsageAccount{{
				Provider:                "deepseek",
				Name:                    "DeepSeek",
				Ref:                     "balance:DeepSeek",
				Problem:                 "unreachable",
				BalanceCredential:       "resolved-same",
				BalanceSealedCredential: "sealed:v2",
			}}, true
		default:
			return []proto.UsageAccount{{
				Provider:                "deepseek",
				Name:                    "DeepSeek",
				Ref:                     "balance:DeepSeek",
				Problem:                 "failed",
				BalanceSealedCredential: "sealed:v2",
			}}, true
		}
	}, nil, nil)

	b.pollDue(ctx, now)
	sampled := b.accounts(now)[0].SampledAt

	step = 1
	t1 := now.Add(usageBalancePoll)
	b.pollDue(ctx, t1)
	if got := b.accounts(t1); len(got) != 1 || got[0].Balance == nil || got[0].Balance.Amount != "12.34" ||
		got[0].Problem != "unreachable" || !got[0].Stale || !got[0].SampledAt.Equal(sampled) {
		t.Fatalf("same resolved key under new seal must keep amount: %+v", got)
	}

	step = 2
	t2 := t1.Add(usageBalancePoll)
	b.pollDue(ctx, t2)
	if got := b.accounts(t2); len(got) != 1 || got[0].Balance == nil || got[0].Balance.Amount != "12.34" ||
		got[0].Problem != "failed" || !got[0].Stale || !got[0].SampledAt.Equal(sampled) {
		t.Fatalf("temporary open failure on updated seal must keep amount: %+v", got)
	}
}

// A key rotation followed by a BalanceSkipped poll must clear the previous credential's balance instead of leaving it
// visible across deadline exhaustion, while unchanged skipped launchers keep their balance and never-read skipped
// launchers still create no row (#11825).
func TestBalancePollerClearsAmountWhenCredentialRotatesBeforeSkippedRead(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	step := 0
	var b usageBalances
	b.init(func(context.Context) ([]proto.UsageAccount, bool) {
		switch step {
		case 0:
			rotated := balance("ds-rotated", "12.34", time.Time{})
			rotated.BalanceCredential = "resolved-old"
			rotated.BalanceSealedCredential = "sealed:ref-1"
			same := balance("ds-same", "5.00", time.Time{})
			same.BalanceCredential = "resolved-same"
			same.BalanceSealedCredential = "sealed:ref-same"
			return []proto.UsageAccount{rotated, same}, true
		case 1, 2:
			// Overall deadline expires after LauncherBalances resolved the rotated key.
			return []proto.UsageAccount{
				{
					Provider:                "deepseek",
					Name:                    "ds-rotated",
					Ref:                     "balance:ds-rotated",
					BalanceCredential:       "resolved-new",
					BalanceSealedCredential: "sealed:ref-1",
					BalanceSkipped:          true,
				},
				{
					Provider:                "deepseek",
					Name:                    "ds-same",
					Ref:                     "balance:ds-same",
					BalanceCredential:       "resolved-same",
					BalanceSealedCredential: "sealed:ref-same",
					BalanceSkipped:          true,
				},
				{
					Provider:                "deepseek",
					Name:                    "ds-never-read",
					Ref:                     "balance:ds-never-read",
					BalanceCredential:       "resolved-brand-new",
					BalanceSealedCredential: "sealed:ref-new",
					BalanceSkipped:          true,
				},
			}, true
		default:
			rotated := balance("ds-rotated", "50.00", time.Time{})
			rotated.BalanceCredential = "resolved-new"
			rotated.BalanceSealedCredential = "sealed:ref-1"
			same := balance("ds-same", "5.00", time.Time{})
			same.BalanceCredential = "resolved-same"
			same.BalanceSealedCredential = "sealed:ref-same"
			return []proto.UsageAccount{rotated, same}, true
		}
	}, nil, nil)

	b.pollDue(ctx, now)
	first := b.accounts(now)
	if len(first) != 2 || first[0].Name != "ds-rotated" || first[0].Balance.Amount != "12.34" ||
		first[1].Name != "ds-same" || first[1].Balance.Amount != "5.00" {
		t.Fatalf("first read: %+v", first)
	}
	sameSampled := first[1].SampledAt

	// First skipped poll after rotation drops ds-rotated's old balance, keeps ds-same, and ignores ds-never-read.
	step = 1
	t1 := now.Add(usageBalancePoll)
	b.pollDue(ctx, t1)
	got1 := b.accounts(t1)
	if len(got1) != 1 || got1[0].Name != "ds-same" || got1[0].Balance == nil || got1[0].Balance.Amount != "5.00" ||
		got1[0].Problem != "" || got1[0].Stale || !got1[0].SampledAt.Equal(sameSampled) {
		t.Fatalf("rotated skipped launcher must clear old amount and never-read skipped must create no row: %+v", got1)
	}
	if _, ok := b.state["balance:ds-never-read"]; ok {
		t.Fatalf("never-read skipped launcher must not create state entry")
	}

	// Repeated skipped poll continues hiding the old amount without fabricating a failed row.
	step = 2
	t2 := t1.Add(usageBalancePoll)
	b.pollDue(ctx, t2)
	if got2 := b.accounts(t2); len(got2) != 1 || got2[0].Name != "ds-same" {
		t.Fatalf("repeated skipped poll must not resurrect old amount: %+v", got2)
	}

	// Once a non-skipped poll succeeds with the rotated key, the new balance appears normally.
	step = 3
	t3 := t2.Add(usageBalancePoll)
	b.pollDue(ctx, t3)
	got3 := b.accounts(t3)
	if len(got3) != 2 || got3[0].Name != "ds-rotated" || got3[0].Balance == nil || got3[0].Balance.Amount != "50.00" ||
		got3[0].Problem != "" || got3[0].Stale {
		t.Fatalf("subsequent successful read after rotation: %+v", got3)
	}
}
