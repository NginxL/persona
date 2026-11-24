package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

const intakeTestID = "00000000-0000-4000-8000-000000000001"

// Only HTTP/service tests use this store. The Postgres tests exercise the real SQL,
// commit ordering and sequence copying, which an in-memory store cannot establish.
type intakeTestStore struct {
	memStore
	intakeMu sync.Mutex
	rows     map[string]db.HealthIntakeRow
	seq      int64
	fail     bool
}

func (m *intakeTestStore) HealthIntakeAdd(_ context.Context, v db.HealthIntakeRow) (db.HealthIntakeRow, bool, error) {
	m.intakeMu.Lock()
	defer m.intakeMu.Unlock()
	if m.fail {
		return v, false, errors.New("synthetic storage failure")
	}
	if old, ok := m.rows[v.ID]; ok {
		return old, false, nil
	}
	if m.rows == nil {
		m.rows = map[string]db.HealthIntakeRow{}
	}
	m.seq++
	v.Seq, v.CreatedAt = m.seq, time.Now().UTC()
	m.rows[v.ID] = v
	return v, true, nil
}

func (m *intakeTestStore) HealthIntakeUndo(_ context.Context, id string) (*db.HealthIntakeRow, bool, error) {
	m.intakeMu.Lock()
	defer m.intakeMu.Unlock()
	if m.fail {
		return nil, false, errors.New("synthetic storage failure")
	}
	v, ok := m.rows[id]
	if !ok {
		return nil, false, nil
	}
	changed := v.DeletedAt == nil
	if changed {
		m.seq++
		now := time.Now().UTC()
		v.DeletedAt, v.Seq = &now, m.seq
		m.rows[id] = v
	}
	return &v, changed, nil
}

func (m *intakeTestStore) HealthIntakeRange(_ context.Context, from, until time.Time) ([]db.HealthIntakeRow, error) {
	m.intakeMu.Lock()
	defer m.intakeMu.Unlock()
	if m.fail {
		return nil, errors.New("synthetic storage failure")
	}
	out := []db.HealthIntakeRow{}
	for _, v := range m.rows {
		if v.DeletedAt == nil && !v.At.Before(from) && v.At.Before(until) {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b db.HealthIntakeRow) int { return b.At.Compare(a.At) })
	return out, nil
}

func (m *intakeTestStore) HealthIntakeChanges(_ context.Context, after int64, limit int) ([]db.HealthIntakeRow, error) {
	m.intakeMu.Lock()
	defer m.intakeMu.Unlock()
	if m.fail {
		return nil, errors.New("synthetic storage failure")
	}
	out := []db.HealthIntakeRow{}
	for _, v := range m.rows {
		if v.Seq > after {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b db.HealthIntakeRow) int { return int(a.Seq - b.Seq) })
	return out[:min(limit, len(out))], nil
}

func intakeHTTP(s *Service, method, path, body, role string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set(RoleHeader, role)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestHealthIntakeSettings(t *testing.T) {
	d := defaultHealthIntakeSettings()
	want := `{"water":{"goal_ml":2000,"cup_ml":250,"remind":false,"start":"09:00","end":"21:00","interval_minutes":60,"weekends":true,"stop_when_done":true},"protein":{"goal_g":60}}`
	got, _ := json.Marshal(d)
	if string(got) != want || d.validate() != nil {
		t.Fatalf("defaults = %s, %v", got, d.validate())
	}
	for _, edit := range []func(*healthIntakeSettings){
		func(v *healthIntakeSettings) { v.Water.Goal = 499 },
		func(v *healthIntakeSettings) { v.Water.Goal = 6001 },
		func(v *healthIntakeSettings) { v.Water.Cup = 49 },
		func(v *healthIntakeSettings) { v.Water.Cup = 1001 },
		func(v *healthIntakeSettings) { v.Water.Interval = 14 },
		func(v *healthIntakeSettings) { v.Water.Interval = 241 },
		func(v *healthIntakeSettings) { v.Protein.Goal = 9 },
		func(v *healthIntakeSettings) { v.Protein.Goal = 301 },
		func(v *healthIntakeSettings) { v.Water.Start = "9:00" },
		func(v *healthIntakeSettings) { v.Water.End = "24:00" },
		func(v *healthIntakeSettings) { v.Water.End = v.Water.Start },
		func(v *healthIntakeSettings) { v.Water.Start, v.Water.End = "21:00", "09:00" },
	} {
		v := d
		edit(&v)
		if v.validate() == nil {
			t.Errorf("accepted invalid settings: %+v", v)
		}
	}
	for _, v := range []healthIntakeSettings{
		{Water: healthWaterSettings{Goal: 500, Cup: 50, Interval: 15, Start: "00:00", End: "23:59"}, Protein: healthProteinSettings{Goal: 10}},
		{Water: healthWaterSettings{Goal: 6000, Cup: 1000, Interval: 240, Start: "00:00", End: "23:59"}, Protein: healthProteinSettings{Goal: 300}},
	} {
		if err := v.validate(); err != nil {
			t.Errorf("valid boundary: %v", err)
		}
	}
	st := &intakeTestStore{}
	s := newServiceOn(t, newFakeHub(), st)
	before := st.puts
	if got, err := s.healthIntakeSettings(context.Background()); err != nil || got != d || st.puts != before {
		t.Fatalf("reading defaults wrote settings: %+v %v", got, err)
	}
	if err := st.PutSetting(context.Background(), healthIntakeKey, map[string]any{"water": map[string]any{"cup_ml": 300}}); err != nil {
		t.Fatal(err)
	}
	gotSettings, err := s.healthIntakeSettings(context.Background())
	d.Water.Cup = 300
	if err != nil || gotSettings != d {
		t.Fatalf("partial saved settings: %+v %v", gotSettings, err)
	}
	st.failKey(healthIntakeKey, errors.New("synthetic read error"))
	if _, err := s.healthIntakeSettings(context.Background()); err == nil {
		t.Fatal("storage error became successful defaults")
	}
}

func TestHealthIntakeEntryValidation(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	valid := healthIntakeRequest{ID: intakeTestID, Kind: "water"}
	settings := defaultHealthIntakeSettings()
	if v, err := valid.entry(settings, now); err != nil || v.Amount != 250 || !v.At.Equal(now) {
		t.Fatalf("default cup/time: %+v %v", v, err)
	}
	for _, kind := range []string{"water", "protein"} {
		for _, amount := range []float64{-1, 0, 1e-300, 1e-9, 1e-8, 0.01, 0.09, 200.1, 3000.1, math.NaN(), math.Inf(1), 0.1, 1, 30.5, 200, 3000} {
			v := valid
			v.Kind, v.Amount = kind, &amount
			_, err := v.entry(settings, now)
			want := !math.IsNaN(amount) && !math.IsInf(amount, 0) && amount > 0 && math.Abs(amount*10-math.Round(amount*10)) < 1e-7
			want = want && (kind == "water" && amount >= 1 && amount <= 3000 || kind == "protein" && amount >= 0.1 && amount <= 200)
			if (err == nil) != want {
				t.Errorf("%s %v: %v", kind, amount, err)
			}
		}
	}
	for _, edit := range []func(*healthIntakeRequest){
		func(v *healthIntakeRequest) { v.ID = "d1" },
		func(v *healthIntakeRequest) { v.Kind = "other" },
		func(v *healthIntakeRequest) { v.Kind = "protein" },
		func(v *healthIntakeRequest) { v.Note = strings.Repeat("x", 4097) },
		func(v *healthIntakeRequest) { v.Note = "x\x00y" },
	} {
		v := valid
		edit(&v)
		if _, err := v.entry(settings, now); err == nil {
			t.Errorf("accepted %+v", v)
		}
	}
	for _, at := range []time.Time{{}, now.AddDate(-10, 0, 0), now.AddDate(1, 0, 0)} {
		valid.At = &at
		if _, err := valid.entry(settings, now); err != nil {
			t.Errorf("an explicit old or future time is allowed: %v", err)
		}
	}
}

func TestHealthIntakeCalendarDays(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, today := range []time.Time{
		time.Date(2026, 3, 8, 12, 0, 0, 0, loc),  // 23-hour day
		time.Date(2026, 11, 1, 12, 0, 0, 0, loc), // 25-hour day
	} {
		midnight := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
		deleted := today
		rows := []db.HealthIntakeRow{
			{ID: "last", Kind: "water", Amount: 100, At: midnight.Add(-time.Nanosecond)},
			{ID: "start", Kind: "water", Amount: 200, At: midnight},
			{ID: "end", Kind: "protein", Amount: 30.1, At: midnight.AddDate(0, 0, 1).Add(-time.Nanosecond)},
			{ID: "next", Kind: "water", Amount: 900, At: midnight.AddDate(0, 0, 1)},
			{ID: "undo", Kind: "water", Amount: 900, At: today, DeletedAt: &deleted},
		}
		for range 10 {
			rows = append(rows, db.HealthIntakeRow{Kind: "protein", Amount: 0.1, At: today})
		}
		v := sumHealthIntake(rows, defaultHealthIntakeSettings(), today, loc, 7)
		if v.Zone != loc.String() || len(v.Days) != 7 || v.Days[5].Water != 100 || v.Days[6].Water != 200 || v.Protein.Today != 31.1 || v.Water.Today != 200 || len(v.Water.Entries) != 1 || len(v.Protein.Entries) != 11 || v.Days[0].Water != 0 {
			t.Fatalf("calendar aggregation: %+v", v)
		}
	}
}

func TestHealthIntakeHTTP(t *testing.T) {
	f, st := newFakeHub(), &intakeTestStore{}
	s := newServiceOn(t, f, st)
	body := `{"id":"` + intakeTestID + `","kind":"water"}`
	post := func(body string) healthIntakeMutation {
		t.Helper()
		w := intakeHTTP(s, "POST", "/api/v1/assistant/health/intake?space=health", body, "answer")
		var v healthIntakeMutation
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatalf("POST: %d %s", w.Code, w.Body)
		}
		return v
	}
	v := post(body)
	if v.Entry == nil || v.Entry.Seq != 1 || v.Today.Water != 250 || v.Today.Protein != 0 {
		t.Fatalf("mutation reply: %+v", v)
	}
	if retry := post(strings.Replace(body, `"water"`, `"protein","amount":30`, 1)); !reflect.DeepEqual(v, retry) {
		t.Fatalf("retry replaced the original: %+v", retry)
	}
	w := intakeHTTP(s, "GET", "/api/v1/assistant/health/intake", "", "answer")
	var view healthIntakeView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || len(view.Days) != 7 || len(view.Water.Entries) != 1 || view.Water.Goal != 2000 || view.Protein.Goal != 60 || view.Water.Today != 250 {
		t.Fatalf("GET: %d %s", w.Code, w.Body)
	}
	for range 2 {
		w := intakeHTTP(s, "DELETE", "/api/v1/assistant/health/intake/"+intakeTestID, "", "full")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"water":0`) {
			t.Fatalf("undo: %d %s", w.Code, w.Body)
		}
	}
	retry := post(body)
	if retry.Entry.DeletedAt == nil || retry.Entry.Seq != 2 || retry.Today.Water != 0 {
		t.Fatalf("retry revived the record: %+v", retry)
	}
	f.mu.Lock()
	events := slices.Clone(f.events)
	f.mu.Unlock()
	if n := len(slices.DeleteFunc(events, func(s string) bool { return s != "health_intake" })); n != 2 {
		t.Fatalf("broadcast count = %d, want add and undo only", n)
	}
	w = intakeHTTP(s, "GET", "/api/v1/assistant/health/intake/changes?after=1&limit=1", "", "full")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"deleted_at"`) || !strings.Contains(w.Body.String(), `"next":2`) || !strings.Contains(w.Body.String(), `"has_more":false`) {
		t.Fatalf("changes: %d %s", w.Code, w.Body)
	}
	w = intakeHTTP(s, "GET", "/api/v1/assistant/health/intake/changes?after=2", "", "full")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"changes":[],"next":2,"has_more":false}` {
		t.Fatalf("empty cursor: %d %s", w.Code, w.Body)
	}
}

func TestHealthIntakeHTTPRefusals(t *testing.T) {
	st := &intakeTestStore{}
	s := newServiceOn(t, newFakeHub(), st)
	for _, role := range []string{"read", "extension", "scoped", "answer", "full"} {
		for _, route := range []struct {
			method, path, body string
			watch              bool
		}{
			{"GET", "/api/v1/assistant/health/intake", "", true},
			{"POST", "/api/v1/assistant/health/intake", `{"id":"` + intakeTestID + `","kind":"water"}`, true},
			{"DELETE", "/api/v1/assistant/health/intake/" + intakeTestID, "", false},
			{"GET", "/api/v1/assistant/health/intake/changes", "", false},
		} {
			w := intakeHTTP(s, route.method, route.path, route.body, role)
			allowed := role == "full" || role == "answer" && route.watch
			if !allowed && w.Code != 403 || allowed && w.Code != 200 {
				t.Errorf("%s %s %s: %d %s", role, route.method, route.path, w.Code, w.Body)
			}
		}
	}
	for _, path := range []string{"?days=0", "?days=367", "?days=no", "/changes?after=-1", "/changes?after=9223372036854775808", "/changes?limit=0", "/changes?limit=1001"} {
		w := intakeHTTP(s, "GET", "/api/v1/assistant/health/intake"+path, "", "full")
		if w.Code != 400 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	for _, body := range []string{`{}`, `null`, `[]`, `{"id":"x","kind":"water"}`, `{"id":"` + intakeTestID + `","kind":"water"} {}`, `{"id":"` + intakeTestID + `","kind":"water","amount":0}`} {
		if w := intakeHTTP(s, "POST", "/api/v1/assistant/health/intake", body, "full"); w.Code != 400 {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := intakeHTTP(s, "DELETE", "/api/v1/assistant/health/intake/not-a-uuid", "", "full"); w.Code != 400 {
		t.Fatalf("invalid delete id: %d", w.Code)
	}
	if w := intakeHTTP(s, "DELETE", "/api/v1/assistant/health/intake/00000000-0000-4000-8000-000000000099", "", "full"); w.Code != 404 {
		t.Fatalf("missing delete id: %d", w.Code)
	}
	st.fail = true
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/assistant/health/intake", ""},
		{http.MethodPost, "/api/v1/assistant/health/intake", `{"id":"` + intakeTestID + `","kind":"water"}`},
		{http.MethodDelete, "/api/v1/assistant/health/intake/" + intakeTestID, ""},
		{http.MethodGet, "/api/v1/assistant/health/intake/changes", ""},
	} {
		if w := intakeHTTP(s, route.method, route.path, route.body, "full"); w.Code != 503 {
			t.Errorf("storage failure: %d %s", w.Code, w.Body)
		}
	}
}

func TestHealthIntakeChangesHTTPPaging(t *testing.T) {
	ctx, st := context.Background(), &intakeTestStore{}
	s := newServiceOn(t, newFakeHub(), st)
	for i := 1; i <= 3; i++ {
		if _, _, err := st.HealthIntakeAdd(ctx, db.HealthIntakeRow{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i), Kind: "water", Amount: 100, At: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(after int64) (next int64, more bool, rows []db.HealthIntakeRow) {
		t.Helper()
		w := intakeHTTP(s, "GET", fmt.Sprintf("/api/v1/assistant/health/intake/changes?after=%d&limit=2", after), "", "full")
		var page struct {
			Changes []db.HealthIntakeRow `json:"changes"`
			Next    int64                `json:"next"`
			More    bool                 `json:"has_more"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatalf("page: %d %s", w.Code, w.Body)
		}
		return page.Next, page.More, page.Changes
	}
	next, more, rows := read(0)
	if next != 2 || !more || len(rows) != 2 || rows[0].Seq != 1 || rows[1].Seq != 2 {
		t.Fatalf("first page: %d %v %+v", next, more, rows)
	}
	if _, _, err := st.HealthIntakeUndo(ctx, intakeTestID); err != nil {
		t.Fatal(err)
	}
	next, more, rows = read(next)
	if next != 4 || more || len(rows) != 2 || rows[0].Seq != 3 || rows[1].Seq != 4 || rows[1].DeletedAt == nil {
		t.Fatalf("following page: %d %v %+v", next, more, rows)
	}
	next, more, rows = read(next)
	if next != 4 || more || len(rows) != 0 {
		t.Fatalf("empty page: %d %v %+v", next, more, rows)
	}
}

func TestHealthIntakeReadUsesReportedZone(t *testing.T) {
	st := &intakeTestStore{}
	s := newServiceOn(t, newFakeHub(), st)
	setSettings(t, s, func(v *Settings) { v.Timezone = "Pacific/Auckland" })
	loc, err := time.LoadLocation("Pacific/Auckland")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	for i, at := range []time.Time{start.Add(-time.Nanosecond), start, start.AddDate(0, 0, 1)} {
		if _, _, err := st.HealthIntakeAdd(context.Background(), db.HealthIntakeRow{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1), Kind: "water", Amount: 100, At: at}); err != nil {
			t.Fatal(err)
		}
	}
	w := intakeHTTP(s, "GET", "/api/v1/assistant/health/intake?days=2", "", "full")
	var view healthIntakeView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.Zone != loc.String() || view.Water.Today != 100 || len(view.Water.Entries) != 1 || len(view.Days) != 2 || view.Days[0].Water != 100 || view.Days[1].Water != 100 {
		t.Fatalf("reported zone: %d %s", w.Code, w.Body)
	}
	st.failKey(healthIntakeKey, errors.New("synthetic settings failure"))
	if w := intakeHTTP(s, "GET", "/api/v1/assistant/health/intake", "", "full"); w.Code != 503 {
		t.Fatalf("failed settings read: %d %s", w.Code, w.Body)
	}
	s = newServiceOn(t, newFakeHub(), &memStore{})
	if w := intakeHTTP(s, "GET", "/api/v1/assistant/health/intake", "", "full"); w.Code != 503 {
		t.Fatalf("missing intake store: %d %s", w.Code, w.Body)
	}
}
