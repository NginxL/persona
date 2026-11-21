package node

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	hostpkg "github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type runtimeUnseenFixture struct {
	n      *Node
	rt     *fakeRuntime
	h      *runtimeConn
	id     string
	path   string
	params json.RawMessage
}

type runtimeUnseenAnswer struct {
	method  string
	res     any
	err     error
	handled bool
}

func newRuntimeUnseenFixture(t *testing.T) *runtimeUnseenFixture {
	t.Helper()
	n, _ := runtimeNode(t)
	path := sdkTranscript(t, n, sdkSession, "ready")
	rt := dialRuntime(t, n, dshHello())
	ready := n.runtimes.ready()
	if len(ready) != 1 {
		t.Fatalf("runtime connections = %d, want 1", len(ready))
	}
	params, err := json.Marshal(map[string]any{"session_id": sdkSession, "text": "hello", "client_msg_id": "m1"})
	if err != nil {
		t.Fatal(err)
	}
	return &runtimeUnseenFixture{n: n, rt: rt, h: ready[0], id: sdkSession, path: path, params: params}
}

func (f *runtimeUnseenFixture) call(method string) runtimeUnseenAnswer {
	res, err, handled := f.n.runtimeHandle(context.Background(), f.h, method, f.params)
	return runtimeUnseenAnswer{method: method, res: res, err: err, handled: handled}
}

func (f *runtimeUnseenFixture) mutateEcho(t *testing.T, change func(*echoRecord)) {
	t.Helper()
	f.n.echoes.mu.Lock()
	defer f.n.echoes.mu.Unlock()
	for _, r := range f.n.echoes.recs[f.id] {
		if r.ClientMsgID == "m1" {
			change(r)
			return
		}
	}
	t.Fatal("no echo record for m1")
}

// ambiguous makes one ordinary runtime send fail in doubt, then gives its echo record transcript evidence that the
// input was not written. runtimeHandle is called directly: the session deliberately is not registered as runtime-owned,
// because readEcho refuses to prove absence from an arbitrary runtime's own items.
func (f *runtimeUnseenFixture) ambiguous(t *testing.T) {
	t.Helper()
	f.rt.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.send" && len(f.rt.got("session.send")) == 1 {
			return nil, &rpc.Error{Code: "send_failed", Message: "answer lost", Data: json.RawMessage(`{"delivered":"unknown"}`)}
		}
		return map[string]any{"ok": true}, nil
	}
	if answer := f.call("session.send"); !answer.handled || !maybeDelivered(answer.err) {
		t.Fatalf("ordinary send = %+v, want an ambiguous reply", answer)
	}
	base := f.n.echoBase(f.id)
	f.mutateEcho(t, func(r *echoRecord) {
		r.In, r.AfterSeq, r.History = base.in, base.seq, base.history
	})
	turnEnd := strings.Replace(echoTurnEndLine(time.Now()), "older", "a1", 1)
	appendEchoTestLine(t, f.path, turnEnd)
	f.n.mu.Lock()
	f.n.agents[f.id] = hostpkg.Agent{Agent: "claude", AgentStatus: proto.StatusIdle, Cwd: "/w/app"}
	f.n.mu.Unlock()
	if check := f.n.checkEcho(context.Background(), f.id, "m1"); check.state != proto.EchoKnownUnseen {
		t.Fatalf("echo state = %s, want known_unseen", check.state)
	}
	if got := len(f.rt.got("session.send")); got != 1 {
		t.Fatalf("ordinary runtime calls = %d, want 1", got)
	}
}

func TestRuntimeHandleSendUnseen(t *testing.T) {
	t.Run("seen answers without forwarding", func(t *testing.T) {
		f := newRuntimeUnseenFixture(t)
		f.n.echoes.expect(echoRecord{SessionID: f.id, ClientMsgID: "m1", Text: "hello", SentAt: time.Now(),
			Line: "1#0", In: echoInTranscript})
		answer := f.call(proto.MethodSessionSendUnseen)
		res, ok := answer.res.(map[string]any)
		if !answer.handled || answer.err != nil || !ok || res["ok"] != true {
			t.Fatalf("seen answer = %+v", answer)
		}
		if got := len(f.rt.got(proto.MethodSessionSendUnseen)); got != 0 {
			t.Fatalf("forwarded %d times, want 0", got)
		}
	})

	t.Run("known unseen replaces one ambiguous attempt", func(t *testing.T) {
		f := newRuntimeUnseenFixture(t)
		f.ambiguous(t)
		start := make(chan struct{})
		answers := make(chan runtimeUnseenAnswer, 2)
		for _, method := range []string{"session.send", proto.MethodSessionSendUnseen} {
			go func() {
				<-start
				answers <- f.call(method)
			}()
		}
		close(start)
		for range 2 {
			answer := <-answers
			if !answer.handled {
				t.Fatalf("%s was not handled", answer.method)
			}
			switch answer.method {
			case proto.MethodSessionSendUnseen:
				if answer.err != nil {
					t.Fatalf("guarded retry: %v", answer.err)
				}
			case "session.send":
				if answer.err != nil && !maybeDelivered(answer.err) {
					t.Fatalf("concurrent ordinary retry: %v", answer.err)
				}
			}
		}
		for range 2 {
			if answer := f.call("session.send"); answer.err != nil {
				t.Fatalf("acknowledged ordinary retry: %v", answer.err)
			}
		}
		if ordinary, guarded := len(f.rt.got("session.send")), len(f.rt.got(proto.MethodSessionSendUnseen)); ordinary != 1 || guarded != 1 {
			t.Fatalf("ordinary calls = %d, guarded calls = %d; want 1 each", ordinary, guarded)
		}
	})

	t.Run("unknown evidence refuses without forwarding", func(t *testing.T) {
		f := newRuntimeUnseenFixture(t)
		answer := f.call(proto.MethodSessionSendUnseen)
		if !answer.handled || rpcCode(answer.err) != proto.CodeEchoUnconfirmed {
			t.Fatalf("unknown evidence answer = %+v", answer)
		}
		if got := len(f.rt.got(proto.MethodSessionSendUnseen)); got != 0 {
			t.Fatalf("forwarded %d times, want 0", got)
		}
	})

	t.Run("changed attempt on recheck refuses without forwarding", func(t *testing.T) {
		f := newRuntimeUnseenFixture(t)
		f.ambiguous(t)
		lock, err := f.n.inputTurn(context.Background(), f.id)
		if err != nil {
			t.Fatal(err)
		}
		unlock := sync.OnceFunc(lock)
		defer unlock()
		answer := make(chan runtimeUnseenAnswer, 1)
		go func() { answer <- f.call(proto.MethodSessionSendUnseen) }()
		waitFor(t, "guarded retry waiting for input", func() bool {
			_, canRetry := f.n.sends.completed("m1")
			return !canRetry
		})
		f.mutateEcho(t, func(r *echoRecord) { r.SentAt = r.SentAt.Add(-time.Second) })
		unlock()
		got := <-answer
		if !got.handled || rpcCode(got.err) != proto.CodeEchoUnconfirmed {
			t.Fatalf("changed-attempt answer = %+v", got)
		}
		if calls := len(f.rt.got(proto.MethodSessionSendUnseen)); calls != 0 {
			t.Fatalf("forwarded %d times, want 0", calls)
		}
		if ordinary := f.call("session.send"); !maybeDelivered(ordinary.err) {
			t.Fatalf("original ambiguous receipt was not restored: %v", ordinary.err)
		}
		if calls := len(f.rt.got("session.send")); calls != 1 {
			t.Fatalf("ordinary runtime calls = %d, want 1", calls)
		}
	})

	t.Run("unsupported passes through as a refusal", func(t *testing.T) {
		f := newRuntimeUnseenFixture(t)
		f.ambiguous(t)
		f.rt.answer = func(method string, params json.RawMessage) (any, error) {
			if method == proto.MethodSessionSendUnseen {
				return nil, &rpc.Error{Code: "unsupported", Message: "unknown method session.send_unseen",
					Data: json.RawMessage(`{"delivered":"none","reason":"older_sdk"}`)}
			}
			return map[string]any{"ok": true}, nil
		}
		answer := f.call(proto.MethodSessionSendUnseen)
		var re *rpc.Error
		if !answer.handled || !errors.As(answer.err, &re) {
			t.Fatalf("unsupported answer = %+v", answer)
		}
		if re.Code != "unsupported" || re.Message != "unknown method session.send_unseen" ||
			string(re.Data) != `{"delivered":"none","reason":"older_sdk"}` {
			t.Fatalf("unsupported changed: code=%q message=%q data=%s", re.Code, re.Message, re.Data)
		}
		if got := len(f.rt.got(proto.MethodSessionSendUnseen)); got != 1 {
			t.Fatalf("guarded runtime calls = %d, want 1", got)
		}
	})
}
