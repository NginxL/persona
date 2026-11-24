package node

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/host"
)

// Reuse the in-memory screen host: virtual-time tests must not wrap a real socket.
// Each read observes a complete captured screen, with a working frame between ready ones.
type readinessFrames struct {
	stateless
	frames  []string
	reads   []time.Time
	missing bool
}

func (h *readinessFrames) Read(context.Context, host.PaneRef, host.ReadOpts) (string, error) {
	frame := h.frames[min(len(h.reads), len(h.frames)-1)]
	h.reads = append(h.reads, time.Now())
	return frame, nil
}

func (h *readinessFrames) Agent(ctx context.Context, pane host.PaneRef) (*host.Agent, error) {
	if h.missing {
		return &host.Agent{}, nil
	}
	return h.stateless.Agent(ctx, pane)
}

func TestReadyPollKeepsFixtureAndProductionCadence(t *testing.T) {
	empty := screenFixture(t, "claude-2.1.312-empty-162.txt")
	for _, fixture := range []bool{true, false} {
		name, poll := "production", 500*time.Millisecond
		if fixture {
			name, poll = "fixture", 20*time.Millisecond
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				n := &Node{}
				if fixture {
					n, _ = testNode(t)
				}
				h := &readinessFrames{stateless: stateless{screenHost{bareHost: bareHost{kind: host.Orca}}},
					frames: []string{empty, empty + "\nesc to interrupt\n", empty, empty}}
				n.hosts = host.NewRegistry(h)
				began := time.Now()
				if agent, err := n.waitReady(context.Background(), "orca:t1:l1", nil); err != nil || agent == nil {
					t.Fatalf("ready frames were not accepted: %v", err)
				}
				if len(h.reads) != 4 {
					t.Fatalf("readiness did not reset after a working frame: %d reads", len(h.reads))
				}
				for i, at := range h.reads {
					if elapsed, want := at.Sub(began), time.Duration(i+1)*poll; elapsed != want {
						t.Fatalf("read %d after %v, want %v", i+1, elapsed, want)
					}
				}
			})
		})
	}
}

func TestReadyPollCancellationDoesNotReadTheHost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n, _ := testNode(t)
		h := &readinessFrames{stateless: stateless{screenHost{bareHost: bareHost{kind: host.Orca}}}, frames: []string{"unused"}}
		n.hosts = host.NewRegistry(h)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := n.waitReady(ctx, "orca:t1:l1", nil); done <- err }()
		time.Sleep(10 * time.Millisecond) // sleep-ok: advance virtual time before cancelling the pending poll
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) || len(h.reads) != 0 {
			t.Fatalf("cancelled poll read its host: %d reads, %v", len(h.reads), err)
		}
	})
}

func TestReadyPollFixtureKeepsTheStartDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n, _ := testNode(t)
		n.claudeStart = 100 * time.Millisecond
		h := &readinessFrames{stateless: stateless{screenHost{bareHost: bareHost{kind: host.Orca}}}, frames: []string{"shell prompt"}, missing: true}
		n.hosts = host.NewRegistry(h)
		began := time.Now()
		_, err := n.waitReady(context.Background(), "orca:t1:l1", nil)
		if err == nil || !strings.Contains(err.Error(), "the tab's shell had not run it yet") || len(h.reads) != 5 || time.Since(began) != n.claudeStart {
			t.Fatalf("start deadline changed: %d reads in %v, %v", len(h.reads), time.Since(began), err)
		}
	})
}

func TestClaudeStartupFailureBeforeReady(t *testing.T) {
	for _, message := range []string{
		"There's an issue with the selected model (missing-model).",
		"Warning: Unknown --effort value 'ultra' — ignoring it and using the default effort.",
		"Error: No conversation found with session ID example",
	} {
		t.Run(message, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				n, _ := testNode(t)
				h := &readinessFrames{stateless: stateless{screenHost{bareHost: bareHost{kind: host.Orca}}},
					frames: []string{"│ " + message + " │\n  ? for shortcuts"}, missing: true}
				n.hosts = host.NewRegistry(h)
				_, err := n.waitReady(t.Context(), "orca:t1:l1", nil)
				if err == nil || err.Error() != message || len(h.reads) != 1 {
					t.Fatalf("want immediate diagnostic %q, got %v after %d reads", message, err, len(h.reads))
				}
			})
		})
	}
}

func TestClaudeRestoreRetainsExplicitModelAndEffort(t *testing.T) {
	n, _ := testNode(t)
	for _, effort := range []string{"xhigh", "max", effortAuto} {
		argv, ok := n.claudeResumeArgv(claudeLaunch{Launcher: "claude", Model: "gateway-model", Effort: effort}, startID)
		if !ok || !strings.Contains(argv[1], "--model gateway-model") || !strings.Contains(argv[1], "--resume "+startID) {
			t.Fatalf("resume command lost its model or session: %q", argv)
		}
		if got := strings.Contains(argv[1], "--effort "+effort); got != (effort != effortAuto) {
			t.Fatalf("unexpected effort on restore: %q", argv)
		}
	}
}

type exitedReadinessHost struct{ *readinessFrames }

func (*exitedReadinessHost) PaneForeground(context.Context, host.PaneRef) ([]string, error) {
	return []string{"zsh"}, nil
}

func TestClaudeDetectsShellReturnBeforeAgentDiscovery(t *testing.T) {
	consumed := filepath.Join(t.TempDir(), "consumed-start")
	synctest.Test(t, func(t *testing.T) {
		n, _ := testNode(t)
		h := &exitedReadinessHost{&readinessFrames{stateless: stateless{screenHost{bareHost: bareHost{kind: host.Orca}}},
			frames: []string{"a launcher quit immediately\n$ "}, missing: true}}
		n.hosts = host.NewRegistry(h)
		started := time.Now()
		_, err := n.waitReady(t.Context(), "orca:t1:l1", &startProbe{startFile: consumed})
		if err == nil || !strings.Contains(err.Error(), "a launcher quit immediately") || time.Since(started) > 4*time.Second {
			t.Fatalf("launcher exit took %s: %v", time.Since(started), err)
		}
	})
}
