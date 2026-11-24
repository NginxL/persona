package assistant

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func waterTestTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestHealthWaterScheduleRules(t *testing.T) {
	base := "2026-11-16T10:00:00Z"
	tests := []struct {
		name, now, want, reason string
		change                  func(*healthWaterSettings, *waterPlanFixture)
	}{
		{"R1 saved zone", base, "2026-11-16T11:00:00Z", "", func(c *healthWaterSettings, s *waterPlanFixture) {
			s.Zone = "Asia/Tokyo"
			c.Start = "20:00"
			c.End = "22:00"
		}},
		{"R2 invalid order", base, "", "invalid", func(c *healthWaterSettings, s *waterPlanFixture) { c.Start = "22:00" }},
		{"R2 equal", base, "", "invalid", func(c *healthWaterSettings, s *waterPlanFixture) { c.End = c.Start }},
		{"R2 bad clock", base, "", "invalid", func(c *healthWaterSettings, s *waterPlanFixture) { c.Start = "9:00" }},
		{"R2 inclusive start", "2026-11-16T09:00:00Z", "2026-11-16T09:00:00Z", "", nil},
		{"R2 inclusive end", "2026-11-16T21:00:00Z", "2026-11-16T21:00:00Z", "", nil},
		{"R3 first at start", "2026-11-16T07:00:00Z", "2026-11-16T09:00:00Z", "", nil},
		{"R3 drink restarts interval", base, "2026-11-16T10:30:00Z", "", func(c *healthWaterSettings, s *waterPlanFixture) {
			s.Records = []db.HealthIntakeRow{{At: waterTestTime("2026-11-16T09:30:00Z"), Kind: "water", Amount: 250}}
		}},
		{"R3 reminder is later", base, "2026-11-16T10:45:00Z", "", func(c *healthWaterSettings, s *waterPlanFixture) {
			s.LastReminder = waterTestTime("2026-11-16T09:45:00Z")
			s.Records = []db.HealthIntakeRow{{At: waterTestTime("2026-11-16T09:30:00Z"), Kind: "water", Amount: 250}}
		}},
		{"R3 drink is later", base, "2026-11-16T10:50:00Z", "", func(c *healthWaterSettings, s *waterPlanFixture) {
			s.LastReminder = waterTestTime("2026-11-16T09:45:00Z")
			s.Records = []db.HealthIntakeRow{{At: waterTestTime("2026-11-16T09:50:00Z"), Kind: "water", Amount: 250}}
		}},
		{"R3 old events do not delay morning", "2026-11-16T08:00:00Z", "2026-11-16T09:00:00Z", "", func(c *healthWaterSettings, s *waterPlanFixture) {
			s.LastReminder = waterTestTime("2026-11-15T21:00:00Z")
		}},
		{"R4 snooze overrides interval", base, "2026-11-16T10:10:00Z", "", func(c *healthWaterSettings, s *waterPlanFixture) {
			s.LastReminder = waterTestTime("2026-11-16T09:55:00Z")
			s.Snooze = waterTestTime("2026-11-16T10:10:00Z")
		}},
		{"R5 computed after end", "2026-11-16T20:30:00Z", "2026-11-17T09:00:00Z", "closed", func(c *healthWaterSettings, s *waterPlanFixture) {
			s.LastReminder = waterTestTime("2026-11-16T20:20:00Z")
		}},
		{"R5 snooze after end", "2026-11-16T20:45:00Z", "2026-11-17T09:00:00Z", "closed", func(c *healthWaterSettings, s *waterPlanFixture) { s.Snooze = waterTestTime("2026-11-16T21:15:00Z") }},
		{"R6 Saturday", "2026-11-14T12:00:00Z", "2026-11-16T09:00:00Z", "weekend", func(c *healthWaterSettings, s *waterPlanFixture) { c.Weekends = false }},
		{"R6 Sunday", "2026-11-15T12:00:00Z", "2026-11-16T09:00:00Z", "weekend", func(c *healthWaterSettings, s *waterPlanFixture) { c.Weekends = false }},
		{"R6 Friday overflow", "2026-11-13T21:30:00Z", "2026-11-16T09:00:00Z", "closed", func(c *healthWaterSettings, s *waterPlanFixture) { c.Weekends = false }},
		{"R7 goal reached", base, "2026-11-17T09:00:00Z", "done", func(c *healthWaterSettings, s *waterPlanFixture) {
			s.Records = []db.HealthIntakeRow{{At: waterTestTime(base), Kind: "water", Amount: 2000}}
		}},
		{"R7 goal ignored when disabled", base, "2026-11-16T11:00:00Z", "", func(c *healthWaterSettings, s *waterPlanFixture) {
			c.Stop = false
			s.Records = []db.HealthIntakeRow{{At: waterTestTime(base), Kind: "water", Amount: 2000}}
		}},
		{"R7 Friday goal skips weekend", "2026-11-13T10:00:00Z", "2026-11-16T09:00:00Z", "done", func(c *healthWaterSettings, s *waterPlanFixture) {
			c.Weekends = false
			s.Records = []db.HealthIntakeRow{{At: waterTestTime("2026-11-13T09:30:00Z"), Kind: "water", Amount: 2100}}
		}},
		{"R8 disabled", base, "", "off", func(c *healthWaterSettings, s *waterPlanFixture) { c.Remind = false }},
		{"R9 one catchup", base, base, "", func(c *healthWaterSettings, s *waterPlanFixture) {
			s.LastReminder = waterTestTime("2026-11-15T10:00:00Z")
		}},
		{"R9 no catchup after window", "2026-11-16T22:00:00Z", "2026-11-17T09:00:00Z", "closed", nil},
		{"R10 spring DST window", "2026-03-08T06:30:00Z", "2026-03-08T13:00:00Z", "", func(c *healthWaterSettings, s *waterPlanFixture) { s.Zone = "America/New_York" }},
		{"R10 fall DST window", "2026-11-01T05:30:00Z", "2026-11-01T14:00:00Z", "", func(c *healthWaterSettings, s *waterPlanFixture) { s.Zone = "America/New_York" }},
		{"R10 local midnight excludes yesterday", "2026-03-09T04:00:00Z", "2026-03-09T13:00:00Z", "", func(c *healthWaterSettings, s *waterPlanFixture) {
			s.Zone = "America/New_York"
			s.Records = []db.HealthIntakeRow{{At: waterTestTime("2026-03-09T03:59:59Z"), Kind: "water", Amount: 2000}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := waterTestDefaults()
			s := waterPlanFixture{Zone: "UTC"}
			if tt.change != nil {
				tt.change(&c, &s)
			}
			got := fixtureSchedule(c, s, waterTestTime(tt.now))
			if got.Reason != tt.reason {
				t.Fatalf("reason %q, want %q", got.Reason, tt.reason)
			}
			if tt.want == "" {
				if !got.Next.IsZero() {
					t.Fatalf("unexpected next %v", got.Next)
				}
			} else if !got.Next.Equal(waterTestTime(tt.want)) {
				t.Fatalf("next %v, want %s", got.Next, tt.want)
			}
		})
	}
}

func TestHealthWaterFallbackZone(t *testing.T) {
	for _, name := range []string{"", "invalid/zone"} {
		if zoneOf(name) != time.Local {
			t.Fatal("not the process local zone")
		}
		now := time.Date(2026, 11, 16, 8, 0, 0, 0, time.Local)
		p := fixtureSchedule(waterTestDefaults(), waterPlanFixture{Zone: name}, now)
		if !p.Next.Equal(waterAt(now, 9*60)) {
			t.Fatalf("local plan: %v", p)
		}
	}
}

func TestHealthWaterCalendarTotalsDST(t *testing.T) {
	for _, tc := range []struct {
		name, start, end string
		hours            int
	}{
		{"spring", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z", 23},
		{"fall", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z", 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, end := waterTestTime(tc.start), waterTestTime(tc.end)
			l := zoneOf("America/New_York")
			s := waterPlanFixture{Records: []db.HealthIntakeRow{{At: start.Add(-time.Second), Kind: "water", Amount: 100}, {At: start, Kind: "water", Amount: 200}, {At: end.Add(-time.Second), Kind: "water", Amount: 300}, {At: end, Kind: "water", Amount: 400}}}
			if got := waterFixtureTotal(s, start, l); got != 500 {
				t.Fatalf("total %g", got)
			}
			if end.Sub(start) != time.Duration(tc.hours)*time.Hour {
				t.Fatal("bad fixture")
			}
		})
	}
}

// Fixtures preserve the extension schedule's table while using manager-owned rows and zoneOf.
type waterPlanFixture struct {
	Zone         string
	Records      []db.HealthIntakeRow
	LastReminder time.Time
	Snooze       time.Time
}

func waterTestDefaults() healthWaterSettings {
	c := defaultHealthIntakeSettings().Water
	c.Remind = true
	return c
}

func fixtureSchedule(c healthWaterSettings, s waterPlanFixture, now time.Time) waterPlan {
	return scheduleWater(c, waterReminderState{Last: s.LastReminder, Snooze: s.Snooze}, s.Records, now, zoneOf(s.Zone))
}

func waterFixtureTotal(s waterPlanFixture, now time.Time, loc *time.Location) float64 {
	return sumHealthIntake(s.Records, defaultHealthIntakeSettings(), now, loc, 1).Water.Today
}
