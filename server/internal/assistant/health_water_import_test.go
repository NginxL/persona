package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

// This bridge can record the final drink at exactly the switch boundary.
type importWaterHub struct {
	*fakeHub
	water      *assistantwire.WaterExtension
	beforeStop func()
	failStop   bool
	readError  error
}

func (h *importWaterHub) WaterExtension(context.Context, bool) (*assistantwire.WaterExtension, error) {
	if h.water == nil || h.readError != nil {
		return nil, h.readError
	}
	v := *h.water
	return &v, nil
}
func (h *importWaterHub) DisableWaterExtension(w http.ResponseWriter, _ *http.Request, _ string) bool {
	if h.failStop {
		http.Error(w, `{"error":"synthetic switch failure"}`, 409)
		return false
	}
	if h.beforeStop != nil {
		h.beforeStop()
	}
	h.water.Enabled = false
	return true
}

type importMemoryStore struct{ intakeTestStore }

func (m *importMemoryStore) HealthIntakeImport(ctx context.Context, rows []db.HealthIntakeRow) (int, error) {
	n := 0
	for _, r := range rows {
		_, added, err := m.HealthIntakeAdd(ctx, r)
		if err != nil {
			return 0, err
		}
		if added {
			n++
		}
	}
	return n, nil
}
func (m *importMemoryStore) HealthIntakeFinishWaterImport(ctx context.Context, settings, reminder, receipt any) error {
	for _, v := range []struct {
		key   string
		value any
	}{{healthIntakeKey, settings}, {waterReminderKey, reminder}, {waterImportKey, receipt}} {
		if err := m.PutSetting(ctx, v.key, v.value); err != nil {
			return err
		}
	}
	return nil
}

func importFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func importHarness(t *testing.T, realDB bool) (*Service, *importWaterHub, healthIntakeStore) {
	t.Helper()
	var store Store = &importMemoryStore{}
	if realDB {
		d, err := db.Open(context.Background(), newDatabase(t), db.ManagerSet)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		store = d
	}
	f := newFakeHub()
	s := newServiceOn(t, f, store)
	h := &importWaterHub{fakeHub: f, water: &assistantwire.WaterExtension{StorageKey: "water", Version: "0.4.0", Enabled: true,
		Config: json.RawMessage(`{"goal_ml":2400,"interval_minutes":90}`), State: importFixture(t, "water-import.json")}}
	s.hub = h
	return s, h, s.store().(healthIntakeStore)
}
func doWaterImport(t *testing.T, s *Service, status int) waterImportResult {
	t.Helper()
	w := intakeHTTP(s, "POST", "/api/v1/assistant/health/intake/import", "", "full")
	if w.Code != status {
		t.Fatalf("import = %d %s, want %d", w.Code, w.Body, status)
	}
	var out waterImportResult
	if status == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}
func importSetting(t *testing.T, s *Service, key string) string {
	t.Helper()
	var raw json.RawMessage
	_, err := s.store().Setting(context.Background(), key, &raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func waterImportAcceptance(t *testing.T, realDB bool) {
	t.Run("repeated_ids_history_retry_and_undo", func(t *testing.T) {
		s, h, st := importHarness(t, realDB)
		first := doWaterImport(t, s, 200)
		if first.Imported != 3 || !first.ExtensionDisabled || h.water.Enabled || first.Settings.Water.Goal != 2400 || first.Settings.Water.Interval != 90 || !first.Settings.Water.Remind || first.Settings.Protein.Goal != 60 {
			t.Fatalf("result: %+v", first)
		}
		rows, err := st.HealthIntakeChanges(context.Background(), 0, 100)
		if err != nil || len(rows) != 3 || rows[0].ID == rows[1].ID {
			t.Fatalf("all history: %+v %v", rows, err)
		}
		if rows[1].At.Sub(rows[0].At) < 400*24*time.Hour {
			t.Fatal("fixture does not span 400 days")
		}
		var receipt waterImportReceipt
		if ok, err := s.store().Setting(context.Background(), waterImportKey, &receipt); err != nil || !ok || receipt.Records != 3 || receipt.Version != "0.4.0" || receipt.At.IsZero() {
			t.Fatalf("receipt: %+v %v", receipt, err)
		}
		reminder, err := s.waterReminderState(context.Background())
		if err != nil || reminder.Last.Format(time.RFC3339) != "2025-03-27T11:00:00Z" || reminder.Snooze.Format(time.RFC3339) != "2025-03-27T11:30:00Z" {
			t.Fatalf("timing: %+v %v", reminder, err)
		}
		second := doWaterImport(t, s, 200)
		if second.Imported != 0 || second.Settings != first.Settings {
			t.Fatalf("retry: %+v", second)
		}
		if _, _, err := st.HealthIntakeUndo(context.Background(), rows[0].ID); err != nil {
			t.Fatal(err)
		}
		h.water.Enabled = true
		if got := doWaterImport(t, s, 200); got.Imported != 0 {
			t.Fatal(got)
		}
		all, err := st.HealthIntakeChanges(context.Background(), 0, 100)
		if err != nil || len(all) != 3 {
			t.Fatalf("changes: %+v %v", all, err)
		}
		deleted := 0
		for _, row := range all {
			if row.DeletedAt != nil {
				deleted++
			}
		}
		if deleted != 1 {
			t.Fatal("undone record revived")
		}
	})
	t.Run("corrupt_file_writes_nothing", func(t *testing.T) {
		s, h, st := importHarness(t, realDB)
		h.water.State = importFixture(t, "water-import-corrupt.json")
		doWaterImport(t, s, 400)
		rows, err := st.HealthIntakeChanges(context.Background(), 0, 100)
		if err != nil || len(rows) != 0 || !h.water.Enabled {
			t.Fatalf("corrupt import mutated state: %+v %v", rows, err)
		}
		for _, key := range []string{healthIntakeKey, waterReminderKey, waterImportKey} {
			if importSetting(t, s, key) != "" {
				t.Fatalf("wrote %s", key)
			}
		}
	})
	t.Run("switch_failure_preserves_settings_and_timing", func(t *testing.T) {
		s, h, st := importHarness(t, realDB)
		c := defaultHealthIntakeSettings()
		c.Water.Remind = true
		c.Water.Interval = 45
		r := waterReminderState{Last: time.Now().Add(-time.Hour).UTC(), Snooze: time.Now().Add(time.Hour).UTC(), Reason: "held"}
		if err := s.store().PutSetting(context.Background(), healthIntakeKey, c); err != nil {
			t.Fatal(err)
		}
		if err := s.store().PutSetting(context.Background(), waterReminderKey, r); err != nil {
			t.Fatal(err)
		}
		beforeC, beforeR := importSetting(t, s, healthIntakeKey), importSetting(t, s, waterReminderKey)
		h.failStop = true
		doWaterImport(t, s, 409)
		if !h.water.Enabled || importSetting(t, s, healthIntakeKey) != beforeC || importSetting(t, s, waterReminderKey) != beforeR || importSetting(t, s, waterImportKey) != "" {
			t.Fatal("failed switch changed settings/reminders/receipt")
		}
		rows, err := st.HealthIntakeChanges(context.Background(), 0, 100)
		if err != nil || len(rows) != 3 {
			t.Fatalf("first transaction: %+v %v", rows, err)
		}
		h.failStop = false
		if out := doWaterImport(t, s, 200); out.Imported != 0 {
			t.Fatal("retry duplicated records")
		}
	})
	for _, mode := range []string{"extension_off", "reminder_off", "untouched"} {
		t.Run(mode, func(t *testing.T) {
			s, h, _ := importHarness(t, realDB)
			if mode == "extension_off" {
				h.water.Enabled = false
			}
			if mode == "reminder_off" {
				h.water.Config = json.RawMessage(`{"remind":false}`)
			}
			if mode == "untouched" {
				h.water.Config = nil
			}
			out := doWaterImport(t, s, 200)
			if out.Settings.Water.Remind != (mode == "untouched") {
				t.Fatalf("reminder migrated incorrectly: %+v", out)
			}
		})
	}
	t.Run("final_drink_is_imported_after_stop", func(t *testing.T) {
		s, h, st := importHarness(t, realDB)
		// Append using JSON rather than changing any real extension data.
		h.beforeStop = func() {
			var v map[string]any
			if err := json.Unmarshal(h.water.State, &v); err != nil {
				t.Fatal(err)
			}
			v["records"] = append(v["records"].([]any), map[string]any{"id": "d3", "at": "2025-03-27T12:00:00Z", "ml": 175})
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			h.water.State = b
		}
		if out := doWaterImport(t, s, 200); out.Imported != 4 {
			t.Fatalf("late drink missing: %+v", out)
		}
		rows, err := st.HealthIntakeChanges(context.Background(), 0, 100)
		if err != nil || len(rows) != 4 {
			t.Fatalf("rows: %+v %v", rows, err)
		}
	})
}

func TestHealthWaterImport(t *testing.T)         { waterImportAcceptance(t, false) }
func TestHealthWaterImportDatabase(t *testing.T) { waterImportAcceptance(t, true) }

func TestHealthWaterImportValidation(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{}`, `{"records":[{"id":"d1","at":"bad","ml":250}]}`, `{"records":[{"id":"d1","at":"2025-01-01T00:00:00Z","ml":0}]}`, `{"records":[{"id":"","at":"2025-01-01T00:00:00Z","ml":250}]}`, `{"pending":{"attempts":4}}`, `{"seen":{"n":"bad"}}`, `{"pending":{"notice":{"title":42}}}`} {
		v := &assistantwire.WaterExtension{StorageKey: "water", State: json.RawMessage(raw)}
		_, _, err := readLegacyWater(v)
		if (err == nil) != (raw == `{}`) {
			t.Errorf("validation %s: %v", raw, err)
		}
	}
	a := &assistantwire.WaterExtension{StorageKey: "water", State: json.RawMessage(`{"records":[{"id":"d1","at":"2025-01-01T00:00:00.123456789Z","ml":250}]}`)}
	b := *a
	b.State = json.RawMessage(`{"records":[{"id":"d1","at":"2025-01-01T09:00:00.123456789+09:00","ml":250}]}`)
	ar, _, _ := readLegacyWater(a)
	br, _, _ := readLegacyWater(&b)
	if ar[0].ID != br[0].ID {
		t.Fatal("same UTC instant got different ids")
	}
	b.StorageKey = "previous-water"
	br, _, _ = readLegacyWater(&b)
	if ar[0].ID == br[0].ID {
		t.Fatal("storage keys collided")
	}
}

func TestHealthWaterImportPendingAndCredentials(t *testing.T) {
	s, h, _ := importHarness(t, false)
	for _, tc := range []struct {
		installed, on, receipt bool
		pending                string
	}{{false, false, false, ""}, {true, true, false, "pending"}, {true, false, false, "pending"}, {true, false, true, ""}, {true, true, true, "pending"}} {
		t.Run(strings.Join([]string{stringBool(tc.installed), stringBool(tc.on), stringBool(tc.receipt)}, "_"), func(t *testing.T) {
			if tc.installed {
				h.water = &assistantwire.WaterExtension{Enabled: tc.on}
			} else {
				h.water = nil
			}
			if tc.receipt {
				if err := s.store().PutSetting(context.Background(), waterImportKey, waterImportReceipt{At: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			w := intakeHTTP(s, "GET", "/api/v1/assistant/health/intake", "", "full")
			var v map[string]any
			json.Unmarshal(w.Body.Bytes(), &v)
			got, _ := v["water_extension"].(string)
			if w.Code != 200 || got != tc.pending {
				t.Fatalf("pending: %d %s", w.Code, w.Body)
			}
			if tc.pending == "" {
				if _, found := v["water_extension"]; found {
					t.Fatal("empty flag must be omitted")
				}
			}
		})
	}
	for _, role := range []string{"answer", "extension", "read"} {
		w := intakeHTTP(s, "POST", "/api/v1/assistant/health/intake/import", "", role)
		if w.Code != 403 {
			t.Fatalf("%s allowed: %d", role, w.Code)
		}
	}
	h.readError = errors.New("synthetic read error")
	if w := intakeHTTP(s, "GET", "/api/v1/assistant/health/intake", "", "full"); w.Code != 503 {
		t.Fatal("read error hidden")
	}
}
func stringBool(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// Invalid metadata and a corrupt second read cannot publish settings or a receipt.
func TestHealthWaterImportReadAndConfigFailures(t *testing.T) {
	for _, mode := range []string{"bad_config", "second_read", "changed_extension", "changed_config", "missing"} {
		t.Run(mode, func(t *testing.T) {
			s, h, _ := importHarness(t, false)
			want := 400
			switch mode {
			case "bad_config":
				h.water.Config = json.RawMessage(`{"goal_ml":1}`)
			case "second_read":
				h.beforeStop = func() { h.water.State = json.RawMessage(`bad`) }
			case "changed_extension":
				h.beforeStop = func() { h.water.StorageKey = "replacement" }
				want = 409
			case "changed_config":
				h.beforeStop = func() { h.water.Config = json.RawMessage(`{"goal_ml":2500}`) }
				want = 409
			case "missing":
				h.water = nil
				want = 404
			}
			doWaterImport(t, s, want)
			if got := importSetting(t, s, waterImportKey); got != "" {
				t.Fatal("failure wrote receipt")
			}
			c, err := s.healthIntakeSettings(context.Background())
			if err != nil || !reflect.DeepEqual(c, defaultHealthIntakeSettings()) {
				t.Fatalf("settings changed: %+v %v", c, err)
			}
		})
	}
}

var _ waterExtensions = (*importWaterHub)(nil)
