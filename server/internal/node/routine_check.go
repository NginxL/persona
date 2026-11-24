package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
)

func init() {
	nodeRPC(proto.MethodRoutineCheck, func(_ *Node, ctx context.Context, raw json.RawMessage) (proto.RoutineCheckResult, error) {
		return routineCheck(ctx, raw)
	})
}

func routineCheck(ctx context.Context, raw json.RawMessage) (proto.RoutineCheckResult, error) {
	var p proto.RoutineCheckParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return proto.RoutineCheckResult{}, err
	}
	if strings.TrimSpace(p.Command) == "" || len(p.Command) > proto.RoutineCheckMaxCommand ||
		p.Timeout < 1 || p.Timeout > proto.RoutineCheckMaxTimeout {
		return proto.RoutineCheckResult{}, errors.New("invalid routine check command or timeout")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.Timeout)*time.Second)
	defer cancel()

	// Keep rc functions, aliases and PATH, but separate shell initialization from the check's output (#8876).
	// The subshell preserves the command's status without interactive bash printing its own "exit" afterward.
	// This unattended check needs no job control: keep its children in the group cancellation owns.
	marker := "\x1epersona-routine-" + rand.Text() + "\x1f"
	cmd := loginShell(ctx, "set +m; builtin printf '%s' "+shellQuote(marker)+"; ( builtin eval "+shellQuote(p.Command)+" )")
	cmd.Dir, _ = os.UserHomeDir()
	killGroupOnCancel(cmd)
	output := routineCheckOutput{marker: []byte(marker)}
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		return proto.RoutineCheckResult{}, err
	}
	// exec stops watching the deadline once the shell exits, even if a child still holds the output open.
	waited := make(chan struct{})
	var watch sync.WaitGroup
	stop := sync.OnceFunc(func() { _ = osproc.StopGroup(cmd.Process.Pid, true) })
	watch.Go(func() {
		select {
		case <-ctx.Done():
			stop()
		case <-waited:
		}
	})
	err := cmd.Wait() // killGroupOnCancel also sets a bounded WaitDelay.
	close(waited)
	watch.Wait()
	stop() // A child may have closed its output and kept running.
	if ctx.Err() != nil {
		return proto.RoutineCheckResult{}, ctx.Err()
	}
	var exit *exec.ExitError
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) && !errors.As(err, &exit) {
		return proto.RoutineCheckResult{}, err
	}
	return proto.RoutineCheckResult{
		ExitCode: cmd.ProcessState.ExitCode(),
		Reason:   strings.TrimSpace(strings.ToValidUTF8(string(output.reason()), "")),
	}, nil
}

// Both streams share one pipe, so the shell's marker precedes all command output, including stderr.
// Retain only enough startup bytes to recognize a split marker and the first line if startup fails before it.
type routineCheckOutput struct {
	marker, pending  []byte
	startup, command routineCheckLine
	started          bool
}

func (w *routineCheckOutput) Write(p []byte) (int, error) {
	n := len(p)
	if w.started {
		return w.command.Write(p)
	}
	_, _ = w.startup.Write(p)
	data := append(w.pending, p...)
	if i := bytes.Index(data, w.marker); i >= 0 {
		w.started = true
		_, _ = w.command.Write(data[i+len(w.marker):])
		w.pending = nil
	} else {
		w.pending = append(w.pending[:0], data[max(0, len(data)-len(w.marker)+1):]...)
	}
	return n, nil
}

func (w *routineCheckOutput) reason() []byte {
	if w.started {
		return w.command.line
	}
	return w.startup.line
}

type routineCheckLine struct {
	line []byte
	done bool
}

func (w *routineCheckLine) Write(p []byte) (int, error) {
	n := len(p)
	if w.done {
		return n, nil
	}
	if i := bytes.IndexByte(p, '\n'); i >= 0 {
		p = p[:i]
		w.done = true
	}
	room := 512 - len(w.line)
	w.line = append(w.line, p[:min(room, len(p))]...)
	w.done = w.done || len(w.line) == 512
	return n, nil
}
