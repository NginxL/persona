package devices

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestUsageDayAndHourBounds(t *testing.T) {
	// Days start at midnight in the zone; the last bound is tomorrow's.
	east := time.FixedZone("east", 8*3600)
	now := time.Date(2026, 11, 17, 0, 30, 0, 0, east)
	days := usageDayBounds(now, east, 3)
	if len(days) != 4 || days[0] != time.Date(2026, 11, 15, 0, 0, 0, 0, east) || days[3] != time.Date(2026, 11, 18, 0, 0, 0, 0, east) {
		t.Fatalf("days = %v", days)
	}
	// By the hour: from the first day's midnight to the hour after now's (2 days and 1 hour: 49 hours, 50 bounds).
	hours := usageHourBounds(now, east, 3)
	if len(hours) != 50 || hours[0] != days[0] || hours[49] != time.Date(2026, 11, 17, 1, 0, 0, 0, east) {
		t.Fatalf("%d hours from %v to %v", len(hours)-1, hours[0], hours[len(hours)-1])
	}

	// A zone half an hour off UTC: its own hours, at :30 UTC.
	india := time.FixedZone("india", 5*3600+1800)
	now = time.Date(2026, 11, 17, 10, 45, 0, 0, india)
	hours = usageHourBounds(now, india, 1)
	if len(hours) != 12 || hours[0] != time.Date(2026, 11, 17, 0, 0, 0, 0, india) || hours[11] != time.Date(2026, 11, 17, 11, 0, 0, 0, india) {
		t.Fatalf("%d bounds from %v to %v", len(hours), hours[0], hours[len(hours)-1])
	}

	// The day the clocks go back has 25 hours; the hour that comes twice is there twice.
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no zone data:", err)
	}
	now = time.Date(2026, 11, 2, 0, 10, 0, 0, ny) // ten past midnight the day after
	hours = usageHourBounds(now, ny, 2)
	if len(hours) != 27 { // 25 hours on 1 Nov, the first hour of 2 Nov, and its end
		t.Fatalf("%d bounds", len(hours))
	}
	ones := 0
	for _, h := range hours[:25] {
		if h.In(ny).Hour() == 1 {
			ones++
		}
	}
	if ones != 2 {
		t.Fatalf("1 a.m. %d times", ones)
	}
}

// usageHistoryMemStore keeps the history: it answers UsageSpans with spans and counts the hour rollups.
type usageHistoryMemStore struct {
	*usageMemStore
	spans      []db.UsageSpan // what UsageSpans answers, and agentSpans by agent
	agentSpans []db.UsageSpan
	bounds     []time.Time
	machine    bool // a call asked by machine
}

func (s *usageHistoryMemStore) RollupUsageHours(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (s *usageHistoryMemStore) UsageSpans(_ context.Context, bounds []time.Time, by db.UsageBy) ([]db.UsageSpan, error) {
	s.bounds, s.machine = bounds, s.machine || by == db.UsageByMachine
	if by == db.UsageByAgent {
		return s.agentSpans, nil
	}
	return s.spans, nil
}

// Without the history in Postgres the days and hours come from memory (the last 26 hours): today is the same sum as
// today's tokens (GET /api/v1/usage), every kind, in the user's zone.
func TestUsageHistoryFromMemory(t *testing.T) {
	loc := time.FixedZone("test", 8*3600)
	d := New(Options{Hub: &fakeHost{}, Store: newMemStore(), Location: func() *time.Location { return loc }})
	now := time.Date(2026, 11, 17, 9, 30, 0, 0, loc)
	minute := now.Unix() / 60 * 60
	for _, m := range []struct {
		id      string
		buckets []proto.UsageBucket
	}{
		{"a", []proto.UsageBucket{
			{Minute: minute - 60, Model: "opus", Launcher: "claude", Agent: "claude", Input: 1, CacheWrite: 2, CacheRead: 300, Output: 4, Requests: 1},
			{Minute: minute - 3*3600, Model: "opus", Launcher: "claude", Agent: "claude", CacheRead: 1000, Output: 5, Requests: 2},
			{Minute: minute - 10*3600, Model: "opus", Output: 70, Requests: 1}, // 23:30 yesterday
		}},
		{"b", []proto.UsageBucket{{Minute: minute - 30*60, Model: "gpt", Launcher: "codex", Agent: "codex", Input: 10, CacheRead: 90, Output: 9, Requests: 1}}},
	} {
		b, _ := json.Marshal(map[string]any{"full": true, "launchers": true, "buckets": m.buckets})
		d.usage.apply(m.id, b, now)
	}
	days, err := d.usageDays(context.Background(), 3, now)
	if err != nil || days.Stored || days.Zone != "test" || len(days.Days) != 3 {
		t.Fatalf("days = %+v, %v", days, err)
	}
	today := days.Days[2]
	if today.Date != "2026-11-17" || today.Total() != d.usage.summary(now, loc).Tokens || today.Total() != 1421 || today.Requests != 4 {
		t.Fatalf("today = %+v, today's tokens = %d", today, d.usage.summary(now, loc).Tokens)
	}
	if y := days.Days[1]; y.Date != "2026-11-16" || y.Output != 70 || days.Days[0].Total() != 0 {
		t.Fatalf("yesterday = %+v, before = %+v", y, days.Days[0])
	}

	hours, err := d.usageHours(context.Background(), 2, now)
	if err != nil || len(hours.Hours) != 34 { // yesterday's 24 and today's 0:00 to 9:00
		t.Fatalf("hours = %d, %v", len(hours.Hours), err)
	}
	if h := hours.Hours[23]; h.Date != "2026-11-16" || h.Hour != 23 || h.Output != 70 {
		t.Fatalf("23:00 yesterday = %+v", h)
	}
	if h := hours.Hours[33]; h.Hour != 9 || h.Total() != 1+2+300+4+10+90+9 {
		t.Fatalf("9:00 = %+v", h)
	}
	if hours.Total.Total() != today.Total()+70 || len(hours.Machines) != 2 || hours.Machines[0].ID != "a" || hours.Machines[1].Total() != 109 {
		t.Fatalf("total %+v machines %+v", hours.Total, hours.Machines)
	}
	// Each agent's share, largest first; usage from an older node is under "".
	if as := hours.Agents; len(as) != 3 || as[0].Agent != "claude" || as[0].Total() != 1312 || as[1].Agent != "codex" || as[1].Total() != 109 ||
		as[2].Agent != "" || as[2].Output != 70 {
		t.Fatalf("agents = %+v", as)
	}
}

// With the history in Postgres the sums are the store's (db.UsageSpans): a day's spans add up, each machine has its
// name, a machine no longer here none.
func TestUsageHistoryFromStore(t *testing.T) {
	loc := time.FixedZone("test", 8*3600)
	st := &usageHistoryMemStore{usageMemStore: &usageMemStore{memStore: newMemStore(), done: make(chan struct{}, 8)}}
	d := newTestDevices(t)
	d.opts.Store, d.opts.Location = st, func() *time.Location { return loc }
	entry, _ := json.Marshal(MachineConfig{ID: "a", Name: "Studio", Kind: kindLink})
	d.Configure(devicewire.Config{Machines: []json.RawMessage{entry}})
	now := time.Date(2026, 11, 17, 9, 30, 0, 0, loc)

	st.spans = []db.UsageSpan{{Span: 0, CacheRead: 5}, {Span: 139, Output: 7, Requests: 1}}
	days, err := d.usageDays(context.Background(), usageDaysDefault, now)
	if err != nil || !days.Stored || len(days.Days) != usageDaysDefault || st.machine || len(st.bounds) != usageDaysDefault+1 {
		t.Fatalf("days = %d stored %v (%v), by machine %v", len(days.Days), days.Stored, err, st.machine)
	}
	if days.Days[0].CacheRead != 5 || days.Days[139].Output != 7 || days.Days[139].Date != "2026-11-17" {
		t.Fatalf("first %+v last %+v", days.Days[0], days.Days[139])
	}

	st.spans = []db.UsageSpan{{Span: 1, Machine: "a", Output: 3}, {Span: 2, Machine: "gone", Output: 50}, {Span: 2, Machine: "a", Output: 4}}
	hours, err := d.usageHours(context.Background(), 1, now)
	if err != nil || !st.machine || len(hours.Hours) != 10 || hours.Hours[2].Output != 54 {
		t.Fatalf("hours = %+v, %v", hours.Hours, err)
	}
	if ms := hours.Machines; len(ms) != 2 || ms[0].ID != "gone" || ms[0].Name != "" || ms[1].Name != "Studio" || ms[1].Output != 7 {
		t.Fatalf("machines = %+v", ms)
	}
	if len(hours.Agents) != 0 {
		t.Fatalf("agents without usage by agent = %+v", hours.Agents)
	}
	st.agentSpans = []db.UsageSpan{{Span: 1, Agent: "codex", Output: 3}, {Span: 2, Agent: "claude", Output: 50}, {Span: 2, Agent: "codex", Output: 4},
		{Span: 12, Agent: "pi", Output: 900}} // past the hours asked for
	if hours, _ = d.usageHours(context.Background(), 1, now); len(hours.Agents) != 2 || hours.Agents[0].Agent != "claude" || hours.Agents[1].Output != 7 {
		t.Fatalf("agents = %+v", hours.Agents)
	}
}

func TestUsageHistoryRoutes(t *testing.T) {
	d := newTestDevices(t)
	mux := http.NewServeMux()
	d.usageHistoryRoutes(mux)
	get := func(url string) (int, map[string]json.RawMessage) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		var body struct {
			Usage map[string]json.RawMessage `json:"usage"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body.Usage
	}
	count := func(raw json.RawMessage) int {
		var list []json.RawMessage
		_ = json.Unmarshal(raw, &list)
		return len(list)
	}
	if code, u := get("/api/v1/usage/days"); code != 200 || count(u["days"]) != usageDaysDefault {
		t.Fatalf("days: %d %d", code, count(u["days"]))
	}
	if code, u := get("/api/v1/usage/days?days=371"); code != 200 || count(u["days"]) != 371 {
		t.Fatalf("a year: %d %d", code, count(u["days"]))
	}
	if code, u := get("/api/v1/usage/hours"); code != 200 || count(u["hours"]) < 6*24+1 || string(u["machines"]) != "[]" || string(u["agents"]) != "[]" {
		t.Fatalf("hours: %d %d %s %s", code, count(u["hours"]), u["machines"], u["agents"])
	}
	for _, bad := range []string{"/api/v1/usage/days?days=0", "/api/v1/usage/days?days=372", "/api/v1/usage/days?days=x", "/api/v1/usage/hours?days=8"} {
		if code, _ := get(bad); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, code)
		}
	}
}
