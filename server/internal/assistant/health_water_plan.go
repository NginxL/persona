package assistant

import (
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

const waterReminderKey = "health.intake.water_reminder"

type waterReminderState struct {
	Last   time.Time `json:"last"`
	Snooze time.Time `json:"snooze"`
	Reason string    `json:"reason"`
}

type waterPlan struct {
	Next   time.Time
	Reason string
}

func waterClock(s string) (int, bool) {
	t, err := time.Parse("15:04", s)
	return t.Hour()*60 + t.Minute(), err == nil && t.Format("15:04") == s
}

func waterAt(t time.Time, minutes int) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), minutes/60, minutes%60, 0, 0, t.Location())
}

// scheduleWater ports the water extension's calendar rules. Overdue occurrences collapse to
// one send at now; dates and window boundaries follow the phone's last reported time zone.
func scheduleWater(c healthWaterSettings, s waterReminderState, rows []db.HealthIntakeRow, now time.Time, loc *time.Location) waterPlan {
	now = now.In(loc)
	start, ok := waterClock(c.Start)
	end, okEnd := waterClock(c.End)
	if !ok || !okEnd || start >= end {
		return waterPlan{Reason: "invalid"}
	}
	if !c.Remind {
		return waterPlan{Reason: "off"}
	}
	nextDay := func(reason string) waterPlan {
		d := now.AddDate(0, 0, 1)
		for !c.Weekends && (d.Weekday() == time.Saturday || d.Weekday() == time.Sunday) {
			d = d.AddDate(0, 0, 1)
		}
		return waterPlan{waterAt(d, start), reason}
	}
	if !c.Weekends && (now.Weekday() == time.Saturday || now.Weekday() == time.Sunday) {
		return nextDay("weekend")
	}
	settings := defaultHealthIntakeSettings()
	settings.Water = c
	v := sumHealthIntake(rows, settings, now, loc, 1)
	if c.Stop && v.Water.Today >= float64(c.Goal) {
		return nextDay("done")
	}
	if now.After(waterAt(now, end)) {
		return nextDay("closed")
	}
	next := waterAt(now, start)
	last := s.Last
	for _, r := range rows {
		if r.Kind == "water" && r.DeletedAt == nil && r.At.After(last) {
			last = r.At
		}
	}
	if !last.IsZero() {
		candidate := last.In(loc).Add(time.Duration(c.Interval) * time.Minute)
		if candidate.After(next) {
			next = candidate
		}
	}
	if !s.Snooze.IsZero() {
		next = s.Snooze.In(loc)
		if next.Before(waterAt(now, start)) {
			next = waterAt(now, start)
		}
	}
	if next.After(waterAt(now, end)) {
		return nextDay("closed")
	}
	if next.Before(now) {
		next = now
	}
	return waterPlan{Next: next}
}
