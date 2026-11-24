package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/nwverse/persona/server/internal/db"
)

const deviceSessionRetention = 14 * 24 * time.Hour

var errDeviceSessionPongTimeout = fmt.Errorf("pong timeout: %w", context.DeadlineExceeded)

func (h *Hub) serveDeviceSessions(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	since := now.Add(-24 * time.Hour)
	if raw := r.URL.Query().Get("since"); raw != "" {
		var err error
		since, err = time.Parse(time.RFC3339, raw)
		if err != nil || since.After(now) {
			writeErr(w, http.StatusBadRequest, errors.New("since must be an RFC3339 timestamp no later than now"))
			return
		}
	}
	if since.Before(now.Add(-deviceSessionRetention)) {
		since = now.Add(-deviceSessionRetention)
	}
	if h.store == nil {
		writeJSON(w, db.DeviceSessionHistory{Sessions: []db.DeviceSession{}})
		return
	}
	history, err := h.store.DeviceSessions(r.Context(), r.PathValue("id"), since, now)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, history)
}

// startDeviceSessions runs before serving requests and finishes before the database closes.
func (h *Hub) startDeviceSessions(ctx context.Context) (func(), error) {
	if h.store == nil {
		return func() {}, nil
	}
	if err := h.store.StopDeviceSessions(ctx, time.Now()); err != nil {
		return nil, err
	}
	// A skipped pass during a snapshot is repeated at the next tick: nothing
	// here is time-critical, and the snapshot's database has to stay still.
	prune := func() {
		if !h.quiesce.begin() {
			return
		}
		defer h.quiesce.end()
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := h.store.PruneLockTests(pctx, time.Now().Add(-deviceSessionRetention)); err != nil {
			h.logf("prune lock tests: %v", err)
		}
		if err := h.store.PruneDeviceSessions(pctx, time.Now().Add(-deviceSessionRetention)); err != nil {
			h.logf("prune device sessions: %v", err)
		}
	}
	prune()
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				prune()
			}
		}
	}()
	return func() {
		cancel()
		<-done
		parent := context.Background()
		if h.shutdown != nil {
			parent = h.shutdown.start()
		}
		sctx, stop := context.WithTimeout(parent, shutdownStepTimeout)
		defer stop()
		if err := h.store.StopDeviceSessions(sctx, time.Now()); err != nil {
			h.logf("stop device sessions: %v", err)
		}
	}, nil
}

type deviceSessionConnection struct {
	hub      *Hub
	client   *client
	ctx      context.Context
	id       int64
	mu       sync.Mutex
	writeErr error
}

func (h *Hub) deviceSessionConnected(r *http.Request, c *client) *deviceSessionConnection {
	p := principalOf(r.Context())
	if h.store == nil || p.ID == "" || p.PairHash != "" {
		return nil
	}
	source := ""
	if ip := remoteIP(r); ip.IsValid() {
		bits := 64
		if ip.Is4() {
			bits = 24
		}
		source = netip.PrefixFrom(ip, bits).Masked().String()
	}
	// A connection that arrives during a snapshot is served without a session
	// row: the row and its heartbeat would be writes the snapshot must not see.
	// The next connection records one.
	if !h.quiesce.begin() {
		return nil
	}
	defer h.quiesce.end()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	id, err := h.store.InsertDeviceSession(ctx, db.DeviceSession{DeviceID: p.ID,
		ConnectedAt: time.Now(), Source: source, HeartbeatInterval: defaultHeartbeat().Interval})
	if err != nil {
		h.logf("insert device session: %v", err)
		return nil
	}
	return &deviceSessionConnection{hub: h, client: c, ctx: r.Context(), id: id}
}

func (s *deviceSessionConnection) writerStopped(err error) {
	if s != nil {
		s.mu.Lock()
		s.writeErr = err
		s.mu.Unlock()
	}
}

func (s *deviceSessionConnection) heartbeat(hb heartbeatConfig) {
	if s == nil {
		return
	}
	// A skipped heartbeat during a snapshot is written again on the next one.
	if !s.hub.quiesce.begin() {
		return
	}
	defer s.hub.quiesce.end()
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	if err := s.hub.store.UpdateDeviceSessionHeartbeat(ctx, s.id, hb.Interval); err != nil {
		s.hub.logf("update device session heartbeat: %v", err)
	}
}

func (s *deviceSessionConnection) disconnected(readErr error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	err := s.writeErr
	s.mu.Unlock()
	if err == nil || errors.Is(err, context.Canceled) {
		err = readErr
	}
	reason := "error"
	var closeErr websocket.CloseError
	switch {
	case s.ctx.Err() != nil || s.client.stopping.Load():
		reason = "hub_stopped"
	case errors.Is(err, errDeviceSessionPongTimeout):
		reason = "pong_timeout"
	case errors.As(err, &closeErr):
		if closeErr.Reason == "replaced" {
			reason = "replaced"
		} else if closeErr.Code == websocket.StatusNormalClosure || closeErr.Code == websocket.StatusGoingAway {
			reason = "closed"
		}
	}
	s.client.recordSocketTraffic("websocket_close", reason, 0, 0)
	s.client.mu.Lock()
	interval := s.client.meta.heartbeat.Interval
	s.client.mu.Unlock()
	if interval == 0 {
		interval = defaultHeartbeat().Interval
	}
	s.hub.finishDeviceSession(s.id, time.Now(), reason, interval)
}

// deviceSessionFinish is a connection's end left for the resume by a snapshot
// that paused writes.
type deviceSessionFinish struct {
	id       int64
	at       time.Time
	reason   string
	interval int
}

// finishDeviceSession records a connection's end asynchronously, or keeps it
// pending until a snapshot's pause is lifted: the row must not change while
// its database is dumped. Appending before scheduling preserves FIFO order
// without blocking the disconnect path on database latency.
func (h *Hub) finishDeviceSession(id int64, at time.Time, reason string, interval int) {
	if h.store == nil {
		return
	}
	h.deviceFinishesMu.Lock()
	h.deviceFinishes = append(h.deviceFinishes, deviceSessionFinish{id: id, at: at, reason: reason, interval: interval})
	h.deviceFinishesMu.Unlock()
	if hook := h.testAfterFinishAppend; hook != nil {
		hook()
	}
	h.scheduleDeviceSessionFinishes()
}

// scheduleDeviceSessionFinishes starts at most one tracked worker to flush
// queued device session ends asynchronously.
func (h *Hub) scheduleDeviceSessionFinishes() {
	if h.store == nil {
		return
	}
	h.deviceFinishesMu.Lock()
	if len(h.deviceFinishes) == 0 || h.deviceFinishesRunning {
		h.deviceFinishesMu.Unlock()
		return
	}
	h.deviceFinishesRunning = true
	h.nodeWorkers.Add(1)
	h.deviceFinishesMu.Unlock()
	go func() {
		defer h.nodeWorkers.Done()
		h.flushDeviceSessionFinishes()
	}()
}

// flushDeviceSessionFinishes writes the sessions that ended, in the order they
// ended. Serializing with deviceFinishesFlushMu guarantees FIFO order even if
// a newly scheduled worker starts while a paused worker is exiting.
func (h *Hub) flushDeviceSessionFinishes() {
	if h.store == nil {
		return
	}
	h.deviceFinishesFlushMu.Lock()
	defer h.deviceFinishesFlushMu.Unlock()
	for {
		h.deviceFinishesMu.Lock()
		if len(h.deviceFinishes) == 0 {
			h.deviceFinishesRunning = false
			h.deviceFinishesMu.Unlock()
			return
		}
		h.deviceFinishesMu.Unlock()
		// Acquire a quiesce turn, or clear deviceFinishesRunning atomically
		// under quiesce.mu when paused so resume cannot observe running==true
		// and strand queued entries (quiesce.go).
		if !h.quiesce.beginOrDefer(func() {
			h.deviceFinishesMu.Lock()
			h.deviceFinishesRunning = false
			h.deviceFinishesMu.Unlock()
		}) {
			if hook := h.testAfterFinishRefuse; hook != nil {
				hook()
			}
			return
		}
		h.deviceFinishesMu.Lock()
		if len(h.deviceFinishes) == 0 {
			h.deviceFinishesRunning = false
			h.deviceFinishesMu.Unlock()
			h.quiesce.end()
			return
		}
		next := h.deviceFinishes[0]
		h.deviceFinishes = h.deviceFinishes[1:]
		h.deviceFinishesMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := h.store.FinishDeviceSession(ctx, next.id, next.at, next.reason, next.interval); err != nil {
			h.logf("finish device session: %v", err)
		}
		cancel()
		h.quiesce.end()
	}
}
