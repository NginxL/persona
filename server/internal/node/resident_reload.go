package node

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// ResidentReload drains work while Attach is still serving. Only the final exec closes existing connections;
// idle conversations that need the SIGKILL fallback are reaped before that transport gap begins.
type ResidentReload struct {
	mu             sync.Mutex
	active         int
	paused         bool
	headlessPaused bool
	grace          time.Duration
	graceSet       bool
	idle           chan struct{}
	peers          map[*rpc.Peer]func()
}

func (r *ResidentReload) enter() (func(), error) {
	if r == nil {
		return func() {}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused {
		return nil, &rpc.Error{Code: nodeproto.CodeRestarting, Message: nodeproto.CodeRestarting}
	}
	if r.active == 0 {
		r.idle = make(chan struct{})
	}
	r.active++
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.active--
		if r.active == 0 {
			close(r.idle)
		}
	}, nil
}

func (r *ResidentReload) enterHeadless() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused || r.headlessPaused {
		return &rpc.Error{Code: nodeproto.CodeRestarting, Message: nodeproto.CodeRestarting}
	}
	return nil
}

// SetHeadlessPaused controls only new external assistant turns. Other RPCs and recovery of an admitted turn continue.
func (r *ResidentReload) SetHeadlessPaused(paused bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.headlessPaused = paused
	r.mu.Unlock()
}

// SetGrace sets the next Prepare's admission grace. Zero preserves the indefinite natural-idle wait.
func (r *ResidentReload) SetGrace(grace time.Duration) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.grace, r.graceSet = grace, true
	r.mu.Unlock()
}

func (r *ResidentReload) peer(p *rpc.Peer, disconnect func()) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.peers == nil {
		r.peers = map[*rpc.Peer]func(){}
	}
	if disconnect != nil {
		r.peers[p] = disconnect
	} else {
		delete(r.peers, p)
	}
}

func (n *Node) SetResidentReload(r *ResidentReload) { n.reload = r }

func (r *ResidentReload) Resume() {
	r.mu.Lock()
	r.paused = false
	r.mu.Unlock()
}

// Prepare never stops an answering conversation or an admitted RPC. Natural idle pauses admission immediately;
// after the grace it stays paused while admitted replies and current turns finish. New requests get an explicit
// not-started code. A pre-transfer headless pause stays in force until the manager clears it.
func (r *ResidentReload) Prepare(ctx context.Context) error {
	r.mu.Lock()
	grace, graceSet := r.grace, r.graceSet
	r.mu.Unlock()
	if !graceSet {
		grace = time.Duration(proto.DefaultNodeUpdateHandoffGraceSeconds) * time.Second
	}
	var handoffDeadline time.Time
	if grace > 0 {
		handoffDeadline = time.Now().Add(grace)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		expired := !handoffDeadline.IsZero() && !time.Now().Before(handoffDeadline)
		r.mu.Lock()
		idle, ready := r.idle, r.active == 0
		if ready || expired {
			r.paused = true
		}
		if expired {
			r.headlessPaused = true
		}
		r.mu.Unlock()
		if ready {
			if !AssistantsBusy() {
				break
			}
			if !expired {
				r.Resume()
			}
		}
		delay := 200 * time.Millisecond
		if !expired && !handoffDeadline.IsZero() {
			delay = min(delay, time.Until(handoffDeadline))
		}
		timer := time.NewTimer(delay)
		if !ready {
			select {
			case <-idle:
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		} else {
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
		timer.Stop()
	}
	var procs []*assistantProc
	liveAssistants.Range(func(key, _ any) bool {
		procs = append(procs, key.(*assistantProc))
		return true
	})
	stopAndWait(procs)
	return ctx.Err()
}

func (r *ResidentReload) Announce(ctx context.Context) error {
	r.mu.Lock()
	var peers []*rpc.Peer
	for peer := range r.peers {
		peers = append(peers, peer)
	}
	r.mu.Unlock()
	for _, peer := range peers {
		err := r.exchange(ctx, peer, func() error {
			return peer.NotifyContext(ctx, nodeproto.EventRestarting, map[string]any{})
		})
		if errors.Is(err, rpc.ErrClosed) {
			continue
		}
		if err != nil {
			return err
		}
		// The ordered request fences the announcement and drains manager calls before the old node closes.
		if err := r.exchange(ctx, peer, func() error {
			return peer.Call(ctx, "state.load", map[string]bool{"restart_barrier": true}, nil)
		}); err != nil && !errors.Is(err, rpc.ErrClosed) {
			return err
		}
	}
	return nil
}

// ReportUpdatePreflightFailure lets the manager stop waiting for a replacement while the current node keeps serving.
func (r *ResidentReload) ReportUpdatePreflightFailure(ctx context.Context, id string, err error) {
	failure := proto.NodeUpdatePreflightFailure{ID: id, Error: err.Error()}
	r.mu.Lock()
	defer r.mu.Unlock()
	for peer := range r.peers {
		// The notification must never stall the reload worker or interrupt the surviving connection.
		go func() {
			send, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			_ = peer.NotifyContext(send, nodeproto.EventUpdatePreflightFailed, failure)
		}()
	}
}

// exchange also bounds an already-started write. Aborting an unconfirmed announcement must close the physical
// connection: a queued or partially written control message must not arrive later and suspend a surviving manager.
func (r *ResidentReload) exchange(ctx context.Context, peer *rpc.Peer, send func() error) error {
	done := make(chan error, 1)
	go func() { done <- send() }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		r.mu.Lock()
		disconnect := r.peers[peer]
		r.mu.Unlock()
		peer.Close(err)
		if disconnect != nil {
			disconnect()
		}
	}
	return err
}

func (r *ResidentReload) CancelAnnouncement() {
	r.mu.Lock()
	var peers []*rpc.Peer
	for peer := range r.peers {
		peers = append(peers, peer)
	}
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, peer := range peers {
		_ = r.exchange(ctx, peer, func() error {
			return peer.Call(ctx, "state.load", map[string]bool{"restart_barrier": false}, nil)
		})
	}
}
