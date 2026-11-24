package node

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type handoffTrackingWriteCloser struct{ closes atomic.Int32 }

func (w *handoffTrackingWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (w *handoffTrackingWriteCloser) Close() error {
	w.closes.Add(1)
	return nil
}

func TestResidentReloadHandoffDrainsTurnAndAdmittedRPC(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &ResidentReload{}
		n := assistantNode(t)
		n.SetResidentReload(r)
		const id = "11111111-2222-3333-4444-555555555555"
		stdin := &handoffTrackingWriteCloser{}
		p := &assistantProc{id: id, agent: "claude", busy: true, exited: make(chan struct{}), stdin: stdin, cmd: &exec.Cmd{}}
		n.procs[id] = p
		liveAssistants.Store(p, struct{}{})
		t.Cleanup(func() { liveAssistants.Delete(p) })

		finishRPC, err := r.enter()
		if err != nil {
			t.Fatal(err)
		}
		var finishOnce sync.Once
		finishAdmitted := func() { finishOnce.Do(finishRPC) }
		releaseRPC := make(chan struct{})
		var releaseOnce sync.Once
		releaseAdmitted := func() { releaseOnce.Do(func() { close(releaseRPC) }) }
		t.Cleanup(releaseAdmitted)
		rpcReplied := make(chan struct{})
		go func() {
			<-releaseRPC
			close(rpcReplied)
			finishAdmitted()
		}()
		t.Cleanup(finishAdmitted)

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		prepared := make(chan error, 1)
		go func() { prepared <- r.Prepare(ctx) }()
		synctest.Wait()
		select {
		case err := <-prepared:
			t.Fatalf("reload finished before the admitted RPC and current turn drained: %v", err)
		default:
		}

		// A virtual hour is an upper bound for this behavioral test, not an assertion about the configured default.
		synctest.Sleep(time.Hour)
		synctest.Wait()
		late, lateErr := r.enter()
		if lateErr == nil {
			late()
		} else {
			// Exercise the real RPC admission path too. A paused node returns before assistant.send can execute a turn.
			params, _ := json.Marshal(map[string]any{"session_id": id, "text": "must not execute"})
			_, sendErr := n.handle(ctx, "assistant.send", params)
			var coded *rpc.Error
			if !errors.As(sendErr, &coded) || coded.Code != nodeproto.CodeRestarting {
				t.Errorf("post-grace assistant.send was not refused before execution: %v", sendErr)
			}
		}
		select {
		case err := <-prepared:
			t.Errorf("reload proceeded while the current turn and admitted RPC were unfinished: %v", err)
		default:
		}
		stoppedEarly := stdin.closes.Load() != 0

		releaseAdmitted()
		<-rpcReplied
		synctest.Wait()
		select {
		case err := <-prepared:
			t.Errorf("reload did not wait for the current turn after the admitted RPC replied: %v", err)
		default:
		}
		p.mu.Lock()
		p.busy = false
		p.mu.Unlock()
		select {
		case err := <-prepared:
			if err != nil {
				t.Fatalf("reload failed after current work drained: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("reload did not proceed after current work drained")
		}

		if lateErr == nil {
			t.Error("resident reload admitted new work after the handoff grace while the current turn and RPC were still draining")
		}
		if stoppedEarly {
			t.Error("resident reload stopped the current headless turn instead of letting it finish")
		}
		if finish, err := r.enter(); err == nil {
			finish()
			t.Error("the drained old resident admitted new work")
		}
	})
}

func TestSessionsListUpdateHandoffControlsHeadlessAdmission(t *testing.T) {
	n := assistantNode(t)
	r := &ResidentReload{}
	n.SetResidentReload(r)
	if _, err := request(t, n, "sessions.list", map[string]any{"update_handoff": true}); err != nil {
		t.Fatal(err)
	}
	done, err := r.enter()
	if err != nil {
		t.Fatalf("headless handoff blocked an unrelated RPC: %v", err)
	}
	done()
	if err := r.enterHeadless(); err == nil {
		t.Fatal("headless handoff admitted a new turn")
	} else {
		var coded *rpc.Error
		if !errors.As(err, &coded) || coded.Code != nodeproto.CodeRestarting {
			t.Fatalf("wrong headless refusal: %v", err)
		}
	}
	if _, err := request(t, n, "sessions.list", map[string]any{"update_handoff": false}); err != nil {
		t.Fatal(err)
	}
	if err := r.enterHeadless(); err != nil {
		t.Fatalf("clearing handoff did not restore headless admission: %v", err)
	}
}

func TestResidentReloadNaturalIdleSkipsHandoffWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &ResidentReload{}
		started := time.Now()
		prepared := make(chan error, 1)
		go func() { prepared <- r.Prepare(context.Background()) }()
		select {
		case err := <-prepared:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("a naturally idle resident waited for the handoff grace")
		}
		if elapsed := time.Since(started); elapsed >= time.Hour {
			t.Fatalf("the natural-idle fast path waited for the grace: %s", elapsed)
		}
	})
}
