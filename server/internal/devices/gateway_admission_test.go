package devices

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/usagesource"
)

var quotaFixtureTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func quotaInventory(now time.Time) proto.GatewayAccounts {
	early, late, expired := now.Add(12*time.Minute), now.Add(21*time.Minute), now.Add(-time.Minute)
	ratio, remaining := 1.0, 0.0
	return proto.GatewayAccounts{
		Sources: []proto.GatewaySource{
			{ID: "pool-a", Complete: true, Listed: 4, CheckedAt: &now},
			{ID: "pool-b", Complete: true, Listed: 1, CheckedAt: &now},
		},
		Accounts: []proto.GatewayAccount{
			{Source: "pool-a", AccountID: "a1", Provider: "claude", Status: "exhausted", CheckedAt: &now, Windows: []proto.GatewayWindow{
				{Scope: "account", UsedRatio: &ratio, ResetsAt: &early},
				{Scope: "account", Remaining: &remaining, ResetsAt: &late},
				{Scope: "model", UsedRatio: &ratio, ResetsAt: &expired},
			}},
			{Source: "pool-a", AccountID: "a2", Provider: "claude", Status: "cooldown", CheckedAt: &now, CooldownUntil: &early},
			{Source: "pool-a", AccountID: "a3", Provider: "claude", Status: "disabled"},
			{Source: "pool-a", AccountID: "a4", Provider: "codex", Status: "active", Schedulable: true, CheckedAt: &now},
			{Source: "pool-b", AccountID: "b1", Provider: "claude", Status: "active", Schedulable: true, CheckedAt: &now},
		},
	}
}

func TestGatewayRetryAtDecisionIsPerSourceAndProvider(t *testing.T) {
	now := quotaFixtureTime
	inventory := quotaInventory(now)
	if got := gatewayRetryAt(inventory, "pool-a", "claude", now); !got.Equal(now.Add(12 * time.Minute)) {
		t.Fatalf("got %v, want the fastest disabled-by-quota account's recovery", got)
	}
	for _, request := range [][2]string{{"pool-b", "claude"}, {"pool-a", "codex"}, {"pool-b", "codex"}, {"missing", "claude"}} {
		if got := gatewayRetryAt(inventory, request[0], request[1], now); !got.IsZero() {
			t.Errorf("%v must not wait: %v", request, got)
		}
	}
	account := inventory.Accounts[0]
	if got := quotaAccountRecovery(account, now); !got.Equal(now.Add(21 * time.Minute)) {
		t.Fatalf("both account windows must recover, got %v", got)
	}
}

func TestGatewayRetryAtNeedsCompleteFreshRecoverableEvidence(t *testing.T) {
	now := quotaFixtureTime
	stale := now.Add(-gatewayAdmissionFresh - time.Second)
	future := now.Add(time.Second)
	for _, tc := range []struct {
		name   string
		change func(*proto.GatewayAccounts)
	}{
		{"incomplete source", func(i *proto.GatewayAccounts) { i.Sources[0].Complete = false }},
		{"duplicate source", func(i *proto.GatewayAccounts) { i.Sources = append(i.Sources, i.Sources[0]) }},
		{"failed source", func(i *proto.GatewayAccounts) { i.Sources[0].Error = "failed" }},
		{"old source", func(i *proto.GatewayAccounts) { i.Sources[0].CheckedAt = &stale }},
		{"missing account", func(i *proto.GatewayAccounts) { i.Accounts = i.Accounts[1:] }},
		{"unknown provider on a live account", func(i *proto.GatewayAccounts) { i.Accounts[0].Provider = "" }},
		{"future account observation", func(i *proto.GatewayAccounts) { i.Accounts[1].CheckedAt = &future }},
		{"old account observation", func(i *proto.GatewayAccounts) { i.Accounts[1].CheckedAt = &stale }},
		{"stale account", func(i *proto.GatewayAccounts) { i.Accounts[1].Stale = true }},
		{"missing observation", func(i *proto.GatewayAccounts) { i.Accounts[1].CheckedAt = nil }},
		{"schedulable", func(i *proto.GatewayAccounts) { i.Accounts[0].Schedulable = true }},
		{"unresolved cooldown", func(i *proto.GatewayAccounts) { i.Accounts[1].CooldownUntil = nil }},
		{"unresolved limit", func(i *proto.GatewayAccounts) { i.Accounts[0].Windows[1].ResetsAt = nil }},
		{"wrong quota scope", func(i *proto.GatewayAccounts) { i.Accounts[0].Windows = i.Accounts[0].Windows[2:] }},
		{"requires sign-in", func(i *proto.GatewayAccounts) { i.Accounts[1].Status = "refresh_required" }},
		{"all accounts disabled", func(i *proto.GatewayAccounts) { i.Accounts[0].Status, i.Accounts[1].Status = "disabled", "disabled" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inventory := quotaInventory(now)
			tc.change(&inventory)
			if got := gatewayRetryAt(inventory, "pool-a", "claude", now); !got.IsZero() {
				t.Fatalf("unconfirmed observation produced a wait until %v", got)
			}
		})
	}
}

func TestGatewayRetryAtRejectsRelayRosterWithAnUnidentifiedAccount(t *testing.T) {
	now := quotaFixtureTime
	for _, malformed := range []bool{false, true} {
		fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/admin/accounts":
				rows := []map[string]any{{"id": "quota-bound", "provider": "claude", "status": "exhausted", "schedulable": false}}
				if malformed {
					rows = append(rows, map[string]any{"provider": "claude", "status": "active", "schedulable": true})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"accounts": rows})
			case "/api/admin/accounts/usage":
				account := map[string]any{"account_id": "quota-bound", "checked_at": now, "windows": []map[string]any{{
					"kind": "five_hour", "scope": "account", "used_ratio": 1, "resets_at": now.Add(6 * time.Minute), "checked_at": now,
				}}}
				_ = json.NewEncoder(w).Encode(map[string]any{"accounts": []map[string]any{account}})
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		report, err := usagesource.Fetch(context.Background(), usagesource.Config{
			Type: usagesource.TypeRelay, URL: fixture.URL, Key: "fixture-key", Client: fixture.Client(),
		}, now)
		fixture.Close()
		if err != nil || len(report.Inventory) != 1 || report.Seen.Listed != 1 || report.Complete == malformed {
			t.Fatalf("malformed=%v: advisory data must survive and completeness must match: %+v %v", malformed, report, err)
		}
		report.Inventory[0].Source = "bound"
		inventory := proto.GatewayAccounts{Sources: []proto.GatewaySource{{
			ID: "bound", Complete: report.Complete, Listed: report.Seen.Listed, CheckedAt: &now,
		}}, Accounts: report.Inventory}
		recovery := gatewayRetryAt(inventory, "bound", "claude", now)
		if malformed && !recovery.IsZero() || !malformed && !recovery.Equal(now.Add(6*time.Minute)) {
			t.Fatalf("malformed=%v: recovery %v", malformed, recovery)
		}
	}
}
