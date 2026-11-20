//go:build !windows

package node

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

func TestExternalEmptyStartKeepsItsProcessForFirstMessage(t *testing.T) {
	for _, warm := range []bool{false, true} {
		name := "cold"
		if warm {
			name = "warm"
		}
		t.Run(name, func(t *testing.T) {
			f, launcher, execution, _ := externalAssistantFixture(t, warm)
			id, cwd := execution.External.SessionID, t.TempDir()
			var snapshots atomic.Int32
			f.fh.mu.Lock()
			f.fh.context = viewErrorsAnswer
			f.fh.onContext = func(context.Context) { snapshots.Add(1) }
			f.fh.mu.Unlock()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			got, err := f.n.assistantSend(withNewID(ctx, id), "", cwd, launcher, " \t\n", nil, "default", "", execution)
			if err != nil || got != id {
				t.Fatalf("empty start: id=%q, error=%v", got, err)
			}
			p := f.proc(id)
			if p == nil {
				t.Fatal("empty start lost its native process")
			}
			cancel() // the allocation RPC's lifetime must not own the idle conversation
			if err := f.n.setModes(t.Context(), id, "plan", ""); err != nil {
				t.Fatalf("allocated process did not survive its RPC: %v", err)
			}
			if inputs := emptyStartUserInputs(t, f.file("external-stdin")); len(inputs) != 0 {
				t.Fatalf("empty start sent %d model inputs", len(inputs))
			}
			if snapshots.Load() != 0 || f.fh.ackCount() != 0 {
				t.Fatal("empty start consumed or acknowledged context")
			}
			if st, ok := f.n.state.get(id); !ok || !st.AwaitingFirstMessage {
				t.Fatal("empty start lost its pending first message")
			}
			const message = "First message after external allocation"
			got, err = f.n.assistantSend(t.Context(), id, cwd, launcher, message, nil, "plan", "", execution)
			if err != nil || got != id {
				t.Fatalf("first message: id=%q, error=%v", got, err)
			}
			f.idle(t, id)
			if f.proc(id) != p || p.model.External == nil || *p.model.External != *execution.External {
				t.Fatal("first message replaced the allocated process or its pinned binding")
			}
			inputs := emptyStartUserInputs(t, f.file("external-stdin"))
			if len(inputs) != 1 || len(inputs[0].Message.Content) != 2 ||
				!strings.Contains(inputs[0].Message.Content[0].Text, "<persona-view-errors>") ||
				inputs[0].Message.Content[1].Text != message {
				t.Fatalf("first message lost or duplicated its input/context: %+v", inputs)
			}
			cliWait(t, "the first external message's context receipt", func() bool { return f.fh.ackCount() == 1 })
			if snapshots.Load() != 1 {
				t.Fatalf("read %d snapshots for one message", snapshots.Load())
			}
			if st, _ := f.n.state.get(id); st.AwaitingFirstMessage {
				t.Fatal("delivered message still awaits its first input")
			}
		})
	}
}
