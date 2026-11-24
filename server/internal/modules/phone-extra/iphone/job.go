package iphone

import (
	"context"
	"errors"
	"time"
)

// Job is the background pairing, forgetting or installing of the bridge the app started (one at a time): its state
// comes back with the machine's phone status, as the computer extension's install job does.
type Job struct {
	Kind  string `json:"kind"`  // pair / forget / install / prepare
	UDID  string `json:"udid"`  // the phone it is for ("" for install)
	State string `json:"state"` // running / done / failed / cancelled
	Step  string `json:"step,omitempty"`
	Error string `json:"error,omitempty"`
	Code  string `json:"code,omitempty"` // the failure's code (the bridge's, e.g. trust_pending; install's, e.g. download_failed)
	// Waiting: while a pairing runs, what it waits for from the person at the phone (CodeTrustPending, CodeLocked), ""
	// when it waits for nothing (PairProgress).
	Waiting string `json:"waiting,omitempty"`
	// Stage: while an install or a prepare runs, what it does (StageDownload, StageVerify, StageInstall; StagePrepare).
	Stage   string    `json:"stage,omitempty"`
	Started time.Time `json:"started"`
}

// Job states.
const (
	JobRunning   = "running"
	JobDone      = "done"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// pairTimeout bounds a pairing job: long enough for someone to find the phone, unlock it and tap Trust.
var pairTimeout = 3 * time.Minute

// Job is the current or last job, or nil.
func (m *Manager) Job() *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil {
		return nil
	}
	j := *m.job
	return &j
}

// StartPair starts pairing udid in the background.
func (m *Manager) StartPair(udid string) error {
	return m.startJob("pair", udid, func(ctx context.Context, step func(string, string)) error {
		ctx, cancel := context.WithTimeout(ctx, pairTimeout)
		defer cancel()
		_, err := m.Pair(ctx, udid, step)
		return err
	})
}

// StartForget forgets udid in the background (quick, but the same shape as pairing for the app).
func (m *Manager) StartForget(udid string) error {
	return m.startJob("forget", udid, func(context.Context, func(string, string)) error { return m.Forget(udid) })
}

// Cancel stops the running job.
func (m *Manager) Cancel() {
	m.mu.Lock()
	stop := m.jobStop
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (m *Manager) startJob(kind, udid string, run func(context.Context, func(step, code string)) error) error {
	if kind != "install" && !ValidUDID(udid) {
		return &Error{Code: "bad_request", Message: "invalid device id"}
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return &Error{Code: CodeUnreachable, Message: "the node is shutting down"}
	}
	if m.job != nil && m.job.State == JobRunning {
		m.mu.Unlock()
		return &Error{Code: CodeBusy, Message: "a " + m.job.Kind + " job is already running"}
	}
	ctx, cancel := context.WithCancel(m.ctx)
	job := &Job{Kind: kind, UDID: udid, State: JobRunning, Started: m.now().UTC()}
	m.job, m.jobStop = job, cancel
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		defer cancel()
		err := run(ctx, func(step, code string) {
			m.mu.Lock()
			job.Step = step
			if kind == "pair" {
				job.Waiting = code
			} else {
				job.Stage = code
			}
			m.mu.Unlock()
		})
		m.mu.Lock()
		defer m.mu.Unlock()
		job.Waiting, job.Stage = "", ""
		switch {
		case err == nil:
			job.State, job.Step = JobDone, ""
		case errors.Is(err, context.Canceled):
			job.State = JobCancelled
		default:
			job.State, job.Error, job.Code = JobFailed, err.Error(), Code(err)
			if errors.Is(err, context.DeadlineExceeded) {
				job.Code, job.Error = "timeout", "the phone didn't confirm in time: unlock it, tap Trust and enter its passcode, then try again"
				switch kind {
				case "install":
					job.Error = "the iPhone bridge didn't install in time"
				case "prepare":
					job.Error = "the developer disk image didn't mount in time"
				}
			}
		}
		if m.jobStop != nil && m.job == job {
			m.jobStop = nil
		}
	}()
	return nil
}
