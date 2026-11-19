package node

import (
	"context"
	"io"
	"os/exec"
	"sync"

	"github.com/nwverse/persona/server/internal/osproc"
)

// externalStarts holds only pending sends. Stop cancels the current generation before waiting for the typing lock;
// sends registered afterwards are a new user operation and are not canceled by an older Stop or deferred cleanup.
type externalStarts struct {
	mu      sync.Mutex
	pending map[string]map[*externalStartup]struct{}
}

type externalStartup struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.Closer
	// detached is the atomic handoff from initialization to a live conversation. Cancellation after that handoff must
	// not kill a process simply because the RPC that started it ended.
	detached bool
}

type externalStartupKey struct{}

func (n *Node) beginExternalStartup(ctx context.Context, id string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	s := &externalStartup{ctx: ctx, cancel: cancel}
	n.externalStarts.mu.Lock()
	if n.externalStarts.pending == nil {
		n.externalStarts.pending = map[string]map[*externalStartup]struct{}{}
	}
	if n.externalStarts.pending[id] == nil {
		n.externalStarts.pending[id] = map[*externalStartup]struct{}{}
	}
	n.externalStarts.pending[id][s] = struct{}{}
	n.externalStarts.mu.Unlock()
	watch := context.AfterFunc(ctx, s.killPending)
	return context.WithValue(ctx, externalStartupKey{}, s), func() {
		n.externalStarts.mu.Lock()
		delete(n.externalStarts.pending[id], s)
		if len(n.externalStarts.pending[id]) == 0 {
			delete(n.externalStarts.pending, id)
		}
		n.externalStarts.mu.Unlock()
		cancel()
		s.killPending()
		watch()
	}
}

func (r *externalStarts) has(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending[id]) > 0
}

func (n *Node) cancelExternalStartups(id string) {
	n.externalStarts.mu.Lock()
	pending := make([]*externalStartup, 0, len(n.externalStarts.pending[id]))
	for s := range n.externalStarts.pending[id] {
		pending = append(pending, s)
	}
	n.externalStarts.mu.Unlock()
	for _, s := range pending {
		s.cancel()
	}
}

func externalStartupOf(ctx context.Context) *externalStartup {
	s, _ := ctx.Value(externalStartupKey{}).(*externalStartup)
	return s
}

func externalStartContext(execution modelExecution) context.Context {
	if execution.StartupContext != nil {
		return execution.StartupContext
	}
	return context.Background()
}

func startExternalCommand(ctx context.Context, cmd *exec.Cmd, stdin io.Closer) error {
	var err error
	if startup := externalStartupOf(ctx); startup != nil {
		err = startup.start(cmd, stdin)
	} else if ctx.Err() != nil {
		err = ctx.Err()
	} else {
		err = cmd.Start()
	}
	return externalAdapterError(ctx, err)
}

func (s *externalStartup) start(cmd *exec.Cmd, stdin io.Closer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return err
	}
	s.cmd, s.stdin = cmd, stdin
	return cmd.Start()
}

func (s *externalStartup) killPending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.detached || s.cmd == nil {
		return
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.cmd.Process != nil {
		_ = osproc.StopGroup(s.cmd.Process.Pid, true)
	}
	s.cmd, s.stdin = nil, nil
}

func (s *externalStartup) detach() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return err
	}
	s.detached, s.cmd, s.stdin = true, nil, nil
	return nil
}

func finishExternalStartup(ctx context.Context) error {
	if s := externalStartupOf(ctx); s != nil {
		if err := s.detach(); err != nil {
			return externalAdapterError(ctx, err)
		}
	}
	return nil
}
