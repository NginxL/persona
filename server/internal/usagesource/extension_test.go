package usagesource

import (
	"encoding/json"
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
