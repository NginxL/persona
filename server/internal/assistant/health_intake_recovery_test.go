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
)

// This controllable bus holds only notify.request; record calls keep using the real handler.
type blockedWaterBus struct {
	*fakeHub
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *blockedWaterBus) NotifyRequest(ctx context.Context, _ NotifyRequest) (NotifyResult, error) {
	h.once.Do(func() { close(h.entered) })
	select {
	case <-h.release:
		return NotifyResult{OK: true, Devices: new(0), Reason: "filtered"}, nil
	case <-ctx.Done():
		return NotifyResult{}, ctx.Err()
	}
}

func TestHealthWaterBlockedDeliveryDoesNotBlockIntake(t *testing.T) {
	for _, action := range []string{"intake", "snooze"} {
		t.Run(action, func(t *testing.T) {
			f, st := newFakeHub(), &intakeTestStore{}
			bus := &blockedWaterBus{fakeHub: f, entered: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			unblock := func() { release.Do(func() { close(bus.release) }) }
			defer unblock()
			s := withSettings(t, f, New(Options{Hub: bus, Store: func() Store { return st }, Logf: testLogf(t)}))
			setSettings(t, s, func(v *Settings) { v.Timezone = "UTC" })
			c := defaultHealthIntakeSettings()
			c.Water.Remind = true
			if err := st.PutSetting(context.Background(), healthIntakeKey, c); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- s.waterReminderTick(ctx, waterTestTime("2026-11-16T10:00:00Z")) }()
			select {
			case <-bus.entered:
			case <-ctx.Done():
				t.Fatal("reminder never reached the controlled bus")
			}
			response := make(chan *httptest.ResponseRecorder, 1)
			started := time.Now()
			go func() {
				if action == "intake" {
					response <- intakeHTTP(s, "POST", "/api/v1/assistant/health/intake", `{"id":"`+intakeTestID+`","kind":"water"}`, "answer")
				} else {
					response <- waterActionHTTP(s, "synthetic-reminder", "snooze", "", "full")
				}
			}()
			select {
			case w := <-response:
				if w.Code != 200 {
					t.Fatalf("record/action while delivery blocked: %d %s", w.Code, w.Body)
				}
				t.Logf("%s returned while notify.request was still blocked, elapsed %s", action, time.Since(started))
			case <-time.After(500 * time.Millisecond):
				t.Fatal("intake/action waited on the blocked notify.request")
			}
			before, err := s.waterReminderState(context.Background())
			if err != nil || before.Reason != "" || action == "snooze" && before.Snooze.IsZero() {
				t.Fatalf("new state: %+v %v", before, err)
			}
			unblock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			after, err := s.waterReminderState(context.Background())
			if err != nil || after != before {
				t.Fatalf("stale delivery overwrote a newer timer: before %+v, after %+v, %v", before, after, err)
			}
		})
	}
}

func TestHealthIntakeInvalidStoredSettingsRemainRepairable(t *testing.T) {
	for _, raw := range []string{
		`{"water":{"goal_ml":1,"start":"22:00","end":"09:00"},"protein":{"goal_g":1}}`,
		`{"water":{"cup_ml":"invalid"}}`,
		`{`,
	} {
		t.Run(raw, func(t *testing.T) {
			st, f := &intakeTestStore{}, newFakeHub()
			var logMu sync.Mutex
			var logs []string
			logf := func(format string, args ...any) {
				logMu.Lock()
				defer logMu.Unlock()
				logs = append(logs, fmt.Sprintf(format, args...))
			}
			s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: logf}))
			st.mu.Lock()
			if st.vals == nil {
				st.vals = map[string][]byte{}
			}
			st.vals[healthIntakeKey] = []byte(raw)
			st.mu.Unlock()
			w := intakeHTTP(s, "GET", "/api/v1/assistant/health/intake", "", "full")
			var view healthIntakeView
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || view.Water.Goal != 2000 || view.Protein.Goal != 60 {
				t.Fatalf("invalid settings blocked read: %d %s", w.Code, w.Body)
			}
			w = intakeHTTP(s, "POST", "/api/v1/assistant/health/intake", `{"id":"`+intakeTestID+`","kind":"water"}`, "answer")
			var mutation healthIntakeMutation
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &mutation) != nil || mutation.Entry == nil || mutation.Entry.Amount != 250 {
				t.Fatalf("invalid settings blocked record: %d %s", w.Code, w.Body)
			}
			res := intakePageResult(t, intakePageHTTP(s, "water", "settings", nil, "", 4, "full"))
			fields := map[string]string{}
			for _, item := range res.View.Sections[0].Items {
				if item.Type == "field" {
					fields[item.Name] = item.Value
				}
			}
			if fields["goal_ml"] != "2000" || fields["cup_ml"] != "250" || fields["remind"] != "false" {
				t.Fatalf("repair form did not show safe defaults: %v", fields)
			}
			if w := intakePageHTTP(s, "water", "save", map[string]string{"goal_ml": "499"}, res.View.State, 4, "full"); w.Code != 400 {
				t.Fatalf("save stopped validating: %d %s", w.Code, w.Body)
			}
			st.mu.Lock()
			stored := string(st.vals[healthIntakeKey])
			st.mu.Unlock()
			if stored != raw {
				t.Fatal("a read or rejected save rewrote invalid settings")
			}
			intakePageResult(t, intakePageHTTP(s, "water", "save", map[string]string{"goal_ml": "2400"}, res.View.State, 4, "full"))
			settings, err := s.healthIntakeSettings(context.Background())
			if err != nil || settings.Water.Goal != 2400 || settings.Protein.Goal != 60 || settings.Water.Cup != 250 {
				t.Fatalf("settings were not repaired: %+v %v", settings, err)
			}
			logMu.Lock()
			logged := strings.Join(logs, "\n")
			logMu.Unlock()
			if !strings.Contains(logged, "invalid stored settings; using defaults") {
				t.Fatal("invalid settings fallback was not logged")
			}
		})
	}
}

func TestHealthIntakeCommittedWaterSurvivesReminderStateFailure(t *testing.T) {
	for _, failure := range []string{"write", "read"} {
		t.Run(failure, func(t *testing.T) {
			st, f := &intakeTestStore{}, newFakeHub()
			var logMu sync.Mutex
			var logs []string
			s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: func(format string, args ...any) {
				logMu.Lock()
				defer logMu.Unlock()
				logs = append(logs, fmt.Sprintf(format, args...))
			}}))
			if failure == "write" {
				st.setFailPut(errors.New("synthetic reminder state save failure"))
			} else {
				st.failKey(waterReminderKey, errors.New("synthetic reminder state read failure"))
			}
			w := intakeHTTP(s, "POST", "/api/v1/assistant/health/intake", `{"id":"`+intakeTestID+`","kind":"water","at":"2000-01-01T00:00:00Z"}`, "answer")
			var result healthIntakeMutation
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Entry == nil || result.Entry.ID != intakeTestID || result.Entry.Amount != 250 {
				t.Fatalf("committed record returned failure: %d %s", w.Code, w.Body)
			}
			if len(st.rows) != 1 || result.Today.Water != 0 {
				t.Fatal("backdated measurement changed or record was not committed")
			}
			logMu.Lock()
			logged := strings.Join(logs, "\n")
			logMu.Unlock()
			if !strings.Contains(logged, "water reminder state reset:") {
				t.Fatal("reminder state failure was not logged")
			}
		})
	}
}

func TestHealthIntakeTinyAmountsAreClientErrors(t *testing.T) {
	for _, kind := range []string{"water", "protein"} {
		for _, amount := range []string{"1e-300", "1e-9", "1e-8", "0.01", "0.09"} {
			st := &intakeTestStore{fail: true} // A reached store would answer 503: rejection must happen first.
			s := newServiceOn(t, newFakeHub(), st)
			body := fmt.Sprintf(`{"id":%q,"kind":%q,"amount":%s}`, intakeTestID, kind, amount)
			if w := intakeHTTP(s, "POST", "/api/v1/assistant/health/intake", body, "full"); w.Code != 400 {
				t.Fatalf("%s %s: %d %s", kind, amount, w.Code, w.Body)
			}
		}
	}
	for _, tc := range []struct{ kind, amount string }{{"water", "1"}, {"protein", "0.1"}} {
		s := newServiceOn(t, newFakeHub(), &intakeTestStore{})
		body := fmt.Sprintf(`{"id":%q,"kind":%q,"amount":%s}`, intakeTestID, tc.kind, tc.amount)
		if w := intakeHTTP(s, "POST", "/api/v1/assistant/health/intake", body, "full"); w.Code != 200 {
			t.Fatalf("minimum valid amount: %d %s", w.Code, w.Body)
		}
	}
}
