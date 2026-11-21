package hub

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

// sentStore keeps sent_messages like the table does.
type sentStore struct {
	*memStore
	mu   sync.Mutex
	rows map[string]string
}

func newSentStore() *sentStore { return &sentStore{memStore: newMemStore(), rows: map[string]string{}} }

func (s *sentStore) ClaimSend(_ context.Context, id, _, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.rows[id]; ok {
		return st, nil
	}
	s.rows[id] = sendSending
	return "", nil
}

func (s *sentStore) SettleSend(_ context.Context, id, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state == "" {
		delete(s.rows, id)
	} else {
		s.rows[id] = state
	}
	return nil
}

// pipeNode connects machine id to an in-process node answering session.send with send; it returns the hub's side of
// the connection, which the test may close like a dropped ssh link.
func pipeNode(t *testing.T, h *Hub, id string, send func(ctx context.Context) error) *rpc.Peer {
	t.Helper()
	m := h.byID[id]
	if m == nil {
		m = newMachine(h, MachineConfig{ID: id, Name: id})
		h.byID[id] = m
		h.machines = append(h.machines, m)
	}
	hubIn, nodeOut := io.Pipe()
	nodeIn, hubOut := io.Pipe()
	hubPeer, nodePeer := rpc.New("h", hubOut), rpc.New("n", nodeOut)
	nodePeer.OnRequest = func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
		if method != "session.send" {
			return map[string]any{}, nil
		}
		return map[string]any{"ok": true}, send(ctx)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = hubPeer.Serve(ctx, hubIn) }()
	go func() { _ = nodePeer.Serve(ctx, nodeIn) }()
	m.mu.Lock()
	m.peer, m.online = hubPeer, true
	m.mu.Unlock()
	return hubPeer
}

const sendPath = "/api/v1/sessions/box/s1/messages"

// An exact approval reply ends at the manager: it has no node or model delivery receipt.
func TestOwnerApprovalReplyDoesNotReachNode(t *testing.T) {
	var typed atomic.Int32
	h, m, manager := undeliveredHub(t, func(context.Context) error {
		typed.Add(1)
		return nil
	})
	manager.approvalTarget = "ix_owner_batch_exact"
	w := sendAs(h, m, `{"text":"发","client_msg_id":"approval-1","approval_reply_to":"ix_owner_batch_exact"}`)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || out["ok"] != true || out["approval_reply"] != true || out["interaction_id"] != manager.approvalTarget || out["delivered"] != nil {
		t.Fatalf("approval response: %d %+v", w.Code, out)
	}
	if typed.Load() != 0 {
		t.Fatalf("exact approval reached the node %d time(s)", typed.Load())
	}
	if kept := manager.keeps(); len(kept) != 0 {
		t.Fatalf("exact approval was kept as an undelivered model message: %+v", kept)
	}
}

// A retry with the same client_msg_id — the app resends what got no answer — is not typed again, also when it comes
// while the first is still being typed.
func TestSendRetryIsNotTypedTwice(t *testing.T) {
	h := newTestHub(t)
	h.store = newSentStore()
	var typed atomic.Int32
	pipeNode(t, h, "box", func(context.Context) error {
		typed.Add(1)
		time.Sleep(100 * time.Millisecond)
		return nil
	})
	mux := h.routes()
	body := `{"text":"你好","client_msg_id":"m1"}`
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, out := call(t, mux, "POST", sendPath, h.cfg.Token, body); code != 200 {
				t.Errorf("%d %v", code, out)
			}
		}()
	}
	wg.Wait()
	if code, _ := call(t, mux, "POST", sendPath, h.cfg.Token, body); code != 200 || typed.Load() != 1 {
		t.Fatalf("typed %d times (last %d)", typed.Load(), code)
	}
	call(t, mux, "POST", sendPath, h.cfg.Token, `{"text":"你好","client_msg_id":"m2"}`)
	if typed.Load() != 2 {
		t.Fatal("a new id is a new message")
	}
}

// The connection to the node broke while the message was being typed (2026-10-31: a reconnect during a send — the
// message arrived, the app was told "rpc: connection closed" and offered a resend). The answer says it may have
// arrived; the app's retry is not typed by the new node, nor after a hub restart.
func TestSendCutOffIsUnknownAndNotRetyped(t *testing.T) {
	store := newSentStore()
	h := newTestHub(t)
	h.store = store
	var typed atomic.Int32
	started := make(chan struct{})
	hubSide := pipeNode(t, h, "box", func(context.Context) error {
		typed.Add(1)
		close(started)
		time.Sleep(time.Second)
		return nil
	})
	go func() {
		<-started
		hubSide.Close(io.ErrUnexpectedEOF)
	}()
	mux := h.routes()
	body := `{"text":"部署一下","client_msg_id":"m1"}`
	code, out := call(t, mux, "POST", sendPath, h.cfg.Token, body)
	if code != 502 || out["delivered"] != "unknown" {
		t.Fatalf("cut off: %d %v", code, out)
	}
	pipeNode(t, h, "box", func(context.Context) error { typed.Add(1); return nil }) // the node reconnected
	if code, out := call(t, mux, "POST", sendPath, h.cfg.Token, body); out["delivered"] != "unknown" || typed.Load() != 1 {
		t.Fatalf("retry after reconnect: %d %v, typed %d", code, out, typed.Load())
	}
	h2 := newTestHub(t) // the hub restarted
	h2.store = store
	pipeNode(t, h2, "box", func(context.Context) error { typed.Add(1); return nil })
	if code, out := call(t, h2.routes(), "POST", sendPath, h2.cfg.Token, body); out["delivered"] != "unknown" || typed.Load() != 1 {
		t.Fatalf("retry after hub restart: %d %v, typed %d", code, out, typed.Load())
	}
}

// Delivered before the hub restarted: the retry is acknowledged. Refused (nothing typed): the retry sends.
func TestSendRetryAfterRestartAndRefusal(t *testing.T) {
	store := newSentStore()
	h := newTestHub(t)
	h.store = store
	var typed atomic.Int32
	pipeNode(t, h, "box", func(context.Context) error { typed.Add(1); return nil })
	body := `{"text":"好","client_msg_id":"m1"}`
	call(t, h.routes(), "POST", sendPath, h.cfg.Token, body)
	h2 := newTestHub(t)
	h2.store = store
	pipeNode(t, h2, "box", func(context.Context) error { typed.Add(1); return nil })
	if code, _ := call(t, h2.routes(), "POST", sendPath, h2.cfg.Token, body); code != 200 || typed.Load() != 1 {
		t.Fatalf("delivered then retried after a restart: %d, typed %d", code, typed.Load())
	}

	refuse := true
	pipeNode(t, h2, "box", func(context.Context) error {
		if refuse {
			refuse = false
			return &rpc.Error{Code: "input_draft", Message: "draft", Data: json.RawMessage(`{"delivered":"none","draft":"x"}`)}
		}
		typed.Add(1)
		return nil
	})
	body = `{"text":"再来","client_msg_id":"m2"}`
	if code, out := call(t, h2.routes(), "POST", sendPath, h2.cfg.Token, body); code != 409 || out["code"] != "input_draft" {
		t.Fatalf("refusal: %d %v", code, out)
	}
	if code, _ := call(t, h2.routes(), "POST", sendPath, h2.cfg.Token, body); code != 200 || typed.Load() != 2 {
		t.Fatalf("retry of a refused send: %d, typed %d", code, typed.Load())
	}
}
