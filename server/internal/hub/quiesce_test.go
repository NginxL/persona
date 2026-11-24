package hub

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/webpush"
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
	h.nodeWorkers.Wait()
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

// Finding #1: authentication still runs before quiesceGate (strangers get 401,
// not 503), authenticated requests skip TouchDeviceToken while paused, and an
// already-launched TouchDeviceToken finishes before pause returns.
func TestDevelopmentPauseSkipsAndWaitsForDeviceTokenTouch(t *testing.T) {
	h := newTestHub(t)
	tok := "jd_fixture_token_touch_test"
	store := &tokenTouchStore{
		meshStore: newMeshStore(),
		token:     &db.DeviceToken{ID: "dev_touch", Name: "Phone", Kind: "phone", Role: roleFull},
		hash:      hashToken(tok),
	}
	h.store = store
	handler := h.auth(h.quiesceGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	// Part A: while paused, strangers still get 401 (gate is after auth),
	// authenticated GET succeeds without calling TouchDeviceToken, and after
	// resume the next GET does touch the token.
	ctx := context.Background()
	if err := h.quiesce.pause(ctx); err != nil {
		t.Fatal(err)
	}
	// Stranger POST -> 401, not 503.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/example", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("stranger during pause -> %d, want 401", w.Code)
	}
	// Authenticated GET -> 200, no TouchDeviceToken write.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/example", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated GET during pause -> %d, want 200", w.Code)
	}
	waitNoWriteInFlight(t, h)
	if n := store.touches.Load(); n != 0 {
		t.Fatalf("TouchDeviceToken ran %d times while paused", n)
	}
	// Authenticated POST -> 503, still no TouchDeviceToken write.
	reqPost := httptest.NewRequest(http.MethodPost, "/api/v1/example", nil)
	reqPost.Header.Set("Authorization", "Bearer "+tok)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, reqPost)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("authenticated POST during pause -> %d, want 503", w.Code)
	}
	waitNoWriteInFlight(t, h)
	if n := store.touches.Load(); n != 0 {
		t.Fatalf("TouchDeviceToken ran %d times after refused POST while paused", n)
	}

	// Part B: after resume, an authenticated GET launches TouchDeviceToken, and
	// a concurrent pause waits until that in-flight touch completes.
	h.quiesce.resume()
	started := make(chan struct{})
	release := make(chan struct{})
	store.onTouch = func() {
		close(started)
		<-release
	}
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/example", nil)
	req2.Header.Set("Authorization", "Bearer "+tok)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req2)
	if w.Code != http.StatusOK {
		t.Fatalf("GET after resume -> %d", w.Code)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("TouchDeviceToken did not run after resume")
	}
	paused := make(chan error, 1)
	go func() { paused <- h.quiesce.pause(ctx) }()
	select {
	case err := <-paused:
		t.Fatalf("pause returned while TouchDeviceToken was still in flight: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-paused:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("pause did not return after TouchDeviceToken completed")
	}
	h.quiesce.resume()
}

type tokenTouchStore struct {
	*meshStore
	token   *db.DeviceToken
	hash    string
	touches atomic.Int32
	onTouch func()
}

func (s *tokenTouchStore) DeviceTokenByHash(_ context.Context, hash string) (*db.DeviceToken, error) {
	if hash == s.hash {
		cp := *s.token
		return &cp, nil
	}
	return nil, nil
}

func (s *tokenTouchStore) TouchDeviceToken(_ context.Context, _ string) error {
	s.touches.Add(1)
	if s.onTouch != nil {
		s.onTouch()
	}
	return nil
}

// Finding #2: timed and background Store writers defer due work while paused
// and wake on resume without performing any Store write during the pause.
func TestDevelopmentPauseDefersTimedBackgroundWriters(t *testing.T) {
	t.Run("lock tests", func(t *testing.T) {
		h := newTestHub(t)
		due := time.Now().Add(-time.Second)
		store := &bgWriteStore{meshStore: newMeshStore(), lockTestDue: &due}
		h.store = store
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := h.quiesce.pause(ctx); err != nil {
			t.Fatal(err)
		}
		var refused atomic.Int32
		h.quiesce.testOnPauseDefer = func() { refused.Add(1) } // only the lock test dispatcher writes in this hub
		stop, err := h.startLockTests(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer stop()
		waitFor(t, "the due lock test refused while paused", func() bool { return refused.Load() >= 1 })
		if n := store.lockTestAttempts.Load(); n != 0 {
			t.Fatalf("AttemptLockTest ran %d times while paused", n)
		}
		h.resumeDevelopmentWriters()
		waitFor(t, "due lock test dispatched after resume", func() bool {
			return store.lockTestAttempts.Load() >= 1
		})
	})

	t.Run("machine entries", func(t *testing.T) {
		prev := machineEntriesDelay
		machineEntriesDelay = 10 * time.Millisecond
		defer func() { machineEntriesDelay = prev }()

		h := newTestHub(t)
		store := &bgWriteStore{meshStore: newMeshStore()}
		h.store = store
		h.devices.mu.Lock()
		h.devices.keptRead = true
		h.devices.reportedRead = true
		h.devices.reported = reportedMachines{"assistant": {"m1": {Cfg: json.RawMessage(`{"id":"m1"}`)}}}
		h.devices.mu.Unlock()
		ctx := context.Background()
		if err := h.quiesce.pause(ctx); err != nil {
			t.Fatal(err)
		}
		h.machineEntriesChanged()
		waitFor(t, "the machine entries write deferred while paused", func() bool {
			h.devices.mu.Lock()
			defer h.devices.mu.Unlock()
			return h.devices.reportedDeferred
		})
		if store.settingWrites(machineEntriesKey) != 0 {
			t.Fatal("machine entries wrote to store while paused")
		}
		h.resumeDevelopmentWriters()
		waitFor(t, "deferred machine entries written after resume", func() bool {
			return store.settingWrites(machineEntriesKey) >= 1
		})
	})

	t.Run("live activity claim hold and save", func(t *testing.T) {
		h := newTestHub(t)
		store := &bgWriteStore{meshStore: newMeshStore()}
		h.store = store
		la := h.live
		ctx := context.Background()
		if err := h.quiesce.pause(ctx); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		if _, claimed := la.claimStart(db.LiveActivity{DeviceID: "dev1", PushToStartToken: "tok"}, now); claimed {
			t.Fatal("claimStart succeeded while paused")
		}
		la.holdStart(context.Background(), "dev1", now, fleetRefresh)
		la.update("dev1", func(v *db.LiveActivity) { v.PushToken = "" })
		if store.laClaims.Load() != 0 || store.laHolds.Load() != 0 || store.laSaves.Load() != 0 {
			t.Fatalf("live activity wrote while paused: claims=%d holds=%d saves=%d",
				store.laClaims.Load(), store.laHolds.Load(), store.laSaves.Load())
		}
		h.resumeDevelopmentWriters()
		waitFor(t, "deferred live activity hold and save flushed after resume", func() bool {
			return store.laHolds.Load() >= 1 && store.laSaves.Load() >= 1
		})
		la.mu.Lock()
		repair := la.repair
		la.mu.Unlock()
		select {
		case <-la.signal:
		default:
			if !repair {
				t.Fatal("live activity loop was not woken after deferred claimStart")
			}
		}
	})

	t.Run("client ops maintenance", func(t *testing.T) {
		prev := clientOpsCleanupEvery
		clientOpsCleanupEvery = 10 * time.Millisecond
		defer func() { clientOpsCleanupEvery = prev }()

		h := newTestHub(t)
		store := &bgWriteStore{meshStore: newMeshStore()}
		h.store = store
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := h.quiesce.pause(ctx); err != nil {
			t.Fatal(err)
		}
		if err := h.startClientOps(ctx); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the client ops cleanup deferred while paused", func() bool {
			h.clientOps.mu.Lock()
			defer h.clientOps.mu.Unlock()
			return h.clientOps.deferred
		})
		if n := store.clientOpCleanups.Load(); n != 0 {
			t.Fatalf("CleanupClientOps ran %d times while paused", n)
		}
		h.resumeDevelopmentWriters()
		waitFor(t, "deferred client ops cleanup ran after resume", func() bool {
			return store.clientOpCleanups.Load() >= 1
		})
	})

	t.Run("push maintenance and held pushes", func(t *testing.T) {
		prev := pushDeviceMaintenance
		pushDeviceMaintenance = 10 * time.Millisecond
		defer func() { pushDeviceMaintenance = prev }()

		h := newTestHub(t)
		store := &bgWriteStore{meshStore: newMeshStore()}
		h.store = store
		h.push = newPusher(t.TempDir(), store, func(string, ...any) {})
		h.push.quiesce = &h.quiesce
		h.push.web = newWebPusher(store, func(string, ...any) {})
		h.push.web.quiesce = &h.quiesce
		// Reset counters after startup reconciliation in newPusher.
		store.pushReconciles.Store(0)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := h.quiesce.pause(ctx); err != nil {
			t.Fatal(err)
		}
		go h.push.maintenanceLoop(ctx)
		h.push.recordIntercept(heldPushRecord{At: time.Now(), Title: "held"})
		// GET /api/v1/push/web must not persist a new VAPID key while paused.
		if _, err := h.push.web.key(context.Background()); err == nil {
			t.Fatal("webpush key creation succeeded while paused")
		}
		// Each pass of the loop tries reconcileDevices first, whose refusal marks maintenance deferred; the held-push
		// writer stops when its write is refused.
		waitFor(t, "push maintenance and the held-push writer deferred while paused", func() bool {
			h.push.mu.Lock()
			deferred := h.push.maintenanceDeferred
			h.push.mu.Unlock()
			h.push.gradingMu.Lock()
			defer h.push.gradingMu.Unlock()
			return deferred && !h.push.gradingWriting
		})
		if n := store.pushReconciles.Load(); n != 0 {
			t.Fatalf("ReconcilePushDevices ran %d times while paused", n)
		}
		if store.settingWrites(heldPushesSettingKey) != 0 {
			t.Fatal("saveIntercepts wrote while paused")
		}
		if store.settingWrites(webPushKeysSetting) != 0 {
			t.Fatal("webpush key wrote while paused")
		}
		h.resumeDevelopmentWriters()
		waitFor(t, "deferred push maintenance and held pushes ran after resume", func() bool {
			return store.pushReconciles.Load() >= 1 && store.settingWrites(heldPushesSettingKey) >= 1
		})
	})

	t.Run("persona tiers", func(t *testing.T) {
		h := newTestHub(t)
		store := &bgWriteStore{meshStore: newMeshStore()}
		h.store = store
		ctx := context.Background()
		if err := h.quiesce.pause(ctx); err != nil {
			t.Fatal(err)
		}
		h.setPersonaTier(ctx, "pkg", &personaTiers{Default: "base"}, "gold", "alice")
		if store.settingWrites(personaTiersKey) != 0 {
			t.Fatal("persona tiers wrote while paused")
		}
		h.resumeDevelopmentWriters()
		waitFor(t, "deferred persona tiers written after resume", func() bool {
			return store.settingWrites(personaTiersKey) >= 1
		})
	})
}

type bgWriteStore struct {
	*meshStore
	lockTestDue      *time.Time
	lockTestAttempts atomic.Int32
	laClaims         atomic.Int32
	laHolds          atomic.Int32
	laSaves          atomic.Int32
	clientOpCleanups atomic.Int32
	pushReconciles   atomic.Int32
	settingMu        sync.Mutex
	settingCounts    map[string]int
}

func (s *bgWriteStore) settingWrites(key string) int {
	s.settingMu.Lock()
	defer s.settingMu.Unlock()
	return s.settingCounts[key]
}

func (s *bgWriteStore) PutSetting(ctx context.Context, key string, v any) error {
	s.settingMu.Lock()
	if s.settingCounts == nil {
		s.settingCounts = map[string]int{}
	}
	s.settingCounts[key]++
	s.settingMu.Unlock()
	return s.meshStore.PutSetting(ctx, key, v)
}

func (s *bgWriteStore) NextLockTestDue(context.Context) (*time.Time, error) {
	if s.lockTestAttempts.Load() > 0 {
		return nil, nil
	}
	return s.lockTestDue, nil
}

func (s *bgWriteStore) DueLockTests(context.Context, time.Time) ([]db.LockTest, error) {
	if s.lockTestAttempts.Load() > 0 {
		return nil, nil
	}
	return []db.LockTest{{TestID: "lt_1", DeviceID: "dev1", DueAt: *s.lockTestDue}}, nil
}

func (s *bgWriteStore) AttemptLockTest(context.Context, string, int64, time.Time) (bool, error) {
	s.lockTestAttempts.Add(1)
	return true, nil
}

func (s *bgWriteStore) ClaimLiveActivityStart(context.Context, string, string, time.Time, time.Duration, time.Duration) (time.Time, bool, error) {
	s.laClaims.Add(1)
	return time.Time{}, true, nil
}

func (s *bgWriteStore) HoldLiveActivityStart(context.Context, string, time.Time, time.Duration) (bool, error) {
	s.laHolds.Add(1)
	return true, nil
}

func (s *bgWriteStore) SaveLiveActivity(context.Context, db.LiveActivity) error {
	s.laSaves.Add(1)
	return nil
}

func (s *bgWriteStore) CleanupClientOps(context.Context, time.Time) error {
	s.clientOpCleanups.Add(1)
	return nil
}

func (s *bgWriteStore) ReconcilePushDevices(context.Context, time.Time, time.Duration) ([]db.PushDeviceRetirement, error) {
	s.pushReconciles.Add(1)
	return nil, nil
}

// Finding #4: both failure branches of pauseDevelopmentWriters wake all
// deferred workers (ext reports, hello writers, device session finishes, and
// assistantGate) instead of only calling quiesce.resume().
func TestPauseDevelopmentWritersFailureWakesDeferredWorkers(t *testing.T) {
	for _, branch := range []string{"quiesce-wait-failure", "assistant-stop-failure"} {
		t.Run(branch, func(t *testing.T) {
			h := newTestHub(t)
			store := &deviceSessionWrites{meshStore: newMeshStore()}
			h.store = store
			h.startHelloWriters()
			defer h.stopHelloWriters()

			pauseCtx, pauseCancel := context.WithCancel(context.Background())
			defer pauseCancel()
			var done chan error
			var endOnce sync.Once
			endInFlight := func() { endOnce.Do(h.quiesce.end) }

			if branch == "quiesce-wait-failure" {
				// Hold an in-flight write so quiesce.pause sets paused=true and blocks.
				if !h.quiesce.begin() {
					t.Fatal("initial begin failed")
				}
				defer endInFlight()
				done = make(chan error, 1)
				go func() { done <- h.pauseDevelopmentWriters(pauseCtx) }()
				waitFor(t, "quiesce paused", func() bool {
					h.quiesce.mu.Lock()
					defer h.quiesce.mu.Unlock()
					return h.quiesce.paused
				})
			} else {
				// Register a stubborn child that never exits when cancelled.
				childCancelled := make(chan struct{})
				if !h.assistantProc.register(make(chan struct{}), func() { close(childCancelled) }) {
					t.Fatal("failed to register assistant child")
				}
				done = make(chan error, 1)
				go func() { done <- h.pauseDevelopmentWriters(pauseCtx) }()
				select {
				case <-childCancelled:
				case <-time.After(time.Second):
					t.Fatal("pauseDevelopmentWriters did not cancel assistant child")
				}
			}

			// Queue deferred writes across ext report, hello writer, and device session while paused.
			wantExt := extwire.Report{Features: []string{"after-failed-pause"}}
			h.queueExtReportSave(wantExt)
			h.updateClientApp("dev_hello", func(a assistantwire.ClientApp, _ bool) assistantwire.ClientApp {
				a.Platform = "ios"
				a.SeenAt = time.Now()
				return a
			})
			h.finishDeviceSession(42, time.Now(), "closed", 30)

			waitFor(t, "ext report deferred while paused", func() bool {
				h.ext.saveMu.Lock()
				defer h.ext.saveMu.Unlock()
				return h.ext.savePending != nil && !h.ext.saveRunning
			})

			// Abort the pause and wait for cancellation before ending the in-flight
			// write so writeQuiescer.pause cannot race between idle and ctx.Done.
			pauseCancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("pauseDevelopmentWriters error = %v, want context.Canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("pauseDevelopmentWriters did not return after cancel")
			}
			if branch == "quiesce-wait-failure" {
				endInFlight()
			}

			// Assistant gate must be open again on both branches.
			resumed := make(chan bool, 1)
			go func() { resumed <- h.assistantProc.waitResumed(context.Background()) }()
			select {
			case ok := <-resumed:
				if !ok {
					t.Fatal("assistantProc did not resume")
				}
			case <-time.After(time.Second):
				t.Fatal("assistantProc stayed paused after failed pauseDevelopmentWriters")
			}

			// Every deferred worker must be woken and persist its pending write.
			ctx := context.Background()
			waitFor(t, "deferred ext report written", func() bool {
				var got extwire.Report
				ok, err := store.Setting(ctx, extReportKey, &got)
				return err == nil && ok && slices.Equal(got.Features, wantExt.Features)
			})
			waitFor(t, "deferred hello client app written", func() bool {
				var got map[string]assistantwire.ClientApp
				ok, err := store.Setting(ctx, clientAppsSetting, &got)
				return err == nil && ok && got["dev_hello"].Platform == "ios"
			})
			waitFor(t, "deferred device session finish written", func() bool {
				store.mu.Lock()
				defer store.mu.Unlock()
				return store.finished == 1 && store.finishID == 42
			})
		})
	}
}

// Finding #5 (extensions_link.go): deterministic interleavings between
// saveExtReports observing pause / popping savePending and resumeDevelopmentWriters.
func TestExtReportSaveResumeInterleaving(t *testing.T) {
	t.Run("resume between pop and beginOrDefer", func(t *testing.T) {
		h := newTestHub(t)
		store := newMeshStore()
		h.store = store
		ctx := context.Background()
		if err := h.quiesce.pause(ctx); err != nil {
			t.Fatal(err)
		}
		reached := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		h.ext.saveBeforeBegin = func() {
			once.Do(func() {
				close(reached)
				<-release
			})
		}
		want := extwire.Report{Features: []string{"race-before-begin"}}
		h.queueExtReportSave(want)
		select {
		case <-reached:
		case <-time.After(time.Second):
			t.Fatal("saveExtReports did not reach saveBeforeBegin")
		}
		// Resume runs while savePending is nil and saveRunning is true.
		h.resumeDevelopmentWriters()
		close(release)
		waitFor(t, "report persisted without another report arriving", func() bool {
			var got extwire.Report
			ok, err := store.Setting(ctx, extReportKey, &got)
			return err == nil && ok && slices.Equal(got.Features, want.Features)
		})
	})

	t.Run("resume racing with paused beginOrDefer handoff", func(t *testing.T) {
		h := newTestHub(t)
		store := newMeshStore()
		h.store = store
		ctx := context.Background()
		if err := h.quiesce.pause(ctx); err != nil {
			t.Fatal(err)
		}
		inDefer := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		h.quiesce.testOnPauseDefer = func() {
			once.Do(func() {
				close(inDefer)
				<-release
			})
		}
		want := extwire.Report{Features: []string{"race-in-defer"}}
		h.queueExtReportSave(want)
		select {
		case <-inDefer:
		case <-time.After(time.Second):
			t.Fatal("saveExtReports did not enter paused beginOrDefer")
		}
		// Start resume while saveExtReports has observed paused==true and has
		// not yet restored savePending / cleared saveRunning.
		resumeDone := startResume(t, h)
		close(release)
		select {
		case <-resumeDone:
		case <-time.After(time.Second):
			t.Fatal("resumeDevelopmentWriters did not finish")
		}
		waitFor(t, "report persisted after synchronized resume", func() bool {
			var got extwire.Report
			ok, err := store.Setting(ctx, extReportKey, &got)
			return err == nil && ok && slices.Equal(got.Features, want.Features)
		})
	})
}

// Finding #5 (devicesessions.go): deterministic interleavings between
// finishDeviceSession and resumeDevelopmentWriters, preserving FIFO order.
func TestDeviceSessionFinishResumeInterleavingAndOrder(t *testing.T) {
	for _, point := range []string{"after-append", "in-beginOrDefer", "after-refuse"} {
		t.Run(point, func(t *testing.T) {
			h := newTestHub(t)
			store := &orderedDeviceSessionWrites{meshStore: newMeshStore()}
			h.store = store
			ctx := context.Background()
			if err := h.quiesce.pause(ctx); err != nil {
				t.Fatal(err)
			}
			reached := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			hook := func() {
				once.Do(func() {
					close(reached)
					<-release
				})
			}
			switch point {
			case "after-append":
				h.testAfterFinishAppend = hook
			case "in-beginOrDefer":
				h.quiesce.testOnPauseDefer = hook
			default:
				h.testAfterFinishRefuse = hook
			}

			finish1Done := make(chan struct{})
			go func() {
				h.finishDeviceSession(7, time.Now(), "first", 30)
				close(finish1Done)
			}()
			select {
			case <-reached:
			case <-time.After(time.Second):
				t.Fatalf("finishDeviceSession did not reach %s", point)
			}

			// Resume races right at the interleaving point, and a second session
			// ends right after resume starts.
			resumeDone := startResume(t, h)
			close(release)
			select {
			case <-finish1Done:
			case <-time.After(time.Second):
				t.Fatal("finishDeviceSession(7) did not return")
			}
			select {
			case <-resumeDone:
			case <-time.After(time.Second):
				t.Fatal("resumeDevelopmentWriters did not return")
			}

			h.finishDeviceSession(8, time.Now(), "second", 30)
			h.nodeWorkers.Wait()

			store.mu.Lock()
			got := slices.Clone(store.ids)
			store.mu.Unlock()
			if !slices.Equal(got, []int64{7, 8}) {
				t.Fatalf("finished session ids = %v, want [7 8]", got)
			}
		})
	}
}

type orderedDeviceSessionWrites struct {
	*meshStore
	ids []int64
}

func (s *orderedDeviceSessionWrites) FinishDeviceSession(_ context.Context, id int64, _ time.Time, _ string, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids = append(s.ids, id)
	return nil
}

type busSeenRecord struct {
	id    string
	where *db.BusIdentity
}

type busSeenRecordingStore struct {
	managerStore
	seenMu  sync.Mutex
	records []busSeenRecord
	latest  map[string]*db.BusIdentity
}

var _ managerStore = (*busSeenRecordingStore)(nil)

func (s *busSeenRecordingStore) BusIdentities(context.Context) ([]db.BusIdentity, error) {
	return nil, nil
}
func (s *busSeenRecordingStore) SeeBusIdentity(_ context.Context, id string, where *db.BusIdentity) error {
	var cp *db.BusIdentity
	if where != nil {
		v := *where
		cp = &v
	}
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	if s.latest == nil {
		s.latest = map[string]*db.BusIdentity{}
	}
	s.records = append(s.records, busSeenRecord{id: id, where: cp})
	s.latest[id] = cp
	return nil
}

// Bus roster updates (connect metadata and nil disconnects) deferred while paused
// are coalesced per identity and flushed on resume without regressing newer updates.
func TestDevelopmentPauseDefersBusSeenUpdates(t *testing.T) {
	h := newTestHub(t)
	store := &busSeenRecordingStore{}
	h.bus.idents = store
	ctx := context.Background()
	if err := h.quiesce.pause(ctx); err != nil {
		t.Fatal(err)
	}
	h.busSeen(ctx, "mgr-1", &db.BusIdentity{Host: "host-a", Version: "1.0"})
	h.busSeen(ctx, "mgr-1", &db.BusIdentity{Host: "host-b", Version: "2.0"})
	h.busSeen(ctx, "mgr-2", &db.BusIdentity{Host: "host-c", Version: "1.0"})
	h.busSeen(ctx, "mgr-2", nil) // disconnect

	store.seenMu.Lock()
	pausedCount := len(store.records)
	store.seenMu.Unlock()
	if pausedCount != 0 {
		t.Fatalf("SeeBusIdentity called %d times while paused", pausedCount)
	}

	h.resumeDevelopmentWriters()
	waitFor(t, "deferred busSeen updates flushed", func() bool {
		store.seenMu.Lock()
		defer store.seenMu.Unlock()
		_, ok1 := store.latest["mgr-1"]
		_, ok2 := store.latest["mgr-2"]
		return ok1 && ok2
	})
	store.seenMu.Lock()
	got1 := store.latest["mgr-1"]
	got2 := store.latest["mgr-2"]
	store.seenMu.Unlock()
	if got1 == nil || got1.Host != "host-b" || got1.Version != "2.0" {
		t.Fatalf("mgr-1 persisted where = %+v, want host-b 2.0", got1)
	}
	if got2 != nil {
		t.Fatalf("mgr-2 persisted where = %+v, want nil disconnect", got2)
	}

	// Ordering race: a newer post-resume disconnect is never overwritten by a stale deferred connect.
	if err := h.quiesce.pause(ctx); err != nil {
		t.Fatal(err)
	}
	h.busSeen(ctx, "mgr-3", &db.BusIdentity{Host: "stale-host"})
	h.bus.seenMu.Lock()
	entry := h.bus.seen["mgr-3"]
	h.bus.seenMu.Unlock()
	entry.mu.Lock()
	h.resumeDevelopmentWriters()
	newerDone := make(chan struct{})
	go func() {
		h.busSeen(ctx, "mgr-3", nil)
		close(newerDone)
	}()
	waitFor(t, "newer busSeen queued", func() bool {
		h.bus.seenMu.Lock()
		defer h.bus.seenMu.Unlock()
		return entry.gen >= 2
	})
	entry.mu.Unlock()
	select {
	case <-newerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("newer busSeen did not finish")
	}
	h.nodeWorkers.Wait()
	store.seenMu.Lock()
	got3, ok3 := store.latest["mgr-3"]
	store.seenMu.Unlock()
	if !ok3 || got3 != nil {
		t.Fatalf("mgr-3 persisted where = %+v (ok=%v), want nil", got3, ok3)
	}
}

type busSeenCtxStore struct {
	managerStore
	mu       sync.Mutex
	latest   map[string]*db.BusIdentity
	onSee    func(ctx context.Context, id string, where *db.BusIdentity) error
	calls    int
	lastHost string
}

var _ managerStore = (*busSeenCtxStore)(nil)

func (s *busSeenCtxStore) BusIdentities(context.Context) ([]db.BusIdentity, error) {
	return nil, nil
}

func (s *busSeenCtxStore) SeeBusIdentity(ctx context.Context, id string, where *db.BusIdentity) error {
	if s.onSee != nil {
		if err := s.onSee(ctx, id, where); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var cp *db.BusIdentity
	if where != nil {
		v := *where
		cp = &v
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.latest == nil {
		s.latest = map[string]*db.BusIdentity{}
	}
	s.latest[id] = cp
	if cp != nil {
		s.lastHost = cp.Host
	} else {
		s.lastHost = ""
	}
	return nil
}

// Coalesced trailing busSeen updates must not inherit an earlier caller's
// canceled or nearly expired context; they run with a fresh bounded context so
// a reconnect queued behind a disconnect is never dropped.
func TestBusSeenCoalescedTrailingWriteUsesFreshContext(t *testing.T) {
	type callerTagKey struct{}
	for _, tc := range []string{"canceled-first-caller", "nearly-expired-first-caller"} {
		t.Run(tc, func(t *testing.T) {
			h := newTestHub(t)
			inFirst := make(chan struct{})
			releaseFirst := make(chan struct{})
			var firstOnce sync.Once
			var trailingInheritedCaller bool
			var trailingCtxErr error
			var trailingRemaining time.Duration
			var trailingSawDeadline bool

			store := &busSeenCtxStore{
				onSee: func(ctx context.Context, _ string, where *db.BusIdentity) error {
					if where == nil {
						firstOnce.Do(func() {
							close(inFirst)
							<-releaseFirst
						})
						return ctx.Err()
					}
					trailingInheritedCaller = ctx.Value(callerTagKey{}) != nil
					trailingCtxErr = ctx.Err()
					if dl, ok := ctx.Deadline(); ok {
						trailingSawDeadline = true
						trailingRemaining = time.Until(dl)
					}
					return ctx.Err()
				},
			}
			h.bus.idents = store

			baseCtx := context.WithValue(context.Background(), callerTagKey{}, true)
			var firstCtx context.Context
			var cancelFirst context.CancelFunc
			if tc == "canceled-first-caller" {
				firstCtx, cancelFirst = context.WithCancel(baseCtx)
			} else {
				firstCtx, cancelFirst = context.WithTimeout(baseCtx, 250*time.Millisecond)
			}
			defer cancelFirst()

			firstDone := make(chan struct{})
			go func() {
				h.busSeen(firstCtx, "mgr-coal", nil)
				close(firstDone)
			}()
			select {
			case <-inFirst:
			case <-time.After(time.Second):
				t.Fatal("first busSeen write did not start")
			}

			secondDone := make(chan struct{})
			go func() {
				h.busSeen(context.Background(), "mgr-coal", &db.BusIdentity{Host: "reconnected", Version: "2.0"})
				close(secondDone)
			}()
			waitFor(t, "second busSeen coalesced behind first write", func() bool {
				h.bus.seenMu.Lock()
				defer h.bus.seenMu.Unlock()
				e := h.bus.seen["mgr-coal"]
				return e != nil && e.gen >= 2
			})

			if tc == "canceled-first-caller" {
				cancelFirst()
			}
			close(releaseFirst)

			select {
			case <-firstDone:
			case <-time.After(2 * time.Second):
				t.Fatal("first busSeen did not return")
			}
			select {
			case <-secondDone:
			case <-time.After(2 * time.Second):
				t.Fatal("second busSeen did not return")
			}

			if trailingInheritedCaller {
				t.Fatal("coalesced trailing busSeen inherited first caller's context")
			}
			if trailingCtxErr != nil {
				t.Fatalf("coalesced trailing busSeen saw ctx.Err() = %v, want nil", trailingCtxErr)
			}
			if !trailingSawDeadline || trailingRemaining < time.Second {
				t.Fatalf("coalesced trailing busSeen deadline ok=%v remaining=%v, want fresh bounded timeout", trailingSawDeadline, trailingRemaining)
			}
			store.mu.Lock()
			got := store.latest["mgr-coal"]
			store.mu.Unlock()
			if got == nil || got.Host != "reconnected" || got.Version != "2.0" {
				t.Fatalf("persisted mgr-coal = %+v, want host=reconnected version=2.0", got)
			}
		})
	}
}

type hookedSettingsStore struct {
	*meshStore
	hookMu sync.Mutex
	onPut  func(key string, v any)
	onSave func(v db.LiveActivity)
	laLast db.LiveActivity
	laSeq  []string
}

func (s *hookedSettingsStore) PutSetting(ctx context.Context, key string, v any) error {
	s.hookMu.Lock()
	fn := s.onPut
	s.hookMu.Unlock()
	if fn != nil {
		fn(key, v)
	}
	return s.meshStore.PutSetting(ctx, key, v)
}

func (s *hookedSettingsStore) SaveLiveActivity(_ context.Context, v db.LiveActivity) error {
	s.hookMu.Lock()
	fn := s.onSave
	s.hookMu.Unlock()
	if fn != nil {
		fn(v)
	}
	s.hookMu.Lock()
	s.laLast = v
	s.laSeq = append(s.laSeq, v.PushToken)
	s.hookMu.Unlock()
	return nil
}

// Deferred Codex inventory writes cannot overwrite a newer post-resume report.
func TestResumedCodexInventoryCannotOverwriteNewerReport(t *testing.T) {
	t.Run("newer report arrives while resumed write is in flight", func(t *testing.T) {
		h := newTestHub(t)
		store := &hookedSettingsStore{meshStore: newMeshStore()}
		h.store = store
		m := newMachine(h, MachineConfig{ID: "box"})
		oldRaw, _ := json.Marshal(proto.CodexConfig{LegacyHome: "/old-home"})
		newRaw, _ := json.Marshal(proto.CodexConfig{LegacyHome: "/new-home"})

		ctx := context.Background()
		if err := h.quiesce.pause(ctx); err != nil {
			t.Fatal(err)
		}
		m.rememberCodexInventory(oldRaw)

		inOldPut := make(chan struct{})
		releaseOld := make(chan struct{})
		var once sync.Once
		store.onPut = func(key string, v any) {
			if cfg, ok := v.(proto.CodexConfig); ok && cfg.LegacyHome == "/old-home" {
				once.Do(func() {
					close(inOldPut)
					<-releaseOld
				})
			}
		}

		h.resumeDevelopmentWriters()
		select {
		case <-inOldPut:
		case <-time.After(2 * time.Second):
			t.Fatal("resumed Codex inventory write did not start")
		}
		newerDone := make(chan struct{})
		go func() {
			m.rememberCodexInventory(newRaw)
			close(newerDone)
		}()
		waitFor(t, "newer Codex inventory queued", func() bool {
			h.codexPendingMu.Lock()
			defer h.codexPendingMu.Unlock()
			return h.codexPending["box"].gen >= 2
		})
		close(releaseOld)
		select {
		case <-newerDone:
		case <-time.After(2 * time.Second):
			t.Fatal("newer Codex inventory did not finish")
		}
		h.nodeWorkers.Wait()
		var got proto.CodexConfig
		if ok, err := store.Setting(ctx, codexInventoryKey("box"), &got); err != nil || !ok || got.LegacyHome != "/new-home" {
			t.Fatalf("stored Codex inventory = %+v (ok=%v err=%v), want /new-home", got, ok, err)
		}
	})

	t.Run("newer report arrives before resumed worker acquires write lock", func(t *testing.T) {
		h := newTestHub(t)
		store := &hookedSettingsStore{meshStore: newMeshStore()}
		h.store = store
		m := newMachine(h, MachineConfig{ID: "box"})
		oldRaw, _ := json.Marshal(proto.CodexConfig{LegacyHome: "/old-home"})
		newRaw, _ := json.Marshal(proto.CodexConfig{LegacyHome: "/new-home"})

		ctx := context.Background()
		if err := h.quiesce.pause(ctx); err != nil {
			t.Fatal(err)
		}
		m.rememberCodexInventory(oldRaw)
		h.codexPendingMu.Lock()
		entry := h.codexPending["box"]
		h.codexPendingMu.Unlock()

		entry.mu.Lock()
		h.resumeDevelopmentWriters()
		newerDone := make(chan struct{})
		go func() {
			m.rememberCodexInventory(newRaw)
			close(newerDone)
		}()
		waitFor(t, "newer Codex inventory queued", func() bool {
			h.codexPendingMu.Lock()
			defer h.codexPendingMu.Unlock()
			return entry.gen >= 2
		})
		entry.mu.Unlock()
		select {
		case <-newerDone:
		case <-time.After(2 * time.Second):
			t.Fatal("newer Codex inventory did not finish")
		}
		h.nodeWorkers.Wait()
		var got proto.CodexConfig
		if ok, err := store.Setting(ctx, codexInventoryKey("box"), &got); err != nil || !ok || got.LegacyHome != "/new-home" {
			t.Fatalf("stored Codex inventory = %+v (ok=%v err=%v), want /new-home", got, ok, err)
		}
	})
}

// Deferred assistant profile saves cannot overwrite a newer post-resume profile.
func TestResumedAssistantProfileCannotOverwriteNewerReport(t *testing.T) {
	t.Run("newer profile arrives while resumed write is in flight", func(t *testing.T) {
		h := newTestHub(t)
		store := &hookedSettingsStore{meshStore: newMeshStore()}
		h.store = store
		ctx := context.Background()
		if err := h.quiesce.pause(ctx); err != nil {
			t.Fatal(err)
		}
		h.keepAssistantProfile(ctx, &assistantwire.Profile{Timezone: "UTC", Main: "old-main"})

		inOldPut := make(chan struct{})
		releaseOld := make(chan struct{})
		var once sync.Once
		store.onPut = func(key string, v any) {
			if p, ok := v.(*assistantwire.Profile); ok && p.Main == "old-main" {
				once.Do(func() {
					close(inOldPut)
					<-releaseOld
				})
			}
		}

		h.resumeDevelopmentWriters()
		select {
		case <-inOldPut:
		case <-time.After(2 * time.Second):
			t.Fatal("resumed assistant profile write did not start")
		}
		newerDone := make(chan struct{})
		go func() {
			h.keepAssistantProfile(ctx, &assistantwire.Profile{Timezone: "Asia/Tokyo", Main: "new-main"})
			close(newerDone)
		}()
		waitFor(t, "newer assistant profile queued", func() bool {
			h.assistantProfileSave.mu.Lock()
			defer h.assistantProfileSave.mu.Unlock()
			return h.assistantProfileSave.gen >= 2
		})
		close(releaseOld)
		select {
		case <-newerDone:
		case <-time.After(2 * time.Second):
			t.Fatal("newer assistant profile did not finish")
		}
		h.nodeWorkers.Wait()
		var got assistantwire.Profile
		if ok, err := store.Setting(ctx, assistantProfileKey, &got); err != nil || !ok || got.Main != "new-main" || got.Timezone != "Asia/Tokyo" {
			t.Fatalf("stored assistant profile = %+v (ok=%v err=%v), want new-main/Asia/Tokyo", got, ok, err)
		}
	})

	t.Run("newer profile arrives before resumed worker acquires write lock", func(t *testing.T) {
		h := newTestHub(t)
		store := &hookedSettingsStore{meshStore: newMeshStore()}
		h.store = store
		ctx := context.Background()
		if err := h.quiesce.pause(ctx); err != nil {
			t.Fatal(err)
		}
		h.keepAssistantProfile(ctx, &assistantwire.Profile{Timezone: "UTC", Main: "old-main"})

		h.assistantProfileSave.writeMu.Lock()
		h.resumeDevelopmentWriters()
		newerDone := make(chan struct{})
		go func() {
			h.keepAssistantProfile(ctx, &assistantwire.Profile{Timezone: "Asia/Tokyo", Main: "new-main"})
			close(newerDone)
		}()
		waitFor(t, "newer assistant profile queued", func() bool {
			h.assistantProfileSave.mu.Lock()
			defer h.assistantProfileSave.mu.Unlock()
			return h.assistantProfileSave.gen >= 2
		})
		h.assistantProfileSave.writeMu.Unlock()
		select {
		case <-newerDone:
		case <-time.After(2 * time.Second):
			t.Fatal("newer assistant profile did not finish")
		}
		h.nodeWorkers.Wait()
		var got assistantwire.Profile
		if ok, err := store.Setting(ctx, assistantProfileKey, &got); err != nil || !ok || got.Main != "new-main" || got.Timezone != "Asia/Tokyo" {
			t.Fatalf("stored assistant profile = %+v (ok=%v err=%v), want new-main/Asia/Tokyo", got, ok, err)
		}
	})
}

// Deferred Live Activity saves cannot overwrite newer post-resume updateContext state.
func TestResumedLiveActivitySaveCannotOverwriteNewerUpdate(t *testing.T) {
	t.Run("newer update arrives while resumed save is in flight", func(t *testing.T) {
		h := newTestHub(t)
		store := &hookedSettingsStore{meshStore: newMeshStore()}
		h.store = store
		la := h.live
		ctx := context.Background()
		if err := h.quiesce.pause(ctx); err != nil {
			t.Fatal(err)
		}
		if err := la.updateContext(ctx, "dev1", func(v *db.LiveActivity) { v.PushToken = "old-tok" }); err != nil {
			t.Fatal(err)
		}

		inOldSave := make(chan struct{})
		releaseOld := make(chan struct{})
		var once sync.Once
		store.onSave = func(v db.LiveActivity) {
			if v.PushToken == "old-tok" {
				once.Do(func() {
					close(inOldSave)
					<-releaseOld
				})
			}
		}

		h.resumeDevelopmentWriters()
		select {
		case <-inOldSave:
		case <-time.After(2 * time.Second):
			t.Fatal("resumed Live Activity save did not start")
		}
		newerDone := make(chan error, 1)
		go func() {
			newerDone <- la.updateContext(ctx, "dev1", func(v *db.LiveActivity) { v.PushToken = "new-tok" })
		}()
		waitFor(t, "newer Live Activity update queued", func() bool {
			la.mu.Lock()
			defer la.mu.Unlock()
			return la.rowGen["dev1"] >= 2
		})
		close(releaseOld)
		select {
		case err := <-newerDone:
			if err != nil {
				t.Fatalf("newer updateContext error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("newer updateContext did not finish")
		}
		h.nodeWorkers.Wait()
		store.hookMu.Lock()
		got := store.laLast.PushToken
		seq := slices.Clone(store.laSeq)
		store.hookMu.Unlock()
		if got != "new-tok" {
			t.Fatalf("final persisted Live Activity token = %q (seq=%v), want new-tok", got, seq)
		}
	})

	t.Run("newer update arrives before resumed worker acquires device lock", func(t *testing.T) {
		h := newTestHub(t)
		store := &hookedSettingsStore{meshStore: newMeshStore()}
		h.store = store
		la := h.live
		ctx := context.Background()
		if err := h.quiesce.pause(ctx); err != nil {
			t.Fatal(err)
		}
		if err := la.updateContext(ctx, "dev1", func(v *db.LiveActivity) { v.PushToken = "old-tok" }); err != nil {
			t.Fatal(err)
		}
		la.mu.Lock()
		devMu := la.deviceSaveMuLocked("dev1")
		la.mu.Unlock()

		devMu.Lock()
		h.resumeDevelopmentWriters()
		newerDone := make(chan error, 1)
		go func() {
			newerDone <- la.updateContext(ctx, "dev1", func(v *db.LiveActivity) { v.PushToken = "new-tok" })
		}()
		waitFor(t, "newer Live Activity update queued", func() bool {
			la.mu.Lock()
			defer la.mu.Unlock()
			return la.rowGen["dev1"] >= 2
		})
		devMu.Unlock()
		select {
		case err := <-newerDone:
			if err != nil {
				t.Fatalf("newer updateContext error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("newer updateContext did not finish")
		}
		h.nodeWorkers.Wait()
		store.hookMu.Lock()
		got := store.laLast.PushToken
		seq := slices.Clone(store.laSeq)
		store.hookMu.Unlock()
		if got != "new-tok" {
			t.Fatalf("final persisted Live Activity token = %q (seq=%v), want new-tok", got, seq)
		}
	})
}

// Resumed persona tier snapshots cannot overwrite newer post-resume tier state.
func TestResumedPersonaTiersCannotOverwriteNewerState(t *testing.T) {
	t.Run("newer tier arrives while resumed write is in flight", func(t *testing.T) {
		h := newTestHub(t)
		store := &hookedSettingsStore{meshStore: newMeshStore()}
		h.store = store
		ctx := context.Background()
		if err := h.quiesce.pause(ctx); err != nil {
			t.Fatal(err)
		}
		tiers := &personaTiers{Default: "base"}
		h.setPersonaTier(ctx, "pkg", tiers, "egg", "alice")

		inOldPut := make(chan struct{})
		releaseOld := make(chan struct{})
		var once sync.Once
		store.onPut = func(key string, v any) {
			if m, ok := v.(map[string]tierSeen); ok && m["pkg"].Tier == "egg" {
				once.Do(func() {
					close(inOldPut)
					<-releaseOld
				})
			}
		}

		h.resumeDevelopmentWriters()
		select {
		case <-inOldPut:
		case <-time.After(2 * time.Second):
			t.Fatal("resumed persona tiers write did not start")
		}
		newerDone := make(chan struct{})
		go func() {
			h.setPersonaTier(ctx, "pkg", tiers, "cosmic", "alice")
			close(newerDone)
		}()
		waitFor(t, "newer persona tier queued", func() bool {
			h.tiers.mu.Lock()
			defer h.tiers.mu.Unlock()
			return h.tiers.saveGen >= 2
		})
		close(releaseOld)
		select {
		case <-newerDone:
		case <-time.After(2 * time.Second):
			t.Fatal("newer setPersonaTier did not finish")
		}
		h.nodeWorkers.Wait()
		var got map[string]tierSeen
		if ok, err := store.Setting(ctx, personaTiersKey, &got); err != nil || !ok || got["pkg"].Tier != "cosmic" {
			t.Fatalf("stored persona tiers = %+v (ok=%v err=%v), want cosmic", got, ok, err)
		}
	})

	t.Run("newer tier arrives before resumed worker acquires write lock", func(t *testing.T) {
		h := newTestHub(t)
		store := &hookedSettingsStore{meshStore: newMeshStore()}
		h.store = store
		ctx := context.Background()
		if err := h.quiesce.pause(ctx); err != nil {
			t.Fatal(err)
		}
		tiers := &personaTiers{Default: "base"}
		h.setPersonaTier(ctx, "pkg", tiers, "egg", "alice")

		h.tiers.writeMu.Lock()
		h.resumeDevelopmentWriters()
		newerDone := make(chan struct{})
		go func() {
			h.setPersonaTier(ctx, "pkg", tiers, "cosmic", "alice")
			close(newerDone)
		}()
		waitFor(t, "newer persona tier queued", func() bool {
			h.tiers.mu.Lock()
			defer h.tiers.mu.Unlock()
			return h.tiers.saveGen >= 2
		})
		h.tiers.writeMu.Unlock()
		select {
		case <-newerDone:
		case <-time.After(2 * time.Second):
			t.Fatal("newer setPersonaTier did not finish")
		}
		h.nodeWorkers.Wait()
		var got map[string]tierSeen
		if ok, err := store.Setting(ctx, personaTiersKey, &got); err != nil || !ok || got["pkg"].Tier != "cosmic" {
			t.Fatalf("stored persona tiers = %+v (ok=%v err=%v), want cosmic", got, ok, err)
		}
	})
}

type liveActivityOrderStore struct {
	*meshStore
	mu          sync.Mutex
	events      []string
	heldUntil   map[string]time.Time
	holdEntered chan struct{}
	releaseHold chan struct{}
	holdOnce    sync.Once
}

func (s *liveActivityOrderStore) HoldLiveActivityStart(_ context.Context, device string, asOf time.Time, hold time.Duration) (bool, error) {
	if s.holdEntered != nil {
		s.holdOnce.Do(func() {
			close(s.holdEntered)
			<-s.releaseHold
		})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "hold")
	if s.heldUntil == nil {
		s.heldUntil = map[string]time.Time{}
	}
	s.heldUntil[device] = asOf.Add(hold)
	return true, nil
}

func (s *liveActivityOrderStore) ClaimLiveActivityStart(_ context.Context, device, _ string, now time.Time, _, _ time.Duration) (time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "claim")
	if due := s.heldUntil[device]; now.Before(due) {
		return due, false, nil
	}
	return time.Time{}, true, nil
}

// On resume, deferred Live Activity holds are persisted before the repair pass
// (or any concurrent start claim) can call ClaimLiveActivityStart, preventing a
// duplicate activity when a snapshot outlasts the rolling fleetRefresh cooldown.
func TestLiveActivityResumeReplaysDeferredHoldBeforeStartClaim(t *testing.T) {
	h := newTestHub(t)
	startAt := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	afterSnapshot := startAt.Add(fleetRefresh + time.Minute)
	store := &liveActivityOrderStore{
		meshStore:   newMeshStore(),
		holdEntered: make(chan struct{}),
		releaseHold: make(chan struct{}),
	}
	h.store = store
	la := h.live
	la.now = func() time.Time { return afterSnapshot }
	row := db.LiveActivity{DeviceID: "dev1", PushToStartToken: "start-tok"}

	ctx := context.Background()
	if err := h.quiesce.pause(ctx); err != nil {
		t.Fatal(err)
	}
	// Defer both a fleetMaxAge hold and a start claim while paused.
	la.holdStart(context.Background(), row.DeviceID, startAt, fleetMaxAge)
	if _, claimed := la.claimStart(row, afterSnapshot); claimed {
		t.Fatal("claimStart succeeded while paused")
	}
	store.mu.Lock()
	pausedEvents := len(store.events)
	store.mu.Unlock()
	if pausedEvents != 0 {
		t.Fatalf("live activity store called %d times while paused", pausedEvents)
	}

	// Resume: hold replay starts and blocks on releaseHold. Neither la.repair nor
	// la.signal may wake start claims until the deferred hold finishes persisting,
	// and a concurrent claimStart must also wait for the hold to commit.
	h.resumeDevelopmentWriters()
	select {
	case <-store.holdEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("deferred hold replay did not start on resume")
	}
	la.mu.Lock()
	repairBeforeHold := la.repair
	la.mu.Unlock()
	if repairBeforeHold {
		t.Fatal("la.repair was set before deferred holds persisted")
	}
	select {
	case <-la.signal:
		t.Fatal("la.signal woke repair flush before deferred holds persisted")
	case <-time.After(25 * time.Millisecond):
	}

	// Even an unrelated concurrent claimStart while the hold is still in flight
	// waits for the hold to commit before consulting ClaimLiveActivityStart.
	claimRes := make(chan bool, 1)
	go func() {
		_, claimed := la.claimStart(row, afterSnapshot)
		claimRes <- claimed
	}()
	select {
	case claimed := <-claimRes:
		t.Fatalf("concurrent claimStart returned (claimed=%v) before deferred hold persisted", claimed)
	case <-time.After(25 * time.Millisecond):
	}

	close(store.releaseHold)
	select {
	case claimed := <-claimRes:
		if claimed {
			t.Fatal("claimStart bypassed persisted fleetMaxAge hold and started a duplicate activity")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("claimStart did not finish after hold persisted")
	}
	h.nodeWorkers.Wait()

	// Repair wake is delivered once the hold replay finishes.
	la.mu.Lock()
	repairAfterHold := la.repair
	la.mu.Unlock()
	if !repairAfterHold {
		t.Fatal("la.repair was not set after deferred holds persisted")
	}
	select {
	case <-la.signal:
	default:
		t.Fatal("la.signal was not signaled after deferred holds persisted")
	}

	store.mu.Lock()
	gotEvents := slices.Clone(store.events)
	store.mu.Unlock()
	if !slices.Equal(gotEvents, []string{"hold", "claim"}) {
		t.Fatalf("live activity resume event order = %v, want [hold claim]", gotEvents)
	}
}

// When the hub's first Web Push VAPID key has not yet been created and a
// background dispatch arrives during a snapshot pause, key creation and push
// delivery are deferred without noting a failure, then completed on resume.
func TestWebPushDefersFirstKeyCreationAndDispatchUntilResume(t *testing.T) {
	for _, raceResume := range []bool{false, true} {
		name := "pause-then-resume"
		if raceResume {
			name = "resume-racing-beginOrDefer"
		}
		t.Run(name, func(t *testing.T) {
			f := newFakePushService(t)
			h := webPushHub(t, f)
			store := &bgWriteStore{meshStore: newMeshStore()}
			h.store = store
			w := h.push.web
			w.store = store
			w.quiesce = &h.quiesce

			b := f.browser(t, "/push/first")
			sub := webSub{Device: "web-first", Endpoint: b.Sub.Endpoint, P256DH: b.Sub.P256DH, Auth: b.Sub.Auth}
			if err := w.subscribe(sub); err != nil {
				t.Fatal(err)
			}

			var sentMu sync.Mutex
			var sentErrs []error
			w.sent = func(_ string, _ int, err error) {
				sentMu.Lock()
				sentErrs = append(sentErrs, err)
				sentMu.Unlock()
			}

			ctx := context.Background()
			if err := h.quiesce.pause(ctx); err != nil {
				t.Fatal(err)
			}

			payload := []byte(`{"v":1,"kind":"alert","title":"Deferred first push"}`)
			msg := webpush.Message{TTL: webPushTTL, Urgency: webpush.UrgencyHigh}

			if raceResume {
				inDefer := make(chan struct{})
				release := make(chan struct{})
				var once sync.Once
				h.quiesce.testOnPauseDefer = func() {
					once.Do(func() {
						close(inDefer)
						<-release
					})
				}
				w.dispatch(sub, payload, msg)
				select {
				case <-inDefer:
				case <-time.After(time.Second):
					t.Fatal("webpush dispatch did not enter paused beginOrDefer")
				}
				resumeDone := startResume(t, h)
				close(release)
				select {
				case <-resumeDone:
				case <-time.After(time.Second):
					t.Fatal("resumeDevelopmentWriters did not finish")
				}
			} else {
				w.dispatch(sub, payload, msg)
				waitFor(t, "first push queued while paused", func() bool {
					w.mu.Lock()
					defer w.mu.Unlock()
					return len(w.pendingPushes) == 1
				})
				if store.settingWrites(webPushKeysSetting) != 0 {
					t.Fatal("VAPID key was written while paused")
				}
				if d := w.delivery(sub.Device); d.FailedAt != nil || d.Error != "" {
					t.Fatalf("paused deferral noted a delivery failure: %+v", d)
				}
				sentMu.Lock()
				if len(sentErrs) != 0 {
					sentMu.Unlock()
					t.Fatalf("paused deferral invoked sent callback: %v", sentErrs)
				}
				sentMu.Unlock()
				f.none(t)

				h.resumeDevelopmentWriters()
			}

			r := f.next(t)
			got := webOpened(t, b, r)
			if got["title"] != "Deferred first push" {
				t.Fatalf("delivered push = %v, want Deferred first push", got)
			}
			if store.settingWrites(webPushKeysSetting) != 1 {
				t.Fatalf("VAPID key setting writes = %d, want 1", store.settingWrites(webPushKeysSetting))
			}
			waitFor(t, "delivery noted as OK and sent callback invoked after resume", func() bool {
				d := w.delivery(sub.Device)
				sentMu.Lock()
				n := len(sentErrs)
				sentMu.Unlock()
				return d.OKAt != nil && d.FailedAt == nil && d.Error == "" && n >= 1
			})
			sentMu.Lock()
			if len(sentErrs) != 1 || sentErrs[0] != nil {
				sentMu.Unlock()
				t.Fatalf("sent callbacks = %v, want [nil]", sentErrs)
			}
			sentMu.Unlock()
		})
	}
}

// A newer Web Push dispatch arriving after quiesce.resume() admits writes, but
// before resumeWrites() replays an older deferred push for the same
// subscription, stays chained behind the deferred queue entry and delivers second.
func TestWebPushDeferredPushNotOvertakenByPostResumeDispatch(t *testing.T) {
	f := newFakePushService(t)
	h := webPushHub(t, f)
	store := &bgWriteStore{meshStore: newMeshStore()}
	h.store = store
	w := h.push.web
	w.store = store
	w.quiesce = &h.quiesce

	b := f.browser(t, "/push/order")
	sub := webSub{Device: "web-order", Endpoint: b.Sub.Endpoint, P256DH: b.Sub.P256DH, Auth: b.Sub.Auth}
	if err := w.subscribe(sub); err != nil {
		t.Fatal(err)
	}

	if err := h.quiesce.pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	msg := webpush.Message{TTL: webPushTTL, Urgency: webpush.UrgencyHigh}
	w.dispatch(sub, []byte(`{"v":1,"kind":"alert","title":"First deferred"}`), msg)
	waitFor(t, "first push deferred while paused", func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return len(w.pendingPushes) == 1
	})

	// Resume the quiescer (admitting new writes) before resumeWrites replays the
	// deferred push, then dispatch a newer push to the same subscription.
	h.quiesce.resume()
	w.dispatch(sub, []byte(`{"v":1,"kind":"alert","title":"Second post-resume"}`), msg)
	// The newer push must remain queued behind the deferred push's open done channel.
	f.none(t)

	h.push.resumeWrites()

	first := webOpened(t, b, f.next(t))
	second := webOpened(t, b, f.next(t))
	if first["title"] != "First deferred" || second["title"] != "Second post-resume" {
		t.Fatalf("delivery order = [%v, %v], want [First deferred, Second post-resume]", first["title"], second["title"])
	}
	waitFor(t, "subscription queue tail cleaned up", func() bool {
		w.queueMu.Lock()
		defer w.queueMu.Unlock()
		return len(w.tails) == 0
	})
}

type hookedDeviceStore struct {
	*meshStore
	upserts  atomic.Int32
	onUpsert func(db.Device)
}

func (s *hookedDeviceStore) UpsertDevice(_ context.Context, d db.Device) error {
	s.upserts.Add(1)
	if s.onUpsert != nil {
		s.onUpsert(d)
	}
	return nil
}

// Non-admitted device registrations obey writeQuiescer (refused while paused,
// counted as in-flight while running), while an HTTP registration already
// admitted through quiesceGate skips nested begin and holds its turn until done.
func TestPushRegisterRespectsQuiesceAndAdmittedContext(t *testing.T) {
	t.Run("direct register refused while paused", func(t *testing.T) {
		h := newTestHub(t)
		store := &hookedDeviceStore{meshStore: newMeshStore()}
		h.store = store
		h.push.store = store
		if err := h.quiesce.pause(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer h.quiesce.resume()

		if err := h.push.register(Device{DeviceID: "dev-paused", Name: "Phone"}); err == nil {
			t.Fatal("direct register succeeded while writes were paused")
		}
		if n := store.upserts.Load(); n != 0 {
			t.Fatalf("UpsertDevice ran %d times while paused", n)
		}
		if _, ok := h.push.device("dev-paused"); ok {
			t.Fatal("refused registration was published in memory")
		}
	})

	t.Run("in-flight direct register blocks pause until commit", func(t *testing.T) {
		h := newTestHub(t)
		inUpsert := make(chan struct{})
		release := make(chan struct{})
		store := &hookedDeviceStore{
			meshStore: newMeshStore(),
			onUpsert: func(db.Device) {
				close(inUpsert)
				<-release
			},
		}
		h.store = store
		h.push.store = store

		regErr := make(chan error, 1)
		go func() {
			regErr <- h.push.register(Device{DeviceID: "dev-inflight", Name: "Phone"})
		}()
		select {
		case <-inUpsert:
		case <-time.After(time.Second):
			t.Fatal("direct register did not reach UpsertDevice")
		}

		paused := make(chan error, 1)
		go func() { paused <- h.quiesce.pause(context.Background()) }()
		select {
		case err := <-paused:
			t.Fatalf("pause returned while direct register was in flight: %v", err)
		case <-time.After(30 * time.Millisecond):
		}

		close(release)
		if err := <-regErr; err != nil {
			t.Fatalf("direct register error: %v", err)
		}
		select {
		case err := <-paused:
			if err != nil {
				t.Fatalf("pause error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("pause did not return after direct register finished")
		}
		h.quiesce.resume()
	})

	t.Run("admitted HTTP register skips nested begin while pause waits", func(t *testing.T) {
		h := newTestHub(t)
		store := &hookedDeviceStore{meshStore: newMeshStore()}
		h.store = store
		h.push.store = store

		mux := http.NewServeMux()
		h.deviceRoutes(mux)
		admitted := make(chan struct{})
		proceed := make(chan struct{})
		handler := h.quiesceGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(admitted)
			<-proceed
			mux.ServeHTTP(w, r)
		}))

		req := httptest.NewRequest(http.MethodPost, "/api/v1/devices", strings.NewReader(`{"device_id":"dev-http","name":"Phone"}`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(context.WithValue(req.Context(), principalKey{}, principal{ID: "cred_1", Role: roleFull}))
		rec := httptest.NewRecorder()

		reqDone := make(chan struct{})
		go func() {
			handler.ServeHTTP(rec, req)
			close(reqDone)
		}()
		select {
		case <-admitted:
		case <-time.After(time.Second):
			t.Fatal("HTTP register did not enter quiesceGate")
		}

		// Start pause after quiesceGate admitted the request, before registerContext runs.
		paused := make(chan error, 1)
		go func() { paused <- h.quiesce.pause(context.Background()) }()
		waitFor(t, "quiesce marked paused", func() bool {
			h.quiesce.mu.Lock()
			defer h.quiesce.mu.Unlock()
			return h.quiesce.paused
		})
		select {
		case err := <-paused:
			t.Fatalf("pause returned before admitted HTTP register finished: %v", err)
		case <-time.After(25 * time.Millisecond):
		}

		// Admitted request must not fail with a nested quiesce refusal.
		close(proceed)
		select {
		case <-reqDone:
		case <-time.After(time.Second):
			t.Fatal("admitted HTTP register did not finish")
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("admitted HTTP register status = %d: %s", rec.Code, rec.Body.String())
		}
		if store.upserts.Load() != 1 {
			t.Fatalf("UpsertDevice count = %d, want 1", store.upserts.Load())
		}
		select {
		case err := <-paused:
			if err != nil {
				t.Fatalf("pause error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("pause did not return after admitted HTTP register finished")
		}
		h.quiesce.resume()
	})
}

type blockingDeviceSessionStore struct {
	*meshStore
	mu            sync.Mutex
	ids           []int64
	active        int
	maxConcurrent int
	onFinish      func(id int64)
}

func (s *blockingDeviceSessionStore) FinishDeviceSession(_ context.Context, id int64, _ time.Time, _ string, _ int) error {
	s.mu.Lock()
	s.active++
	if s.active > s.maxConcurrent {
		s.maxConcurrent = s.active
	}
	s.mu.Unlock()
	if s.onFinish != nil {
		s.onFinish(id)
	}
	s.mu.Lock()
	s.ids = append(s.ids, id)
	s.active--
	s.mu.Unlock()
	return nil
}

// finishDeviceSession does not block disconnect callers behind slow DB writes:
// a burst of closes returns immediately while a single tracked worker flushes
// the queue in FIFO order, and a failed pause schedules resume asynchronously.
func TestFinishDeviceSessionIsNonBlockingAndSerialized(t *testing.T) {
	t.Run("burst disconnects return immediately and flush FIFO", func(t *testing.T) {
		h := newTestHub(t)
		entered := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		store := &blockingDeviceSessionStore{
			meshStore: newMeshStore(),
			onFinish: func(id int64) {
				if id == 1 {
					once.Do(func() {
						close(entered)
						<-release
					})
				}
			},
		}
		h.store = store

		burstDone := make(chan struct{})
		go func() {
			for id := int64(1); id <= 4; id++ {
				h.finishDeviceSession(id, time.Now(), "closed", 30)
			}
			close(burstDone)
		}()
		select {
		case <-burstDone:
		case <-time.After(200 * time.Millisecond):
			t.Fatal("finishDeviceSession burst blocked behind slow FinishDeviceSession")
		}
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("background flush worker did not start")
		}

		// Second schedule call while worker is running must not spawn another worker.
		h.scheduleDeviceSessionFinishes()
		close(release)
		h.nodeWorkers.Wait()

		store.mu.Lock()
		got := slices.Clone(store.ids)
		maxConc := store.maxConcurrent
		store.mu.Unlock()
		if !slices.Equal(got, []int64{1, 2, 3, 4}) {
			t.Fatalf("finished session ids = %v, want [1 2 3 4]", got)
		}
		if maxConc != 1 {
			t.Fatalf("max concurrent FinishDeviceSession = %d, want 1", maxConc)
		}
		h.deviceFinishesMu.Lock()
		running := h.deviceFinishesRunning
		queued := len(h.deviceFinishes)
		h.deviceFinishesMu.Unlock()
		if running || queued != 0 {
			t.Fatalf("after drain: running=%v queued=%d, want false/0", running, queued)
		}
	})

	t.Run("failed pause wakes single async flush without blocking resume", func(t *testing.T) {
		h := newTestHub(t)
		entered := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		store := &blockingDeviceSessionStore{
			meshStore: newMeshStore(),
			onFinish: func(id int64) {
				if id == 10 {
					once.Do(func() {
						close(entered)
						<-release
					})
				}
			},
		}
		h.store = store

		if !h.quiesce.begin() {
			t.Fatal("initial begin failed")
		}
		var endOnce sync.Once
		endInFlight := func() { endOnce.Do(h.quiesce.end) }
		defer endInFlight()
		pauseCtx, pauseCancel := context.WithCancel(context.Background())
		defer pauseCancel()
		pauseDone := make(chan error, 1)
		go func() { pauseDone <- h.pauseDevelopmentWriters(pauseCtx) }()
		waitFor(t, "quiesce paused", func() bool {
			h.quiesce.mu.Lock()
			defer h.quiesce.mu.Unlock()
			return h.quiesce.paused
		})

		h.finishDeviceSession(10, time.Now(), "closed", 30)
		h.finishDeviceSession(11, time.Now(), "closed", 30)
		h.nodeWorkers.Wait()

		// Wait for pauseDevelopmentWriters to observe cancellation before ending
		// the in-flight write so only ctx.Done can unblock writeQuiescer.pause.
		pauseCancel()
		select {
		case err := <-pauseDone:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("pauseDevelopmentWriters error = %v, want context.Canceled", err)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatal("failed pauseDevelopmentWriters blocked on device session flush")
		}
		endInFlight()

		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("failed pause did not wake pending device session flush")
		}
		close(release)
		h.nodeWorkers.Wait()

		store.mu.Lock()
		got := slices.Clone(store.ids)
		maxConc := store.maxConcurrent
		store.mu.Unlock()
		if !slices.Equal(got, []int64{10, 11}) {
			t.Fatalf("finished session ids = %v, want [10 11]", got)
		}
		if maxConc != 1 {
			t.Fatalf("max concurrent FinishDeviceSession = %d, want 1", maxConc)
		}
	})
}

type failingMiddleLiveActivityStore struct {
	*meshStore
	mu     sync.Mutex
	tokens []string
	onSave func(db.LiveActivity) error
}

func (s *failingMiddleLiveActivityStore) SaveLiveActivity(_ context.Context, v db.LiveActivity) error {
	s.mu.Lock()
	s.tokens = append(s.tokens, v.PushToStartToken)
	s.mu.Unlock()
	if s.onSave != nil {
		return s.onSave(v)
	}
	return nil
}

// When one flush loop drains three generations (1: ok, 2: error, 3: ok) while
// callers wait on devMu, the middle caller still receives its SaveLiveActivity
// error instead of seeing the newest generation's nil result.
func TestLiveActivitySaveRetainsIntermediateFailureForWaiter(t *testing.T) {
	h := newTestHub(t)
	middleErr := db.ErrPushRegistrationConflict
	gen1Entered := make(chan struct{})
	releaseGen1 := make(chan struct{})
	gen2Entered := make(chan struct{})
	releaseGen2 := make(chan struct{})
	var gen1Once, gen2Once sync.Once
	store := &failingMiddleLiveActivityStore{
		meshStore: newMeshStore(),
		onSave: func(v db.LiveActivity) error {
			switch v.PushToStartToken {
			case "tok-1":
				gen1Once.Do(func() {
					close(gen1Entered)
					<-releaseGen1
				})
				return nil
			case "tok-2":
				gen2Once.Do(func() {
					close(gen2Entered)
					<-releaseGen2
				})
				return middleErr
			case "tok-3":
				return nil
			default:
				return nil
			}
		},
	}
	h.store = store
	la := h.live
	ctx := context.Background()

	err1Ch := make(chan error, 1)
	go func() {
		err1Ch <- la.updateContext(ctx, "dev1", func(v *db.LiveActivity) { v.PushToStartToken = "tok-1" })
	}()
	select {
	case <-gen1Entered:
	case <-time.After(time.Second):
		t.Fatal("gen 1 save did not start")
	}

	// Queue gen 2 while gen 1 holds devMu.
	err2Ch := make(chan error, 1)
	go func() {
		err2Ch <- la.updateContext(ctx, "dev1", func(v *db.LiveActivity) { v.PushToStartToken = "tok-2" })
	}()
	waitFor(t, "gen 2 queued", func() bool {
		la.mu.Lock()
		defer la.mu.Unlock()
		return la.rowGen["dev1"] == 2
	})

	// Finish gen 1 so the same flush loop picks up gen 2.
	close(releaseGen1)
	select {
	case <-gen2Entered:
	case <-time.After(time.Second):
		t.Fatal("gen 2 save did not start")
	}

	// Queue gen 3 while gen 2 is still in flight in caller 1's flush loop.
	err3Ch := make(chan error, 1)
	go func() {
		err3Ch <- la.updateContext(ctx, "dev1", func(v *db.LiveActivity) { v.PushToStartToken = "tok-3" })
	}()
	waitFor(t, "gen 3 queued", func() bool {
		la.mu.Lock()
		defer la.mu.Unlock()
		return la.rowGen["dev1"] == 3
	})

	// Release gen 2 (fails with middleErr); caller 1's loop then drains gen 3 (succeeds).
	close(releaseGen2)

	for i, tc := range []struct {
		ch   chan error
		want error
	}{
		{err1Ch, nil},
		{err2Ch, middleErr},
		{err3Ch, nil},
	} {
		select {
		case err := <-tc.ch:
			if !errors.Is(err, tc.want) {
				t.Fatalf("caller %d error = %v, want %v", i+1, err, tc.want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("caller %d did not return", i+1)
		}
	}

	store.mu.Lock()
	gotTokens := slices.Clone(store.tokens)
	store.mu.Unlock()
	if !slices.Equal(gotTokens, []string{"tok-1", "tok-2", "tok-3"}) {
		t.Fatalf("saved tokens = %v, want [tok-1 tok-2 tok-3]", gotTokens)
	}
	la.mu.Lock()
	leaked := len(la.saveWaiters)
	la.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("saveWaiters leaked %d entries", leaked)
	}
}

type holdDurationOrderStore struct {
	*meshStore
	mu        sync.Mutex
	holds     []time.Duration
	heldUntil map[string]time.Time
	onHold    func(device string, asOf time.Time, hold time.Duration)
}

func (s *holdDurationOrderStore) HoldLiveActivityStart(_ context.Context, device string, asOf time.Time, hold time.Duration) (bool, error) {
	if s.onHold != nil {
		s.onHold(device, asOf, hold)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holds = append(s.holds, hold)
	if s.heldUntil == nil {
		s.heldUntil = map[string]time.Time{}
	}
	s.heldUntil[device] = asOf.Add(hold)
	return true, nil
}

// A deferred fleetMaxAge hold replayed on resume cannot land after and
// overwrite a newer post-resume liftHold (whether background or HTTP-admitted).
func TestLiveActivityDeferredHoldCannotOverwriteNewerLift(t *testing.T) {
	t.Run("lift arrives before resumeWrites runs", func(t *testing.T) {
		for _, heldCtx := range []bool{false, true} {
			name := "background-ctx"
			if heldCtx {
				name = "quiesce-held-ctx"
			}
			t.Run(name, func(t *testing.T) {
				h := newTestHub(t)
				store := &holdDurationOrderStore{meshStore: newMeshStore()}
				h.store = store
				la := h.live
				startAt := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
				liftAt := startAt.Add(30 * time.Second)

				if err := h.quiesce.pause(context.Background()); err != nil {
					t.Fatal(err)
				}
				la.holdStart(context.Background(), "dev1", startAt, fleetMaxAge)

				// Simulate the window after quiesce.resume() before resumeWrites()'s
				// background goroutine drains deferredHolds.
				h.quiesce.resume()
				liftCtx := context.Background()
				if heldCtx {
					if !h.quiesce.begin() {
						t.Fatal("quiesce begin failed after resume")
					}
					defer h.quiesce.end()
					liftCtx = withQuiesceHeld(liftCtx)
				}
				la.liftHold(liftCtx, "dev1", liftAt)
				la.resumeWrites()
				h.nodeWorkers.Wait()

				store.mu.Lock()
				gotHolds := slices.Clone(store.holds)
				finalDue := store.heldUntil["dev1"]
				store.mu.Unlock()
				if !slices.Equal(gotHolds, []time.Duration{fleetMaxAge, fleetRefresh}) {
					t.Fatalf("hold durations = %v, want [%s %s]", gotHolds, fleetMaxAge, fleetRefresh)
				}
				wantDue := liftAt.Add(fleetRefresh)
				if !finalDue.Equal(wantDue) {
					t.Fatalf("final heldUntil = %s, want %s", finalDue, wantDue)
				}
			})
		}
	})

	t.Run("lift waits for in-flight resumed hold replay", func(t *testing.T) {
		h := newTestHub(t)
		holdEntered := make(chan struct{})
		releaseHold := make(chan struct{})
		var once sync.Once
		store := &holdDurationOrderStore{
			meshStore: newMeshStore(),
			onHold: func(_ string, _ time.Time, hold time.Duration) {
				if hold == fleetMaxAge {
					once.Do(func() {
						close(holdEntered)
						<-releaseHold
					})
				}
			},
		}
		h.store = store
		la := h.live
		startAt := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
		liftAt := startAt.Add(30 * time.Second)

		if err := h.quiesce.pause(context.Background()); err != nil {
			t.Fatal(err)
		}
		la.holdStart(context.Background(), "dev1", startAt, fleetMaxAge)

		h.resumeDevelopmentWriters()
		select {
		case <-holdEntered:
		case <-time.After(2 * time.Second):
			t.Fatal("resumed hold replay did not start")
		}

		liftDone := make(chan struct{})
		go func() {
			la.liftHold(context.Background(), "dev1", liftAt)
			close(liftDone)
		}()
		select {
		case <-liftDone:
			t.Fatal("liftHold completed before in-flight deferred hold finished")
		case <-time.After(25 * time.Millisecond):
		}

		close(releaseHold)
		select {
		case <-liftDone:
		case <-time.After(2 * time.Second):
			t.Fatal("liftHold did not complete after deferred hold finished")
		}
		h.nodeWorkers.Wait()

		store.mu.Lock()
		gotHolds := slices.Clone(store.holds)
		finalDue := store.heldUntil["dev1"]
		store.mu.Unlock()
		if !slices.Equal(gotHolds, []time.Duration{fleetMaxAge, fleetRefresh}) {
			t.Fatalf("hold durations = %v, want [%s %s]", gotHolds, fleetMaxAge, fleetRefresh)
		}
		wantDue := liftAt.Add(fleetRefresh)
		if !finalDue.Equal(wantDue) {
			t.Fatalf("final heldUntil = %s, want %s", finalDue, wantDue)
		}
	})
}

// waitNoWriteInFlight waits until no write holds a quiescer turn. A request starts its async write (the token touch)
// only under a turn it takes before it answers, so once none is in flight, any write the request started has run.
func waitNoWriteInFlight(t *testing.T, h *Hub) {
	t.Helper()
	waitFor(t, "no write in flight", func() bool {
		h.quiesce.mu.Lock()
		defer h.quiesce.mu.Unlock()
		return h.quiesce.inflight == 0
	})
}

// startResume runs resumeDevelopmentWriters on a goroutine of its own and returns once that goroutine has gone as far
// as it can while the test holds a writer: it returned, or it waits on a lock inside resumeDevelopmentWriters (the
// quiescer's, which the held writer keeps). That is the interleaving the race tests exercise.
func startResume(t *testing.T, h *Hub) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	self := make(chan string, 1)
	go func() {
		self <- goroutineID()
		h.resumeDevelopmentWriters()
		close(done)
	}()
	id := <-self
	waitFor(t, "resume to return or wait on the held writer", func() bool {
		select {
		case <-done:
			return true
		default:
		}
		return goroutineWaitsOnLockIn(id, "resumeDevelopmentWriters")
	})
	return done
}

// goroutineID is the calling goroutine's "goroutine N" as its stack trace names it.
func goroutineID() string {
	buf := make([]byte, 64)
	header, _, _ := strings.Cut(string(buf[:runtime.Stack(buf, false)]), " [")
	return header
}

// goroutineWaitsOnLockIn reports whether goroutine id is parked on a mutex (or a semaphore) with fn on its stack.
func goroutineWaitsOnLockIn(id, fn string) bool {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	for _, g := range strings.Split(string(buf), "\n\n") {
		header, stack, _ := strings.Cut(g, "\n")
		if !strings.HasPrefix(header, id+" [") || !strings.Contains(stack, fn) {
			continue
		}
		return strings.Contains(header, "Mutex") || strings.Contains(header, "semacquire")
	}
	return false
}
