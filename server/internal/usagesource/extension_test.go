package usagesource

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFromExtensionKeepsWhatFitsAndLeavesTheRestOut(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	raw := json.RawMessage(`{"accounts": [
		{"id": "pool", "provider": "Claude", "name": "Pool", "headline": "available 5/6\nhighest 95%",
		 "windows": [
		  {"label": "5h", "used_percent": 40, "group": "5h", "title": "Pool", "note": "5/6 up"},
		  {"window_minutes": 10080, "used_percent": 140, "resets_at": "2026-11-25T00:00:00Z", "group": "weekly", "model": "fable"},
		  {"label": "5h"},
		  {"label": "credits", "used_percent": 5},
		  {"label": "7d", "used_percent": -3}
		 ]},
		{"provider": "claude", "name": "Hidden", "notice": "Not visible to this key"},
		{"provider": "Bad Name", "name": "x", "windows": [{"label": "5h", "used_percent": 1}]},
		{"provider": "claude", "name": "Empty"}
	]}`)
	res, err := FromExtension(raw, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Accounts) != 2 || res.Seen.Listed != 4 || res.Seen.Accounts != 2 {
		t.Fatalf("%+v", res)
	}
	a := res.Accounts[0]
	if !a.Pass || a.Provider != "claude" || a.Ref != "pool" || a.Headline != "available 5/6 highest 95%" || !a.SampledAt.Equal(now) {
		t.Fatalf("%+v", a)
	}
	// the window without a number, the one that is not a time window and the negative one are gone; 140 is clamped
	if len(a.Windows) != 3 {
		t.Fatalf("%+v", a.Windows)
	}
	if w := a.Windows[0]; w.Label != "5h" || w.WindowMinutes != 300 || w.Group != "5h" || w.Title != "Pool" || w.Note != "5/6 up" {
		t.Fatalf("%+v", w)
	}
	if w := a.Windows[1]; w.UsedPercent != 100 || w.Label != "7d" || w.Model != "fable" || w.ResetsAt == nil {
		t.Fatalf("%+v", w)
	}
	if n := res.Accounts[1]; n.Notice != "Not visible to this key" || len(n.Windows) != 0 || n.Name != "Hidden" {
		t.Fatalf("%+v", n)
	}
}

func TestFromExtensionCutsLongTextAndTooManyRows(t *testing.T) {
	now := time.Now()
	long := strings.Repeat("x", 1000)
	body := map[string]any{"accounts": []any{map[string]any{"provider": "claude", "name": long, "headline": long,
		"windows": []any{map[string]any{"label": "5h", "used_percent": 1, "group": long, "title": long, "note": long}}}}}
	raw, _ := json.Marshal(body)
	res, err := FromExtension(raw, now)
	if err != nil || len(res.Accounts) != 1 {
		t.Fatal(err, res)
	}
	a := res.Accounts[0]
	w := a.Windows[0]
	if len([]rune(a.Name)) > extNameMost || len([]rune(a.Headline)) > extLineMost || len([]rune(w.Group)) > extShortMost || len([]rune(w.Note)) > extLineMost {
		t.Fatalf("not cut: %d %d %d", len(a.Name), len(a.Headline), len(w.Group))
	}
	many := make([]any, AccountsMost+20)
	for i := range many {
		many[i] = map[string]any{"provider": "claude", "name": "a", "notice": "n"}
	}
	raw, _ = json.Marshal(map[string]any{"accounts": many})
	if res, _ := FromExtension(raw, now); len(res.Accounts) != AccountsMost {
		t.Fatalf("%d accounts", len(res.Accounts))
	}
}

func TestFromExtensionRefusesWhatIsNotAnAnswer(t *testing.T) {
	if _, err := FromExtension(json.RawMessage(`[1,2]`), time.Now()); CodeOf(err) != CodeBadResponse {
		t.Fatal(err)
	}
}

func TestStoredTypeKeepsExtension(t *testing.T) {
	if StoredType(TypeExtension) != TypeExtension || Known(TypeExtension) {
		t.Fatal("extension is read from manager.json, not one the admin page makes")
	}
}

// A gateway's protection line can differ per account and per window: each valid one (0-100) is kept on its own window,
// and anything else (absent, below 0, above 100, not a number) is "not said", never clamped into a value.
func TestFromExtensionKeepsEachWindowsProtectionLine(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	raw := json.RawMessage(`{"accounts": [
		{"provider": "claude", "name": "Seat 1", "windows": [
		  {"label": "5h", "used_percent": 40, "protect_percent": 98},
		  {"label": "7d", "used_percent": 10, "protect_percent": 99.5}]},
		{"provider": "claude", "name": "Seat 2", "windows": [
		  {"label": "5h", "used_percent": 40, "protect_percent": 90},
		  {"label": "7d", "used_percent": 10, "protect_percent": 0}]},
		{"provider": "claude", "name": "Seat 3", "windows": [
		  {"label": "5h", "used_percent": 40},
		  {"label": "7d", "used_percent": 10, "protect_percent": 140}]},
		{"provider": "claude", "name": "Seat 4", "windows": [
		  {"label": "5h", "used_percent": 40, "protect_percent": -1}]}
	]}`)
	res, err := FromExtension(raw, now)
	if err != nil {
		t.Fatal(err)
	}
	line := func(a, w int) string {
		p := res.Accounts[a].Windows[w].ProtectPercent
		if p == nil {
			return "none"
		}
		return strings.TrimRight(strings.TrimRight(strconvF(*p), "0"), ".")
	}
	for _, c := range []struct {
		a, w int
		want string
	}{{0, 0, "98"}, {0, 1, "99.5"}, {1, 0, "90"}, {1, 1, "0"}, {2, 0, "none"}, {2, 1, "none"}, {3, 0, "none"}} {
		if got := line(c.a, c.w); got != c.want {
			t.Errorf("account %d window %d: protect_percent %s, want %s", c.a, c.w, got, c.want)
		}
	}
	// Absent on the wire stays absent, so a hub or an extension that never heard of it adds nothing.
	if b, _ := json.Marshal(res.Accounts[2].Windows[0]); strings.Contains(string(b), "protect_percent") {
		t.Errorf("an unsaid line is not sent: %s", b)
	}
	if b, _ := json.Marshal(res.Accounts[0].Windows[0]); !strings.Contains(string(b), `"protect_percent":98`) {
		t.Errorf("the line is sent as a number: %s", b)
	}
}

func strconvF(f float64) string { return strconv.FormatFloat(f, 'f', 6, 64) }
