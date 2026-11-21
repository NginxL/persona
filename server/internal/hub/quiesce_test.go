package hub

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
)

func TestWriteQuiescerWaitsForInFlightWrites(t *testing.T) {
	var q writeQuiescer
	if !q.begin() {
		t.Fatal("a write was refused before any pause")
	}
	paused := make(chan error, 1)
	go func() { paused <- q.pause(context.Background()) }()
	select {
	case err := <-paused:
		t.Fatalf("pause returned with a write in flight: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if q.begin() {
		t.Fatal("a write was admitted while paused")
	}
	q.end()
	select {
	case err := <-paused:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("pause did not return after the write finished")
	}
	q.resume()
	if !q.begin() {
		t.Fatal("writes did not resume")
	}
	q.end()
}

func TestWriteQuiescerPauseCanBeCancelled(t *testing.T) {
	var q writeQuiescer
	if !q.begin() {
		t.Fatal("a write was refused before any pause")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.pause(ctx); err == nil {
		t.Fatal("pause ignored its cancelled context")
	}
	q.end()
	q.resume()
}

func TestAssistantGatePausesAndResumes(t *testing.T) {
	var g assistantGate
	ctx := context.Background()
	if err := g.pause(ctx); err != nil {
		t.Fatal(err)
	}
	resumed := make(chan bool, 1)
	go func() { resumed <- g.waitResumed(ctx) }()
	select {
	case <-resumed:
		t.Fatal("the supervisor resumed while paused")
	case <-time.After(30 * time.Millisecond):
	}
	if g.register(make(chan struct{}), func() {}) {
		t.Fatal("a child was registered while paused")
	}
	g.start()
	select {
	case ok := <-resumed:
		if !ok {
			t.Fatal("the supervisor did not resume")
		}
	case <-time.After(time.Second):
		t.Fatal("resume did not wake the supervisor")
	}

	// A running child is stopped and pause waits for its exit.
	exit := make(chan struct{})
	cancelled := make(chan struct{})
	if !g.register(exit, func() { close(cancelled) }) {
		t.Fatal("a child was not registered")
	}
	done := make(chan error, 1)
	go func() { done <- g.pause(ctx) }()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("pause did not cancel the child")
	}
	select {
	case err := <-done:
		t.Fatalf("pause returned before the child exited: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if paused := g.unregister(); !paused {
		t.Fatal("the supervisor lost the pause")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	g.start()
}

// A report that arrived while a snapshot paused writes is written by the
// resume itself: the pending workers are only woken once writes are admitted.
func TestDevelopmentResumeWritesPendingExtReport(t *testing.T) {
	h := newTestHub(t)
	store := newMeshStore()
	h.store = store
	ctx := context.Background()
	if err := h.quiesce.pause(ctx); err != nil {
		t.Fatal(err)
	}
	defer h.quiesce.resume()
	want := extwire.Report{Features: []string{"fixture-report"}}
	h.queueExtReportSave(want)
	waitFor(t, "the paused report to stay pending", func() bool {
		h.ext.saveMu.Lock()
		defer h.ext.saveMu.Unlock()
		return h.ext.savePending != nil && !h.ext.saveRunning
	})
	h.resumeDevelopmentWriters()
	waitFor(t, "the pending report to be written after resume", func() bool {
		var got extwire.Report
		ok, err := store.Setting(ctx, extReportKey, &got)
		return err == nil && ok && slices.Equal(got.Features, want.Features)
	})
}

// A device session that ends while a snapshot paused writes keeps its end for
// the resume, and one that starts then records no row.
func TestDevelopmentPauseDefersDeviceSessionWrites(t *testing.T) {
	h := newTestHub(t)
	store := &deviceSessionWrites{meshStore: newMeshStore()}
	h.store = store
	ctx := context.Background()
	if err := h.quiesce.pause(ctx); err != nil {
		t.Fatal(err)
	}
	defer h.quiesce.resume()
	h.finishDeviceSession(7, time.Now(), "closed", 30)
	if store.finished != 0 {
		t.Fatal("a device session was finished while writes were paused")
	}
	// A connection that arrives now is served without a session row.
	r := httptest.NewRequest(http.MethodGet, "/api/v1/ws", nil)
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{ID: "dev_fixture", Role: roleFull}))
	if c := h.deviceSessionConnected(r, &client{}); c != nil {
		t.Fatal("a device session was inserted while writes were paused")
	}
	if store.inserted != 0 {
		t.Fatal("a device session row was written while writes were paused")
	}
	h.resumeDevelopmentWriters()
	waitFor(t, "the deferred session end to be written", func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.finished == 1
	})
	if store.finishID != 7 || store.finishReason != "closed" {
		t.Fatalf("finish = %d %q", store.finishID, store.finishReason)
	}
}

// deviceSessionWrites counts the device-session writes that reach the store.
type deviceSessionWrites struct {
	*meshStore
	inserted, finished int
	finishID           int64
	finishReason       string
}

func (s *deviceSessionWrites) InsertDeviceSession(context.Context, db.DeviceSession) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inserted++
	return int64(s.inserted), nil
}

func (s *deviceSessionWrites) FinishDeviceSession(_ context.Context, id int64, _ time.Time, reason string, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished++
	s.finishID, s.finishReason = id, reason
	return nil
}

// Every route outside the apps' API that writes the hub's database is refused
// while a snapshot pauses writes: signing in (password and browser), trading a
// manager's install code, and the admin pages.
func TestDevelopmentPauseRefusesOuterWriteRoutes(t *testing.T) {
	h := newTestHub(t)
	h.store = newMeshStore()
	mux := http.NewServeMux()
	h.passwordLoginRoutes(mux)
	h.browserLoginRoutes(mux)
	h.managerJoinRoutes(mux)
	setAdmin(h, AdminConfig{Access: accessAny})
	h.adminRoutes(mux, http.NotFoundHandler(), http.NotFoundHandler())
	ctx := context.Background()
	if err := h.quiesce.pause(ctx); err != nil {
		t.Fatal(err)
	}
	defer h.quiesce.resume()
	for _, path := range []string{
		"/api/v1/auth/password/finish",
		"/api/v1/auth/browser-login/approve",
		"/api/v1/bus/join",
		"/api/v1/admin/network/access",
	} {
		t.Run(path, func(t *testing.T) {
			method := http.MethodPost
			if strings.HasPrefix(path, "/api/v1/admin/") {
				method = http.MethodPut
			}
			r := httptest.NewRequest(method, path, strings.NewReader("{}"))
			r.Header.Set("Content-Type", "application/json")
			r.TLS = &tls.ConnectionState{} // not the plaintext refusal
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s -> %d: %s", path, w.Code, w.Body.String())
			}
		})
	}
}
