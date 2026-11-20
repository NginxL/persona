package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A real closed peer distinguishes a request never written from a lost answer after the write.
func taskClosedTransportError(t *testing.T, sent bool) error {
	t.Helper()
	var wire bytes.Buffer
	var p *rpc.Peer
	if !sent {
		p = rpc.New("task-fixture", &wire)
		p.Close(io.EOF)
	} else {
		// Call's writer closes only once its complete request has been accepted.
		p = rpc.New("task-fixture", taskClosingWriter{wire: &wire, close: func() { p.Close(io.EOF) }})
	}
	err := p.Call(context.Background(), DeviceAssistantSend, map[string]any{"text": "Check files"}, nil)
	if errors.Is(err, rpc.ErrNotSent) == sent || !errors.Is(err, rpc.ErrClosed) || (wire.Len() > 0) != sent {
		t.Fatalf("transport boundary: sent=%v, bytes=%d, error=%v", sent, wire.Len(), err)
	}
	return fmt.Errorf("scheduled send: %w", err)
}

type taskClosingWriter struct {
	wire  *bytes.Buffer
	close func()
}

func (w taskClosingWriter) Write(p []byte) (int, error) {
	n, err := w.wire.Write(p)
	w.close()
	return n, err
}

func TestTaskResumeConfirmedUnsentRetriesOnce(t *testing.T) {
	for _, source := range []bool{false, true} {
		for _, failure := range []string{"wrapped timeout before write", "closed before write", "still unsent", "closed after write", "lost reply", "node accepted"} {
			name := "own/"
			if source {
				name = "source/"
			}
			t.Run(name+failure, func(t *testing.T) {
				f := newFakeHub().withMachine()
				f.setSessions("server", proto.Session{SessionID: "prior", Launcher: "claude", Cwd: "/tmp/assistant", State: proto.StateNeedsYou})
				s := newTestService(t, f)
				var attempts, effects atomic.Int32
				persistent := failure == "still unsent"
				retry := failure == "wrapped timeout before write" || failure == "closed before write" || persistent
				f.answerNode(DeviceAssistantSend, func(_ string, raw json.RawMessage) (any, error) {
					var p struct {
						SessionID  string               `json:"session_id"`
						ResumeOnly bool                 `json:"resume_only"`
						Source     *proto.MessageSource `json:"message_source"`
					}
					if err := json.Unmarshal(raw, &p); err != nil {
						t.Fatal(err)
					}
					if p.SessionID != "prior" || !p.ResumeOnly || p.Source == nil || p.Source.Kind != proto.SourceFollowup {
						t.Fatalf("retry replaced the original one-off: %s", raw)
					}
					if attempts.Add(1) == 1 || persistent {
						if !retry {
							effects.Add(1) // a lost receipt can follow a real effect
						}
						switch failure {
						case "wrapped timeout before write", "still unsent":
							return nil, fmt.Errorf("node dispatch: %w", rpc.NotSent(context.DeadlineExceeded))
						case "closed before write", "closed after write":
							return nil, taskClosedTransportError(t, !retry)
						case "lost reply":
							return nil, context.DeadlineExceeded
						default:
							return nil, &rpc.Error{Code: "send_failed", Message: "accepted without receipt", Data: json.RawMessage(`{"delivered":"yes"}`)}
						}
					}
					effects.Add(1)
					return map[string]string{"session_id": p.SessionID}, nil
				})
				synctest.Test(t, func(t *testing.T) {
					at := time.Now().Add(-time.Minute)
					// runDue has already disabled a due one-off before dispatching this run.
					task := Task{ID: "job", Prompt: "Check files", Repeat: repeatOnce, At: at}
					if source {
						task.MachineID, task.SessionID = "server", "prior"
					} else {
						task.RunMachineID, task.RunSessionID, task.RunLauncher = "server", "prior", "claude"
					}
					setTasks(s, task)
					if _, err := s.runTask(task, at, time.Now(), false, false); err == nil {
						t.Fatal("failed request reported success")
					}
					_, runs := scheduleState(s)
					if len(runs) != 1 || runs[0].Status != runFailed || runs[0].DeliveryUnknown == retry || runs[0].Retried || runs[0].SessionID != "" {
						t.Fatalf("first delivery evidence: %+v", runs)
					}
					if len(f.pushed()) != 1 {
						t.Fatal("failed one-off lost its owner notification")
					}
					time.Sleep(runRetryDelay / 2) // sleep-ok: advance the synctest clock past the production retry boundary, no wall time
					synctest.Wait()
					if attempts.Load() != 1 {
						t.Fatal("one-off retried before the existing delay")
					}
					time.Sleep(runRetryDelay) // sleep-ok: advance the synctest clock past the production retry boundary, no wall time
					synctest.Wait()
					_, runs = scheduleState(s)
					want := int32(1)
					if retry {
						want = 2
					}
					wantEffects := int32(1)
					if persistent {
						wantEffects = 0
					}
					if attempts.Load() != want || effects.Load() != wantEffects || len(runs) != int(want) {
						t.Fatalf("after retry delay: attempts=%d effects=%d runs=%+v", attempts.Load(), effects.Load(), runs)
					}
					if retry {
						status, session := runOK, "prior"
						if persistent {
							status, session = runFailed, ""
							if len(f.pushed()) != 2 {
								t.Fatal("failed retry lost its owner notification")
							}
						}
						if !runs[1].Retried || runs[1].Status != status || runs[1].DeliveryUnknown || runs[1].SessionID != session {
							t.Fatalf("retry outcome: %+v", runs[1])
						}
					}
					current, ok := taskByID(s, "job")
					if !ok || current.ID != task.ID || current.Repeat != repeatOnce || !current.At.Equal(at) || current.Enabled {
						t.Fatalf("one-off identity/schedule changed: %+v", current)
					}
					time.Sleep(2 * runRetryDelay) // sleep-ok: advance the synctest clock past the production retry boundary, no wall time
					synctest.Wait()
					if attempts.Load() != want || effects.Load() != wantEffects {
						t.Fatal("one-off was delivered again after its single retry")
					}
				})
			})
		}
	}
}

func TestTaskResumeModelPreparationRetriesBeforeSend(t *testing.T) {
	f := newFakeHub().withMachine()
	f.setSessions("server", proto.Session{SessionID: "prior", Launcher: "claude", Cwd: "/tmp/assistant", State: proto.StateNeedsYou})
	s := newTestService(t, f)
	var prepares, sends atomic.Int32
	f.answerNode(DeviceAssistantPrepare, func(string, json.RawMessage) (any, error) {
		if prepares.Add(1) == 1 {
			// A provider changes after the run snapshot but before finishModel builds the resume request.
			putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "claude", Kind: "api", API: "responses"}})
		}
		return map[string]string{"cwd": "/tmp/assistant"}, nil
	})
	f.answerNode(DeviceAssistantSend, func(_ string, raw json.RawMessage) (any, error) {
		var p struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.SessionID != "prior" {
			t.Fatalf("preparation failure replaced the conversation: %s", raw)
		}
		sends.Add(1)
		return map[string]string{"session_id": p.SessionID}, nil
	})
	synctest.Test(t, func(t *testing.T) {
		at := time.Now().Add(-time.Minute)
		task := Task{ID: "job", Prompt: "Check files", Repeat: repeatOnce, At: at, RunMachineID: "server", RunSessionID: "prior", RunLauncher: "claude"}
		setTasks(s, task)
		if _, err := s.runTask(task, at, time.Now(), false, false); err == nil || prepares.Load() != 1 || sends.Load() != 0 {
			t.Fatalf("resume preparation was not refused before sending: %v prepares=%d sends=%d", err, prepares.Load(), sends.Load())
		}
		_, runs := scheduleState(s)
		if len(runs) != 1 || runs[0].Status != runFailed || runs[0].DeliveryUnknown {
			t.Fatalf("preparation recorded uncertain delivery: %+v", runs)
		}
		putLaunchers(t, s)            // the owner fixes the provider before the existing retry is due
		time.Sleep(runRetryDelay / 2) // sleep-ok: advance the synctest clock past the production retry boundary, no wall time
		synctest.Wait()
		if sends.Load() != 0 {
			t.Fatal("preparation failure retried too early")
		}
		time.Sleep(runRetryDelay) // sleep-ok: advance the synctest clock past the production retry boundary, no wall time
		synctest.Wait()
		_, runs = scheduleState(s)
		if sends.Load() != 1 || len(runs) != 2 || !runs[1].Retried || runs[1].Status != runOK || runs[1].SessionID != "prior" {
			t.Fatalf("preparation failure did not retain one retry: sends=%d runs=%+v", sends.Load(), runs)
		}
		time.Sleep(2 * runRetryDelay) // sleep-ok: advance the synctest clock past the production retry boundary, no wall time
		synctest.Wait()
		if sends.Load() != 1 {
			t.Fatal("preparation failure delivered more than once")
		}
	})
}

func TestTaskExecutionBusyRefusalStartsFreshWithoutReplayingUnknown(t *testing.T) {
	for _, repeat := range []string{repeatOnce, repeatInterval} {
		for _, uncertain := range []bool{false, true} {
			name := repeat + "/refused"
			if uncertain {
				name = repeat + "/unknown"
			}
			t.Run(name, func(t *testing.T) {
				f := newFakeHub().withMachine()
				s := newTestService(t, f)
				setTasks(s, Task{ID: "job", Prompt: "Check files", Repeat: repeat, EveryMinutes: 30, Enabled: repeat != repeatOnce})
				setModel(t, s, "task.job", ModelBinding{Provider: "launcher.claude", Model: "chosen-model", Effort: "high"})
				synctest.Test(t, func(t *testing.T) {
					first, err := runNow(t, s, "job")
					if err != nil {
						t.Fatal(err)
					}
					wanted := f.lastSend(t)
					status := proto.StatusWorking // a one-off goes even while its conversation is busy
					if repeat != repeatOnce {
						status = proto.StatusIdle // background-only work is not the hub's foreground busy skip
					}
					f.setSessions("server", proto.Session{SessionID: first.SessionID, Launcher: "claude", Model: "older-model", Status: status})
					var old, fresh atomic.Int32
					f.answerNode(DeviceAssistantSend, func(_ string, raw json.RawMessage) (any, error) {
						var p map[string]any
						_ = json.Unmarshal(raw, &p)
						for _, key := range []string{"launcher", "model", "effort", "model_fingerprint", "cwd", "task_execution"} {
							if p[key] != wanted[key] {
								t.Errorf("replacement lost %s: %v, want %v", key, p[key], wanted[key])
							}
						}
						if p["session_id"] == first.SessionID {
							old.Add(1)
							if uncertain {
								return nil, rpc.ErrClosed
							}
							return nil, errors.New("task execution context differs from this conversation; start a new conversation")
						}
						fresh.Add(1)
						return map[string]string{"session_id": "fresh"}, nil
					})
					task, _ := taskByID(s, "job")
					got, err := s.runTask(task, time.Now(), time.Now(), false, false)
					if uncertain {
						if err == nil || got.SessionID != "" || fresh.Load() != 0 {
							t.Fatalf("unknown delivery replaced its conversation: %+v %v", got, err)
						}
					} else if err != nil || got.SessionID != "fresh" || fresh.Load() != 1 {
						t.Fatalf("pre-delivery refusal did not start the chosen execution: %+v %v", got, err)
					}
					// End both synthetic turns inside this clock so no watcher outlives its fixture.
					f.setSessions("server", proto.Session{SessionID: first.SessionID, State: proto.StateNeedsYou}, proto.Session{SessionID: "fresh", State: proto.StateNeedsYou})
					time.Sleep(2 * runRetryDelay) // sleep-ok: advance the synctest clock past the production retry boundary, no wall time
					synctest.Wait()
					if old.Load() != 1 || fresh.Load() != map[bool]int32{true: 0, false: 1}[uncertain] {
						t.Fatalf("delivery replayed: old=%d fresh=%d", old.Load(), fresh.Load())
					}
					f.mu.Lock()
					defer f.mu.Unlock()
					if len(f.stopped) != 0 || len(f.retired) != 0 {
						t.Fatal("active conversation was stopped during replacement")
					}
				})
			})
		}
	}
}
