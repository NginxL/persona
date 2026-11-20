package devices

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// heldMem is the settings and held_operations in memory, as db.DB keeps them.
type heldMem struct {
	*memStore
	mu   sync.Mutex
	seq  int64
	rows map[int64]db.HeldOperation
	fail error // every held_operations call fails with it, when set
	// loads counts the loads (RecoverHeldOperations); retryGate, when set, holds a Retry's write until it is closed,
	// and retrying is told when one waits there.
	loads     int
	retryGate chan struct{}
	retrying  chan struct{}
	// holdErr fails a hold, nothing kept; committedErr fails one that was kept (its answer lost after the commit);
	// lookupErr fails every look-up by key.
	holdErr, committedErr, lookupErr error
	// claimLost: the next claim is taken but answers an error (its answer lost); settleErr fails the next write from
	// being sent; retryErr fails Retries.
	claimLost bool
	settleErr error
	retryErr  error
	// beforeDelete, when set, runs (under the lock) as a delete begins: something else changes the row first; so does
	// beforeDeleteTry for a delete of one try. settleTryGate, when set, holds a write of one try until it is closed,
	// and settlingTry is told when one waits there.
	beforeDelete, beforeDeleteTry func(s *heldMem, seq int64)
	// beforeSettle, when set, runs (under the lock) as the next write from being sent begins, of one try or not.
	beforeSettle               func(s *heldMem, seq int64)
	settleTryGate, settlingTry chan struct{}
	deleteTryErr               error // fails the next delete of one try
	// holdGate holds the next hold until it is closed (holdWaiting told when one waits there); unsureGate the next
	// delete of an unsure hold (unsureWaiting told).
	holdGate, holdWaiting, unsureGate, unsureWaiting chan struct{}
}

func newHeldMem() *heldMem {
	return &heldMem{memStore: newMemStore(), rows: map[int64]db.HeldOperation{}}
}

func (s *heldMem) HoldOperation(_ context.Context, op db.HeldOperation) (db.HeldOperation, bool, error) {
	s.mu.Lock()
	gate, waiting := s.holdGate, s.holdWaiting
	s.holdGate = nil
	s.mu.Unlock()
	if gate != nil {
		waiting <- struct{}{}
		<-gate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return db.HeldOperation{}, false, s.fail
	}
	if s.holdErr != nil {
		return db.HeldOperation{}, false, s.holdErr
	}
	if s.committedErr != nil {
		defer func() { s.seq++; op.Seq, op.State = s.seq, db.HeldWaiting; s.rows[op.Seq] = heldShownOf(op) }()
		return db.HeldOperation{}, false, s.committedErr
	}
	for _, r := range s.rows {
		if r.Kind == op.Kind && r.MachineID == op.MachineID && r.SessionID == op.SessionID && r.OpID == op.OpID {
			return r, false, nil
		}
	}
	s.seq++
	op.Seq, op.State, op.Failure, op.Error, op.Attempts, op.TriedAt = s.seq, db.HeldWaiting, "", "", 0, time.Time{}
	op.Call = slices.Clone(op.Call)
	op = heldShownOf(op)
	s.rows[op.Seq] = op
	return op, true, nil
}

// heldShownOf is op with what its row shows of its call (as db.DB reads it in the database).
func heldShownOf(op db.HeldOperation) db.HeldOperation {
	var c struct {
		Caller *devicewire.Caller `json:"caller"`
		Params struct {
			Text   string `json:"text"`
			Images any    `json:"images"`
		} `json:"params"`
	}
	_ = json.Unmarshal(op.Call, &c)
	if c.Caller != nil {
		op.CredentialID = c.Caller.CredentialID
	}
	op.Text, op.Images = c.Params.Text, 0
	if list, ok := c.Params.Images.([]any); ok {
		op.Images = len(list)
	}
	return op
}

func (s *heldMem) HeldOperationOf(_ context.Context, kind, machine, session, opID string) (db.HeldOperation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return db.HeldOperation{}, false, s.fail
	}
	if s.lookupErr != nil {
		return db.HeldOperation{}, false, s.lookupErr
	}
	for _, r := range s.rows {
		if r.Kind == kind && r.MachineID == machine && r.SessionID == session && r.OpID == opID {
			return r, true, nil
		}
	}
	return db.HeldOperation{}, false, nil
}

func (s *heldMem) DeleteUnsureHeld(_ context.Context, kind, machine, session, opID string) (bool, error) {
	s.mu.Lock()
	gate, waiting := s.unsureGate, s.unsureWaiting
	s.unsureGate = nil
	s.mu.Unlock()
	if gate != nil {
		waiting <- struct{}{}
		<-gate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return false, s.fail
	}
	for seq, r := range s.rows {
		if r.Kind == kind && r.MachineID == machine && r.SessionID == session && r.OpID == opID && r.State == db.HeldWaiting && r.Attempts == 0 {
			delete(s.rows, seq)
			return true, nil
		}
	}
	return false, nil
}

func (s *heldMem) HeldOperations(_ context.Context, kind string) ([]db.HeldOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return nil, s.fail
	}
	var out []db.HeldOperation
	for _, r := range s.rows {
		if r.Kind == kind {
			r.Call = nil // as db.DB lists them
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b db.HeldOperation) int { return cmp.Compare(a.Seq, b.Seq) })
	return out, nil
}

func (s *heldMem) RecoverHeldOperations(_ context.Context, kind string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if s.fail != nil {
		return 0, s.fail
	}
	var n int64
	for seq, r := range s.rows {
		if r.Kind == kind && r.State == db.HeldSending {
			r.State, r.Failure, r.Error = db.HeldFailed, db.HeldUnclear, ""
			s.rows[seq] = r
			n++
		}
	}
	return n, nil
}

func (s *heldMem) ClaimHeldOperation(_ context.Context, seq int64, at time.Time) (db.HeldOperation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return db.HeldOperation{}, false, s.fail
	}
	r, ok := s.rows[seq]
	if !ok || r.State != db.HeldWaiting {
		return db.HeldOperation{}, false, nil
	}
	r.State, r.TriedAt, r.Attempts = db.HeldSending, at, r.Attempts+1
	s.rows[seq] = r
	if s.claimLost {
		s.claimLost = false
		return db.HeldOperation{}, false, errors.New("the claim's answer was lost")
	}
	return r, true, nil
}

func (s *heldMem) SettleHeldOperation(_ context.Context, seq int64, from, state, failure, reason string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return false, s.fail
	}
	if from == db.HeldSending {
		s.runBeforeSettle(seq)
	}

	r, ok := s.rows[seq]
	if !ok || r.State != from {
		return false, nil
	}
	r.State, r.Failure, r.Error = state, failure, reason
	s.rows[seq] = r
	return true, nil
}

func (s *heldMem) SettleHeldTry(_ context.Context, seq int64, attempts int, state, failure, reason string) (bool, error) {
	s.mu.Lock()
	gate, settling := s.settleTryGate, s.settlingTry
	s.mu.Unlock()
	if gate != nil {
		settling <- struct{}{}
		<-gate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return false, s.fail
	}
	if err := s.settleErr; err != nil {
		s.settleErr = nil
		return false, err
	}
	s.runBeforeSettle(seq)
	r, ok := s.rows[seq]
	if !ok || r.State != db.HeldSending || r.Attempts != attempts {
		return false, nil
	}
	r.State, r.Failure, r.Error = state, failure, reason
	s.rows[seq] = r
	return true, nil
}

func (s *heldMem) DeleteHeldTry(_ context.Context, seq int64, attempts int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return false, s.fail
	}
	if f := s.beforeDeleteTry; f != nil {
		s.beforeDeleteTry = nil
		f(s, seq)
	}
	if err := s.deleteTryErr; err != nil {
		s.deleteTryErr = nil
		return false, err
	}
	r, ok := s.rows[seq]
	if !ok || r.State != db.HeldSending || r.Attempts != attempts {
		return false, nil
	}
	delete(s.rows, seq)
	return true, nil
}

func (s *heldMem) RetryHeldOperation(_ context.Context, seq int64, at time.Time) (bool, bool, error) {
	s.mu.Lock()
	gate, retrying := s.retryGate, s.retrying
	s.mu.Unlock()
	if gate != nil {
		retrying <- struct{}{}
		<-gate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return false, false, s.fail
	}
	if s.retryErr != nil {
		return false, false, s.retryErr
	}
	r, ok := s.rows[seq]
	if !ok || r.State != db.HeldFailed || r.Failure == db.HeldSignedOut {
		return false, false, nil
	}
	check := r.Failure == db.HeldUnclear
	r.State, r.Failure, r.Error, r.HeldAt = db.HeldWaiting, "", "", at
	if check {
		r.Failure = db.HeldUnclear
	}
	s.rows[seq] = r
	return true, check, nil
}

func (s *heldMem) RenewHeldOperation(_ context.Context, seq int64, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return false, s.fail
	}
	r, ok := s.rows[seq]
	if !ok || r.State != db.HeldWaiting {
		return false, nil
	}
	r.HeldAt = at
	s.rows[seq] = r
	return true, nil
}

func (s *heldMem) loadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads
}

func (s *heldMem) DeleteHeldOperation(_ context.Context, seq int64, states ...string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return false, s.fail
	}
	if f := s.beforeDelete; f != nil {
		s.beforeDelete = nil
		f(s, seq)
	}
	r, ok := s.rows[seq]
	if !ok || len(states) > 0 && !slices.Contains(states, r.State) || len(states) == 0 && r.State == db.HeldSending {
		return false, nil
	}
	delete(s.rows, seq)
	return true, nil
}

// all are the rows, in order, their calls with them.
func (s *heldMem) all() []db.HeldOperation {
	var out []db.HeldOperation
	for _, r := range s.snapshot() {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b db.HeldOperation) int { return cmp.Compare(a.Seq, b.Seq) })
	return out
}

func (s *heldMem) snapshot() map[int64]db.HeldOperation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int64]db.HeldOperation, len(s.rows))
	for k, v := range s.rows {
		out[k] = v
	}
	return out
}

func (s *heldMem) setFail(err error) {
	s.mu.Lock()
	s.fail = err
	s.mu.Unlock()
}

// heldNode is a node in this process that records the messages it is sent; gate, when set, keeps a send waiting
// until it is closed, and answer, when set, is what a send gets.
type heldNode struct {
	mu     sync.Mutex
	sends  []map[string]any
	conn   net.Conn
	peer   *rpc.Peer
	gate   chan struct{}
	answer func(params map[string]any) (any, error)
	echo   func(proto.SessionEchoParams) proto.SessionEchoState
	// guarded records sends through session.send_unseen, including ones the node declines.
	guarded []string
	listed  []string // the sessions it lists; nil: s1 and s2
	// items are what session.items answers, by session; reads counts those calls. itemsGate, when set, holds a read
	// until it is closed (reading is told when one waits there); itemsErr fails the next read.
	items     map[string][]proto.Item
	reads     int
	itemsGate chan struct{}
	reading   chan struct{}
	itemsErr  error
	itemsFail error // fails every read
}

func (n *heldNode) itemReads() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.reads
}

// list has the node list these sessions from now on and say so (sessions.changed).
func (n *heldNode) list(ids ...string) {
	n.mu.Lock()
	n.listed = ids
	p := n.peer
	n.mu.Unlock()
	_ = p.Notify("sessions.changed", map[string]any{"sessions": n.sessions()})
}

func (n *heldNode) sessions() []proto.Session {
	n.mu.Lock()
	defer n.mu.Unlock()
	ids := n.listed
	if ids == nil {
		ids = []string{"s1", "s2"}
	}
	var out []proto.Session
	for _, id := range ids {
		out = append(out, proto.Session{SessionID: id, Status: proto.StatusIdle})
	}
	return out
}

func (n *heldNode) dial(context.Context) (net.Conn, error) {
	managerSide, nodeSide := net.Pipe()
	p := rpc.New("n", nodeSide)
	p.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case "node.info":
			info := proto.NodeInfo{Hostname: "box", OS: "linux", Arch: "amd64", Home: "/home/me", Version: "test"}
			if n.echo != nil {
				info.Protocol = nodeproto.Version
				info.Capabilities = []string{nodeproto.Sessions, nodeproto.SessionEchoStatus}
			}
			return info, nil
		case "sessions.list":
			return map[string]any{"sessions": n.sessions()}, nil
		case "session.items":
			var q struct {
				SessionID string `json:"session_id"`
			}
			_ = json.Unmarshal(params, &q)
			n.mu.Lock()
			n.reads++
			gate, reading, fail := n.itemsGate, n.reading, cmp.Or(n.itemsErr, n.itemsFail)
			n.itemsErr = nil
			n.mu.Unlock()
			if gate != nil {
				reading <- struct{}{}
				<-gate
			}
			if fail != nil {
				return nil, fail
			}
			n.mu.Lock()
			items := n.items[q.SessionID]
			n.mu.Unlock()
			return map[string]any{"items": items}, nil
		case proto.MethodSessionEchoStatus:
			if n.echo == nil {
				return nil, fmt.Errorf("unknown method %s", method)
			}
			var p proto.SessionEchoParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			return proto.SessionEchoStatus{State: n.echo(p)}, nil
		case "session.send", proto.MethodSessionSendUnseen:
			var m map[string]any
			_ = json.Unmarshal(params, &m)
			n.mu.Lock()
			if method == proto.MethodSessionSendUnseen {
				n.guarded = append(n.guarded, m["client_msg_id"].(string))
			}
			n.sends = append(n.sends, m)
			gate, answer := n.gate, n.answer
			n.mu.Unlock()
			if gate != nil {
				<-gate
			}
			if answer != nil {
				return answer(m)
			}
			return map[string]any{"ok": true}, nil
		}
		return map[string]any{}, nil
	}
	n.mu.Lock()
	n.conn, n.peer = nodeSide, p
	n.mu.Unlock()
	go func() { _ = p.Serve(context.Background(), nodeSide); nodeSide.Close() }()
	return managerSide, nil
}

// sent are the client_msg_ids of the messages sent so far, in order.
func (n *heldNode) sent() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, m := range n.sends {
		id, _ := m["client_msg_id"].(string)
		out = append(out, id)
	}
	return out
}

func (n *heldNode) drop() {
	n.mu.Lock()
	c := n.conn
	n.mu.Unlock()
	_ = c.Close()
}

// heldDevices is devices that hold messages (an in-memory store, the held loop running), tick its loop's period;
// published counts the rows' republications per machine.
func heldDevices(t *testing.T, st *heldMem, tick time.Duration) (*Devices, *published) {
	t.Helper()
	return heldDevicesAt(t, st, tick, nil)
}

// heldDevicesAt is heldDevices on clock now (nil: the time), set before its loop first looks.
func heldDevicesAt(t *testing.T, st *heldMem, tick time.Duration, now func() time.Time) (*Devices, *published) {
	t.Helper()
	oldTick := heldTick
	heldTick = tick
	d := newTestDevices(t)
	d.opts.Store = st
	d.held.now = now
	pub := &published{n: map[string]int{}}
	d.opts.HeldChanged = pub.add
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); d.heldLoop(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		waited := make(chan struct{})
		go func() { d.workers.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-time.After(10 * time.Second):
			t.Error("the held deliveries did not end")
		}
		heldTick = oldTick
	})
	eventually(t, 5*time.Second, "held messages loaded", func() bool {
		d.held.mu.Lock()
		defer d.held.mu.Unlock()
		return d.held.loaded
	})
	return d, pub
}

// heldIdle waits until no delivery of d's held messages runs: a wake marks each session it starts on before it
// returns, and the delivery clears that mark as it stops, so what the wakes before this started has acted by then.
func heldIdle(t *testing.T, d *Devices) {
	t.Helper()
	eventually(t, 5*time.Second, "the held deliveries done", func() bool {
		d.held.mu.Lock()
		defer d.held.mu.Unlock()
		return len(d.held.sending) == 0
	})
}

// heldUnconfirmedLine is the line under a message left to the owner after it was looked for.
const heldUnconfirmedLine = "Couldn't confirm it arrived. Retry to send it anyway, or delete it."

// retryAndSend is the owner's Retry of a message that may have arrived, then, once its transcript was read without it
// (unconfirmed), the Retry that sends it anyway.
func retryAndSend(t *testing.T, d *Devices, mux http.Handler, sid, id string) {
	t.Helper()
	path := "/api/v1/sessions/mac/" + sid + "/held/" + id + "/retry"
	if code, body := personCall(t, mux, "POST", path); code != http.StatusOK || body["ok"] != true {
		t.Fatalf("retry: %d %v", code, body)
	}
	eventually(t, 5*time.Second, id+" unconfirmed", func() bool {
		for _, m := range d.HeldMessages("mac", i18n.En)[sid] {
			if m.ID == id {
				return m.Error == heldUnconfirmedLine
			}
		}
		return false
	})
	if code, body := personCall(t, mux, "POST", path); code != http.StatusOK || body["ok"] != true {
		t.Fatalf("retry anyway: %d %v", code, body)
	}
}

// heldTicks does what the held loop does on each tick, times over, and waits until what they started has acted.
func heldTicks(t *testing.T, d *Devices, times int) {
	t.Helper()
	ctx := context.Background()
	for range times {
		d.heldRepair(ctx)
		d.heldExpire(ctx)
		d.held.mu.Lock()
		var machines []string
		for machine := range d.held.rows {
			machines = append(machines, machine)
		}
		d.held.mu.Unlock()
		for _, machine := range machines {
			d.heldWake(machine)
		}
		heldIdle(t, d)
	}
}

type published struct {
	mu sync.Mutex
	n  map[string]int
}

func (p *published) add(machine string) {
	p.mu.Lock()
	p.n[machine]++
	p.mu.Unlock()
}

func (p *published) count(machine string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n[machine]
}

// offlineMachine is a machine whose node is not connected (no run loop).
func offlineMachine(d *Devices, id, name string) *Machine {
	m := newMachine(d, MachineConfig{ID: id, Name: name})
	d.addEntry(m)
	return m
}

// laterMachine runs machine id on node n, whose node can be reached only once up is closed.
func laterMachine(t *testing.T, d *Devices, id, name string, n *heldNode, up chan struct{}) *Machine {
	t.Helper()
	m := newMachine(d, MachineConfig{ID: id, Name: name, Kind: "ssh"})
	m.dialNode = func(ctx context.Context) (net.Conn, error) {
		select {
		case <-up:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return n.dial(ctx)
	}
	d.addEntry(m)
	runMachine(t, m)
	return m
}

// heldSend is the hub's send of message id with text to session sid of machine, marked as one that may be held.
func heldSend(machine, sid, id, text string) devicewire.NodeCall {
	params, _ := json.Marshal(map[string]any{"session_id": sid, "client_msg_id": id, "text": text})
	return devicewire.NodeCall{MachineID: machine, Method: "session.send", Params: params, TimeoutMS: 2000, Request: "r-" + id,
		Caller:     &devicewire.Caller{Kind: "device", CredentialID: "phone"},
		Submission: &devicewire.Submission{ID: "sub-" + id, Kind: "human_input", OriginalText: text},
		Authority:  &devicewire.ShareAuthority{Issuer: "hub", Machine: machine, Generation: "g1"},
		Hold:       &devicewire.Hold{Lang: "en"}}
}

// heldAnswerOf is a CodeHeld answer's held message; it fails the test for any other answer.
func heldAnswerOf(t *testing.T, out json.RawMessage, err error) proto.UndeliveredMessage {
	t.Helper()
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != devicewire.CodeHeld {
		t.Fatalf("not held: %s %v", out, err)
	}
	var a devicewire.HeldAnswer
	if json.Unmarshal(re.Data, &a) != nil || a.Delivered != "later" || a.Undelivered == nil {
		t.Fatalf("held answer %s", re.Data)
	}
	return *a.Undelivered
}

func heldIDs(list []proto.UndeliveredMessage) []string {
	var out []string
	for _, m := range list {
		out = append(out, m.ID)
	}
	return out
}

// A message to an Agent whose computer is not connected is held: the hub gets CodeHeld with the message as its row
// shows it, the call is kept as the hub sent it (caller, submission and share authority; not its hold, wait or request
// key), and the same message sent again is the same held one. A send the hub did not mark fails as before.
func TestHeldWhenTheComputerIsAway(t *testing.T) {
	st := newHeldMem()
	d, pub := heldDevices(t, st, time.Hour)
	offlineMachine(d, "mac", "Studio Mac")
	ctx := context.Background()

	out, err := d.Call(ctx, heldSend("mac", "s1", "m1", "hello"))
	got := heldAnswerOf(t, out, err)
	want := proto.UndeliveredMessage{ID: "m1", Text: "hello", At: got.At, State: proto.UndeliveredWaiting, Computer: "Studio Mac",
		Error: "Will send when Studio Mac is back"}
	if got != want || got.At.IsZero() {
		t.Fatalf("held = %+v, want %+v", got, want)
	}
	rows := st.all()
	if len(rows) != 1 || rows[0].MachineID != "mac" || rows[0].SessionID != "s1" || rows[0].OpID != "m1" || rows[0].Lang != "en" ||
		rows[0].State != db.HeldWaiting {
		t.Fatalf("stored %+v", rows)
	}
	var kept map[string]json.RawMessage
	_ = json.Unmarshal(rows[0].Call, &kept)
	for _, k := range []string{"caller", "submission", "share_authority", "params", "method", "machine_id"} {
		if kept[k] == nil {
			t.Errorf("the stored call lost %s: %s", k, rows[0].Call)
		}
	}
	for _, k := range []string{"hold", "timeout_ms", "request"} {
		if kept[k] != nil {
			t.Errorf("the stored call keeps %s: %s", k, rows[0].Call)
		}
	}
	if list := d.HeldMessages("mac", i18n.En)["s1"]; len(list) != 1 || list[0] != want {
		t.Fatalf("the row shows %+v", list)
	}
	eventually(t, 2*time.Second, "the rows republished", func() bool { return pub.count("mac") > 0 })

	// The same message again (the app retried it): the same one, held once.
	out, err = d.Call(ctx, heldSend("mac", "s1", "m1", "hello"))
	if again := heldAnswerOf(t, out, err); again.ID != "m1" || len(st.all()) != 1 {
		t.Fatalf("held twice: %+v, %d rows", again, len(st.all()))
	}

	// Not marked by the hub, or without its own id: machine_offline as before, nothing kept.
	plain := heldSend("mac", "s1", "m2", "x")
	plain.Hold = nil
	noID := heldSend("mac", "s1", "", "x")
	for _, p := range []devicewire.NodeCall{plain, noID} {
		var re *rpc.Error
		if _, err := d.Call(ctx, p); !errors.As(err, &re) || re.Code != devicewire.CodeMachineOffline {
			t.Errorf("%s: %v", p.Params, err)
		}
	}
	if len(st.all()) != 1 {
		t.Fatalf("kept %d", len(st.all()))
	}
	// A store that does not keep it: the failure as before.
	st.setFail(errors.New("database down"))
	var re *rpc.Error
	if _, err := d.Call(ctx, heldSend("mac", "s2", "m3", "x")); !errors.As(err, &re) || re.Code != devicewire.CodeMachineOffline {
		t.Fatalf("store down: %v", err)
	}
}

// Once the computer's node is back (configured and listed), what was held for it goes at once, in order per session,
// each once, as the hub sent it (the owner's input mark that its caller and submission give, its client_msg_id), and
// leaves the rows. A newer message to a session with held ones waits behind them.
func TestHeldMessagesGoWhenTheComputerIsBack(t *testing.T) {
	st := newHeldMem()
	d, pub := heldDevices(t, st, time.Hour) // no tick: only the connection wakes them
	n := &heldNode{}
	up := make(chan struct{})
	m := laterMachine(t, d, "mac", "Studio Mac", n, up)
	ctx := context.Background()
	for _, c := range []struct{ sid, id string }{{"s1", "a"}, {"s2", "x"}, {"s1", "b"}} {
		out, err := d.Call(ctx, heldSend("mac", c.sid, c.id, "text "+c.id))
		heldAnswerOf(t, out, err)
	}
	if got := heldIDs(d.HeldMessages("mac", i18n.En)["s1"]); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("s1 holds %v", got)
	}
	before := pub.count("mac")

	close(up)
	eventually(t, 5*time.Second, "the held messages sent", func() bool { return len(n.sent()) == 3 && len(st.all()) == 0 })
	got := n.sent()
	if i, j := slices.Index(got, "a"), slices.Index(got, "b"); i < 0 || j < i || !slices.Contains(got, "x") {
		t.Fatalf("sent %v: not in order", got)
	}
	n.mu.Lock()
	for _, s := range n.sends {
		if s["owner_input"] != true || !strings.HasPrefix(s["text"].(string), "text ") {
			t.Errorf("sent %v: not as the hub sent it", s)
		}
	}
	n.mu.Unlock()
	if d.HeldMessages("mac", i18n.En) != nil {
		t.Fatalf("still shown: %v", d.HeldMessages("mac", i18n.En))
	}
	eventually(t, 2*time.Second, "the rows republished", func() bool { return pub.count("mac") > before })

	// Each once: another wake sends nothing again.
	d.heldWake("mac")
	heldIdle(t, d)
	if len(n.sent()) != 3 {
		t.Fatalf("sent again: %v", n.sent())
	}
	// Online, nothing held: a message goes straight to the node.
	if out, err := d.Call(ctx, heldSend("mac", "s1", "c", "now")); err != nil || !m.reachable() {
		t.Fatalf("online send: %s %v", out, err)
	}
	if got := n.sent(); len(got) != 4 || got[3] != "c" {
		t.Fatalf("sent %v", got)
	}
}

func TestHeldMessagesFromSignedOutDeviceAreDeleteOnly(t *testing.T) {
	for _, event := range []bool{true, false} {
		name := "received_event"
		if !event {
			name = "missed_event"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st := newHeldMem()
			d, _ := heldDevices(t, st, time.Hour)
			n := &heldNode{}
			up := make(chan struct{})
			laterMachine(t, d, "mac", "Studio Mac", n, up)

			for _, id := range []string{"signed-out", "other-device"} {
				p := heldSend("mac", "s1", id, id)
				p.Caller.CredentialID = id
				out, err := d.Call(ctx, p)
				if row := heldAnswerOf(t, out, err); row.State != proto.UndeliveredWaiting {
					t.Fatalf("initial row: %+v", row)
				}
			}
			assertFailed := func() {
				t.Helper()
				rows := d.HeldMessages("mac", i18n.En)["s1"]
				if len(rows) == 0 || rows[0].ID != "signed-out" || rows[0].State != proto.UndeliveredFailed ||
					!rows[0].DeleteOnly || rows[0].Error != "Not sent: the device that sent it was signed out" {
					t.Fatalf("held rows: %+v", rows)
				}
			}
			host := d.host()
			if event {
				raw, err := json.Marshal(bus.DeviceCredential{ID: "signed-out"})
				if err != nil || !d.Event(bus.EventCredentialRevoked, raw) {
					t.Fatalf("revocation event: %v", err)
				}
				assertFailed()
				rows := d.HeldMessages("mac", i18n.En)["s1"]
				if len(rows) != 2 || rows[1].ID != "other-device" || rows[1].State != proto.UndeliveredWaiting || rows[1].DeleteOnly {
					t.Fatalf("other device's row: %+v", rows)
				}
			} else {
				host.mu.Lock()
				host.revokedDevices = map[string]bool{"signed-out": true}
				host.credentialCheckErr = errors.New("hub temporarily unavailable")
				host.mu.Unlock()
			}

			close(up)
			if !event {
				eventually(t, 5*time.Second, "the first authorization check deferred delivery", func() bool {
					rows := st.all()
					return len(rows) == 2 && rows[0].State == db.HeldWaiting && rows[0].Attempts > 0
				})
				heldIdle(t, d)
				if got := n.sent(); len(got) != 0 {
					t.Fatalf("sent while the credential could not be checked: %q", got)
				}
				host.mu.Lock()
				host.credentialCheckErr = nil
				host.mu.Unlock()
				d.heldWake("mac")
			}
			eventually(t, 5*time.Second, "only the other device's message delivered", func() bool {
				return len(n.sent()) > 0
			})
			heldIdle(t, d)
			if got := n.sent(); !slices.Equal(got, []string{"other-device"}) {
				t.Fatalf("sent %q", got)
			}
			assertFailed()
			if rows := st.all(); len(rows) != 1 || rows[0].Failure != db.HeldSignedOut {
				t.Fatalf("persisted failure: %+v", rows)
			}

			mux := d.Handler(http.NotFoundHandler())
			path := "/api/v1/sessions/mac/s1/held/signed-out"
			if code, body := personCall(t, mux, http.MethodPost, path+"/retry"); code != http.StatusConflict {
				t.Fatalf("retry: %d %v", code, body)
			}
			if code, body := personCall(t, mux, http.MethodDelete, path); code != http.StatusOK || body["ok"] != true {
				t.Fatalf("delete: %d %v", code, body)
			}
			d.heldWake("mac")
			heldIdle(t, d)
			if got := n.sent(); !slices.Equal(got, []string{"other-device"}) || len(d.HeldMessages("mac", i18n.En)) != 0 || len(st.all()) != 0 {
				t.Fatalf("after delete, sent %q, stored %+v", got, st.all())
			}
		})
	}
}

// A try cut off by the connection dropping with it out may have arrived: it is never sent again by itself, the row
// says so, and the later ones of its session wait behind it, a new one too, even with the computer back; the owner's
// Retry sends it again, then the rest.
func TestHeldUnclearIsNotSentAgain(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, 50*time.Millisecond)
	n := &heldNode{gate: make(chan struct{})}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		out, err := d.Call(ctx, heldSend("mac", "s1", id, id))
		heldAnswerOf(t, out, err)
	}
	close(up)
	eventually(t, 5*time.Second, "a sent", func() bool { return len(n.sent()) == 1 })
	if list := d.HeldMessages("mac", i18n.En)["s1"]; list[0].Error != "Sending…" || list[0].State != proto.UndeliveredWaiting {
		t.Fatalf("while sent: %+v", list)
	}
	n.drop() // the connection drops with a out
	gate := n.gate
	n.mu.Lock()
	n.gate = nil
	n.mu.Unlock()
	close(gate)
	eventually(t, 5*time.Second, "a unclear", func() bool {
		rows := st.all()
		return len(rows) == 2 && rows[0].State == db.HeldFailed && rows[0].Failure == db.HeldUnclear
	})
	list := d.HeldMessages("mac", i18n.En)["s1"]
	if list[0].State != proto.UndeliveredFailed || list[0].Error != "Might not have arrived. Retry checks the chat first." ||
		list[1].State != proto.UndeliveredWaiting || list[1].Error != "Waits behind the message above" {
		t.Fatalf("rows %+v", list)
	}
	// Back and ticking: neither goes; a new message waits behind them.
	eventually(t, 10*time.Second, "back", func() bool { return d.machineByID("mac").reachable() })
	out, err := d.Call(ctx, heldSend("mac", "s1", "c", "c"))
	if held := heldAnswerOf(t, out, err); held.Error != "Waits behind the message above" {
		t.Fatalf("c: %+v", held)
	}
	heldTicks(t, d, 3)
	if got := n.sent(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("sent %v", got)
	}
	// The owner retries a: looked for first (not there: left to the owner, nothing sent), then sent anyway; then b
	// and c.
	retryAndSend(t, d, d.Handler(http.NotFoundHandler()), "s1", "a")
	eventually(t, 5*time.Second, "all sent", func() bool { return len(st.all()) == 0 })
	if got := n.sent(); !slices.Equal(got, []string{"a", "a", "b", "c"}) {
		t.Fatalf("sent %v", got)
	}
}

// A node's refusal (nothing typed) fails the message with the node's words; it does not hold the next one back.
// A new message to its session then goes straight to the node.
func TestHeldRefusedDoesNotBlock(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{answer: func(m map[string]any) (any, error) {
		if m["client_msg_id"] == "a" {
			return nil, &rpc.Error{Code: "input_draft", Message: "a draft is in the way", Data: json.RawMessage(`{"delivered":"none"}`)}
		}
		return map[string]any{"ok": true}, nil
	}}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		out, err := d.Call(ctx, heldSend("mac", "s1", id, id))
		heldAnswerOf(t, out, err)
	}
	close(up)
	eventually(t, 5*time.Second, "b sent", func() bool { return slices.Equal(n.sent(), []string{"a", "b"}) && len(st.all()) == 1 })
	list := d.HeldMessages("mac", i18n.En)["s1"]
	if len(list) != 1 || list[0].ID != "a" || list[0].State != proto.UndeliveredFailed || list[0].Error != "Not sent: a draft is in the way" {
		t.Fatalf("rows %+v", list)
	}
	if out, err := d.Call(ctx, heldSend("mac", "s1", "c", "c")); err != nil {
		t.Fatalf("c: %s %v", out, err)
	}
	if got := n.sent(); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("sent %v", got)
	}
}

// A day after it was held a message still waiting fails as expired; Retry gives it a new day.
func TestHeldExpiresAfterADay(t *testing.T) {
	st := newHeldMem()
	var mu sync.Mutex
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	d, _ := heldDevicesAt(t, st, time.Hour, clock)
	offlineMachine(d, "mac", "Studio Mac")
	ctx := context.Background()
	out, err := d.Call(ctx, heldSend("mac", "s1", "a", "a"))
	heldAnswerOf(t, out, err)
	mu.Lock()
	now = now.Add(23 * time.Hour)
	mu.Unlock()
	d.heldExpire(ctx)
	if st.all()[0].State != db.HeldWaiting {
		t.Fatal("expired early")
	}
	mu.Lock()
	now = now.Add(time.Hour)
	mu.Unlock()
	d.heldExpire(ctx)
	if r := st.all()[0]; r.State != db.HeldFailed || r.Failure != db.HeldExpired {
		t.Fatalf("stored %+v", r)
	}
	if list := d.HeldMessages("mac", i18n.En)["s1"]; list[0].State != proto.UndeliveredFailed || list[0].Error != "Not sent: Studio Mac was away for 24 hours" {
		t.Fatalf("row %+v", list)
	}
	// Expired does not hold a new message back: it is held as the computer is away, after it.
	out, err = d.Call(ctx, heldSend("mac", "s1", "b", "b"))
	if held := heldAnswerOf(t, out, err); held.Error != "Will send when Studio Mac is back" {
		t.Fatalf("b: %+v", held)
	}
	found, err := d.HeldRetry(ctx, "s1", "a")
	if !found || err != nil {
		t.Fatalf("retry: %v %v", found, err)
	}
	if r := st.all()[0]; r.State != db.HeldWaiting || !r.HeldAt.Equal(clock()) || r.Failure != "" {
		t.Fatalf("after retry %+v", r)
	}
	if list := d.HeldMessages("mac", i18n.En)["s1"]; list[0].State != proto.UndeliveredWaiting {
		t.Fatalf("row %+v", list)
	}
}

// Delete takes a waiting or failed message away, from a full device only; while it is being sent it is refused (409),
// and a message there is not is 404.
func TestHeldDelete(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{gate: make(chan struct{})}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	ctx := context.Background()
	for _, c := range []struct{ sid, id string }{{"s1", "a"}, {"s2", "x"}} {
		out, err := d.Call(ctx, heldSend("mac", c.sid, c.id, c.id))
		heldAnswerOf(t, out, err)
	}
	mux := d.Handler(http.NotFoundHandler())
	if code, body := clipCall(t, mux, "DELETE", "/api/v1/sessions/mac/s2/held/x", "", "answer"); code != http.StatusForbidden || body["code"] != proto.CodeReadOnly {
		t.Fatalf("answer-only device: %d %v", code, body)
	}
	// A full role that is not a person's own device (the hub's token, a pairing code): a Retry would send it again as
	// its sender.
	for _, method := range []string{"DELETE", "POST"} {
		path := "/api/v1/sessions/mac/s2/held/x"
		if method == "POST" {
			path += "/retry"
		}
		if code, body := clipCall(t, mux, method, path, "", "full"); code != http.StatusForbidden || body["code"] != proto.CodeReadOnly {
			t.Fatalf("%s from a full role without a person: %d %v", method, code, body)
		}
	}
	answerPerson := httptest.NewRequest("DELETE", "/api/v1/sessions/mac/s2/held/x", nil) // a person, but answer-only
	answerPerson.Header.Set(assistantwire.RoleHeader, "answer")
	answerPerson.Header.Set(assistantwire.PersonHeader, "1")
	rec := httptest.NewRecorder()
	if mux.ServeHTTP(rec, answerPerson); rec.Code != http.StatusForbidden {
		t.Fatalf("answer-only person: %d", rec.Code)
	}
	if len(st.all()) != 2 {
		t.Fatalf("changed without a person's full device: %+v", st.all())
	}
	if code, body := personCall(t, mux, "DELETE", "/api/v1/sessions/mac/s2/held/x"); code != http.StatusOK || body["ok"] != true {
		t.Fatalf("delete: %d %v", code, body)
	}
	if code, _ := personCall(t, mux, "DELETE", "/api/v1/sessions/mac/s2/held/x"); code != http.StatusNotFound {
		t.Fatalf("again: %d", code)
	}
	if code, _ := personCall(t, mux, "POST", "/api/v1/sessions/mac/s2/held/x/retry"); code != http.StatusNotFound {
		t.Fatalf("retry of a deleted one: %d", code)
	}
	if found, err := d.HeldDelete(ctx, "s2", "x"); found || err != nil {
		t.Fatalf("assistant's delete of a deleted one: %v %v", found, err)
	}
	close(up)
	eventually(t, 5*time.Second, "a being sent", func() bool { return len(n.sent()) == 1 })
	if code, body := personCall(t, mux, "DELETE", "/api/v1/sessions/mac/s1/held/a"); code != http.StatusConflict || body["code"] != "conflict" {
		t.Fatalf("while sent: %d %v", code, body)
	}
	found, err := d.HeldDelete(i18n.With(ctx, i18n.En), "s1", "a")
	var re *rpc.Error
	if !found || !errors.As(err, &re) || re.Code != "conflict" || re.Message != "This message is being sent right now. Try again in a moment." {
		t.Fatalf("assistant's delete while sent: %v %v", found, err)
	}
	close(n.gate)
	eventually(t, 5*time.Second, "a sent", func() bool { return len(st.all()) == 0 })
	if got := n.sent(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("sent %v", got)
	}
}

// A manager that starts again finds what the last one held: a message it left being sent may have arrived (unclear,
// never sent again by itself, the next one waits behind it); one left waiting goes once the computer is back.
func TestHeldAcrossARestart(t *testing.T) {
	st := newHeldMem()
	call := func(sid, id string) json.RawMessage {
		p := heldSend("mac", sid, id, id)
		p.Hold, p.TimeoutMS, p.Request = nil, 0, ""
		raw, _ := json.Marshal(p)
		return raw
	}
	at := time.Now().Add(-time.Hour)
	for _, c := range []struct{ sid, id string }{{"s1", "a"}, {"s1", "b"}, {"s2", "x"}} {
		if _, _, err := st.HoldOperation(context.Background(), db.HeldOperation{Kind: db.HeldKindMessage, OpID: c.id, MachineID: "mac",
			SessionID: c.sid, Call: call(c.sid, c.id), Lang: "en", SentAt: at, HeldAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, _ := st.ClaimHeldOperation(context.Background(), 1, at); !ok { // the last manager stopped while sending a
		t.Fatal("claim")
	}
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	list := d.HeldMessages("mac", i18n.En)
	if s1 := list["s1"]; len(s1) != 2 || s1[0].State != proto.UndeliveredFailed || s1[1].Error != "Waits behind the message above" {
		t.Fatalf("s1 %+v", s1)
	}
	if r := st.all()[0]; r.State != db.HeldFailed || r.Failure != db.HeldUnclear {
		t.Fatalf("stored %+v", r)
	}
	close(up)
	eventually(t, 5*time.Second, "x sent", func() bool { return slices.Equal(n.sent(), []string{"x"}) })
	heldTicks(t, d, 2)
	if got := n.sent(); !slices.Equal(got, []string{"x"}) || len(st.all()) != 2 {
		t.Fatalf("sent %v, %d left", got, len(st.all()))
	}
}

// What a delivery's error says it did: only what surely wrote nothing goes again by itself; the node's coded refusal
// fails it with its words; everything else may have arrived.
func TestHeldOutcomeOf(t *testing.T) {
	refusal := &rpc.Error{Code: "input_draft", Message: "a draft is in the way", Data: json.RawMessage(`{"delivered":"none"}`)}
	for _, c := range []struct {
		err    error
		want   heldOutcome
		reason string
	}{
		{nil, heldDelivered, ""},
		{fmt.Errorf("mac: %w", apierr.ErrOffline), heldNotSent, ""},
		{rpc.NotSent(rpc.ErrClosed), heldNotSent, ""},
		{rpc.NotSent(context.Canceled), heldNotSent, ""},
		{rpc.NotSent(context.DeadlineExceeded), heldNotSent, ""},
		{rpc.ErrSuspended, heldNotSent, ""},
		{&rpc.Error{Code: devicewire.CodeMachineOffline, Message: "away"}, heldNotSent, ""},
		{wireError(fmt.Errorf("mac: %w", apierr.ErrOffline)), heldNotSent, ""},
		{wireError(rpc.NotSent(rpc.ErrClosed)), heldNotSent, ""},
		{rpc.ErrDropped, heldUnclear, ""},
		{rpc.ErrClosed, heldUnclear, ""}, // bare: proves nothing (a wrapper may have lost ErrDropped's identity)
		{fmt.Errorf("mac: %w", rpc.ErrClosed), heldUnclear, ""},
		{writerCtxErr{}, heldUnclear, ""}, // ErrNotWritten without ErrNotSent: a writer's own context error
		{fmt.Errorf("mac: %w", rpc.ErrDropped), heldUnclear, ""},
		{context.DeadlineExceeded, heldUnclear, ""},
		{context.Canceled, heldUnclear, ""},
		{errors.New("write: broken pipe"), heldUnclear, ""},
		{&rpc.Error{Message: "it may have gone", Data: json.RawMessage(`{"delivered":"unknown"}`)}, heldUnclear, ""},
		{&rpc.Error{Code: devicewire.CodeNodeClosed, Message: "closed"}, heldUnclear, ""},
		{&rpc.Error{Code: devicewire.CodeDeadline, Message: "late"}, heldUnclear, ""},
		{&rpc.Error{Message: rpc.ErrClosed.Error()}, heldUnclear, ""},
		{refusal, heldRefused, "a draft is in the way"},
		{&rpc.Error{Code: "session_gone"}, heldRefused, "session_gone"},
	} {
		got, reason := heldOutcomeOf(c.err)
		if got != c.want || reason != c.reason {
			t.Errorf("%v: %d %q, want %d %q", c.err, got, reason, c.want, c.reason)
		}
	}
	// Held at the send only when nothing was written and somebody waits for the answer.
	held := map[error]bool{
		apierr.ErrOffline:                               true,
		rpc.NotSent(context.DeadlineExceeded):           true,
		rpc.NotSent(rpc.ErrClosed):                      true,
		rpc.ErrSuspended:                                true,
		&rpc.Error{Code: devicewire.CodeMachineOffline}: true,
		rpc.NotSent(context.Canceled):                   false, // nobody waits for the answer
		rpc.ErrDropped:                                  false,
		rpc.ErrClosed:                                   false,
		writerCtxErr{}:                                  false,
		context.DeadlineExceeded:                        false,
		refusal:                                         false,
	}
	for err, want := range held {
		if heldMayHold(err) != want {
			t.Errorf("heldMayHold(%v) = %v", err, !want)
		}
	}
}

// The words under a held message, in every language: a computer's name, the node's reason.
func TestHeldLinesInEveryLanguage(t *testing.T) {
	for _, l := range i18n.Langs {
		for _, r := range []heldRow{
			{HeldOperation: db.HeldOperation{State: db.HeldWaiting}},
			{HeldOperation: db.HeldOperation{State: db.HeldWaiting, Failure: db.HeldUnclear}},
			{HeldOperation: db.HeldOperation{State: db.HeldSending}},
			{HeldOperation: db.HeldOperation{State: db.HeldFailed, Failure: db.HeldUnclear}},
			{HeldOperation: db.HeldOperation{State: db.HeldFailed, Failure: db.HeldRefused, Error: "REASON"}},
			{HeldOperation: db.HeldOperation{State: db.HeldFailed, Failure: db.HeldRefused}},
			{HeldOperation: db.HeldOperation{State: db.HeldFailed, Failure: db.HeldExpired}},
		} {
			m := r.wire(l, "NAME", false)
			if m.Error == "" || strings.Contains(m.Error, "{") || strings.Contains(m.Error, "devices-held.") {
				t.Errorf("%s %+v: %q", l, r.HeldOperation, m.Error)
			}
			if (r.State == db.HeldWaiting || r.Failure == db.HeldExpired) && !strings.Contains(m.Error, "NAME") {
				t.Errorf("%s %+v: %q has no computer", l, r.HeldOperation, m.Error)
			}
			// A retried one that may have arrived says it is looked for first, not only that it waits.
			plain := (&heldRow{HeldOperation: db.HeldOperation{State: db.HeldWaiting}}).wire(l, "NAME", false).Error
			if r.State == db.HeldWaiting && r.Failure == db.HeldUnclear && m.Error == plain {
				t.Errorf("%s %+v: %q is the plain waiting line", l, r.HeldOperation, m.Error)
			}
			if r.Error != "" && !strings.Contains(m.Error, "REASON") {
				t.Errorf("%s: %q has no reason", l, m.Error)
			}
			if wantFailed := r.State == db.HeldFailed; (m.State == proto.UndeliveredFailed) != wantFailed {
				t.Errorf("%s %+v: state %s", l, r.HeldOperation, m.State)
			}
		}
	}
}

// While the held messages cannot be read nothing is held (the send fails as before), and after a failed read the
// store is left alone for heldLoadBackoff: the sends meanwhile do not wait on it, one after another. Then the send path
// reads them itself.
func TestHeldNeedsTheStore(t *testing.T) {
	old := heldLoadBackoff
	heldLoadBackoff = 200 * time.Millisecond
	t.Cleanup(func() { heldLoadBackoff = old })
	st := newHeldMem()
	st.setFail(errors.New("database down"))
	d := newTestDevices(t)
	d.opts.Store = st
	var mu sync.Mutex
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	d.held.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	offlineMachine(d, "mac", "Studio Mac")
	ctx := context.Background()
	var re *rpc.Error
	for i := range 3 {
		if _, err := d.Call(ctx, heldSend("mac", "s1", "a", "a")); !errors.As(err, &re) || re.Code != devicewire.CodeMachineOffline {
			t.Fatalf("store down, send %d: %v", i, err)
		}
	}
	if found, err := d.HeldDelete(ctx, "s1", "a"); found || err != nil {
		t.Fatalf("delete with the store down: %v %v", found, err)
	}
	if n := st.loadCount(); n != 1 {
		t.Fatalf("the store was asked %d times within its back-off", n)
	}
	st.setFail(nil)
	mu.Lock()
	now = now.Add(heldLoadBackoff - time.Millisecond) // still within the back-off: the store is not asked
	mu.Unlock()
	if _, err := d.Call(ctx, heldSend("mac", "s1", "a", "a")); !errors.As(err, &re) || re.Code != devicewire.CodeMachineOffline || st.loadCount() != 1 {
		t.Fatalf("within the back-off: %v, %d loads", err, st.loadCount())
	}
	mu.Lock()
	now = now.Add(time.Millisecond)
	mu.Unlock()
	out, err := d.Call(ctx, heldSend("mac", "s1", "a", "a"))
	if held := heldAnswerOf(t, out, err); held.ID != "a" || len(st.all()) != 1 || st.loadCount() != 2 {
		t.Fatalf("held %+v, %d stored, %d loads", held, len(st.all()), st.loadCount())
	}
}

// Right after the handshake an Agent of a runtime is not listed yet (it comes back a moment after its node): a
// message held for it waits, through any wake, until the node lists its session, then goes at once, once.
func TestHeldWaitsForItsSessionToBeListed(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour) // no tick: the list arriving wakes it
	n := &heldNode{listed: []string{"s2"}}
	up := make(chan struct{})
	m := laterMachine(t, d, "mac", "Studio Mac", n, up)
	out, err := d.Call(context.Background(), heldSend("mac", "s1", "a", "a"))
	heldAnswerOf(t, out, err)
	close(up)
	eventually(t, 5*time.Second, "connected and listed", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.peer != nil && m.sessionsListed
	})
	heldTicks(t, d, 2)
	if got := n.sent(); len(got) != 0 {
		t.Fatalf("sent before its session was listed: %v", got)
	}
	if rows := st.all(); len(rows) != 1 || rows[0].State != db.HeldWaiting || rows[0].Attempts != 0 {
		t.Fatalf("stored %+v", rows)
	}
	n.list("s1", "s2")
	eventually(t, 5*time.Second, "a sent", func() bool { return len(st.all()) == 0 })
	heldTicks(t, d, 1)
	if got := n.sent(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("sent %v", got)
	}
}

// A session still not listed heldListWait into the connection: the message goes anyway, and the node's answer decides
// (its refusal fails it with its words).
func TestHeldGoesAnywayAfterTheListWait(t *testing.T) {
	old := heldListWait
	heldListWait = 300 * time.Millisecond
	t.Cleanup(func() { heldListWait = old })
	st := newHeldMem()
	d, _ := heldDevices(t, st, 20*time.Millisecond)
	n := &heldNode{listed: []string{"s2"}, answer: func(map[string]any) (any, error) {
		return nil, &rpc.Error{Code: "session_not_found", Message: "no such session", Data: json.RawMessage(`{"delivered":"none"}`)}
	}}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	out, err := d.Call(context.Background(), heldSend("mac", "s1", "a", "a"))
	heldAnswerOf(t, out, err)
	start := time.Now()
	close(up)
	eventually(t, 5*time.Second, "a refused", func() bool {
		rows := st.all()
		return len(rows) == 1 && rows[0].State == db.HeldFailed
	})
	if waited := time.Since(start); waited < heldListWait {
		t.Fatalf("sent after %s, before the list wait", waited)
	}
	if got := n.sent(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("sent %v", got)
	}
	if list := d.HeldMessages("mac", i18n.En)["s1"]; len(list) != 1 || list[0].Error != "Not sent: no such session" {
		t.Fatalf("row %+v", list)
	}
}

// writerCtxErr is what rpc.Call answers when the writer itself failed with the call context's error (a
// websocket.NetConn whose own context ended, part of the frame possibly out): ErrNotWritten, but not ErrNotSent.
type writerCtxErr struct{}

func (writerCtxErr) Error() string { return "failed to write frame: context deadline exceeded" }
func (writerCtxErr) Is(target error) bool {
	return target == rpc.ErrNotWritten || target == context.DeadlineExceeded
}

// putHeld keeps message id for session sid of machine mac in st as a manager held it at, in state (failed with
// failure); its seq.
func putHeld(t *testing.T, st *heldMem, sid, id, state, failure string, at time.Time) int64 {
	t.Helper()
	p := heldSend("mac", sid, id, id)
	p.Hold, p.TimeoutMS, p.Request = nil, 0, ""
	raw, _ := json.Marshal(p)
	op, _, err := st.HoldOperation(context.Background(), db.HeldOperation{Kind: db.HeldKindMessage, OpID: id, MachineID: "mac",
		SessionID: sid, Call: raw, Lang: "en", SentAt: at, HeldAt: at})
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	r := st.rows[op.Seq]
	r.State, r.Failure = state, failure
	st.rows[op.Seq] = r
	st.mu.Unlock()
	return op.Seq
}

// A held message that cannot be made ready to send here (its preparation fails before the node is asked) wrote
// nothing: it fails as refused, with why, and does not hold the next one back.
func TestHeldNotPreparedIsRefused(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	ctx := context.Background()
	out, err := d.Call(ctx, heldSend("mac", "s1", "a", "a"))
	heldAnswerOf(t, out, err)
	bad := heldSend("mac", "s1", "b", "b")
	bad.Params = json.RawMessage(`{"session_id":"s1","client_msg_id":"b","text":5}`) // held behind a; not a text
	out, err = d.Call(ctx, bad)
	heldAnswerOf(t, out, err)
	out, err = d.Call(ctx, heldSend("mac", "s1", "c", "c"))
	heldAnswerOf(t, out, err)
	close(up)
	eventually(t, 5*time.Second, "a and c sent", func() bool { return slices.Equal(n.sent(), []string{"a", "c"}) && len(st.all()) == 1 })
	if r := st.all()[0]; r.OpID != "b" || r.State != db.HeldFailed || r.Failure != db.HeldRefused {
		t.Fatalf("b stored %+v", r)
	}
	if list := d.HeldMessages("mac", i18n.En)["s1"]; len(list) != 1 || list[0].Error != "Not sent: Persona couldn't get it ready to send" {
		t.Fatalf("row %+v", list)
	}
}

// Once the node took a held message it was delivered, whatever comes after its answer (too large to pass on): never
// refused, so Retry could not send it twice.
func TestHeldDeliveredWhateverComesAfter(t *testing.T) {
	old := maxForward
	maxForward = 16 << 10
	t.Cleanup(func() { maxForward = old })
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{answer: func(m map[string]any) (any, error) {
		if m["client_msg_id"] == "a" {
			return map[string]any{"ok": true, "pad": strings.Repeat("x", 32<<10)}, nil
		}
		return map[string]any{"ok": true}, nil
	}}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	for _, id := range []string{"a", "b"} {
		out, err := d.Call(context.Background(), heldSend("mac", "s1", id, id))
		heldAnswerOf(t, out, err)
	}
	close(up)
	eventually(t, 5*time.Second, "both sent", func() bool { return slices.Equal(n.sent(), []string{"a", "b"}) && len(st.all()) == 0 })
}

// A message that may have arrived heads its session: on each new connection that lists the session, its transcript
// is read once; there under its client_msg_id it did arrive, leaves the rows, and the next one goes once.
func TestHeldLandedUnclearIsTakenAsSent(t *testing.T) {
	st := newHeldMem()
	at := time.Now().Add(-time.Hour)
	putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, at)
	putHeld(t, st, "s1", "b", db.HeldWaiting, "", at)
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{items: map[string][]proto.Item{"s1": {{Role: "user", Text: "a", ClientMsgID: "a"}, {Role: "assistant", Text: "ok"}}}}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	close(up)
	eventually(t, 5*time.Second, "b sent", func() bool { return slices.Equal(n.sent(), []string{"b"}) && len(st.all()) == 0 })
	heldTicks(t, d, 1)
	if got, reads := n.sent(), n.itemReads(); !slices.Equal(got, []string{"b"}) || reads != 1 {
		t.Fatalf("sent %v, %d reads", got, reads)
	}
}

// Not in the transcript by its id (words alike do not count), or the transcript not read: it stays for the owner,
// nothing is sent, and it is read once a connection however often the deliveries look.
func TestHeldUnclearNotLandedStays(t *testing.T) {
	st := newHeldMem()
	at := time.Now().Add(-time.Hour)
	putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, at)
	putHeld(t, st, "s1", "b", db.HeldWaiting, "", at)
	d, _ := heldDevices(t, st, time.Hour) // its ticks driven below
	n := &heldNode{items: map[string][]proto.Item{"s1": {{Role: "user", Text: "a"}, {Role: "user", Text: "x", ClientMsgID: "x"}}}}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	close(up)
	eventually(t, 5*time.Second, "the transcript read", func() bool { return n.itemReads() == 1 })
	heldTicks(t, d, 5)
	if got, reads := n.sent(), n.itemReads(); len(got) != 0 || reads != 1 {
		t.Fatalf("sent %v, %d reads", got, reads)
	}
	if rows := st.all(); len(rows) != 2 || !(rows[0].State == db.HeldFailed && rows[0].Failure == db.HeldUnclear) || rows[1].State != db.HeldWaiting {
		t.Fatalf("stored %+v", rows)
	}
	// A new connection reads it again.
	n.drop()
	eventually(t, 10*time.Second, "read on the new connection", func() bool { return n.itemReads() == 2 })
	if got := n.sent(); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
}

// While the owner's Retry of a failed message is being written, it holds the later ones of its session back: they
// never overtake it.
func TestHeldRetryInFlightHoldsLaterOnes(t *testing.T) {
	st := newHeldMem()
	at := time.Now().Add(-time.Hour)
	putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldRefused, at)
	putHeld(t, st, "s1", "b", db.HeldWaiting, "", at)
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	st.mu.Lock()
	st.retryGate, st.retrying = make(chan struct{}), make(chan struct{}, 1)
	gate, retrying := st.retryGate, st.retrying
	st.mu.Unlock()
	retried := make(chan error, 1)
	go func() {
		_, err := d.HeldRetry(context.Background(), "s1", "a")
		retried <- err
	}()
	<-retrying // a's Retry is being written
	close(up)  // the computer comes back meanwhile: its list wakes the deliveries
	eventually(t, 5*time.Second, "connected", func() bool {
		listed, _ := d.machineByID("mac").heldOn("s1")
		return !listed.IsZero()
	})
	d.heldWake("mac")
	heldIdle(t, d)
	if got := n.sent(); len(got) != 0 {
		t.Fatalf("sent %v while a's Retry was written", got)
	}
	close(gate)
	if err := <-retried; err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "both sent", func() bool { return len(st.all()) == 0 })
	if got := n.sent(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("sent %v", got)
	}
}

// A message held behind one that may have arrived waits for the owner, not for its computer: it never expires (nor
// says its computer was away), and its wait starts again, so it has nearly a day once the owner decides. One not
// behind such a message expires after a day.
func TestHeldBehindUnclearDoesNotExpire(t *testing.T) {
	st := newHeldMem()
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, at)
	b := putHeld(t, st, "s1", "b", db.HeldWaiting, "", at)
	putHeld(t, st, "s2", "x", db.HeldWaiting, "", at)
	var mu sync.Mutex
	now := at.Add(25 * time.Hour)
	d, _ := heldDevicesAt(t, st, time.Hour, func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	offlineMachine(d, "mac", "Studio Mac")
	d.heldExpire(context.Background())
	rows := st.snapshot()
	if r := rows[b]; r.State != db.HeldWaiting || !r.HeldAt.Equal(now) {
		t.Fatalf("b %+v", r)
	}
	list := d.HeldMessages("mac", i18n.En)
	if s1 := list["s1"]; len(s1) != 2 || s1[1].State != proto.UndeliveredWaiting || s1[1].Error != "Waits behind the message above" {
		t.Fatalf("s1 %+v", s1)
	}
	if s2 := list["s2"]; len(s2) != 1 || s2[0].Error != "Not sent: Studio Mac was away for 24 hours" {
		t.Fatalf("s2 %+v", s2)
	}
}

// A message that waited a whole day is not sent when its computer comes back before the tick fails it: the expiry
// is heldExpire's alone.
func TestHeldExpiredIsNotSent(t *testing.T) {
	st := newHeldMem()
	at := time.Now()
	putHeld(t, st, "s1", "a", db.HeldWaiting, "", at)
	var mu sync.Mutex
	now := at
	d, _ := heldDevicesAt(t, st, time.Hour, func() time.Time { mu.Lock(); defer mu.Unlock(); return now }) // its first look finds it fresh
	mu.Lock()
	now = at.Add(25 * time.Hour)
	mu.Unlock()
	n := &heldNode{}
	up := make(chan struct{})
	m := laterMachine(t, d, "mac", "Studio Mac", n, up)
	close(up)
	eventually(t, 5*time.Second, "listed", func() bool { listed, _ := m.heldOn("s1"); return !listed.IsZero() })
	d.heldWake("mac")
	heldIdle(t, d)
	if got := n.sent(); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
	d.heldExpire(context.Background())
	if r := st.all()[0]; r.State != db.HeldFailed || r.Failure != db.HeldExpired {
		t.Fatalf("stored %+v", r)
	}
}

// personCall is a request on mux from a person's own full device, as the hub relays it.
func personCall(t *testing.T, mux http.Handler, method, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set(assistantwire.RoleHeader, "full")
	req.Header.Set(assistantwire.PersonHeader, "1")
	mux.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// A hold the store kept but answered with an error (its answer lost after the commit) is found by its key and held:
// the app is told so, rather than the failure while the row goes later by itself. Not kept: the failure as before.
// The same when the message waits behind held ones.
func TestHeldKeptThoughTheStoreSaidNo(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour)
	offlineMachine(d, "mac", "Studio Mac")
	ctx := context.Background()
	st.mu.Lock()
	st.holdErr = errors.New("connection refused")
	st.mu.Unlock()
	var re *rpc.Error
	if _, err := d.Call(ctx, heldSend("mac", "s1", "a", "a")); !errors.As(err, &re) || re.Code != devicewire.CodeMachineOffline || len(st.all()) != 0 {
		t.Fatalf("not kept: %v, %d stored", err, len(st.all()))
	}
	st.mu.Lock()
	st.holdErr, st.committedErr = nil, errors.New("timeout: the commit's answer was lost")
	st.mu.Unlock()
	out, err := d.Call(ctx, heldSend("mac", "s1", "a", "a"))
	if held := heldAnswerOf(t, out, err); held.ID != "a" || len(st.all()) != 1 {
		t.Fatalf("kept: %+v, %d stored", held, len(st.all()))
	}
	out, err = d.Call(ctx, heldSend("mac", "s1", "b", "b")) // behind a
	if held := heldAnswerOf(t, out, err); held.ID != "b" || len(st.all()) != 2 {
		t.Fatalf("kept behind a: %+v, %d stored", held, len(st.all()))
	}
	if got := heldIDs(d.HeldMessages("mac", i18n.En)["s1"]); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("rows %v", got)
	}
}

// What another manager on the same database (a switch-over) is sending is never made to wait again or deleted by the
// owner's Retry or Delete here: 409, and the store keeps it as it is. One the store no longer has is gone here too.
func TestHeldOwnerChangesLeaveOneBeingSentElsewhere(t *testing.T) {
	st := newHeldMem()
	a := putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, time.Now().Add(-time.Hour))
	d, _ := heldDevices(t, st, time.Hour)
	offlineMachine(d, "mac", "Studio Mac")
	st.mu.Lock() // another manager retried it and sends it now
	r := st.rows[a]
	r.State, r.Failure, r.TriedAt = db.HeldSending, "", time.Now()
	st.rows[a] = r
	st.mu.Unlock()
	mux := d.Handler(http.NotFoundHandler())
	for _, c := range []struct{ method, path string }{{"POST", "/api/v1/sessions/mac/s1/held/a/retry"}, {"DELETE", "/api/v1/sessions/mac/s1/held/a"}} {
		if code, body := personCall(t, mux, c.method, c.path); code != http.StatusConflict || body["code"] != "conflict" {
			t.Fatalf("%s: %d %v", c.method, code, body)
		}
		if r := st.snapshot()[a]; r.State != db.HeldSending {
			t.Fatalf("%s changed it: %+v", c.method, r)
		}
	}
	st.mu.Lock() // it went
	delete(st.rows, a)
	st.mu.Unlock()
	if code, _ := personCall(t, mux, "POST", "/api/v1/sessions/mac/s1/held/a/retry"); code != http.StatusNotFound {
		t.Fatalf("retry of one gone: %d", code)
	}
	if d.HeldMessages("mac", i18n.En) != nil {
		t.Fatalf("still shown: %v", d.HeldMessages("mac", i18n.En))
	}
}

// Deleting the message that held the rest of its session back lets the next one go at once, not a tick later.
func TestHeldDeleteLetsTheNextOneGo(t *testing.T) {
	st := newHeldMem()
	at := time.Now().Add(-time.Hour)
	putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, at)
	putHeld(t, st, "s1", "b", db.HeldWaiting, "", at)
	d, _ := heldDevices(t, st, time.Hour) // no tick
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	close(up)
	eventually(t, 5*time.Second, "a looked for", func() bool { return n.itemReads() == 1 })
	if code, body := personCall(t, d.Handler(http.NotFoundHandler()), "DELETE", "/api/v1/sessions/mac/s1/held/a"); code != http.StatusOK {
		t.Fatalf("delete: %d %v", code, body)
	}
	eventually(t, 5*time.Second, "b sent", func() bool { return slices.Equal(n.sent(), []string{"b"}) && len(st.all()) == 0 })
}

// A message that expired lets the ones it held back go at once (the expiry wakes them).
func TestHeldExpiryLetsTheNextOneGo(t *testing.T) {
	st := newHeldMem()
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	putHeld(t, st, "s1", "a", db.HeldWaiting, "", at)
	putHeld(t, st, "s1", "b", db.HeldWaiting, "", at.Add(2*time.Hour))
	var mu sync.Mutex
	now := at.Add(time.Hour)
	d, _ := heldDevicesAt(t, st, time.Hour, func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	mu.Lock()
	now = at.Add(25 * time.Hour) // a has waited a day, b not
	mu.Unlock()
	close(up)
	eventually(t, 5*time.Second, "listed", func() bool { listed, _ := d.machineByID("mac").heldOn("s1"); return !listed.IsZero() })
	d.heldWake("mac")
	heldIdle(t, d)
	if got := n.sent(); len(got) != 0 {
		t.Fatalf("sent %v behind an expired one", got)
	}
	d.heldExpire(context.Background())
	eventually(t, 5*time.Second, "b sent", func() bool { return slices.Equal(n.sent(), []string{"b"}) && len(st.all()) == 1 })
}

// set changes st under its lock.
func (s *heldMem) set(f func(s *heldMem)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// A claim taken whose answer was lost: nothing is sent, the store's "being sent" is followed (it may arrive: unclear,
// holding the rest back), the owner's change waits until that try is over, and the tick writes it unclear then. The
// owner's Retry then sends it, once; the repair itself never sends.
func TestHeldLostClaimIsRepairedOnTheTick(t *testing.T) {
	st := newHeldMem()
	t0 := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	putHeld(t, st, "s1", "a", db.HeldWaiting, "", t0)
	st.set(func(s *heldMem) { s.claimLost = true })
	var mu sync.Mutex
	now := t0.Add(time.Minute)
	d, _ := heldDevicesAt(t, st, time.Hour, func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	close(up)
	eventually(t, 5*time.Second, "the claim taken", func() bool { return st.all()[0].State == db.HeldSending })
	eventually(t, 5*time.Second, "unclear here", func() bool {
		d.heldWake("mac")
		l := d.HeldMessages("mac", i18n.En)["s1"]
		return len(l) == 1 && l[0].State == proto.UndeliveredFailed
	})
	mux := d.Handler(http.NotFoundHandler())
	if code, body := personCall(t, mux, "POST", "/api/v1/sessions/mac/s1/held/a/retry"); code != http.StatusConflict {
		t.Fatalf("retry while that try may run: %d %v", code, body)
	}
	d.heldRepair(context.Background())
	if r := st.all()[0]; r.State != db.HeldSending {
		t.Fatalf("repaired before the try was over: %+v", r)
	}
	mu.Lock()
	now = now.Add(heldSendTimeout + 2*time.Minute)
	mu.Unlock()
	d.heldRepair(context.Background())
	if r := st.all()[0]; r.State != db.HeldFailed || r.Failure != db.HeldUnclear {
		t.Fatalf("not repaired: %+v", r)
	}
	if got := n.sent(); len(got) != 0 {
		t.Fatalf("the repair sent %v", got)
	}
	// Unclear now, it is looked for in the transcript (a Retry meanwhile answers 409): after that read.
	eventually(t, 5*time.Second, "looked for", func() bool {
		d.held.mu.Lock()
		defer d.held.mu.Unlock()
		r := d.held.rows["mac"][0]
		return n.itemReads() == 1 && !r.taken()
	})
	retryAndSend(t, d, mux, "s1", "a")
	eventually(t, 5*time.Second, "a sent", func() bool { return len(st.all()) == 0 })
	if got := n.sent(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("sent %v", got)
	}
}

// The owner's Delete of a message the store still has as being sent from a try that is over (no delivery runs here)
// is written over it; within such a try it is 409.
func TestHeldOwnerChangeOverAStaleSend(t *testing.T) {
	st := newHeldMem()
	t0 := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	a := putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, t0)
	var mu sync.Mutex
	now := t0.Add(time.Minute)
	d, _ := heldDevicesAt(t, st, time.Hour, func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	offlineMachine(d, "mac", "Studio Mac")
	st.set(func(s *heldMem) {
		r := s.rows[a]
		r.State, r.Failure, r.TriedAt = db.HeldSending, "", t0
		s.rows[a] = r
	})
	mux := d.Handler(http.NotFoundHandler())
	if code, _ := personCall(t, mux, "DELETE", "/api/v1/sessions/mac/s1/held/a"); code != http.StatusConflict {
		t.Fatalf("delete within the try: %d", code)
	}
	mu.Lock()
	now = t0.Add(heldSendTimeout + 2*time.Minute)
	mu.Unlock()
	if code, body := personCall(t, mux, "DELETE", "/api/v1/sessions/mac/s1/held/a"); code != http.StatusOK {
		t.Fatalf("delete after the try: %d %v", code, body)
	}
	if len(st.all()) != 0 || d.HeldMessages("mac", i18n.En) != nil {
		t.Fatalf("left %+v", st.all())
	}
}

// A delivery whose outcome could not be written keeps what was decided (refused here) and holds the rest back until
// the tick writes it; then the next one goes. Nothing is sent twice.
func TestHeldUnwrittenOutcomeIsRepaired(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{answer: func(m map[string]any) (any, error) {
		if m["client_msg_id"] == "a" {
			return nil, &rpc.Error{Code: "input_draft", Message: "a draft is in the way", Data: json.RawMessage(`{"delivered":"none"}`)}
		}
		return map[string]any{"ok": true}, nil
	}}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	for _, id := range []string{"a", "b"} {
		out, err := d.Call(context.Background(), heldSend("mac", "s1", id, id))
		heldAnswerOf(t, out, err)
	}
	st.set(func(s *heldMem) { s.settleErr = errors.New("connection reset") })
	close(up)
	eventually(t, 5*time.Second, "a tried", func() bool { return len(n.sent()) == 1 })
	eventually(t, 5*time.Second, "a refused here", func() bool {
		l := d.HeldMessages("mac", i18n.En)["s1"]
		return len(l) == 2 && l[0].Error == "Not sent: a draft is in the way"
	})
	d.heldWake("mac")
	heldIdle(t, d)
	if got := n.sent(); !slices.Equal(got, []string{"a"}) || st.all()[0].State != db.HeldSending {
		t.Fatalf("before the repair: sent %v, stored %+v", got, st.all())
	}
	d.heldRepair(context.Background())
	if r := st.all()[0]; r.State != db.HeldFailed || r.Failure != db.HeldRefused || r.Error != "a draft is in the way" {
		t.Fatalf("not repaired: %+v", r)
	}
	eventually(t, 5*time.Second, "b sent", func() bool { return slices.Equal(n.sent(), []string{"a", "b"}) && len(st.all()) == 1 })
}

// A message the hub keeps in order without Hold (from an app that does not draw held messages) is refused, nothing
// written or kept, while older ones of its session are held; with nothing held it goes as before (and is never kept,
// not even with the computer away). With Hold it is held behind them as before.
func TestHeldOrderedWithoutHold(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{listed: []string{"s2"}} // s1 not listed: its held message waits
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	offlineMachine(d, "away", "Old Laptop")
	ctx := i18n.With(context.Background(), i18n.En)
	out, err := d.Call(ctx, heldSend("mac", "s1", "a", "a"))
	heldAnswerOf(t, out, err)
	close(up)
	eventually(t, 5*time.Second, "connected", func() bool { listed, _ := d.machineByID("mac").heldOn("s2"); return !listed.IsZero() })
	ordered := func(machine, sid, id string) devicewire.NodeCall {
		p := heldSend(machine, sid, id, id)
		p.Hold, p.Ordered = nil, true
		return p
	}
	var re *rpc.Error
	if _, err := d.Call(ctx, ordered("mac", "s1", "b")); !errors.As(err, &re) || re.Code != "held_ahead" ||
		re.Message != "Earlier messages to this Agent are still waiting to be sent. Send this one after them." ||
		string(re.Data) != `{"delivered":"none"}` {
		t.Fatalf("behind a: %v", err)
	}
	if out, err := d.Call(ctx, ordered("mac", "s2", "c")); err != nil {
		t.Fatalf("nothing held in s2: %s %v", out, err)
	}
	if _, err := d.Call(ctx, ordered("away", "s9", "d")); !errors.As(err, &re) || re.Code != devicewire.CodeMachineOffline {
		t.Fatalf("away, nothing held: %v", err)
	}
	hold := heldSend("mac", "s1", "e", "e")
	hold.Ordered = true
	out, err = d.Call(ctx, hold)
	if held := heldAnswerOf(t, out, err); held.ID != "e" {
		t.Fatalf("e: %+v", held)
	}
	if got := n.sent(); !slices.Equal(got, []string{"c"}) {
		t.Fatalf("sent %v", got)
	}
	var kept []string
	for _, r := range st.all() {
		kept = append(kept, r.OpID)
	}
	if !slices.Equal(kept, []string{"a", "e"}) {
		t.Fatalf("kept %v", kept)
	}
}

// The owner's Retry whose write failed lets the ones its busy mark held back go at once.
func TestHeldFailedRetryLetsTheNextOneGo(t *testing.T) {
	st := newHeldMem()
	at := time.Now().Add(-time.Hour)
	putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldRefused, at)
	putHeld(t, st, "s1", "b", db.HeldWaiting, "", at)
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	gate, retrying := make(chan struct{}), make(chan struct{}, 1)
	st.set(func(s *heldMem) { s.retryGate, s.retrying, s.retryErr = gate, retrying, errors.New("connection reset") })
	retried := make(chan error, 1)
	go func() { _, err := d.HeldRetry(context.Background(), "s1", "a"); retried <- err }()
	<-retrying
	close(up)
	eventually(t, 5*time.Second, "connected", func() bool { listed, _ := d.machineByID("mac").heldOn("s1"); return !listed.IsZero() })
	d.heldWake("mac")
	heldIdle(t, d)
	if got := n.sent(); len(got) != 0 {
		t.Fatalf("sent %v while a was being retried", got)
	}
	close(gate)
	if err := <-retried; err == nil {
		t.Fatal("the retry's failure not said")
	}
	eventually(t, 5*time.Second, "b sent", func() bool { return slices.Equal(n.sent(), []string{"b"}) })
}

// A message found in the transcript whose row the store has as waiting by then (another manager's Retry took it
// meanwhile) is dropped all the same: it arrived, and is never sent again; the next one goes.
func TestHeldLandedThoughRetriedElsewhereIsNotSentAgain(t *testing.T) {
	st := newHeldMem()
	at := time.Now().Add(-time.Hour)
	putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, at)
	putHeld(t, st, "s1", "b", db.HeldWaiting, "", at)
	st.set(func(s *heldMem) {
		s.beforeDelete = func(s *heldMem, seq int64) {
			r := s.rows[seq]
			r.State, r.Failure = db.HeldWaiting, ""
			s.rows[seq] = r
		}
	})
	d, _ := heldDevices(t, st, time.Hour) // no tick
	n := &heldNode{items: map[string][]proto.Item{"s1": {{Role: "user", Text: "a", ClientMsgID: "a"}}}}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	close(up)
	eventually(t, 5*time.Second, "b sent", func() bool { return slices.Equal(n.sent(), []string{"b"}) && len(st.all()) == 0 })
	heldTicks(t, d, 1)
	if got := n.sent(); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("sent %v", got)
	}
}

// A message the hub sent with both Hold and Ordered (every declaring app's is) is held, and once its computer is back
// it is delivered, once and in order: its own delivery is never held again, nor refused as being behind itself.
func TestHeldOrderedHoldIsDelivered(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{listed: []string{"s1"}}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	ctx := i18n.With(context.Background(), i18n.En)
	for _, id := range []string{"a", "b"} {
		p := heldSend("mac", "s1", id, id)
		p.Ordered = true
		out, err := d.Call(ctx, p)
		heldAnswerOf(t, out, err)
	}
	for _, r := range st.all() {
		if strings.Contains(string(r.Call), `"ordered"`) || strings.Contains(string(r.Call), `"hold"`) {
			t.Fatalf("kept with its marks: %s", r.Call)
		}
	}
	close(up)
	eventually(t, 5*time.Second, "both sent", func() bool { return len(n.sent()) == 2 && len(st.all()) == 0 })
	if got := n.sent(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("sent %v", got)
	}
}

// staleRefused is devices with held a (refused by the node, its outcome not written: stale here, being sent in the
// store) and b behind it, on a connected node; tick its loop's period.
func staleRefused(t *testing.T, st *heldMem, tick time.Duration) (*Devices, *heldNode) {
	t.Helper()
	d, _ := heldDevices(t, st, tick)
	n := &heldNode{answer: func(m map[string]any) (any, error) {
		if m["client_msg_id"] == "a" {
			return nil, &rpc.Error{Code: "input_draft", Message: "a draft is in the way", Data: json.RawMessage(`{"delivered":"none"}`)}
		}
		return map[string]any{"ok": true}, nil
	}}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	for _, id := range []string{"a", "b"} {
		out, err := d.Call(context.Background(), heldSend("mac", "s1", id, id))
		heldAnswerOf(t, out, err)
	}
	st.set(func(s *heldMem) { s.settleErr = errors.New("connection reset") })
	close(up)
	eventually(t, 5*time.Second, "a stale", func() bool {
		d.held.mu.Lock()
		defer d.held.mu.Unlock()
		r := d.held.rowLocked("mac", st.all()[0].Seq)
		return r != nil && r.stale && st.all()[0].State == db.HeldSending
	})
	return d, n
}

// While the repair writes a stale message it is busy: the owner's Delete meanwhile answers 409 rather than racing it;
// written, the Delete goes.
func TestHeldRepairHoldsOffTheOwner(t *testing.T) {
	st := newHeldMem()
	d, n := staleRefused(t, st, time.Hour)
	gate, settling := make(chan struct{}), make(chan struct{}, 1)
	st.set(func(s *heldMem) { s.settleTryGate, s.settlingTry = gate, settling })
	repaired := make(chan struct{})
	go func() { d.heldRepair(context.Background()); close(repaired) }()
	<-settling
	mux := d.Handler(http.NotFoundHandler())
	if code, body := personCall(t, mux, "DELETE", "/api/v1/sessions/mac/s1/held/a"); code != http.StatusConflict {
		t.Fatalf("delete while the repair writes: %d %v", code, body)
	}
	st.set(func(s *heldMem) { s.settleTryGate = nil })
	close(gate)
	<-repaired
	if r := st.all()[0]; r.State != db.HeldFailed || r.Failure != db.HeldRefused {
		t.Fatalf("repaired %+v", r)
	}
	eventually(t, 5*time.Second, "b sent", func() bool { return slices.Equal(n.sent(), []string{"a", "b"}) })
	if code, body := personCall(t, mux, "DELETE", "/api/v1/sessions/mac/s1/held/a"); code != http.StatusOK || len(st.all()) != 0 {
		t.Fatalf("delete after: %d %v, %d left", code, body, len(st.all()))
	}
}

// The repair leaves a stale message the owner is changing: the owner's Retry, judged on that very try, writes it.
func TestHeldRepairLeavesWhatTheOwnerChanges(t *testing.T) {
	st := newHeldMem()
	d, n := staleRefused(t, st, time.Hour)
	gate, retrying := make(chan struct{}), make(chan struct{}, 1)
	st.set(func(s *heldMem) { s.retryGate, s.retrying = gate, retrying })
	retried := make(chan error, 1)
	go func() { _, err := d.HeldRetry(context.Background(), "s1", "a"); retried <- err }()
	<-retrying
	d.heldRepair(context.Background())
	if r := st.all()[0]; r.State != db.HeldSending {
		t.Fatalf("the repair wrote what the owner changes: %+v", r)
	}
	st.set(func(s *heldMem) { s.retryGate = nil })
	close(gate)
	if err := <-retried; err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "a again, then b", func() bool { return slices.Equal(n.sent(), []string{"a", "a", "b"}) })
}

// The owner's Delete over a try that is over deletes that try only: a newer claim meanwhile (another manager) is not
// deleted, and the Delete answers 409, the row kept here and there.
func TestHeldStaleDeleteThatDidNotApplyIsNotDone(t *testing.T) {
	st := newHeldMem()
	t0 := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	a := putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, t0)
	var mu sync.Mutex
	now := t0.Add(heldSendTimeout + 2*time.Minute)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	d, _ := heldDevicesAt(t, st, time.Hour, clock)
	offlineMachine(d, "mac", "Studio Mac")
	st.set(func(s *heldMem) {
		r := s.rows[a]
		r.State, r.Failure, r.Attempts, r.TriedAt = db.HeldSending, "", 1, t0 // a try long over
		s.rows[a] = r
		s.beforeDeleteTry = func(s *heldMem, seq int64) { // another manager claims it again just then
			r := s.rows[seq]
			r.Attempts, r.TriedAt = 2, clock()
			s.rows[seq] = r
		}
	})
	if code, body := personCall(t, d.Handler(http.NotFoundHandler()), "DELETE", "/api/v1/sessions/mac/s1/held/a"); code != http.StatusConflict {
		t.Fatalf("delete over a newer try: %d %v", code, body)
	}
	if r := st.snapshot()[a]; r.State != db.HeldSending || r.Attempts != 2 {
		t.Fatalf("stored %+v", r)
	}
	if l := d.HeldMessages("mac", i18n.En)["s1"]; len(l) != 1 {
		t.Fatalf("dropped here: %+v", l)
	}
}

// The repair writes only the try it judged: a newer claim of that message in the store (another manager, or this one
// after a Retry) is followed, never overwritten.
func TestHeldRepairNeverOverwritesANewerTry(t *testing.T) {
	st := newHeldMem()
	d, n := staleRefused(t, st, time.Hour)
	seq := st.all()[0].Seq
	st.set(func(s *heldMem) { r := s.rows[seq]; r.Attempts, r.TriedAt = r.Attempts+1, time.Now(); s.rows[seq] = r })
	d.heldRepair(context.Background())
	if r := st.all()[0]; r.State != db.HeldSending || r.Attempts != 2 {
		t.Fatalf("a newer try overwritten: %+v", r)
	}
	if l := d.HeldMessages("mac", i18n.En)["s1"]; l[0].State != proto.UndeliveredFailed || l[0].Error == "Not sent: a draft is in the way" {
		t.Fatalf("not followed: %+v", l)
	}
	if got := n.sent(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("sent %v", got)
	}
}

// A message sent whose row the store has otherwise by then (another manager took it up meanwhile) is followed: not
// dropped here while the store keeps it.
func TestHeldSentButTheStoreHasItOtherwise(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	out, err := d.Call(context.Background(), heldSend("mac", "s1", "a", "a"))
	heldAnswerOf(t, out, err)
	st.set(func(s *heldMem) {
		s.beforeDeleteTry = func(s *heldMem, seq int64) {
			r := s.rows[seq]
			r.State, r.Failure = db.HeldFailed, db.HeldUnclear
			s.rows[seq] = r
		}
	})
	close(up)
	eventually(t, 5*time.Second, "a sent", func() bool { return len(n.sent()) == 1 })
	eventually(t, 5*time.Second, "followed", func() bool {
		l := d.HeldMessages("mac", i18n.En)["s1"]
		return len(l) == 1 && l[0].State == proto.UndeliveredFailed
	})
	if rows := st.all(); len(rows) != 1 || rows[0].State != db.HeldFailed {
		t.Fatalf("stored %+v", rows)
	}
}

// A Retry of a message the store no longer has lets the ones its busy mark held back go at once.
func TestHeldRetryOfAGoneRowWakes(t *testing.T) {
	st := newHeldMem()
	at := time.Now().Add(-time.Hour)
	a := putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldRefused, at)
	putHeld(t, st, "s1", "b", db.HeldWaiting, "", at)
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	gate, retrying := make(chan struct{}), make(chan struct{}, 1)
	st.set(func(s *heldMem) { s.retryGate, s.retrying = gate, retrying })
	retried := make(chan bool, 1)
	go func() { found, _ := d.HeldRetry(context.Background(), "s1", "a"); retried <- found }()
	<-retrying
	close(up)
	eventually(t, 5*time.Second, "connected", func() bool { listed, _ := d.machineByID("mac").heldOn("s1"); return !listed.IsZero() })
	st.set(func(s *heldMem) { delete(s.rows, a); s.retryGate = nil }) // gone meanwhile
	close(gate)
	if found := <-retried; found {
		t.Fatal("retried one that is gone")
	}
	eventually(t, 5*time.Second, "b sent", func() bool { return slices.Equal(n.sent(), []string{"b"}) })
}

func (s *heldMem) runBeforeSettle(seq int64) {
	if f := s.beforeSettle; f != nil {
		s.beforeSettle = nil
		f(s, seq)
	}
}

// The owner's Retry over a try that is over writes that try only: a newer claim meanwhile (another manager) is not
// made to wait again (it would go twice); the Retry answers 409.
func TestHeldStaleRetryNeverOverwritesANewerTry(t *testing.T) {
	st := newHeldMem()
	t0 := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	a := putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, t0)
	var mu sync.Mutex
	now := t0.Add(heldSendTimeout + 2*time.Minute)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	d, _ := heldDevicesAt(t, st, time.Hour, clock)
	offlineMachine(d, "mac", "Studio Mac")
	st.set(func(s *heldMem) {
		r := s.rows[a]
		r.State, r.Failure, r.Attempts, r.TriedAt = db.HeldSending, "", 1, t0 // a try long over
		s.rows[a] = r
		s.beforeSettle = func(s *heldMem, seq int64) { // another manager claims it again just then
			r := s.rows[seq]
			r.Attempts, r.TriedAt = 2, clock()
			s.rows[seq] = r
		}
	})
	if code, body := personCall(t, d.Handler(http.NotFoundHandler()), "POST", "/api/v1/sessions/mac/s1/held/a/retry"); code != http.StatusConflict {
		t.Fatalf("retry over a newer try: %d %v", code, body)
	}
	if r := st.snapshot()[a]; r.State != db.HeldSending || r.Attempts != 2 {
		t.Fatalf("stored %+v", r)
	}
}

// A delivery's outcome is written over its own try only: when the message was claimed again meanwhile (another manager
// in a switch-over), that newer try is followed, never overwritten.
func TestHeldOwnOutcomeNeverOverwritesANewerTry(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{answer: func(map[string]any) (any, error) {
		return nil, &rpc.Error{Code: "input_draft", Message: "a draft is in the way", Data: json.RawMessage(`{"delivered":"none"}`)}
	}}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	out, err := d.Call(context.Background(), heldSend("mac", "s1", "a", "a"))
	heldAnswerOf(t, out, err)
	st.set(func(s *heldMem) {
		s.beforeSettle = func(s *heldMem, seq int64) {
			r := s.rows[seq]
			r.Attempts, r.TriedAt = r.Attempts+1, time.Now()
			s.rows[seq] = r
		}
	})
	close(up)
	eventually(t, 5*time.Second, "a tried", func() bool { return len(n.sent()) == 1 })
	eventually(t, 5*time.Second, "followed", func() bool {
		l := d.HeldMessages("mac", i18n.En)["s1"]
		return len(l) == 1 && l[0].Error == "Might not have arrived. Retry checks the chat first."
	})
	if r := st.all()[0]; r.State != db.HeldSending || r.Attempts != 2 {
		t.Fatalf("a newer try overwritten: %+v", r)
	}
}

// A message sent whose delete failed leaves the rows here at once; the tick deletes it in the store.
func TestHeldSentDeleteFailedIsDeletedLater(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	out, err := d.Call(context.Background(), heldSend("mac", "s1", "a", "a"))
	heldAnswerOf(t, out, err)
	st.set(func(s *heldMem) { s.deleteTryErr = errors.New("connection reset") })
	close(up)
	eventually(t, 5*time.Second, "a sent", func() bool { return len(n.sent()) == 1 && d.HeldMessages("mac", i18n.En) == nil })
	if rows := st.all(); len(rows) != 1 || rows[0].State != db.HeldSending {
		t.Fatalf("stored %+v", rows)
	}
	d.heldRepair(context.Background())
	if rows := st.all(); len(rows) != 0 {
		t.Fatalf("left %+v", rows)
	}
}

// retryAway is a message a that may have arrived, retried by the owner while its computer is away, and b behind it;
// the node's transcript has items; the computer comes back once the test closes up.
func retryAway(t *testing.T, items []proto.Item) (*Devices, *heldMem, *heldNode, chan struct{}) {
	t.Helper()
	st := newHeldMem()
	at := time.Now().Add(-time.Hour)
	putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, at)
	putHeld(t, st, "s1", "b", db.HeldWaiting, "", at)
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{items: map[string][]proto.Item{"s1": items}}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	if found, err := d.HeldRetry(context.Background(), "s1", "a"); !found || err != nil {
		t.Fatalf("retry: %v %v", found, err)
	}
	if r := st.all()[0]; r.State != db.HeldWaiting || r.Failure != db.HeldUnclear {
		t.Fatalf("retried %+v", r)
	}
	if l := d.HeldMessages("mac", i18n.En)["s1"]; l[0].State != proto.UndeliveredWaiting || l[0].Error != "Will check the chat when Studio Mac is back" {
		t.Fatalf("row %+v", l)
	}
	return d, st, n, up
}

// The owner's Retry of a message that may have arrived looks for it before it goes: in the transcript, it is not
// sent again (the Retry is done), and the next one goes.
func TestHeldRetriedUnclearInTheTranscriptIsNotSentAgain(t *testing.T) {
	d, st, n, up := retryAway(t, []proto.Item{{Role: "user", Text: "a", ClientMsgID: "a"}})
	close(up)
	eventually(t, 5*time.Second, "b sent", func() bool { return slices.Equal(n.sent(), []string{"b"}) && len(st.all()) == 0 })
	heldTicks(t, d, 1)
	if got, reads := n.sent(), n.itemReads(); !slices.Equal(got, []string{"b"}) || reads != 1 {
		t.Fatalf("sent %v, %d reads", got, reads)
	}
}

func TestHeldRetryUsesNodeEchoStatus(t *testing.T) {
	for _, tc := range []struct {
		name        string
		state       proto.SessionEchoState
		guardErr    error
		calls       []string
		guarded     []string
		reads       int
		unconfirmed bool
	}{
		{name: "known unseen", state: proto.EchoKnownUnseen, calls: []string{"a", "b"}, guarded: []string{"a"}},
		{name: "seen beyond latest turns", state: proto.EchoSeen, calls: []string{"b"}},
		{name: "unknown", state: proto.EchoUnknown, reads: 1, unconfirmed: true},
		{name: "legacy node", reads: 1, unconfirmed: true},
		{name: "evidence lost before retry", state: proto.EchoKnownUnseen, calls: []string{"a"}, guarded: []string{"a"},
			guardErr: &rpc.Error{Code: proto.CodeEchoUnconfirmed}, unconfirmed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newHeldMem()
			at := time.Now().Add(-time.Hour)
			putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, at)
			putHeld(t, st, "s1", "b", db.HeldWaiting, "", at)
			d, _ := heldDevices(t, st, time.Hour)
			n := &heldNode{}
			if tc.state != "" {
				n.echo = func(p proto.SessionEchoParams) proto.SessionEchoState {
					if p.SessionID != "s1" || p.ClientMsgID != "a" {
						t.Errorf("wrong echo query: %+v", p)
					}
					return tc.state
				}
			}
			if tc.guardErr != nil {
				n.answer = func(map[string]any) (any, error) { return nil, tc.guardErr }
			}
			up := make(chan struct{})
			laterMachine(t, d, "mac", "Studio Mac", n, up)
			if found, err := d.HeldRetry(context.Background(), "s1", "a"); !found || err != nil {
				t.Fatalf("retry: %v %v", found, err)
			}
			close(up)
			eventually(t, 5*time.Second, "retry settles", func() bool {
				rows := st.all()
				if tc.unconfirmed {
					return len(rows) == 2 && rows[0].State == db.HeldFailed && rows[0].Failure == db.HeldUnconfirmed && rows[1].State == db.HeldWaiting
				}
				return len(rows) == 0
			})
			heldTicks(t, d, 2)
			n.mu.Lock()
			guarded := slices.Clone(n.guarded)
			n.mu.Unlock()
			if calls, reads := n.sent(), n.itemReads(); !slices.Equal(calls, tc.calls) || !slices.Equal(guarded, tc.guarded) || reads != tc.reads {
				t.Fatalf("send calls %v, guarded %v, latest-turn reads %d", calls, guarded, reads)
			}
		})
	}
}

func TestHeldKnownUnseenWaitsForOwnerRetry(t *testing.T) {
	st := newHeldMem()
	putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, time.Now())
	d, _ := heldDevices(t, st, time.Hour)
	checked := make(chan struct{}, 1)
	n := &heldNode{echo: func(proto.SessionEchoParams) proto.SessionEchoState {
		checked <- struct{}{}
		return proto.EchoKnownUnseen
	}}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	close(up)
	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		t.Fatal("no echo check on reconnect")
	}
	heldTicks(t, d, 2)
	if got, rows := n.sent(), st.all(); len(got) != 0 || len(rows) != 1 || rows[0].Failure != db.HeldUnclear {
		t.Fatalf("sent %v, rows %+v", got, rows)
	}
}

// Not in its latest turns: no proof it did not arrive, so nothing is sent; it is left to the owner, and the next one
// keeps waiting behind it. The owner's Retry of that sends it anyway, once, without another read; then the next one.
func TestHeldRetriedUnclearNotFoundIsLeftToTheOwner(t *testing.T) {
	d, st, n, up := retryAway(t, []proto.Item{{Role: "user", Text: "a"}}) // the words, not the node's tie
	close(up)
	eventually(t, 5*time.Second, "left to the owner", func() bool {
		r := st.all()[0]
		return r.State == db.HeldFailed && r.Failure == db.HeldUnconfirmed
	})
	heldTicks(t, d, 2)
	l := d.HeldMessages("mac", i18n.En)["s1"]
	if got, reads := n.sent(), n.itemReads(); len(got) != 0 || reads != 1 || l[0].State != proto.UndeliveredFailed ||
		l[0].Error != heldUnconfirmedLine || l[1].Error != "Waits behind the message above" {
		t.Fatalf("sent %v, %d reads, rows %+v", got, reads, l)
	}
	if found, err := d.HeldRetry(context.Background(), "s1", "a"); !found || err != nil {
		t.Fatalf("retry anyway: %v %v", found, err)
	}
	if r := st.all()[0]; r.State != db.HeldWaiting || r.Failure != "" {
		t.Fatalf("retried anyway %+v", r)
	}
	eventually(t, 5*time.Second, "a and b sent", func() bool { return slices.Equal(n.sent(), []string{"a", "b"}) && len(st.all()) == 0 })
	if reads := n.itemReads(); reads != 1 {
		t.Fatalf("read again: %d", reads)
	}
}

// The transcript not read: it is not sent; it waits, and the next look reads it again.
func TestHeldRetriedUnclearUnreadIsNotSent(t *testing.T) {
	d, st, n, up := retryAway(t, nil)
	n.mu.Lock()
	n.itemsErr = errors.New("transcript not found")
	n.mu.Unlock()
	close(up)
	eventually(t, 5*time.Second, "read once", func() bool { return n.itemReads() == 1 })
	heldIdle(t, d)
	if got := n.sent(); len(got) != 0 || st.all()[0].State != db.HeldWaiting {
		t.Fatalf("sent %v, stored %+v", got, st.all())
	}
	heldTicks(t, d, 1) // read again: not there, left to the owner; nothing sent
	if got, r := n.sent(), st.all()[0]; len(got) != 0 || n.itemReads() != 2 || r.State != db.HeldFailed || r.Failure != db.HeldUnconfirmed {
		t.Fatalf("sent %v, %d reads, stored %+v", got, n.itemReads(), r)
	}
}

// While the transcript is read for a message the owner's Retry answers 409: nothing races the read, and nothing is sent
// twice.
func TestHeldCheckHoldsOffARetry(t *testing.T) {
	st := newHeldMem()
	at := time.Now().Add(-time.Hour)
	putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, at)
	putHeld(t, st, "s1", "b", db.HeldWaiting, "", at)
	d, _ := heldDevices(t, st, time.Hour)
	gate, reading := make(chan struct{}), make(chan struct{}, 1)
	n := &heldNode{items: map[string][]proto.Item{"s1": {{Role: "user", Text: "a", ClientMsgID: "a"}}}, itemsGate: gate, reading: reading}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	close(up)
	<-reading
	if code, body := personCall(t, d.Handler(http.NotFoundHandler()), "POST", "/api/v1/sessions/mac/s1/held/a/retry"); code != http.StatusConflict {
		t.Fatalf("retry during the read: %d %v", code, body)
	}
	n.mu.Lock()
	n.itemsGate = nil
	n.mu.Unlock()
	close(gate)
	eventually(t, 5*time.Second, "b sent", func() bool { return slices.Equal(n.sent(), []string{"b"}) && len(st.all()) == 0 })
}

// A Retry looks for it again on the same connection: read once already (not there then), it may be there by now.
func TestHeldRetryRechecksOnTheSameConnection(t *testing.T) {
	st := newHeldMem()
	at := time.Now().Add(-time.Hour)
	putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, at)
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	close(up)
	eventually(t, 5*time.Second, "read on connecting", func() bool { return n.itemReads() == 1 })
	n.mu.Lock() // the try that was cut off shows up in the transcript after all
	n.items = map[string][]proto.Item{"s1": {{Role: "user", Text: "a", ClientMsgID: "a"}}}
	n.mu.Unlock()
	if found, err := d.HeldRetry(context.Background(), "s1", "a"); !found || err != nil {
		t.Fatalf("retry: %v %v", found, err)
	}
	eventually(t, 5*time.Second, "looked for again", func() bool { return len(st.all()) == 0 })
	if got, reads := n.sent(), n.itemReads(); len(got) != 0 || reads != 2 {
		t.Fatalf("sent %v, %d reads", got, reads)
	}
}

// A hold the store may have kept though its answer and the look-up after it both failed (the app was told it failed)
// is dropped by the repair, untried; nothing sends it. One held since (the app sent the same message again) stays.
func TestHeldUnsureHoldIsDropped(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	ctx := context.Background()
	unsure := func(id string) {
		t.Helper()
		st.set(func(s *heldMem) {
			s.committedErr, s.lookupErr = errors.New("timeout"), errors.New("connection refused")
		})
		var re *rpc.Error
		if _, err := d.Call(ctx, heldSend("mac", "s1", id, id)); !errors.As(err, &re) || re.Code != devicewire.CodeMachineOffline {
			t.Fatalf("%s: %v", id, err)
		}
		st.set(func(s *heldMem) { s.committedErr, s.lookupErr = nil, nil })
	}
	unsure("a")
	unsure("b")
	out, err := d.Call(ctx, heldSend("mac", "s1", "b", "b")) // the app sends b again: held now
	if held := heldAnswerOf(t, out, err); held.ID != "b" {
		t.Fatalf("b: %+v", held)
	}
	if rows := st.all(); len(rows) != 2 || d.HeldMessages("mac", i18n.En)["s1"][0].ID != "b" {
		t.Fatalf("stored %+v", rows)
	}
	d.heldRepair(ctx)
	if rows := st.all(); len(rows) != 1 || rows[0].OpID != "b" {
		t.Fatalf("after the repair %+v", rows)
	}
	close(up)
	eventually(t, 5*time.Second, "b sent", func() bool { return len(st.all()) == 0 })
	if got := n.sent(); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("sent %v", got)
	}
}

// A claim names its try by the store's attempts, not this manager's count: after another manager's tries the
// delivery's writes still find it (no stale row, no message taken as unclear).
func TestHeldClaimTakesTheStoresAttempts(t *testing.T) {
	st := newHeldMem()
	a := putHeld(t, st, "s1", "a", db.HeldWaiting, "", time.Now())
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	st.set(func(s *heldMem) { r := s.rows[a]; r.Attempts = 5; s.rows[a] = r }) // tried elsewhere since this one read it
	close(up)
	eventually(t, 5*time.Second, "a sent and gone", func() bool { return len(n.sent()) == 1 && len(st.all()) == 0 })
	if d.HeldMessages("mac", i18n.En) != nil {
		t.Fatalf("still shown: %+v", d.HeldMessages("mac", i18n.En))
	}
}

// A session holds heldPerSession messages at most: one more is refused in words, nothing kept; another session, or the
// same message again, is not.
func TestHeldCapPerSession(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour)
	offlineMachine(d, "mac", "Studio Mac")
	ctx := i18n.With(context.Background(), i18n.En)
	for i := range heldPerSession {
		out, err := d.Call(ctx, heldSend("mac", "s1", fmt.Sprintf("m%d", i), "x"))
		heldAnswerOf(t, out, err)
	}
	var re *rpc.Error
	if _, err := d.Call(ctx, heldSend("mac", "s1", "one-more", "x")); !errors.As(err, &re) || re.Code != "held_full" ||
		re.Message != "Too many messages are waiting for this Agent. Delete some, or send this one when the computer is back." ||
		string(re.Data) != `{"delivered":"none"}` {
		t.Fatalf("one more: %v", err)
	}
	out, err := d.Call(ctx, heldSend("mac", "s1", "m0", "x"))
	if held := heldAnswerOf(t, out, err); held.ID != "m0" {
		t.Fatalf("again: %+v", held)
	}
	out, err = d.Call(ctx, heldSend("mac", "s2", "other", "x"))
	heldAnswerOf(t, out, err)
	if n := len(st.all()); n != heldPerSession+1 {
		t.Fatalf("%d stored", n)
	}
}

// What is kept in memory of a held message is what its row shows, not its call (pictures up to 16 MiB): the call is
// read from the store when it goes, pictures and all.
func TestHeldRowsKeepNoCall(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	p := heldSend("mac", "s1", "a", "look")
	p.Params = json.RawMessage(`{"session_id":"s1","client_msg_id":"a","text":"look","images":[{"data":"aGVsbG8="},{"data":"d29ybGQ="}]}`)
	out, err := d.Call(context.Background(), p)
	if held := heldAnswerOf(t, out, err); held.Text != "look" || held.Images != 2 {
		t.Fatalf("held %+v", held)
	}
	d.held.mu.Lock()
	for _, r := range d.held.rows["mac"] {
		if r.Call != nil {
			t.Errorf("kept the call in memory: %s", r.Call)
		}
	}
	d.held.mu.Unlock()
	close(up)
	eventually(t, 5*time.Second, "a sent", func() bool { return len(n.sent()) == 1 })
	n.mu.Lock()
	images, _ := n.sends[0]["images"].([]any)
	n.mu.Unlock()
	if len(images) != 2 {
		t.Fatalf("sent without its pictures: %d", len(images))
	}
}

// A message the owner retried after a try that may have arrived is the owner's decision already: it does not expire
// however long its computer is away (the expiry would drop its mark, and a later Retry would send it unlooked-for);
// back, it is looked for, and not sent when it is there.
func TestHeldCheckFirstDoesNotExpire(t *testing.T) {
	st := newHeldMem()
	t0 := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, t0)
	var mu sync.Mutex
	now := t0.Add(time.Hour)
	d, _ := heldDevicesAt(t, st, time.Hour, func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	n := &heldNode{items: map[string][]proto.Item{"s1": {{Role: "user", Text: "a", ClientMsgID: "a"}}}}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	if found, err := d.HeldRetry(context.Background(), "s1", "a"); !found || err != nil {
		t.Fatalf("retry: %v %v", found, err)
	}
	mu.Lock()
	now = now.Add(25 * time.Hour)
	mu.Unlock()
	d.heldExpire(context.Background())
	if r := st.all()[0]; r.State != db.HeldWaiting || r.Failure != db.HeldUnclear {
		t.Fatalf("expired: %+v", r)
	}
	close(up)
	eventually(t, 5*time.Second, "found and dropped", func() bool { return len(st.all()) == 0 })
	heldTicks(t, d, 1)
	if got := n.sent(); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
}

// The owner's Delete goes while the transcript is read for that message: nothing of it is sent, whatever the read
// finds, and the next one goes.
func TestHeldDeleteDuringACheck(t *testing.T) {
	st := newHeldMem()
	at := time.Now().Add(-time.Hour)
	putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, at)
	putHeld(t, st, "s1", "b", db.HeldWaiting, "", at)
	d, _ := heldDevices(t, st, time.Hour)
	gate, reading := make(chan struct{}), make(chan struct{}, 1)
	n := &heldNode{items: map[string][]proto.Item{"s1": {{Role: "user", Text: "a", ClientMsgID: "a"}}}, itemsGate: gate, reading: reading}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	close(up)
	<-reading
	if code, body := personCall(t, d.Handler(http.NotFoundHandler()), "DELETE", "/api/v1/sessions/mac/s1/held/a"); code != http.StatusOK {
		t.Fatalf("delete during the read: %d %v", code, body)
	}
	n.mu.Lock()
	n.itemsGate = nil
	n.mu.Unlock()
	close(gate)
	eventually(t, 5*time.Second, "b sent", func() bool { return slices.Equal(n.sent(), []string{"b"}) && len(st.all()) == 0 })
	heldTicks(t, d, 1)
	if got := n.sent(); !slices.Equal(got, []string{"b"}) || d.HeldMessages("mac", i18n.En) != nil {
		t.Fatalf("sent %v, rows %+v", got, d.HeldMessages("mac", i18n.En))
	}
}

// A transcript that cannot be read is tried heldCheckTries times, then the message is left to the owner (unconfirmed),
// not read again on every tick; nothing is sent.
func TestHeldFailedReadsLeaveItToTheOwner(t *testing.T) {
	d, st, n, up := retryAway(t, nil)
	n.mu.Lock()
	n.itemsFail = errors.New("transcript not found")
	n.mu.Unlock()
	close(up)
	eventually(t, 5*time.Second, "read once", func() bool { return n.itemReads() == 1 })
	heldIdle(t, d)
	heldTicks(t, d, heldCheckTries+3)
	if r := st.all()[0]; r.State != db.HeldFailed || r.Failure != db.HeldUnconfirmed {
		t.Fatalf("stored %+v", r)
	}
	if got, reads := n.sent(), n.itemReads(); len(got) != 0 || reads != heldCheckTries {
		t.Fatalf("sent %v, %d reads", got, reads)
	}
	if l := d.HeldMessages("mac", i18n.En)["s1"]; l[0].Error != heldUnconfirmedLine {
		t.Fatalf("row %+v", l)
	}
}

// The repair of a hold the store may have kept (unsure) and the app sending that message again never lose it: a hold
// of the key while its row is being deleted waits for that delete, then holds it anew; it goes once.
func TestHeldUnsureRepairWaitsForARehold(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	ctx := context.Background()
	st.set(func(s *heldMem) {
		s.committedErr, s.lookupErr = errors.New("timeout"), errors.New("connection refused")
	})
	if _, err := d.Call(ctx, heldSend("mac", "s1", "a", "a")); err == nil {
		t.Fatal("held though unsure")
	}
	gate, deleting := make(chan struct{}), make(chan struct{}, 1)
	st.set(func(s *heldMem) {
		s.committedErr, s.lookupErr, s.unsureGate, s.unsureWaiting = nil, nil, gate, deleting
	})
	repaired := make(chan struct{})
	go func() { d.heldRepair(ctx); close(repaired) }()
	<-deleting // the repair deletes a's row now
	resent := make(chan error, 1)
	go func() { _, err := d.Call(ctx, heldSend("mac", "s1", "a", "a")); resent <- err }()
	eventually(t, 5*time.Second, "the hold waiting for the repair, or done", func() bool {
		select {
		case err := <-resent: // done before the repair's delete: what must not happen (the checks below fail)
			resent <- err
			return true
		default:
		}
		d.held.mu.Lock()
		defer d.held.mu.Unlock()
		return d.held.waiting == 1
	})
	close(gate)
	<-repaired
	out, err := json.RawMessage(nil), <-resent
	if held := heldAnswerOf(t, out, err); held.ID != "a" {
		t.Fatalf("a: %+v", held)
	}
	if rows := st.all(); len(rows) != 1 || rows[0].OpID != "a" {
		t.Fatalf("stored %+v", rows)
	}
	close(up)
	eventually(t, 5*time.Second, "a sent", func() bool { return len(st.all()) == 0 })
	if got := n.sent(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("sent %v", got)
	}
}

// The repair leaves an unsure key alone while a hold of it is under way (the app sent it again).
func TestHeldUnsureRepairLeavesAHoldUnderWay(t *testing.T) {
	st := newHeldMem()
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Studio Mac", n, up)
	ctx := context.Background()
	st.set(func(s *heldMem) {
		s.committedErr, s.lookupErr = errors.New("timeout"), errors.New("connection refused")
	})
	if _, err := d.Call(ctx, heldSend("mac", "s1", "a", "a")); err == nil {
		t.Fatal("held though unsure")
	}
	gate, holding := make(chan struct{}), make(chan struct{}, 1)
	st.set(func(s *heldMem) { s.committedErr, s.lookupErr, s.holdGate, s.holdWaiting = nil, nil, gate, holding })
	resent := make(chan error, 1)
	go func() { _, err := d.Call(ctx, heldSend("mac", "s1", "a", "a")); resent <- err }()
	<-holding // the hold of a is under way
	d.heldRepair(ctx)
	if rows := st.all(); len(rows) != 1 {
		t.Fatalf("the repair dropped a hold under way: %+v", rows)
	}
	close(gate)
	if held := heldAnswerOf(t, nil, <-resent); held.ID != "a" {
		t.Fatalf("a: %+v", held)
	}
	d.heldRepair(ctx)
	close(up)
	eventually(t, 5*time.Second, "a sent", func() bool { return len(st.all()) == 0 })
	if got := n.sent(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("sent %v", got)
	}
}

// The reviewer's case, its first half: a retried one to be looked for whose session is not listed (a runtime Agent
// gone from the list) is the owner's decision; days on, it has not expired, and the one behind it is renewed, not
// failed as if its computer were away.
func TestHeldBehindARetriedOneDoesNotExpire(t *testing.T) {
	st := newHeldMem()
	t0 := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, t0)
	b := putHeld(t, st, "s1", "b", db.HeldWaiting, "", t0)
	var mu sync.Mutex
	now := t0.Add(time.Hour)
	d, _ := heldDevicesAt(t, st, time.Hour, func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	n := &heldNode{listed: []string{"s2"}}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Laptop", n, up)
	close(up)
	eventually(t, 5*time.Second, "connected", func() bool { listed, _ := d.machineByID("mac").heldOn("s2"); return !listed.IsZero() })
	if found, err := d.HeldRetry(context.Background(), "s1", "a"); !found || err != nil {
		t.Fatalf("retry: %v %v", found, err)
	}
	mu.Lock()
	now = t0.Add(49 * time.Hour)
	mu.Unlock()
	heldTicks(t, d, 1)
	rows := st.all()
	if rows[0].State != db.HeldWaiting || rows[0].Failure != db.HeldUnclear {
		t.Fatalf("a %+v", rows[0])
	}
	if r := st.snapshot()[b]; r.State != db.HeldWaiting || !r.HeldAt.Equal(t0.Add(49*time.Hour)) {
		t.Fatalf("b %+v", r)
	}
	if got := n.sent(); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
}

// Its second half: once its node has taken messages for heldListWait without listing the session, the retried one is
// looked for anyway; reads that fail (no such session) leave it to the owner after heldCheckTries; nothing is sent.
func TestHeldRetriedOneOfAnUnlistedSessionIsLookedFor(t *testing.T) {
	old := heldListWait
	heldListWait = 0 // the connection is old enough at once
	t.Cleanup(func() { heldListWait = old })
	st := newHeldMem()
	at := time.Now().Add(-time.Hour)
	putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, at)
	putHeld(t, st, "s1", "b", db.HeldWaiting, "", at)
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{listed: []string{"s2"}, itemsFail: errors.New("no such session")}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Laptop", n, up)
	if found, err := d.HeldRetry(context.Background(), "s1", "a"); !found || err != nil {
		t.Fatalf("retry: %v %v", found, err)
	}
	close(up)
	eventually(t, 5*time.Second, "looked for", func() bool { return n.itemReads() == 1 })
	heldIdle(t, d)
	heldTicks(t, d, heldCheckTries)
	if r := st.all()[0]; r.State != db.HeldFailed || r.Failure != db.HeldUnconfirmed {
		t.Fatalf("a %+v", st.all())
	}
	if got, reads := n.sent(), n.itemReads(); len(got) != 0 || reads != heldCheckTries {
		t.Fatalf("sent %v, %d reads", got, reads)
	}
}

// Only reads that fail in a row leave a message that may have arrived to the owner: one that read fine in between
// (not there) starts the count again, across connections days apart.
func TestHeldOnlyFailedReadsInARowLeaveItToTheOwner(t *testing.T) {
	st := newHeldMem()
	a := putHeld(t, st, "s1", "a", db.HeldFailed, db.HeldUnclear, time.Now().Add(-time.Hour))
	d, _ := heldDevices(t, st, time.Hour)
	n := &heldNode{}
	up := make(chan struct{})
	laterMachine(t, d, "mac", "Laptop", n, up)
	close(up)
	eventually(t, 5*time.Second, "read on connecting", func() bool { return n.itemReads() == 1 })
	heldIdle(t, d)
	// Connections 2 (fails), 3 (fails), 4 (reads: not there), 5 (fails): a new connection reads it once again.
	for i, fail := range []bool{true, true, false, true} {
		n.mu.Lock()
		if fail {
			n.itemsErr = errors.New("transcript not found")
		}
		n.mu.Unlock()
		d.held.mu.Lock()
		d.held.rowLocked("mac", a).checked = time.Time{} // as a new connection: not read on it yet
		d.held.mu.Unlock()
		heldTicks(t, d, 1)
		if reads := n.itemReads(); reads != i+2 {
			t.Fatalf("connection %d: %d reads", i+2, reads)
		}
	}
	if r := st.all()[0]; r.State != db.HeldFailed || r.Failure != db.HeldUnclear {
		t.Fatalf("left to the owner after failures not in a row: %+v", r)
	}
}
