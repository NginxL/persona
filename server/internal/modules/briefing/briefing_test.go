package briefing

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/modapi"
)

func TestManifest(t *testing.T) {
	info := Module{}.Info()
	if info.ID != "briefing" || info.API != modapi.API || info.Origin != modapi.OriginBuiltin {
		t.Fatalf("manifest: %+v", info)
	}
	if len(info.Settings) != 1 || info.Settings[0] != SettingKey || len(info.Schedules) != 1 || info.Schedules[0] != modapi.ScheduleFactsName {
		t.Fatalf("manifest slots: %+v", info)
	}
}

// The value persona_settings shows is the defaults until a value is stored, and stored values come back as written.
func TestSettingValueAndCheck(t *testing.T) {
	def, _ := json.Marshal(Defaults())
	if got := string(value(nil)); got != string(def) || got != `{"stalled_minutes":20,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":15}` {
		t.Fatalf("default value %s", got)
	}
	for _, bad := range []string{`{}`, `{"stalled_minutes":20,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":0}`,
		`{"stalled_minutes":0,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":15}`,
		`{"stalled_minutes":20,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":101}`,
		`{"stalled_minutes":10081,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":15}`, `"15"`, `[`} {
		if _, err := check(json.RawMessage(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if got, err := check(json.RawMessage(`null`)); err != nil || got != nil {
		t.Fatalf("null = %s, %v; want the defaults (nil)", got, err)
	}
	raw := `{"max_rows":7,"stalled_minutes":25,"silent_minutes":150,"orphan_minutes":12,"card_minutes":4}`
	stored, err := check(json.RawMessage(raw))
	if err != nil || string(stored) != `{"stalled_minutes":25,"silent_minutes":150,"orphan_minutes":12,"card_minutes":4,"max_rows":7}` {
		t.Fatalf("stored %s, %v", stored, err)
	}
	if got := describe(value(stored)); got != "stalled=25m, silent=150m, orphan=12m, cards=4m, max_rows=7" {
		t.Fatalf("describe %q", got)
	}
}

func TestRules(t *testing.T) {
	w := waitsOf(nil)
	if w.CardAfter() != 3*time.Minute || w.OrphanAfter() != 10*time.Minute || w.StalledAfter(0) != 20*time.Minute ||
		w.SilentAfter(0) != 2*time.Hour || w.StalledAfter(40) != time.Hour || w.SilentAfter(100) != 150*time.Minute {
		t.Fatalf("rules %+v", w)
	}
	if got := waitsOf(json.RawMessage(`not json`)); got != Defaults() {
		t.Fatalf("unreadable stored value: %+v", got)
	}
}

// Owner waits come first and are never folded; the others are ordered by age, then key, and bounded by MaxRows.
func TestFoldKeepsEveryOwnerWait(t *testing.T) {
	now := time.Now()
	var rows []modapi.WaitRow
	for i := 0; i < 17; i++ {
		rows = append(rows, modapi.WaitRow{Kind: "stalled", Since: now.Add(time.Duration(i) * time.Minute), Key: fmt.Sprintf("other-%02d", i)})
	}
	for i := 0; i < 20; i++ {
		kind := []string{"needs_user", "question", "permission", "private"}[i%4]
		rows = append(rows, modapi.WaitRow{Kind: kind, Since: now.Add(time.Duration(i) * time.Minute), Key: fmt.Sprintf("owner-%02d", i)})
	}
	keep, more := Defaults().Fold(rows)
	if len(keep) != 35 || more != 2 {
		t.Fatalf("kept %d, more %d; want 35 and 2", len(keep), more)
	}
	for n, i := range keep {
		if owner := OwnerWait(rows[i].Kind); owner != (n < 20) {
			t.Fatalf("row %d (%+v) out of place", n, rows[i])
		}
	}
	if rows[keep[20]].Key != "other-00" || rows[keep[34]].Key != "other-14" {
		t.Fatalf("the others are not oldest first: %s … %s", rows[keep[20]].Key, rows[keep[34]].Key)
	}
	same := []modapi.WaitRow{{Kind: "stalled", Since: now, Key: "b"}, {Kind: "stalled", Since: now, Key: "a"}}
	if keep, _ := Defaults().Fold(same); keep[0] != 1 {
		t.Fatalf("ties are not broken by key: %v", keep)
	}
}
