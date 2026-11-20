package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// `personad assistant-tui`: one assistant conversation's interactive Claude Code, as the node sees a claude -p
// (assistant_tui.go). Its stdin's first line is the spec (tuiSpec); the rest is the stream-json the node writes to
// claude -p, and its stdout is the stream-json claude -p prints. The interactive side (tmux, the screen, the
// transcript) is assistant_tui_term.go; where it cannot run (no tmux, Windows) the start fails and the print-mode
// command runs instead (fallback).

// tuiOut writes stream-json lines, one writer at a time.
type tuiOut struct {
	mu sync.Mutex
	w  io.Writer
}

func (o *tuiOut) emit(v map[string]any) {
	line, _ := json.Marshal(v)
	o.mu.Lock()
	defer o.mu.Unlock()
	_, _ = o.w.Write(append(line, '\n'))
}

// AssistantTUI runs the bridge; it returns the process's exit status.
func AssistantTUI(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	in := bufio.NewReaderSize(stdin, 1<<20)
	first, err := in.ReadBytes('\n')
	var spec tuiSpec
	if err == nil {
		err = json.Unmarshal(first, &spec)
	}
	if err == nil && (spec.Session == "" || len(spec.Print) == 0) {
		err = errors.New("no session or print-mode command")
	}
	if err != nil {
		fmt.Fprintf(stderr, "assistant-tui: bad spec: %v\n", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	lines := make(chan []byte, 64)
	go func() {
		defer close(lines)
		for {
			l, err := in.ReadBytes('\n')
			if len(strings.TrimSpace(string(l))) > 0 {
				lines <- l
			}
			if err != nil {
				return
			}
		}
	}()
	b := &tuiBridge{spec: spec, out: &tuiOut{w: stdout}, stderr: stderr}
	return b.run(ctx, lines, stdout)
}

// tuiControl is a control request the node writes.
type tuiControl struct {
	Type      string         `json:"type"`
	RequestID string         `json:"request_id"`
	Request   map[string]any `json:"request"`
}

// answer answers a control request: nil err is success.
func (o *tuiOut) answer(id string, err error) {
	r := map[string]any{"subtype": "success", "request_id": id}
	if err != nil {
		r = map[string]any{"subtype": "error", "request_id": id, "error": err.Error()}
	}
	o.emit(map[string]any{"type": "control_response", "response": r})
}

// fallback runs the print-mode command in place of the interactive one that did not start, after saying so: the
// messages written meanwhile (pending) go to it first, then the rest of stdin. Its output is the bridge's.
func (b *tuiBridge) fallback(ctx context.Context, reason string, pending [][]byte, lines <-chan []byte, stdout io.Writer) int {
	fmt.Fprintf(b.stderr, "assistant-tui: interactive Claude Code did not start: %s; running claude -p instead\n", reason)
	cmd := exec.Command(b.spec.Print[0], b.spec.Print[1:]...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = b.spec.Cwd, stdout, b.stderr
	// The environment claude -p gets from the node, which sets Env: with none, exec adds PWD=Dir, the folder as the
	// node wrote it, and Claude Code took a symlinked home's other spelling for a second working directory.
	cmd.Env = os.Environ()
	w, err := cmd.StdinPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil { // no notice: the node sees a process that printed nothing and exited, a failed start
		fmt.Fprintf(b.stderr, "assistant-tui: start claude -p: %v\n", err)
		return 1
	}
	// Said once claude -p runs; the node does not take it for a start (tuiFallbackLine): claude -p's own lines are.
	b.out.emit(map[string]any{"type": "system", "subtype": tuiFallbackSubtype, "reason": reason})
	go func() {
		defer w.Close()
		for _, l := range pending {
			if _, err := w.Write(l); err != nil {
				return
			}
		}
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					return
				}
				if _, err := w.Write(l); err != nil {
					return
				}
			case <-ctx.Done():
				return // the node stops us: the group's signal reaches claude -p too
			}
		}
	}()
	err = cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		return 1
	}
	return 0
}

// tuiStartTimeout bounds an interactive start: the shell, Claude Code and its input box.
var tuiStartTimeout = 2 * time.Minute
