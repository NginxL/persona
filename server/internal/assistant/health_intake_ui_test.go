package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

func intakePageHTTP(s *Service, kind, action string, values map[string]string, state string, version int, role string) *httptest.ResponseRecorder {
	if state == "" {
		raw, _ := json.Marshal(intakePageState{Kind: kind, Days: 7})
		state = string(raw)
	}
	body, _ := json.Marshal(wire.UIRequest{Context: &wire.UIContext{Locale: "en", UIVersion: version}, State: state, Action: action, Values: values})
	return intakeHTTP(s, "POST", "/api/v1/assistant/health/intake/ui?space=health", string(body), role)
}

func intakePageResult(t *testing.T, w *httptest.ResponseRecorder) wire.ActionResult {
	t.Helper()
	var res wire.ActionResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.View == nil {
		t.Fatalf("page: %d %s", w.Code, w.Body)
	}
	if err := wire.CheckView(res.View); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestHealthIntakePageTotalsPaginationAndVersions(t *testing.T) {
	now, c := waterTestTime("2026-11-16T10:00:00Z"), defaultHealthIntakeSettings()
	rows := []db.HealthIntakeRow{
		{ID: intakeTestID, Kind: "water", Amount: 750, At: now},
		{Kind: "water", Amount: 2000, At: now.AddDate(0, 0, -1)},
		{Kind: "water", Amount: 2100, At: now.AddDate(0, 0, -10)},
		{Kind: "protein", Amount: 25.5, At: now, Note: "A synthetic meal"},
		{Kind: "protein", Amount: 60, At: now.AddDate(0, 0, -1)},
		{Kind: "protein", Amount: 70, At: now.AddDate(0, 0, -10)},
	}
	for _, kind := range []string{"water", "protein"} {
		for _, days := range []int{7, 30} {
			v := sumHealthIntake(rows, c, now, time.UTC, days)
			page := drawIntakePage(intakePageState{Kind: kind, Days: days}, c, v, waterReminderState{Reason: "no_device"}, now, time.UTC, "en", 4, true)
			if err := wire.CheckView(&page); err != nil {
				t.Fatal(err)
			}
			progress := page.Sections[0].Items[0].Tiles[0].Gauge
			wantRatio := 0.375
			if kind == "protein" {
				wantRatio = 25.5 / 60
				if page.Sections[2].Items[0].Subtitle != "A synthetic meal" {
					t.Fatal("food note missing")
				}
			} else {
				raw, _ := json.Marshal(page)
				for _, want := range []string{"Low-distraction", "No device", "Reminders are off"} {
					if !strings.Contains(string(raw), want) {
						t.Fatalf("missing status %s", want)
					}
				}
			}
			if *progress.Ratio != wantRatio || page.Sections[2].Items[0].Action.Values["id"] == "" && kind == "water" {
				t.Fatalf("progress or undo: %+v", progress)
			}
			recent := page.Sections[3].Items
			points := recent[1].Series[0].Points
			if len(points) != days || recent[1].Kind != "bar" || points[len(points)-2].Value != map[string]float64{"water": 2000, "protein": 60}[kind] {
				t.Fatal("chart totals")
			}
			wantDays := 1
			if days == 30 {
				wantDays = 2
			}
			if !strings.Contains(recent[2].Text, fmt.Sprintf("Met on %d days", wantDays)) {
				t.Fatalf("days meeting goal: %s", recent[2].Text)
			}
		}
	}
	st := &intakeTestStore{}
	s := newServiceOn(t, newFakeHub(), st)
	for i := range 100 {
		st.HealthIntakeAdd(context.Background(), db.HealthIntakeRow{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), Kind: "water", Amount: 1, At: time.Now()})
	}
	for _, version := range []int{1, 2, 4, 11} {
		res := intakePageResult(t, intakePageHTTP(s, "water", "", nil, "", version, "full"))
		if len(res.View.Sections[2].Items) != 41 {
			t.Fatal("unbounded entries")
		}
		if version == 1 && res.View.Sections[2].Items[0].Action.Values != nil {
			t.Fatal("legacy undo not fitted")
		}
		res = intakePageResult(t, intakePageHTTP(s, "water", "older", nil, res.View.State, version, "full"))
		p, _ := readIntakePage(res.View.State)
		if p.Offset != 40 {
			t.Fatal("not paged")
		}
		res = intakePageResult(t, intakePageHTTP(s, "water", "range", map[string]string{"days": "30"}, res.View.State, version, "full"))
		p, _ = readIntakePage(res.View.State)
		if p.Days != 30 || p.Offset != 40 {
			t.Fatal("range or offset lost")
		}
		res = intakePageResult(t, intakePageHTTP(s, "water", "latest", nil, res.View.State, version, "full"))
		p, _ = readIntakePage(res.View.State)
		if p.Offset != 0 {
			t.Fatal("not returned to latest")
		}
	}
}

func TestHealthIntakePageActionsAndSettings(t *testing.T) {
	st := &intakeTestStore{}
	s := newServiceOn(t, newFakeHub(), st)
	res := intakePageResult(t, intakePageHTTP(s, "water", "drink", nil, "", 4, "full"))
	res = intakePageResult(t, intakePageHTTP(s, "water", "log", map[string]string{"amount": "300ml"}, res.View.State, 4, "full"))
	view, _ := s.healthIntakeView(context.Background(), st, defaultHealthIntakeSettings(), 1)
	if view.Water.Today != 550 || len(view.Water.Entries) != 2 {
		t.Fatalf("water: %+v", view.Water)
	}
	id := view.Water.Entries[0].ID
	res = intakePageResult(t, intakePageHTTP(s, "water", "undo", map[string]string{"id": id}, res.View.State, 4, "full"))
	if st.rows[id].DeletedAt == nil {
		t.Fatal("undo failed")
	}
	intakePageResult(t, intakePageHTTP(s, "water", "undo:"+view.Water.Entries[1].ID, nil, res.View.State, 1, "full"))
	res = intakePageResult(t, intakePageHTTP(s, "protein", "log", map[string]string{"amount": "25.5", "note": "A synthetic meal"}, "", 4, "full"))
	if res.View.Sections[2].Items[0].Subtitle != "A synthetic meal" {
		t.Fatal("protein note missing")
	}
	res = intakePageResult(t, intakePageHTTP(s, "protein", "settings", nil, res.View.State, 4, "full"))
	if res.View.Sections[0].Items[0].Value != "60" {
		t.Fatal("wrong default protein goal")
	}
	res = intakePageResult(t, intakePageHTTP(s, "protein", "save", map[string]string{"goal_g": "90"}, res.View.State, 4, "full"))
	settings, _ := s.healthIntakeSettings(context.Background())
	if settings.Protein.Goal != 90 || settings.Water != defaultHealthIntakeSettings().Water {
		t.Fatal("save touched another kind")
	}
	values := map[string]string{"goal_ml": "2400", "cup_ml": "300", "remind": "true", "start": "08:30", "end": "20:30", "interval_minutes": "45", "weekends": "false", "stop_when_done": "false"}
	res = intakePageResult(t, intakePageHTTP(s, "water", "settings", nil, "", 4, "full"))
	if len(res.View.Sections[0].Items) != 10 {
		t.Fatal("not all water settings visible")
	}
	intakePageResult(t, intakePageHTTP(s, "water", "save", values, res.View.State, 4, "full"))
	settings, _ = s.healthIntakeSettings(context.Background())
	if settings.Water != (healthWaterSettings{Goal: 2400, Cup: 300, Remind: true, Start: "08:30", End: "20:30", Interval: 45, Weekends: false, Stop: false}) || settings.Protein.Goal != 90 {
		t.Fatalf("settings: %+v", settings)
	}
	// A page action cannot remove yesterday's entry or an entry of another kind.
	for i, row := range []db.HealthIntakeRow{{Kind: "water", At: time.Now().AddDate(0, 0, -1)}, {Kind: "protein", At: time.Now()}} {
		row.ID, row.Amount = fmt.Sprintf("10000000-0000-4000-8000-%012d", i+1), 10
		st.HealthIntakeAdd(context.Background(), row)
		intakePageResult(t, intakePageHTTP(s, "water", "undo", map[string]string{"id": row.ID}, "", 4, "full"))
		if st.rows[row.ID].DeletedAt != nil {
			t.Fatal("deleted an entry outside this page")
		}
	}
}

func TestHealthIntakePageRejectsBadSettingsWithoutWriting(t *testing.T) {
	st := &intakeTestStore{}
	s := newServiceOn(t, newFakeHub(), st)
	c := defaultHealthIntakeSettings()
	st.PutSetting(context.Background(), healthIntakeKey, c)
	for _, tc := range []struct {
		kind, key, value string
	}{
		{"water", "goal_ml", "499"}, {"water", "goal_ml", "6001"}, {"water", "cup_ml", "49"}, {"water", "cup_ml", "1001"},
		{"water", "interval_minutes", "14"}, {"water", "interval_minutes", "241"}, {"water", "goal_ml", "NaN"},
		{"water", "start", "9:00"}, {"water", "start", "21:00"}, {"water", "end", "08:00"},
		{"water", "remind", "1"}, {"water", "unknown", "1"}, {"water", "goal_g", "80"},
		{"protein", "goal_g", "9"}, {"protein", "goal_g", "301"}, {"protein", "remind", "true"},
	} {
		w := intakePageHTTP(s, tc.kind, "save", map[string]string{tc.key: tc.value}, "", 4, "full")
		if w.Code != 400 {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body)
		}
		got, _ := s.healthIntakeSettings(context.Background())
		if !reflect.DeepEqual(got, c) {
			t.Fatal("invalid save changed settings")
		}
	}
	st.setFailPut(errors.New("synthetic save failure"))
	if w := intakePageHTTP(s, "protein", "save", map[string]string{"goal_g": "80"}, "", 4, "full"); w.Code != 503 {
		t.Fatalf("failed save: %d %s", w.Code, w.Body)
	}
	st.setFailPut(nil)
	got, _ := s.healthIntakeSettings(context.Background())
	if got != c {
		t.Fatal("failed save changed settings")
	}
	// The manager treats a missing internal RoleHeader as full for legacy trusted callers.
	// The hub's authentication is tested separately; explicit restricted roles stay forbidden.
	for _, role := range []string{"answer", "read", "extension", "scoped", "readonly", "unknown"} {
		for _, action := range []string{"", "settings", "save", "drink", "undo"} {
			if w := intakePageHTTP(s, "water", action, nil, "", 4, role); w.Code != 403 {
				t.Fatalf("role %q action %q: %d %s", role, action, w.Code, w.Body)
			}
		}
	}
}

func TestHealthIntakePageStatusAndLocalization(t *testing.T) {
	for _, tc := range []struct {
		name, now, contains string
		change              func(*healthWaterSettings, *waterPlanFixture)
	}{
		{"next", "2026-11-16T08:00:00Z", "Next reminder 09:00", nil},
		{"closed", "2026-11-16T22:00:00Z", "Next window starts 11-17 09:00", nil},
		{"weekend", "2026-11-14T10:00:00Z", "No weekend reminders", func(c *healthWaterSettings, s *waterPlanFixture) { c.Weekends = false }},
		{"off", "2026-11-16T10:00:00Z", "Reminders are off", func(c *healthWaterSettings, s *waterPlanFixture) { c.Remind = false }},
		{"done", "2026-11-16T10:00:00Z", "Goal met", func(c *healthWaterSettings, s *waterPlanFixture) {
			s.Records = []db.HealthIntakeRow{{Kind: "water", Amount: 2000, At: waterTestTime("2026-11-16T10:00:00Z")}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, fixture := waterTestDefaults(), waterPlanFixture{Zone: "UTC"}
			if tc.change != nil {
				tc.change(&c, &fixture)
			}
			got := intakeReminderStatus("en", c, waterReminderState{}, fixture.Records, waterTestTime(tc.now), time.UTC)
			if !strings.Contains(got, tc.contains) {
				t.Fatalf("%s", got)
			}
		})
	}
	for _, lang := range []i18n.Lang{"en", "zh-Hans", "ja"} {
		for _, kind := range []string{"water", "protein"} {
			for _, edit := range []bool{false, true} {
				c := defaultHealthIntakeSettings()
				v := sumHealthIntake(nil, c, time.Now(), time.UTC, 7)
				page := drawIntakePage(intakePageState{Kind: kind, Days: 7, Edit: edit}, c, v, waterReminderState{}, time.Now(), time.UTC, lang, 4, true)
				if err := wire.CheckView(&page); err != nil {
					t.Fatal(err)
				}
				b, _ := json.Marshal(page)
				if strings.Contains(string(b), "assistant-health_intake") {
					t.Fatalf("missing localized page: %s", b)
				}
			}
		}
	}
}
