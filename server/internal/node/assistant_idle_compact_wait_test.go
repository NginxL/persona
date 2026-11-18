package node

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestIdleCompactionDeliveryWaitFitsRelayBudget(t *testing.T) {
	if idleCompactionDeliveryWait <= 0 || idleCompactionDeliveryWait >= 90*time.Second {
		t.Fatalf("idle delivery wait %s does not fit the manager's 90s relay budget", idleCompactionDeliveryWait)
	}
	if idleCompactionDeliveryWait >= assistantCompactTimeout {
		t.Fatal("delivery still waits for the entire compaction timeout")
	}
}

// The manager does not cancel assistant.send on a caller deadline. Exercise that
// real RPC boundary and the once_id retry path, not just a directly canceled ctx.
func TestIdleCompactionRelayWaitIsBoundedAndRetryable(t *testing.T) {
	n, p, sink := idleCompactFixture(t)
	n.idleCompactWait = 25 * time.Millisecond
	st, _ := n.state.get(p.id)
	since := st.IdleCompaction.Since
	a, b := net.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	caller, receiver := rpc.New("manager", a), rpc.New("node", b)
	receiver.OnRequest = n.handle
	go func() { _ = caller.Serve(ctx, a) }()
	go func() { _ = receiver.Serve(ctx, b) }()
	t.Cleanup(func() { cancel(); _ = a.Close(); _ = b.Close() })
	t.Cleanup(func() {
		idleCompactStream(n, p,
			`{"type":"system","subtype":"compact_boundary","compact_metadata":{"post_tokens":90000}}`,
			`{"type":"result","subtype":"success"}`)
	})
	params := map[string]any{
		"session_id": p.id, "text": "background wake", "is_main": true,
		"idle_compaction": idleCompactPolicy,
		"message_source":  &proto.MessageSource{Kind: proto.SourceSupervision},
		"once_id":         "example-idle-compaction-relay",
	}
	call := func() error {
		callCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		return caller.Call(callCtx, "assistant.send", params, nil)
	}
	done := make(chan error, 1)
	go func() { done <- call() }()
	select {
	case <-sink.compact:
	case <-time.After(3 * time.Second):
		t.Fatal("the relay did not start compaction")
	}
	started := time.Now()
	select {
	case err := <-done:
		if code := refusalCode(err); code != "compacting" {
			t.Fatalf("bounded delivery must return a retryable compacting refusal, got %q (%v)", code, err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle delivery still waits for the full compaction timeout")
	}
	t.Logf("bounded RPC refusal after compact marker: %s", time.Since(started))
	unlocked := make(chan struct{})
	go func() {
		typing := n.typing.lock(p.id)
		life := n.assistantLife.lock(p.id)
		life()
		typing()
		close(unlocked)
	}()
	select {
	case <-unlocked:
	case <-time.After(time.Second):
		t.Fatal("the refused relay retained the main-chat send locks")
	}
	st, _ = n.state.get(p.id)
	if !st.IdleCompaction.Attempted || !st.IdleCompaction.Since.Equal(since) ||
		st.Compaction == nil || st.Compaction.State != proto.CompactionRunning {
		t.Fatalf("bounded delivery changed the idle interval or ended the real compaction: %+v", st)
	}

	// An existing compaction must refuse immediately, not start a fresh wait.
	// A canceled helper context makes the distinction deterministic.
	retryCtx, stop := context.WithCancel(context.WithValue(ctx, idleCompactionInputKey{}, idleCompactionInput{
		At: time.Now(), Policy: &idleCompactPolicy, Source: &proto.MessageSource{Kind: proto.SourceSupervision},
	}))
	stop()
	typing := n.typing.lock(p.id)
	life := n.assistantLife.lock(p.id)
	err := n.idleCompactBeforeInput(retryCtx, p)
	life()
	typing()
	if code := refusalCode(err); code != "compacting" {
		t.Fatalf("in-flight compaction started another wait, got %q (%v)", code, err)
	}
	if code := refusalCode(call()); code != "compacting" {
		t.Fatalf("same once_id retry while compacting: %q", code)
	}
	st, _ = n.state.get(p.id)
	if !st.IdleCompaction.Attempted || !st.IdleCompaction.Since.Equal(since) {
		t.Fatal("a repeated relay consumed another attempt or reset the idle clock")
	}
	if got := sink.String(); strings.Count(got, `"/compact"`) != 1 || strings.Contains(got, "background wake") {
		t.Fatalf("the pending relay was delivered or compacted again: %s", got)
	}

	idleCompactStream(n, p, `{"type":"system","subtype":"compact_boundary","compact_metadata":{"post_tokens":90000}}`)
	if code := refusalCode(call()); code != "compacting" {
		t.Fatalf("a boundary released the pending message before the result: %q", code)
	}
	if strings.Contains(sink.String(), "background wake") {
		t.Fatal("the pending relay was delivered at the boundary")
	}
	idleCompactStream(n, p, `{"type":"result","subtype":"success"}`)
	if err := call(); err != nil {
		t.Fatalf("same once_id could not deliver after the real result: %v", err)
	}
	if err := call(); err != nil {
		t.Fatalf("an acknowledged relay was not idempotent: %v", err)
	}
	if got := sink.String(); strings.Count(got, `"/compact"`) != 1 || strings.Count(got, "background wake") != 1 {
		t.Fatalf("expected one compaction and exactly one original delivery: %s", got)
	}
	t.Log("real result delivered the original once_id exactly once, with one compaction")
}
