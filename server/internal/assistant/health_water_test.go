package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

func waterActionHTTP(s *Service, reminder, action, amount, role string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(waterReminderRequest{Reminder: reminder, Action: action, Amount: amount})
	r := httptest.NewRequest("POST", "/api/v1/assistant/health/water/reminder?space=health", strings.NewReader(string(body)))
	r.Header.Set(RoleHeader, role)
	r.Header.Set("Accept-Language", "en")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestHealthWaterAmountParsing(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"300", 300}, {"300ml", 300}, {"300 毫升", 300}, {" 300 ML ", 300}, {"1", 1}, {"3000", 3000},
		{"0", 0}, {"3001", 0}, {"-300", 0}, {"1.5", 0}, {"", 0}, {"999999999999999999999999", 0}, {"abc300", 0}, {"300 400", 0},
	} {
		t.Run(tc.in, func(t *testing.T) {
			n, ok := waterAmount(tc.in)
			if ok != (tc.want > 0) || ok && n != tc.want {
				t.Fatalf("amount %d %v", n, ok)
			}
			s := newServiceOn(t, newFakeHub(), &intakeTestStore{})
			w := waterActionHTTP(s, "synthetic-reminder", "log", tc.in, "full")
			want := 200
			if tc.want == 0 {
				want = 400
			}
			if w.Code != want {
				t.Fatalf("action: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestHealthWaterReminderDedupSurvivesRestartAndUndo(t *testing.T) {
	st, f := &intakeTestStore{}, newFakeHub()
	s := newServiceOn(t, f, st)
	for range 2 {
		if w := waterActionHTTP(s, "synthetic-reminder", "drink", "", "full"); w.Code != 200 {
			t.Fatalf("drink: %d %s", w.Code, w.Body)
		}
		s = newServiceOn(t, f, st)
	}
	for range 2 {
		if w := waterActionHTTP(s, "synthetic-reminder", "log", "300ml", "full"); w.Code != 200 {
			t.Fatalf("log: %d %s", w.Code, w.Body)
		}
	}
	if len(st.rows) != 2 || st.rows[reminderEntryID("synthetic-reminder", "drink")].Amount != 250 || st.rows[reminderEntryID("synthetic-reminder", "log")].Amount != 300 {
		t.Fatalf("per-action dedup: %v", st.rows)
	}
	id := reminderEntryID("synthetic-reminder", "drink")
	// Independently computed with Python uuid.uuid5(uuid.NAMESPACE_URL, name).
	if id != "497ba7c0-15d6-59ab-830a-65edcf59d592" {
		t.Fatalf("UUIDv5 namespace or name changed: %s", id)
	}
	if w := intakeHTTP(s, "DELETE", "/api/v1/assistant/health/intake/"+id, "", "full"); w.Code != 200 {
		t.Fatal(w.Body)
	}
	// No 24-hour expiry: any old reminder remains idempotent, including a deleted entry.
	if w := waterActionHTTP(s, "synthetic-reminder", "drink", "", "full"); w.Code != 200 || !strings.Contains(w.Body.String(), "deleted") {
		t.Fatalf("retry undo: %d %s", w.Code, w.Body)
	}
	if st.rows[id].DeletedAt == nil || len(st.rows) != 2 {
		t.Fatal("a deleted reminder entry was revived")
	}
	// Invalid input does not consume the log action.
	if w := waterActionHTTP(s, "synthetic-second", "log", "bad", "full"); w.Code != 400 {
		t.Fatal(w.Body)
	}
	if w := waterActionHTTP(s, "synthetic-second", "log", "400", "full"); w.Code != 200 || len(st.rows) != 3 {
		t.Fatalf("valid retry: %d %s", w.Code, w.Body)
	}
	if !workoutUUID.MatchString(id) || id[14] != '5' || id == reminderEntryID("synthetic-reminder", "log") {
		t.Fatalf("unstable UUIDv5: %s", id)
	}
}

func TestHealthWaterSnoozeAndEverySourceRestarts(t *testing.T) {
	for _, source := range []string{"watch", "page", "reminder"} {
		t.Run(source, func(t *testing.T) {
			st := &intakeTestStore{}
			s := newServiceOn(t, newFakeHub(), st)
			before := time.Now()
			if w := waterActionHTTP(s, "synthetic-reminder", "snooze", "", "full"); w.Code != 200 {
				t.Fatal(w.Body)
			}
			a, _ := s.waterReminderState(context.Background())
			if a.Snooze.Before(before.Add(30*time.Minute)) || a.Snooze.After(time.Now().Add(30*time.Minute)) {
				t.Fatalf("not 30 minutes: %v", a)
			}
			waterActionHTTP(s, "synthetic-reminder", "snooze", "", "full")
			b, _ := s.waterReminderState(context.Background())
			if !a.Snooze.Equal(b.Snooze) {
				t.Fatal("retry extended snooze")
			}
			var w *httptest.ResponseRecorder
			switch source {
			case "watch":
				w = intakeHTTP(s, "POST", "/api/v1/assistant/health/intake", `{"id":"`+intakeTestID+`","kind":"water","at":"2000-01-01T00:00:00Z"}`, "answer")
			case "page":
				w = intakePageHTTP(s, "water", "drink", nil, "", 4, "full")
			default:
				w = waterActionHTTP(s, "synthetic-reminder", "drink", "", "full")
			}
			if w.Code != 200 {
				t.Fatalf("record: %d %s", w.Code, w.Body)
			}
			b, _ = s.waterReminderState(context.Background())
			if !b.Snooze.IsZero() || b.Last.Before(before) || b.Reason != "" {
				t.Fatalf("did not reset, including backdated entries: %+v", b)
			}
		})
	}
}

func TestHealthWaterReminderAccessAndFailures(t *testing.T) {
	s := newServiceOn(t, newFakeHub(), &intakeTestStore{})
	for _, role := range []string{"answer", "read", "scoped", "extension", "readonly", "unknown"} {
		if w := waterActionHTTP(s, "synthetic", "drink", "", role); w.Code != 403 {
			t.Fatalf("role %q: %d %s", role, w.Code, w.Body)
		}
	}
	for _, tc := range []struct{ reminder, action string }{{"", "drink"}, {"synthetic", "invalid"}, {strings.Repeat("r", 129), "drink"}, {"r\n", "snooze"}} {
		if w := waterActionHTTP(s, tc.reminder, tc.action, "", "full"); w.Code != 400 {
			t.Fatalf("invalid request: %d %s", w.Code, w.Body)
		}
	}
	for _, body := range []string{`{`, `{} {}`, `{"reminder":"synthetic","action":"log","amount":300}`} {
		if w := intakeHTTP(s, "POST", "/api/v1/assistant/health/water/reminder", body, "full"); w.Code != 400 {
			t.Fatalf("invalid JSON: %d %s", w.Code, w.Body)
		}
	}
	st := &intakeTestStore{}
	s = newServiceOn(t, newFakeHub(), st)
	st.setFailPut(errors.New("synthetic save failure"))
	if w := waterActionHTTP(s, "synthetic", "snooze", "", "full"); w.Code != 503 {
		t.Fatalf("failed snooze: %d %s", w.Code, w.Body)
	}
	if w := waterActionHTTP(s, "synthetic", "drink", "", "full"); w.Code != 200 {
		t.Fatalf("committed entry despite failed reminder reset: %d %s", w.Code, w.Body)
	}
	st.setFailPut(nil)
	if w := waterActionHTTP(s, "synthetic", "drink", "", "full"); w.Code != 200 || len(st.rows) != 1 {
		t.Fatalf("retry after committed entry: %d %s", w.Code, w.Body)
	}
	r, _ := s.waterReminderState(context.Background())
	if r.Last.IsZero() || !r.Snooze.IsZero() {
		t.Fatal("retry failed to repair reminder state")
	}
}

func TestHealthWaterTickCatchupRestartAndDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		result       NotifyResult
		err          error
	}{
		{"delivered", "", NotifyResult{OK: true, Devices: new(1)}, nil},
		{"held", "held", NotifyResult{OK: true, Held: true, Reason: "ln_mode"}, nil},
		{"queued", "queued", NotifyResult{OK: true, Queued: true}, nil},
		{"kept", "kept", NotifyResult{OK: true, Devices: new(0), Kept: 1}, nil},
		{"filtered", "filtered", NotifyResult{OK: true, Devices: new(0), Reason: "filtered"}, nil},
		{"old result", "unconfirmed", NotifyResult{OK: true}, nil},
		{"rejected", "rejected", NotifyResult{}, nil},
		{"unreachable", "unreachable", NotifyResult{}, errors.New("synthetic unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, f := &intakeTestStore{}, newFakeHub()
			f.notifyResult, f.refuseNotify = tc.result, tc.err
			s := newServiceOn(t, f, st)
			setSettings(t, s, func(v *Settings) { v.Timezone = "UTC" })
			now := waterTestTime("2026-11-16T10:00:00Z")
			if err := s.waterReminderTick(context.Background(), now); err != nil || len(f.pushed()) != 0 {
				t.Fatal("reminders not off by default")
			}
			c := defaultHealthIntakeSettings()
			c.Water.Remind = true
			st.PutSetting(context.Background(), healthIntakeKey, c)
			st.PutSetting(context.Background(), waterReminderKey, waterReminderState{Last: now.Add(-48 * time.Hour)})
			err := s.waterReminderTick(context.Background(), now)
			if (err != nil) != (tc.err != nil) {
				t.Fatalf("send error: %v", err)
			}
			r, _ := s.waterReminderState(context.Background())
			if !r.Last.Equal(now) || !r.Snooze.IsZero() || r.Reason != tc.reason {
				t.Fatalf("state: %+v, want %s", r, tc.reason)
			}
			if tc.err == nil {
				n := f.pushed()[0]
				if n.Kind != assistantwire.NotifyHealthWater || n.Level != assistantwire.LevelP1 || n.RelatedKey != "" || n.DeviceID != "" || n.SessionID != "" || n.Health == nil || n.Health.Reminder != n.ID {
					t.Fatalf("notification: %+v", n)
				}
			}
			s = newServiceOn(t, f, st)
			setSettings(t, s, func(v *Settings) { v.Timezone = "UTC" })
			count := len(f.pushed())
			if err := s.waterReminderTick(context.Background(), now.Add(30*time.Minute)); err != nil || len(f.pushed()) != count {
				t.Fatalf("restart resent catch-up: %v", err)
			}
		})
	}
}

func TestHealthWaterTickDoesNotSendOnStorageFailure(t *testing.T) {
	st, f := &intakeTestStore{}, newFakeHub()
	s := newServiceOn(t, f, st)
	setSettings(t, s, func(v *Settings) { v.Timezone = "UTC" })
	c := defaultHealthIntakeSettings()
	c.Water.Remind = true
	st.PutSetting(context.Background(), healthIntakeKey, c)
	st.setFailPut(errors.New("synthetic save failure"))
	if err := s.waterReminderTick(context.Background(), waterTestTime("2026-11-16T10:00:00Z")); err == nil || len(f.pushed()) != 0 {
		t.Fatalf("sent despite failed persistence: %v", err)
	}
}

func TestHealthWaterConcurrentActionsAndScheduler(t *testing.T) {
	st, f := &intakeTestStore{}, newFakeHub()
	f.notifyResult = NotifyResult{OK: true, Devices: new(1)}
	s := newServiceOn(t, f, st)
	setSettings(t, s, func(v *Settings) { v.Timezone = "UTC" })
	c := defaultHealthIntakeSettings()
	c.Water.Remind, c.Water.Start, c.Water.End = true, "00:00", "23:59"
	st.PutSetting(context.Background(), healthIntakeKey, c)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := waterActionHTTP(s, fmt.Sprintf("synthetic-%d", i), "drink", "", "full"); w.Code != 200 {
				t.Errorf("action: %d %s", w.Code, w.Body)
			}
			if err := s.waterReminderTick(context.Background(), time.Now()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(st.rows) != 20 {
		t.Fatalf("lost records: %d", len(st.rows))
	}
}

func TestHealthWaterLoopStopsAndCopyIsLocalized(t *testing.T) {
	s := newServiceOn(t, newFakeHub(), &intakeTestStore{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.waterReminderLoop(ctx) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not stop")
	}
	for _, lang := range []i18n.Lang{"en", "zh-Hans", "ja"} {
		for _, key := range []string{"reminder.title", "reminder.empty", "reminder.progress", "toast.logged", "toast.snooze", "delivery.unknown"} {
			v := intakeText(lang, key, i18n.Args{"today": 250, "goal": 2000, "cups": 7, "amount": 250})
			if v == "" || strings.Contains(v, "assistant-health_intake") || strings.Contains(v, "{goal}") {
				t.Fatalf("missing localized %s %s: %s", lang, key, v)
			}
		}
	}
}

func TestHealthWaterOutboxPreservesReminderContext(t *testing.T) {
	h := newOutboxHub(t)
	out := &memOutbox{}
	b := startOutboxManager(t, h, out)
	n := Notification{ID: "synthetic-reminder", Kind: assistantwire.NotifyHealthWater, Level: assistantwire.LevelP1,
		Title: "Time for some water", Health: &assistantwire.NotifyHealth{Reminder: "synthetic-reminder"}}
	res, err := b.NotifyRequest(context.Background(), NotifyRequest{Notification: n})
	if err != nil || !res.Queued || res.Devices != nil {
		t.Fatalf("offline: %+v %v", res, err)
	}
	items, err := out.OutboxItems(context.Background(), 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("outbox: %v %v", items, err)
	}
	var saved Notification
	if err := json.Unmarshal(items[0].Body, &saved); err != nil || saved.Health == nil || saved.Health.Reminder != n.Health.Reminder || saved.Level != assistantwire.LevelP1 || saved.RelatedKey != "" {
		t.Fatalf("queued context lost: %+v %v", saved, err)
	}
	h.up.Store(true)
	h.waitFor(t, []string{"push synthetic-reminder"})
}

func TestHealthWaterGoalToastAndSnoozeWindow(t *testing.T) {
	for _, stop := range []bool{true, false} {
		c := defaultHealthIntakeSettings()
		c.Water.Stop = stop
		want := "toast.goal"
		if stop {
			want = "toast.goal_stop"
		}
		if got := intakeWaterToast("en", c.Water, 250, 2000); got != intakeText("en", want) {
			t.Fatal(got)
		}
	}
	c := waterTestDefaults()
	now := waterTestTime("2026-11-16T20:45:00Z")
	p := scheduleWater(c, waterReminderState{Snooze: now.Add(30 * time.Minute)}, nil, now, time.UTC)
	if p.Reason != "closed" || !p.Next.Equal(waterTestTime("2026-11-17T09:00:00Z")) {
		t.Fatalf("late snooze did not move to next window: %+v", p)
	}
}

// Avoid satisfying the Store interface by accident if its test-only embedded store changes.
var _ healthIntakeStore = (*intakeTestStore)(nil)
