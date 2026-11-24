package hub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

func (h *Hub) lockTestRoutes(api *http.ServeMux) {
	api.HandleFunc("POST /api/v1/devices/{id}/lock-tests", h.serveLockTest)
	api.HandleFunc("GET /api/v1/devices/{id}/lock-tests/{test_id}", h.serveLockTest)
	api.HandleFunc("POST /api/v1/devices/{id}/lock-tests/{test_id}/receipt", h.serveLockTest)
}

func (h *Hub) serveLockTest(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r.Context())
	deviceID, testID := r.PathValue("id"), r.PathValue("test_id")
	// The device ID is the credential ID, never the untrusted client_hello device_id.
	if p.ID == "" || p.PairHash != "" || p.ID != deviceID {
		writeErr(w, http.StatusForbidden, errors.New("a matching device credential is required"))
		return
	}
	if h.store == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("lock test storage is unavailable"))
		return
	}
	if r.Method == http.MethodGet {
		test, err := h.store.LockTest(r.Context(), deviceID, testID)
		h.writeLockTest(w, test, err, http.StatusNotFound)
		return
	}
	var input struct {
		DelaySeconds      *int  `json:"delay_seconds"`
		NotificationShown *bool `json:"notification_shown"`
	}
	if err := decodeJSONBody(w, r, &input, 4096); err != nil && !errors.Is(err, io.EOF) {
		writeBodyErr(w, err, errors.New("invalid lock test request"))
		return
	}
	if testID != "" {
		test, err := h.store.ReceiveLockTest(r.Context(), deviceID, testID, input.NotificationShown, time.Now())
		h.writeLockTest(w, test, err, http.StatusConflict)
		return
	}
	delay := 30
	if input.DelaySeconds != nil {
		delay = *input.DelaySeconds
	}
	if delay < 1 || delay > 3600 {
		writeErr(w, http.StatusBadRequest, errors.New("delay_seconds must be between 1 and 3600"))
		return
	}
	now := time.Now()
	test := db.LockTest{TestID: "lt_" + randomHex(16), DeviceID: deviceID,
		CreatedAt: now, DueAt: now.Add(time.Duration(delay) * time.Second), Status: "scheduled"}
	if err := h.store.CreateLockTest(r.Context(), test); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	h.lockTests.wake()
	writeJSON(w, test)
}

func (h *Hub) writeLockTest(w http.ResponseWriter, test *db.LockTest, err error, missingStatus int) {
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
	} else if test == nil {
		writeErr(w, missingStatus, errors.New("lock test not found or not sent"))
	} else {
		writeJSON(w, test)
	}
}

func (h *Hub) lockTestClient(deviceID string) *client {
	h.mu.Lock()
	defer h.mu.Unlock()
	var best *client
	for c := range h.clients {
		if c.device != deviceID || c.deviceSessionID == 0 || !c.has("lock_test") {
			continue
		}
		if best == nil || c.since.After(best.since) {
			best = c
		}
	}
	return best
}

func (h *Hub) dispatchLockTests(ctx context.Context, now time.Time) error {
	// A snapshot pauses hub writers; due lock tests stay scheduled and
	// resumeDevelopmentWriters wakes startLockTests to dispatch them.
	if !h.quiesce.begin() {
		return nil
	}
	defer h.quiesce.end()
	tests, err := h.store.DueLockTests(ctx, now)
	if err != nil {
		return err
	}
	for _, test := range tests {
		c := h.lockTestClient(test.DeviceID)
		var sessionID int64
		if c != nil {
			sessionID = c.deviceSessionID
		}
		claimed, err := h.store.AttemptLockTest(ctx, test.TestID, sessionID, now)
		if err != nil {
			return err
		}
		if !claimed || c == nil {
			continue
		}
		// Namespace every new payload key to avoid iOS Envelope decoding collisions.
		c.sendJSON(map[string]any{
			"type": "lock_test", "lock_test_id": test.TestID,
			"lock_test_due_at": test.DueAt, "lock_test_sent_at": now,
			"lock_test_title": i18n.T(c.lang(), "hub-locktest.title"), "lock_test_body": i18n.T(c.lang(), "hub-locktest.body"),
		})
	}
	return nil
}

const (
	// lockTestRecheck bounds every sleep, so a wall clock jump or a test written by another process is picked up.
	lockTestRecheck = time.Minute
	// lockTestRetry spaces out checks while due tests remain, e.g. a batch over the limit or a lost claim.
	lockTestRetry = time.Second
)

// lockTestWake lets a new test re-arm the dispatcher's timer without waiting for the next recheck.
type lockTestWake struct {
	once sync.Once
	ch   chan struct{}
}

func (w *lockTestWake) channel() chan struct{} {
	w.once.Do(func() { w.ch = make(chan struct{}, 1) })
	return w.ch
}

// wake never blocks: one pending wake is enough because the dispatcher rereads the earliest due time.
func (w *lockTestWake) wake() {
	select {
	case w.channel() <- struct{}{}:
	default:
	}
}

// lockTestWait returns how long the dispatcher may sleep, and whether a test is due when it wakes;
// a wake that is only the periodic recheck reads the next due time again instead of querying for due tests.
func (h *Hub) lockTestWait(ctx context.Context, afterDispatch bool) (time.Duration, bool) {
	callCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	next, err := h.store.NextLockTestDue(callCtx)
	if err != nil {
		if ctx.Err() == nil {
			h.logf("next lock test: %v", err)
		}
		return lockTestRetry, true
	}
	if next == nil {
		return lockTestRecheck, false
	}
	wait := time.Until(*next)
	if wait <= 0 && afterDispatch {
		return lockTestRetry, true
	}
	if wait > lockTestRecheck {
		return lockTestRecheck, false
	}
	return max(wait, 0), true
}

// startLockTests sleeps until the earliest scheduled test is due instead of polling the store,
// and re-arms after each dispatch and whenever serveLockTest schedules a test.
func (h *Hub) startLockTests(ctx context.Context) (func(), error) {
	if h.store == nil {
		return func() {}, nil
	}
	if err := h.store.RecoverLockTests(ctx, time.Now()); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	wake := h.lockTests.channel()
	go func() {
		defer close(done)
		timer := time.NewTimer(time.Hour)
		defer timer.Stop()
		dispatched := false
		for {
			wait, due := h.lockTestWait(ctx, dispatched)
			timer.Reset(wait)
			dispatched = false
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case now := <-timer.C:
				if !due {
					continue
				}
				callCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				if err := h.dispatchLockTests(callCtx, now); err != nil && ctx.Err() == nil {
					h.logf("dispatch lock tests: %v", err)
				}
				stop()
				dispatched = true
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}
