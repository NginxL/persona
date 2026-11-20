package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

func TestTaskNewConversationDeliveryBoundary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"reply lost", context.DeadlineExceeded, false},
		{"connection closed", rpc.ErrClosed, false},
		{"relayed disconnect", &rpc.Error{Message: rpc.ErrClosed.Error()}, false},
		{"node uncertain", &rpc.Error{Code: "timeout", Message: "turn receipt lost", Data: json.RawMessage(`{"delivered":"unknown"}`)}, false},
		{"node took it", &rpc.Error{Code: "send_failed", Message: "turn already accepted", Data: json.RawMessage(`{"delivered":"yes"}`)}, false},
		{"never sent", rpc.NotSent(context.DeadlineExceeded), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			s := newTestService(t, f)
			s.runRetry = time.Second
			var sends atomic.Int32
			f.answerNode(DeviceAssistantSend, func(string, json.RawMessage) (any, error) {
				sends.Add(1)
				return nil, tc.err
			})
			synctest.Test(t, func(t *testing.T) {
				setTasks(s, Task{ID: "job", Prompt: "Check files", Repeat: repeatOnce, At: time.Now().Add(-time.Minute)})
				task, _ := taskByID(s, "job")
				if _, err := s.runTask(task, task.At, time.Now(), false, false); err == nil {
					t.Fatal("failed send was reported as started")
				}
				if len(f.pushed()) != 1 {
					t.Fatal("failed one-off did not notify the owner")
				}
				<-time.After(10 * s.runRetry)
				synctest.Wait()
				want := int32(1)
				if tc.retry {
					want = 2
				}
				if sends.Load() != want {
					t.Fatalf("send count = %d, want %d; uncertain delivery must not be retried", sends.Load(), want)
				}
			})
		})
	}
}

func TestTaskOwnConversationUncertainFailureNotifies(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	s.runRetry = time.Second
	setTasks(s, Task{ID: "job", Prompt: "Check files", Repeat: repeatOnce, At: time.Now().Add(-time.Minute)})
	first, err := runNow(t, s, "job")
	if err != nil {
		t.Fatal(err)
	}
	waitRuns(t, s, 1)
	before := len(f.pushed())
	var sends atomic.Int32
	f.answerNode(DeviceAssistantSend, func(_ string, raw json.RawMessage) (any, error) {
		var p struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.SessionID != first.SessionID {
			t.Errorf("uncertain resume replaced its conversation: %q", p.SessionID)
		}
		sends.Add(1)
		return nil, rpc.ErrClosed
	})
	synctest.Test(t, func(t *testing.T) {
		task, _ := taskByID(s, "job")
		if _, err := s.runTask(task, task.At, time.Now(), false, false); err == nil {
			t.Fatal("uncertain resume reported success")
		}
		if len(f.pushed()) != before+1 {
			t.Fatal("uncertain resume lost its failure notice")
		}
		<-time.After(10 * s.runRetry)
		synctest.Wait()
		if sends.Load() != 1 {
			t.Fatalf("uncertain resume was retried %d times", sends.Load())
		}
	})
}

func TestTaskUncertainManualRunCancelsPendingRetry(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	s.runRetry = time.Second
	var sends atomic.Int32
	f.answerNode(DeviceAssistantSend, func(string, json.RawMessage) (any, error) {
		if sends.Add(1) == 1 {
			return nil, rpc.NotSent(context.DeadlineExceeded)
		}
		return nil, rpc.ErrClosed
	})
	synctest.Test(t, func(t *testing.T) {
		setTasks(s, Task{ID: "job", Prompt: "Check files", Repeat: repeatOnce, At: time.Now().Add(-time.Minute)})
		task, _ := taskByID(s, "job")
		if _, err := s.runTask(task, task.At, time.Now(), false, false); err == nil {
			t.Fatal("unsent request reported success")
		}
		<-time.After(s.runRetry / 2)
		if _, err := s.runTask(task, task.At, time.Now(), true, false); err == nil {
			t.Fatal("manual run with lost reply reported success")
		}
		<-time.After(10 * s.runRetry)
		synctest.Wait()
		if sends.Load() != 2 {
			t.Fatalf("earlier retry resent a manual run with uncertain delivery: %d sends", sends.Load())
		}
	})
}

func TestTaskPreparationFailureNotifiesAndRetries(t *testing.T) {
	for _, phase := range []string{"model snapshot", "default directory"} {
		t.Run(phase, func(t *testing.T) {
			f := newFakeHub().withMachine()
			st := newSettingsFaultStore()
			s := newServiceOn(t, f, st)
			s.runRetry = time.Second
			if phase == "model snapshot" {
				st.failRead = modelRecordKey("task.job")
			} else {
				f.answerNode(DeviceAssistantPrepare, func(string, json.RawMessage) (any, error) {
					return nil, errors.New("directory unavailable")
				})
			}
			synctest.Test(t, func(t *testing.T) {
				setTasks(s, Task{ID: "job", Prompt: "Check files", Repeat: repeatOnce, At: time.Now().Add(-time.Minute)})
				task, _ := taskByID(s, "job")
				if _, err := s.runTask(task, task.At, time.Now(), false, false); err == nil || f.sent() != 0 {
					t.Fatalf("preparation failure sent: %v, sends=%d", err, f.sent())
				}
				if len(f.pushed()) != 1 {
					t.Fatal("preparation failure silently lost the one-off")
				}
				<-time.After(10 * s.runRetry)
				synctest.Wait()
				_, runs := scheduleState(s)
				if len(runs) != 2 || !runs[1].Retried || f.sent() != 0 {
					t.Fatalf("preparation failure did not retry once: %+v", runs)
				}
			})
		})
	}
}
