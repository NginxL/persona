package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
)

type scheduleSkipStore interface {
	PutSettings(context.Context, map[string]any) error
}

var (
	errSkipNextGone    = i18n.Err("assistant-schedule.gone")
	errSkipNextChanged = i18n.Err("assistant-schedule.skip_next_changed")
)

func (t *Task) clearSkip() {
	t.SkipNextAt, t.SkipNextZone, t.SkipNextToken = time.Time{}, "", ""
}

func (t Task) skippableOccurrence(at, now time.Time) bool {
	repeating := t.calendar() || t.Repeat == repeatInterval && t.EveryMinutes >= minEvery && t.EveryMinutes <= maxEvery
	return t.Enabled && repeating && at.After(now) && (t.Until.IsZero() || at.Before(t.Until))
}

// A title, prompt, delivery setting or manual run does not replace the rule under this marker.
func (t *Task) keepSkipFrom(old Task) {
	t.clearSkip()
	if old.SkipNextAt.IsZero() || old.Repeat != t.Repeat || old.Enabled != t.Enabled || !t.Enabled ||
		!t.Until.IsZero() && !old.SkipNextAt.Before(t.Until) {
		return
	}
	switch t.Repeat {
	case repeatDaily:
		if old.Time != t.Time || !slices.Equal(old.Weekdays, t.Weekdays) {
			return
		}
	case repeatCron:
		if old.Cron != t.Cron {
			return
		}
	case repeatInterval:
		if old.EveryMinutes != t.EveryMinutes || !old.At.Equal(t.At) {
			return
		}
	default:
		return
	}
	t.SkipNextAt, t.SkipNextZone, t.SkipNextToken = old.SkipNextAt, old.SkipNextZone, old.SkipNextToken
}

func (t Task) skipMatches(loc *time.Location) bool {
	at := t.SkipNextAt
	if at.IsZero() || t.SkipNextToken == "" || !t.Enabled || (!t.Until.IsZero() && !at.Before(t.Until)) {
		return false
	}
	if t.Repeat == repeatInterval {
		return t.EveryMinutes >= minEvery && t.EveryMinutes <= maxEvery && at.Equal(t.At)
	}
	// Time of day belongs to the zone in which the owner marked the occurrence. A subsequent
	// clock or daylight saving transition within that zone does not change the saved UTC instant.
	return t.calendar() && t.SkipNextZone == loc.String() && !t.ArmedAt.After(at)
}

// consumeOwnerSkip advances only through the marked slot. The ordinary scheduler
// still picks the next slot, including a later occurrence already due after downtime.
func (t *Task) consumeOwnerSkip(l i18n.Lang) ScheduleRun {
	at := t.SkipNextAt.UTC()
	t.clearSkip()
	if t.Repeat == repeatInterval {
		t.At = at.Add(time.Duration(t.EveryMinutes) * time.Minute)
	}
	reason := i18n.T(l, "assistant-schedule.skip_next_owner")
	if t.LastRun.Before(at) {
		t.started(at)
		t.ended(runSkipped, reason, at)
	}
	return ScheduleRun{
		ID: "run_" + randomHex(4), Kind: t.kind(), RefID: t.ID, Title: t.label(),
		Due: at, At: at, Status: runSkipped, Error: reason, OwnerSkipped: true,
	}
}

func (s *Service) changeNextSkip(ctx context.Context, id string, at time.Time, token string, cancel bool) (bool, error) {
	if _, ok := s.store().(scheduleSkipStore); !ok {
		return false, errScheduleSave
	}
	loc := s.location(ctx)
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return false, err
	}
	defer sc.mu.Unlock()
	i := sc.indexLocked(id)
	if i < 0 {
		return false, errSkipNextGone
	}
	now := time.Now()
	t := sc.tasks[i]
	pending := t.skipMatches(loc)
	if cancel {
		if !pending || !t.SkipNextAt.Equal(at) || t.SkipNextToken != token || !at.After(now) {
			return false, errSkipNextChanged
		}
		t.clearSkip()
	} else {
		if pending {
			if t.SkipNextAt.Equal(at) && at.After(now) {
				return false, nil // repeat the same request without changing the marked occurrence
			}
			return false, errSkipNextChanged
		}
		if !t.skippableOccurrence(at, now) || !t.next(now, loc).Equal(at) {
			return false, errSkipNextChanged
		}
		t.SkipNextAt, t.SkipNextToken, t.SkipNextZone = at.UTC(), randomHex(16), ""
		if t.calendar() {
			t.SkipNextZone = loc.String()
		}
	}
	tasks := slices.Clone(sc.tasks)
	tasks[i] = t
	if err := s.saveLocked(sc, tasks); err != nil {
		return false, err
	}
	return true, nil
}

func readSkipOccurrence(w http.ResponseWriter, r *http.Request) (time.Time, error) {
	var at time.Time
	if r.Method == http.MethodDelete {
		values := r.URL.Query()["at"]
		if len(values) == 1 {
			return time.Parse(time.RFC3339Nano, values[0])
		}
		return at, errors.New("one occurrence time is required")
	}
	var in struct {
		At time.Time `json:"at"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return at, err
	}
	var extra json.RawMessage
	switch err := dec.Decode(&extra); {
	case err == io.EOF:
		return in.At, nil
	case err != nil:
		return at, err
	default:
		return at, errors.New("unexpected data after the occurrence")
	}
}

func (s *Service) skipNextRoute(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-schedule.skip_next_forbidden"))
		return
	}
	at, err := readSkipOccurrence(w, r)
	token := ""
	if r.Method == http.MethodDelete {
		if values := r.URL.Query()["token"]; len(values) == 1 {
			token = values[0]
		}
	}
	if err != nil || at.IsZero() || r.Method == http.MethodDelete && token == "" {
		writeBodyErr(w, err, i18n.Err("assistant-schedule.skip_next_invalid"))
		return
	}
	id := r.PathValue("id")
	changed, err := s.changeNextSkip(r.Context(), id, at, token, r.Method == http.MethodDelete)
	if err != nil {
		switch {
		case errors.Is(err, errSkipNextGone):
			apierr.Write(w, http.StatusNotFound, err)
		case errors.Is(err, errSkipNextChanged):
			apierr.WriteCode(w, http.StatusConflict, "schedule_skip_changed", err)
		default:
			apierr.Write(w, http.StatusServiceUnavailable, err)
		}
		return
	}
	if changed {
		s.broadcastSchedule()
	}
	task, found := s.taskView(i18n.With(r.Context(), s.requestLang(r)), id)
	if !found {
		apierr.Write(w, http.StatusNotFound, errSkipNextGone)
		return
	}
	apierr.WriteJSON(w, map[string]any{"task": task})
}
