package assistant

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// RFC 4122's URL namespace. Names and this namespace are stable across upgrades and restarts.
var healthIntakeNamespace = [16]byte{0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}

func healthIntakeUUID(name string) string {
	h := sha1.New() // UUIDv5, not a credential or a security hash.
	h.Write(healthIntakeNamespace[:])
	h.Write([]byte(name))
	b := h.Sum(nil)[:16]
	b[6], b[8] = b[6]&0x0f|0x50, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func reminderEntryID(reminder, action string) string {
	return healthIntakeUUID("water-reminder:" + reminder + ":" + action)
}

func (s *Service) waterReminderState(ctx context.Context) (waterReminderState, error) {
	var v waterReminderState
	st := s.store()
	if st == nil {
		return v, i18n.Err("assistant-health_intake.unavailable")
	}
	_, err := st.Setting(ctx, waterReminderKey, &v)
	return v, err
}

func (s *Service) putIntakeSetting(ctx context.Context, key string, value any) error {
	st := s.store()
	if st == nil {
		return i18n.Err("assistant-health_intake.unavailable")
	}
	return st.PutSetting(ctx, key, value)
}

func (s *Service) addHealthIntake(ctx context.Context, st healthIntakeStore, v db.HealthIntakeRow) (db.HealthIntakeRow, bool, error) {
	s.intakeMu.Lock()
	defer s.intakeMu.Unlock()
	return s.addHealthIntakeLocked(ctx, st, v)
}

// All sources use this write path. A retry after the entry committed but saving the reminder
// state failed repairs the state from created_at without changing the record or reviving an undo.
// The caller holds intakeMu, also held by the scheduler and page setting actions.
func (s *Service) addHealthIntakeLocked(ctx context.Context, st healthIntakeStore, v db.HealthIntakeRow) (db.HealthIntakeRow, bool, error) {
	saved, changed, err := st.HealthIntakeAdd(ctx, v)
	if err == nil && changed {
		s.event(ctx, map[string]any{"type": "health_intake"})
	}
	if err != nil || saved.Kind != "water" || saved.DeletedAt != nil {
		return saved, changed, err
	}
	r, err := s.waterReminderState(ctx)
	if err == nil && (changed || saved.CreatedAt.After(r.Last)) {
		r.Last, r.Snooze, r.Reason = saved.CreatedAt, time.Time{}, ""
		err = s.putIntakeSetting(ctx, waterReminderKey, r)
	}
	if err != nil {
		s.logf("health intake: water reminder state reset: %v", err)
	}
	return saved, changed, nil
}

const waterReminderEvery = 30 * time.Second

func (s *Service) waterReminderLoop(ctx context.Context) {
	t := time.NewTicker(waterReminderEvery)
	defer t.Stop()
	for {
		pass, cancel := context.WithTimeout(ctx, 20*time.Second)
		if err := s.waterReminderTick(pass, time.Now()); err != nil && ctx.Err() == nil {
			s.logf("water reminder: %v", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type waterReminderAttempt struct {
	Notification Notification
	State        waterReminderState
}

// The caller holds intakeMu through planning and claiming the occurrence, but not delivery.
func (s *Service) prepareWaterReminderLocked(ctx context.Context, now time.Time) (*waterReminderAttempt, error) {
	c, err := s.healthIntakeSettings(ctx)
	if err != nil || !c.Water.Remind {
		return nil, err
	}
	st, ok := s.store().(healthIntakeStore)
	if !ok {
		return nil, i18n.Err("assistant-health_intake.unavailable")
	}
	r, err := s.waterReminderState(ctx)
	if err != nil {
		return nil, err
	}
	loc := s.healthIntakeLocation(ctx)
	local := now.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	rows, err := st.HealthIntakeRange(ctx, day, day.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	plan := scheduleWater(c.Water, r, rows, now, loc)
	if plan.Next.IsZero() || plan.Next.After(now) {
		return nil, nil
	}
	// Persist the occurrence before sending: one catch-up, including after a restart. The
	// existing notify.request outbox owns delivery while the hub is away; no extension retries.
	r.Last, r.Snooze, r.Reason = now, time.Time{}, ""
	if err := s.putIntakeSetting(ctx, waterReminderKey, r); err != nil {
		return nil, err
	}
	v := sumHealthIntake(rows, c, now, loc, 1)
	l := s.lang(ctx)
	body := intakeText(l, "reminder.empty")
	if v.Water.Today > 0 {
		body = intakeText(l, "reminder.progress", i18n.Args{"today": intakeNumber(v.Water.Today), "goal": c.Water.Goal,
			"cups": int(math.Ceil(max(0, float64(c.Water.Goal)-v.Water.Today) / float64(c.Water.Cup)))})
	}
	id := "water-" + strconv.FormatInt(now.UnixMilli(), 10) + "-" + randomHex(8)
	return &waterReminderAttempt{State: r, Notification: Notification{
		ID: id, Kind: assistantwire.NotifyHealthWater, Title: intakeText(l, "reminder.title"), Body: body,
		Level: assistantwire.LevelP1, Health: &assistantwire.NotifyHealth{Reminder: id},
	}}, nil
}

func (s *Service) waterReminderTick(ctx context.Context, now time.Time) error {
	if s.store() == nil || s.hello(ctx).Assistant.Standby {
		return nil
	}
	s.intakeMu.Lock()
	attempt, err := s.prepareWaterReminderLocked(ctx, now)
	s.intakeMu.Unlock()
	if err != nil || attempt == nil {
		return err
	}
	s.noteCard(ctx, cardHealthWater, attempt.Notification)
	res, err := s.hub.NotifyRequest(ctx, NotifyRequest{Notification: attempt.Notification})
	reason := ""
	switch {
	case err != nil:
		reason = "unreachable"
	case res.Queued:
		reason = "queued"
	case res.Held:
		reason = "held"
	case !res.OK:
		reason = "rejected"
	case res.Devices == nil:
		reason = "unconfirmed"
	case *res.Devices == 0 && res.Kept > 0:
		reason = "kept"
	case *res.Devices == 0:
		reason = res.Reason
		if reason == "" {
			reason = "unconfirmed"
		}
	}
	s.intakeMu.Lock()
	defer s.intakeMu.Unlock()
	r, readErr := s.waterReminderState(ctx)
	if readErr != nil {
		return readErr
	}
	// A drink, snooze or a later occurrence may have changed the state while delivery waited.
	// An old delivery result must not overwrite the new timer or reintroduce its warning.
	if !r.Last.Equal(attempt.State.Last) || !r.Snooze.Equal(attempt.State.Snooze) {
		return err
	}
	r.Reason = reason
	if saveErr := s.putIntakeSetting(ctx, waterReminderKey, r); saveErr != nil {
		return saveErr
	}
	return err
}

func intakeText(l i18n.Lang, key string, args ...i18n.Args) string {
	return i18n.T(l, "assistant-health_intake."+key, args...)
}

func intakeNumber(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }

func decodeIntakeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-health_intake.invalid_entry"))
		return false
	}
	return true
}

// Text input on a notification may include its unit. Keep the extension's integer-ml rules.
// i18n-ignore: the original unit is matched as input, never displayed as copy.
var waterAmountPattern = lazyre.New(`^\s*([0-9]+)\s*(?i:ml|毫升)?\s*$`)

func waterAmount(text string) (int, bool) {
	m := waterAmountPattern.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil && n >= 1 && n <= 3000
}

type waterReminderRequest struct {
	Reminder string `json:"reminder"`
	Action   string `json:"action"`
	Amount   string `json:"amount,omitempty"`
}

func (s *Service) waterReminderAction(w http.ResponseWriter, r *http.Request) {
	if !intakeAccess(w, r, false) {
		return
	}
	var req waterReminderRequest
	if !decodeIntakeJSON(w, r, &req) {
		return
	}
	if req.Reminder == "" || len(req.Reminder) > 128 || strings.TrimSpace(req.Reminder) != req.Reminder ||
		strings.ContainsAny(req.Reminder, "\x00\r\n") || req.Action != "drink" && req.Action != "log" && req.Action != "snooze" {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-health_intake.invalid_reminder"))
		return
	}
	s.intakeMu.Lock()
	defer s.intakeMu.Unlock()
	c, err := s.healthIntakeSettings(r.Context())
	if err != nil {
		intakeUnavailable(w)
		return
	}
	st := s.intakeStore(w)
	if st == nil {
		return
	}
	l := i18n.FromRequest(r).Or(s.lang(r.Context()))
	now := time.Now()
	toast := ""
	if req.Action == "snooze" {
		state, err := s.waterReminderState(r.Context())
		if err != nil {
			intakeUnavailable(w)
			return
		}
		if state.Snooze.IsZero() {
			state.Snooze = now.Add(30 * time.Minute)
		}
		state.Reason = ""
		if err := s.putIntakeSetting(r.Context(), waterReminderKey, state); err != nil {
			intakeUnavailable(w)
			return
		}
		v, err := s.healthIntakeView(r.Context(), st, c, 1)
		if err != nil {
			intakeUnavailable(w)
			return
		}
		toast = intakeText(l, "toast.snooze")
		plan := scheduleWater(c.Water, state, v.Water.Entries, now, s.healthIntakeLocation(r.Context()))
		if plan.Reason == "off" {
			toast = intakeText(l, "status.off")
		} else if !plan.Next.IsZero() && plan.Next.In(s.healthIntakeLocation(r.Context())).Format(time.DateOnly) != now.In(s.healthIntakeLocation(r.Context())).Format(time.DateOnly) {
			toast = intakeText(l, "toast.next_day")
		}
	} else {
		ml := c.Water.Cup
		if req.Action == "log" {
			var ok bool
			ml, ok = waterAmount(req.Amount)
			if !ok {
				apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-health_intake.invalid_amount"))
				return
			}
		}
		saved, _, err := s.addHealthIntakeLocked(r.Context(), st, db.HealthIntakeRow{
			ID: reminderEntryID(req.Reminder, req.Action), Kind: "water", Amount: float64(ml), At: now.UTC(),
		})
		if err != nil {
			intakeUnavailable(w)
			return
		}
		v, err := s.healthIntakeView(r.Context(), st, c, 1)
		if err != nil {
			intakeUnavailable(w)
			return
		}
		toast = intakeWaterToast(l, c.Water, saved.Amount, v.Water.Today)
		if saved.DeletedAt != nil {
			toast = intakeText(l, "toast.undone")
		}
	}
	intakeJSON(w, map[string]string{"toast": toast})
}

func intakeWaterToast(l i18n.Lang, c healthWaterSettings, amount, today float64) string {
	if today >= float64(c.Goal) {
		if c.Stop {
			return intakeText(l, "toast.goal_stop")
		}
		return intakeText(l, "toast.goal")
	}
	return intakeText(l, "toast.logged", i18n.Args{"amount": intakeNumber(amount), "today": intakeNumber(today), "goal": c.Goal})
}

func intakeReminderStatus(l i18n.Lang, c healthWaterSettings, r waterReminderState, rows []db.HealthIntakeRow, now time.Time, loc *time.Location) string {
	p := scheduleWater(c, r, rows, now, loc)
	switch p.Reason {
	case "off", "invalid", "done":
		return intakeText(l, "status."+p.Reason)
	case "weekend", "closed":
		return intakeText(l, "status."+p.Reason, i18n.Args{"at": p.Next.In(loc).Format("01-02 15:04")})
	default:
		return intakeText(l, "status.next", i18n.Args{"at": p.Next.In(loc).Format("15:04")})
	}
}

func intakeDeliveryNote(l i18n.Lang, reason string) string {
	switch reason {
	case "no_device", "filtered", "muted", "unreachable", "rejected", "unconfirmed", "queued", "held", "kept":
		return intakeText(l, "delivery."+reason)
	default:
		return intakeText(l, "delivery.unknown")
	}
}
