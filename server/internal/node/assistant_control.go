package node

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

var (
	assistantControlTimeout = 15 * time.Second
	// A cold shell/CLI has not begun servicing controls yet. Keep that preparation
	// bounded independently of the ordinary acknowledgment wait.
	assistantStartupTimeout = time.Minute
)

// assistantControlUnconfirmed keeps transport failures distinguishable from Claude's explicit refusal,
// while preserving the coded error through RPC. A failed initial launch can still use its startup retries.
type assistantControlUnconfirmed struct {
	error
	cause error
}

func (e *assistantControlUnconfirmed) Unwrap() []error { return []error{e.error, e.cause} }

type assistantControlTimeoutError struct {
	stage string
	wait  time.Duration
}

func (e *assistantControlTimeoutError) Error() string {
	return i18n.T(i18n.Default, "node-assistant-start."+e.stage, i18n.Args{"wait": e.wait.Round(100 * time.Millisecond).String()})
}

func (e *assistantControlTimeoutError) Unwrap() error { return context.DeadlineExceeded }

type assistantControlResponse struct {
	RequestID string          `json:"request_id"`
	Subtype   string          `json:"subtype"`
	Error     string          `json:"error"`
	Response  json.RawMessage `json:"response"` // what a request answers besides success (cancel_async_message's cancelled)
}

// control waits for this request's verdict, never treating a successful stdin write as acceptance.
func (p *assistantProc) control(ctx context.Context, request map[string]any) error {
	_, err := p.controlAnswer(ctx, request)
	return err
}

// controlAnswer is control with the answer Claude gave, null when it gave none.
func (p *assistantProc) controlAnswer(ctx context.Context, request map[string]any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	started := time.Now()
	wait := assistantControlTimeout
	var ready <-chan struct{}
	if !p.hasStarted() && !p.born.IsZero() {
		// Do not wait for stdout before writing: some CLIs emit nothing until
		// they read a request. Stderr from shell plugins is never readiness.
		ready = p.ready
		// A dormant warm spare may legitimately emit nothing until its first
		// request, long after creation. It still gets the ordinary response budget.
		wait = max(assistantControlTimeout, time.Until(p.born.Add(assistantStartupTimeout)))
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	stage := "control_write_timeout"
	timedOut := func() error {
		if ready != nil {
			p.mu.Lock()
			shellReady := !p.shellReadyAt.IsZero() || proto.IsExternalLauncher(p.launcher)
			p.mu.Unlock()
			stage = "shell_start_timeout"
			if shellReady {
				stage = "assistant_ready_timeout"
			}
		}
		return &assistantControlTimeoutError{stage: stage, wait: time.Since(started)}
	}
	id := fmt.Sprintf("persona-%d", p.reqSeq.Add(1))
	result := make(chan assistantControlResponse, 1)
	p.mu.Lock()
	if p.controls == nil {
		p.controls = map[string]chan assistantControlResponse{}
	}
	p.controls[id] = result
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.controls, id)
		p.mu.Unlock()
	}()
	line, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": id, "request": request})
	// A child that stops reading stdin must not hold the settings RPC past its deadline.
	// The caller retires the process on transport failure, closing stdin and releasing this writer.
	written := make(chan error, 1)
	go func() { written <- p.write(line) }()
	for {
		select {
		case err := <-written:
			if err != nil {
				return nil, err
			}
			written = nil
			stage = "control_response_timeout"
		case response := <-result:
			if response.Subtype == "success" {
				return response.Response, nil
			}
			return nil, refusal("assistant_control_failed", response.Error, nil)
		case <-ready:
			ready = nil
			// The assistant is now servicing its protocol. Give this request
			// the full normal response budget, regardless of shell startup time.
			timer.Reset(assistantControlTimeout)
		case <-timer.C:
			return nil, timedOut()
		case <-ctx.Done():
			// A shorter caller deadline must retain the phase and elapsed wait
			// too; assistantSend sees ctx.Err and never retries that deadline.
			if ctx.Err() == context.DeadlineExceeded {
				return nil, timedOut()
			}
			return nil, ctx.Err()
		case <-p.exited:
			return nil, io.EOF
		}
	}
}

func (p *assistantProc) controlResponse(response assistantControlResponse) {
	if response.Subtype != "success" && response.Subtype != "error" {
		return
	}
	p.mu.Lock()
	result := p.controls[response.RequestID]
	delete(p.controls, response.RequestID)
	p.mu.Unlock()
	if result != nil {
		result <- response
	}
}
