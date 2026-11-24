package devices

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestHideUsageProviders(t *testing.T) {
	in := []proto.UsageAccount{
		{Provider: "codex", Name: "Codex Pro"}, {Provider: "Claude", Name: "Claude"},
		{Provider: "agy", Name: "Antigravity"}, {Provider: "antigravity", Name: "Antigravity · Gemini"},
	}
	if got := hideUsageProviders(in, nil); len(got) != 4 {
		t.Fatalf("nothing hidden: %v", got)
	}
	got := hideUsageProviders(in, []string{"codex"})
	if len(got) != 3 || got[0].Provider != "Claude" {
		t.Fatalf("codex hidden: %v", got)
	}
	for _, alias := range []string{"agy", "antigravity"} {
		if got := hideUsageProviders(in, []string{alias}); len(got) != 2 || got[0].Provider != "codex" || got[1].Provider != "Claude" {
			t.Fatalf("hide %s: %+v", alias, got)
		}
	}
	if len(in) != 4 {
		t.Fatal("the input was changed")
	}
}

func TestCleanHiddenProviders(t *testing.T) {
	got, ok := cleanHiddenProviders([]string{" Codex ", "claude", "codex", " Agy ", "antigravity"})
	if !ok || len(got) != 3 || got[0] != "antigravity" || got[1] != "claude" || got[2] != "codex" {
		t.Fatalf("got %v %v", got, ok)
	}
	for _, bad := range [][]string{{""}, {"a b"}, {"../x"}, make([]string, 17)} {
		if _, ok := cleanHiddenProviders(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestAdminUsageReadsLegacyAntigravityVisibility(t *testing.T) {
	d, c := usageTestDevices(t, `{"usage":{"hidden_providers":["agy","antigravity"]}}`)
	if code, body, _ := c.do("GET", "/api/v1/admin/usage-sources", ""); code != http.StatusOK || !oneAntigravity(body["hidden_providers"]) || len(body["providers"].([]any)) != 3 {
		t.Fatalf("want a single Antigravity admin switch: %d %v", code, body)
	}
	now := time.Now()
	d.usageWin.merged = []proto.UsageAccount{{Provider: "antigravity"}}
	d.usageWin.at = now
	code, body, _ := c.do("GET", "/api/v1/usage/windows", "")
	if code != http.StatusOK || len(body["accounts"].([]any)) != 0 || !oneAntigravity(body["hidden_providers"]) {
		t.Fatalf("legacy visibility in usage response: %d %v", code, body)
	}
	code, body, _ = c.do("PUT", "/api/v1/admin/usage-sources", `{"hidden_providers":["agy"]}`)
	if code != http.StatusOK || !oneAntigravity(body["hidden_providers"]) {
		t.Fatalf("legacy alias on write: %d %v", code, body)
	}
	u := managerJSON(t, d)["usage"].(map[string]any)
	if !oneAntigravity(u["hidden_providers"]) {
		t.Fatalf("canonical visibility on disk: %v", u)
	}
}

func oneAntigravity(v any) bool {
	p, _ := v.([]any)
	return len(p) == 1 && p[0] == "antigravity"
}

// The setting is display only: the usage route leaves the provider out, the poll (which the scheduler's capacity view
// reads) still has it, and the setting survives in manager.json and comes back from the admin page.
func TestAdminUsageHiddenProviders(t *testing.T) {
	d, c := usageTestDevices(t, "")
	now := time.Now()
	reset := now.Add(time.Hour)
	fixture := []proto.UsageAccount{
		{ID: "codex-1", Provider: "codex", Name: "Codex Pro", SampledAt: now, Windows: []proto.UsageWindow{{Label: "7d", WindowMinutes: 10080, UsedPercent: 10, ResetsAt: &reset}}},
		{ID: "claude-1", Provider: "claude", Name: "Claude", SampledAt: now, Windows: []proto.UsageWindow{{Label: "5h", WindowMinutes: 300, UsedPercent: 20, ResetsAt: &reset}}},
	}
	seed := func() {
		d.usageWin.mu.Lock()
		defer d.usageWin.mu.Unlock()
		d.usageWin.merged, d.usageWin.at = fixture, now
	}
	names := func() []any {
		t.Helper()
		seed() // an edit drops the merged list; with no machines here a fresh poll would be empty
		code, body, _ := c.do("GET", "/api/v1/usage/windows", "")
		if code != http.StatusOK {
			t.Fatalf("usage/windows: %d", code)
		}
		list, _ := body["accounts"].([]any)
		return list
	}
	if len(names()) != 2 {
		t.Fatalf("before: %v", names())
	}

	code, view, _ := c.do("PUT", "/api/v1/admin/usage-sources", `{"hidden_providers":["Codex"]}`)
	if code != http.StatusOK || len(view["hidden_providers"].([]any)) != 1 || view["hidden_providers"].([]any)[0] != "codex" {
		t.Fatalf("put: %d %v", code, view)
	}
	if got := names(); len(got) != 1 || got[0].(map[string]any)["provider"] != "claude" {
		t.Fatalf("after: %v", got)
	}
	seed()
	if kept := d.usageWin.poll(t.Context(), nil, now); len(kept) != 2 {
		t.Fatalf("the poll must keep every account for the capacity view: %v", kept)
	}
	u, _ := managerJSON(t, d)["usage"].(map[string]any)
	if h, _ := u["hidden_providers"].([]any); len(h) != 1 || h[0] != "codex" {
		t.Fatalf("manager.json: %v", u)
	}
	_, view, _ = c.do("GET", "/api/v1/admin/usage-sources", "")
	if p, _ := view["providers"].([]any); len(p) != 2 || p[0] != "claude" || p[1] != "codex" {
		t.Fatalf("choices: %v", view["providers"])
	}

	if code, _, _ := c.do("PUT", "/api/v1/admin/usage-sources", `{"hidden_providers":["a b"]}`); code != http.StatusBadRequest {
		t.Fatalf("a bad name: %d", code)
	}
	if code, _, _ := c.do("PUT", "/api/v1/admin/usage-sources", `{"poll_minutes":10}`); code != http.StatusOK {
		t.Fatal(code)
	}
	if len(names()) != 1 {
		t.Fatal("an edit of another setting must keep the hidden providers")
	}
	c.do("PUT", "/api/v1/admin/usage-sources", `{"hidden_providers":[]}`)
	if len(names()) != 2 {
		t.Fatalf("cleared: %v", names())
	}
	if u, _ := managerJSON(t, d)["usage"].(map[string]any); u["hidden_providers"] != nil {
		t.Fatalf("cleared setting left in manager.json: %v", u)
	}
}

// The answer names the hidden providers (#13179) and is unchanged when nothing is hidden.
func TestUsageWindowsAnswerHiddenProviders(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	plain := usageWindowsAnswer(nil, nil, now)
	if _, ok := plain["hidden_providers"]; ok {
		t.Fatalf("nothing hidden must not add the field: %v", plain)
	}
	if acc, _ := plain["accounts"].([]proto.UsageAccount); acc == nil {
		t.Fatalf("accounts must be an empty list, not null: %v", plain)
	}
	got := usageWindowsAnswer(nil, []string{"claude", "codex"}, now)
	if h, _ := got["hidden_providers"].([]string); len(h) != 2 || h[0] != "claude" || h[1] != "codex" {
		t.Fatalf("hidden_providers = %v", got["hidden_providers"])
	}
	raw, err := json.Marshal(got)
	if err != nil || !strings.Contains(string(raw), `"hidden_providers":["claude","codex"]`) {
		t.Fatalf("json = %s (%v)", raw, err)
	}
}
