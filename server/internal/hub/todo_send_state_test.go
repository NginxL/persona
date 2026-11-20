package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// A to-do's writes under an op id say how they stand on the socket (todo_send_state, todo_receipts.go, #10465):
// processing as soon as the hub has them, then how the relay ended; only to the full apps that asked.

// todoRoutePath is the conversation route a to-do's message goes on; todoRouteBody names to-do todo_a1 in it.
const (
	todoRoutePath = "/api/v1/assistant/conversations/conv1/route"
	todoRouteBody = `{"todo_id":"todo_a1","text":"Book the earlier train"}`
)

// postTodoRoute posts body to todoRoutePath through mux, with op as X-Persona-Op-Id when set.
func postTodoRoute(t *testing.T, ctx context.Context, h *Hub, mux http.Handler, op, body string) (int, string) {
	t.Helper()
	return postTodo(t, ctx, h, mux, todoRoutePath, op, body)
}

// todoSendApps are the apps a to-do write's states may reach: knows asked for them on its socket's URL, hello in its
// client_hello; the watch (an answer-only device) asked too; other did not ask.
type todoSendApps struct{ knows, hello, watch, other *client }

func newTodoSendApps(t *testing.T, h *Hub) todoSendApps {
	t.Helper()
	a := todoSendApps{
		knows: &client{out: make(chan []byte, 64), role: roleFull, urlFeatures: map[string]bool{proto.FeatureTodoSendState: true}},
		hello: &client{out: make(chan []byte, 64), role: roleFull},
		watch: &client{out: make(chan []byte, 64), role: roleAnswer, urlFeatures: map[string]bool{proto.FeatureTodoSendState: true}},
		other: &client{out: make(chan []byte, 64), role: roleFull},
	}
	addTestClient(h, a.knows, a.hello, a.watch, a.other)
	h.onClientMessage(context.Background(), a.hello, []byte(`{"type":"client_hello","features":["todo_send_state"]}`))
	if got := appGot(a.hello, 200*time.Millisecond); !strings.Contains(got, "client_hello_ack") {
		t.Fatalf("no client_hello_ack: %s", got)
	}
	return a
}

// nextTodoSendState is the next todo_send_state c gets, within five seconds.
func nextTodoSendState(t *testing.T, c *client) proto.TodoSendState {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case raw := <-c.out:
			var event proto.TodoSendState
			if json.Unmarshal(raw, &event) == nil && event.Type == "todo_send_state" {
				return event
			}
		case <-timer.C:
			t.Fatal("no todo_send_state within five seconds")
		}
	}
}

// soon waits up to five seconds for ch, failing with what did not happen.
func soon[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("%s within five seconds", what)
	}
	panic("unreachable")
}

// noTodoSendState: c gets no todo_send_state for a moment.
func noTodoSendState(t *testing.T, c *client, who string) {
	t.Helper()
	if got := appGot(c, 200*time.Millisecond); strings.Contains(got, `"todo_send_state"`) {
		t.Fatalf("%s got a to-do's send state: %s", who, got)
	}
}

// Processing reaches the apps that asked before the manager answers, also after the phone that sent it went away;
// then delivered. A retry of the op id while the write runs waits for its reply and says nothing; asked again later it
// gets the reply back, still saying nothing. The watch and an app that did not ask get nothing. Both routes, the op id
// lowercased as the receipt keeps it, the to-do the body's on the conversation route and the path's on the asks route.
func TestTodoSendStateProcessingThenDelivered(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		post             func(*testing.T, context.Context, *Hub, http.Handler, string, string) (int, string)
	}{
		{"conversation route", todoRoutePath, todoRouteBody, postTodoRoute},
		{"asks route", todoAnswerPath, `{"option":"Kaede"}`, postTodoAnswer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			store := &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}, read: make(chan struct{}, 1)}}
			h.store = store
			manager, _ := connectInboxManager(t, h)
			var calls atomic.Int32
			arrived, release := make(chan struct{}, 1), make(chan struct{})
			manager.mu.Lock()
			manager.http = func(_ context.Context, r assistantwire.HTTPRequest) (any, error) {
				if r.Path != tc.path {
					return assistantwire.HTTPResponse{Status: 404, Body: []byte(`{"error":"Not Found","code":"not_found"}`)}, nil
				}
				calls.Add(1)
				arrived <- struct{}{}
				<-release
				return assistantwire.HTTPResponse{Status: 200, Header: map[string][]string{"Content-Type": {"application/json"}},
					Body: []byte(`{"item":{"id":"f1"}}`)}, nil
			}
			manager.mu.Unlock()
			apps := newTodoSendApps(t, h)
			mux := h.routes()
			const op = "00000000-0000-4000-8000-0000000A0465"
			const lower = "00000000-0000-4000-8000-0000000a0465"

			ctx, cancel := context.WithCancel(context.Background())
			first := make(chan int, 1)
			go func() { code, _ := tc.post(t, ctx, h, mux, op, tc.body); first <- code }()
			for _, c := range []*client{apps.knows, apps.hello} {
				event := nextTodoSendState(t, c)
				if event.State != proto.MessageSendProcessing || event.TodoID != "todo_a1" || event.ClientOpID != lower || event.At.IsZero() {
					t.Fatalf("processing: %+v", event)
				}
			}
			soon(t, arrived, "the manager never got the write") // the manager has it, and has not answered
			cancel()                                            // the phone goes away: the write goes on
			type reply struct {
				code int
				body string
			}
			retry := make(chan reply, 1)
			go func() { code, out := tc.post(t, context.Background(), h, mux, op, tc.body); retry <- reply{code, out} }()
			soon(t, store.ops.read, "the retry did not wait for the receipt") // the retry waits for the first write's receipt
			noTodoSendState(t, apps.knows, "an app while the write runs")
			close(release)
			soon(t, first, "the first write never answered")
			for _, c := range []*client{apps.knows, apps.hello} {
				if event := nextTodoSendState(t, c); event.State != proto.MessageSendDelivered || event.ClientOpID != lower || event.TodoID != "todo_a1" {
					t.Fatalf("delivered: %+v", event)
				}
			}
			if got := soon(t, retry, "the retry never answered"); got.code != http.StatusOK || !strings.Contains(got.body, `"f1"`) {
				t.Fatalf("the retry: %+v", got)
			}
			if code, out := tc.post(t, context.Background(), h, mux, op, tc.body); code != 200 || !strings.Contains(out, `"f1"`) {
				t.Fatalf("asked again: %d %s", code, out)
			}
			noTodoSendState(t, apps.knows, "an app after the retries")
			noTodoSendState(t, apps.watch, "the watch")
			noTodoSendState(t, apps.other, "an app that did not ask")
			if n := calls.Load(); n != 1 {
				t.Fatalf("the manager was asked %d times", n)
			}
		})
	}
}

// Failed only when the answer shows nothing was taken, so a phone may send again under a new op id: a write that
// reached no manager (its receipt not kept), a refusal that says delivered "none", one refused before anything ran.
// A refusal that says nothing of delivery may have typed the text (the conversation route answers 409 conflict for a
// send whose result the manager does not know): unknown, and the same op id gets it back. A 200 that says no Agent
// could take the answer is still delivered: the manager took it and handed it to the supervisor. The receipt records
// what the final state says.
func TestTodoSendStateFinalStates(t *testing.T) {
	h := newTestHub(t)
	store := &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	h.store = store
	apps := newTodoSendApps(t, h)
	mux := h.routes()
	final := func(op string) proto.TodoSendState {
		t.Helper()
		if event := nextTodoSendState(t, apps.knows); event.State != proto.MessageSendProcessing || event.ClientOpID != op {
			t.Fatalf("processing: %+v", event)
		}
		event := nextTodoSendState(t, apps.knows)
		if event.ClientOpID != op {
			t.Fatalf("another op's state: %+v", event)
		}
		return event
	}

	const offline = "00000000-0000-4000-8000-0000000b0465"
	if code, out := postTodoRoute(t, context.Background(), h, mux, offline, todoRouteBody); code != http.StatusServiceUnavailable ||
		!strings.Contains(out, assistantwire.CodeAssistantOffline) {
		t.Fatalf("no manager: %d %s", code, out)
	}
	if event := final(offline); event.State != proto.MessageSendFailed {
		t.Fatalf("no manager: %+v", event)
	}
	if got := receiptStates(store); len(got) != 0 {
		t.Fatalf("a write that reached no manager kept a receipt: %v", got)
	}

	manager, _ := connectInboxManager(t, h)
	answer := assistantwire.HTTPResponse{}
	manager.mu.Lock()
	manager.http = func(context.Context, assistantwire.HTTPRequest) (any, error) { return answer, nil }
	manager.mu.Unlock()
	for _, tc := range []struct {
		name, op, path string
		sent           string // the app's request
		status         int    // the manager's answer
		body           string
		want, receipt  string
	}{
		{"a send the manager is not sure of", "00000000-0000-4000-8000-0000000c0465", todoRoutePath, todoRouteBody, http.StatusConflict,
			`{"error":"context deadline exceeded","code":"conflict"}`, proto.MessageSendUnknown, "unknown"},
		{"an answer not taken, as far as the manager says", "00000000-0000-4000-8000-0000000c1465", todoAnswerPath, `{"text":"Hanami"}`,
			http.StatusBadGateway, `{"error":"Not taken: the computer dropped","code":"accept_not_taken"}`, proto.MessageSendUnknown, "unknown"},
		{"a node's refusal", "00000000-0000-4000-8000-0000000d0465", todoAnswerPath, `{"text":"Hanami"}`, http.StatusLocked,
			`{"error":"A dialog is open","code":"dialog_open","delivered":"none"}`, proto.MessageSendFailed, "failed"},
		{"refused before anything ran", "00000000-0000-4000-8000-0000000d1465", todoAnswerPath, `{"option":"Kaede"}`, http.StatusNotFound,
			`{"error":"todo not found","code":"todo_not_found"}`, proto.MessageSendFailed, "failed"},
		{"recorded for the supervisor", "00000000-0000-4000-8000-0000000e0465", todoAnswerPath, `{"option":"Kaede"}`, http.StatusOK,
			`{"ask":"todo:decision@2026-11-10T09:00:00Z","outcome":"answered","delivered":"none"}`, proto.MessageSendDelivered, "done"},
	} {
		manager.mu.Lock()
		answer = assistantwire.HTTPResponse{Status: tc.status, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(tc.body)}
		manager.mu.Unlock()
		for range 2 { // asked again, the same reply and no second state
			if code, out := postTodo(t, context.Background(), h, mux, tc.path, tc.op, tc.sent); code != tc.status || out != tc.body {
				t.Fatalf("%s: %d %s", tc.name, code, out)
			}
		}
		if event := final(tc.op); event.State != tc.want {
			t.Fatalf("%s: %+v", tc.name, event)
		}
		if got := receiptStates(store)[tc.op]; got != tc.receipt {
			t.Fatalf("%s: receipt %q", tc.name, got)
		}
		noTodoSendState(t, apps.knows, tc.name+" asked again")
	}
}

// finishStore holds a to-do write's receipt while it is stored (finishing, then release), and fails to store it when
// fail is set.
type finishStore struct {
	*receiptStore
	finishing, release chan struct{}
	fail               atomic.Bool
}

func (s *finishStore) FinishClientOp(ctx context.Context, id, state string, result json.RawMessage) error {
	s.finishing <- struct{}{}
	<-s.release
	if s.fail.Load() {
		return errors.New("the database went away")
	}
	return s.receiptStore.FinishClientOp(ctx, id, state, result)
}

// The final state goes out once the receipt is stored, never before: one the hub could not store is unknown, as the
// app's HTTP answer (503 state unknown) and a Check of the op id are, though the manager took the message.
func TestTodoSendStateAfterTheReceipt(t *testing.T) {
	h := newTestHub(t)
	store := &finishStore{receiptStore: &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}},
		finishing: make(chan struct{}, 1), release: make(chan struct{})}
	h.store = store
	manager, _ := connectInboxManager(t, h)
	manager.mu.Lock()
	manager.http = func(context.Context, assistantwire.HTTPRequest) (any, error) {
		return assistantwire.HTTPResponse{Status: 200, Header: map[string][]string{"Content-Type": {"application/json"}},
			Body: []byte(`{"item":{"id":"f1"}}`)}, nil
	}
	manager.mu.Unlock()
	apps := newTodoSendApps(t, h)
	mux := h.routes()
	type reply struct {
		code int
		body string
	}
	for _, tc := range []struct {
		op         string
		fail       bool
		code       int
		want, body string
	}{
		{"00000000-0000-4000-8000-000000120465", false, http.StatusOK, proto.MessageSendDelivered, `"f1"`},
		{"00000000-0000-4000-8000-000000130465", true, http.StatusServiceUnavailable, proto.MessageSendUnknown, `"state":"unknown"`},
	} {
		store.fail.Store(tc.fail)
		answered := make(chan reply, 1)
		go func() {
			code, out := postTodoRoute(t, context.Background(), h, mux, tc.op, todoRouteBody)
			answered <- reply{code, out}
		}()
		if event := nextTodoSendState(t, apps.knows); event.State != proto.MessageSendProcessing || event.ClientOpID != tc.op {
			t.Fatalf("processing: %+v", event)
		}
		soon(t, store.finishing, "the receipt was never stored")
		noTodoSendState(t, apps.knows, "an app while the receipt is stored")
		store.release <- struct{}{}
		if got := soon(t, answered, "the write never answered"); got.code != tc.code || !strings.Contains(got.body, tc.body) {
			t.Fatalf("fail=%t: %d %s", tc.fail, got.code, got.body)
		}
		if event := nextTodoSendState(t, apps.knows); event.State != tc.want || event.ClientOpID != tc.op {
			t.Fatalf("fail=%t: %+v", tc.fail, event)
		}
	}
}

// A message cut off while the manager has it may have been typed: 502 assistant_in_doubt and unknown, kept, so the
// same op id gets that reply again and the manager is asked once. With an op id the conversation route is relayed as a
// send, as the asks route is.
func TestTodoSendStateUnknownWhenCutOff(t *testing.T) {
	h := newTestHub(t)
	store := &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	h.store = store
	manager, link := connectInboxManager(t, h)
	got := make(chan struct{}, 1)
	var calls atomic.Int32
	manager.mu.Lock()
	manager.http = func(ctx context.Context, _ assistantwire.HTTPRequest) (any, error) {
		calls.Add(1)
		got <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	manager.mu.Unlock()
	go func() {
		<-got
		_ = link.ws.CloseNow()
	}()
	apps := newTodoSendApps(t, h)
	mux := h.routes()
	const op = "00000000-0000-4000-8000-0000000f0465"
	send := func() (int, map[string]any) {
		code, out := postTodoRoute(t, context.Background(), h, mux, op, todoRouteBody)
		var body map[string]any
		if err := json.Unmarshal([]byte(out), &body); err != nil {
			t.Fatalf("reply %d %s", code, out)
		}
		return code, body
	}
	if code, out := send(); code != http.StatusBadGateway || out["code"] != assistantwire.CodeAssistantInDoubt || out["delivered"] != "unknown" {
		t.Fatalf("cut off: %d %v", code, out)
	}
	if event := nextTodoSendState(t, apps.knows); event.State != proto.MessageSendProcessing {
		t.Fatalf("processing: %+v", event)
	}
	if event := nextTodoSendState(t, apps.knows); event.State != proto.MessageSendUnknown || event.ClientOpID != op {
		t.Fatalf("cut off: %+v", event)
	}
	if got := receiptStates(store); got[op] != "unknown" {
		t.Fatalf("receipt = %v", got)
	}
	connectInboxManager(t, h)
	if code, out := send(); code != http.StatusBadGateway || out["code"] != assistantwire.CodeAssistantInDoubt {
		t.Fatalf("asked again: %d %v", code, out)
	}
	noTodoSendState(t, apps.knows, "an app after the retry")
	if n := calls.Load(); n != 1 {
		t.Fatalf("the manager was asked %d times", n)
	}
}

// A message on the conversation route without an op id, all an older app sends, is relayed as before: the manager
// gets the same request and the app the same bytes as from the plain relay, a refusal, no manager and a link cut off
// while the manager had it included (never assistant_in_doubt, which only a write under an op id gets); no state goes
// out and no receipt is kept. One whose body names no to-do says nothing either.
func TestTodoRouteWithoutOpIDIsUnchanged(t *testing.T) {
	h := newTestHub(t)
	store := &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	h.store = store
	apps := newTodoSendApps(t, h)
	routed := http.NewServeMux()
	h.assistantRoutesRelayed(routed)
	plain := http.NewServeMux() // the relay this route had before it kept receipts
	plain.HandleFunc("POST /api/v1/assistant/conversations/{id}/route", func(w http.ResponseWriter, r *http.Request) {
		h.forwardOwned(w, r, h.byConversation)
	})
	post := func(mux http.Handler, op, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, todoRoutePath, strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{Role: roleFull}))
		if op != "" {
			r.Header.Set("X-Persona-Op-Id", op)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	same := func(what, body string) {
		t.Helper()
		was, now := post(plain, "", body), post(routed, "", body)
		if was.Code != now.Code || !bytes.Equal(was.Body.Bytes(), now.Body.Bytes()) || !maps.EqualFunc(was.Header(), now.Header(), slices.Equal[[]string]) {
			t.Fatalf("%s: was %d %v %s, now %d %v %s", what, was.Code, was.Header(), was.Body, now.Code, now.Header(), now.Body)
		}
		t.Logf("%s: %d %s", what, now.Code, strings.TrimSpace(now.Body.String()))
	}

	same("no manager", todoRouteBody)
	manager, link := connectInboxManager(t, h)
	var asked []assistantwire.HTTPRequest
	answer := assistantwire.HTTPResponse{Status: 200, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(`{"item":{"id":"f1"}}`)}
	manager.mu.Lock()
	manager.http = func(_ context.Context, r assistantwire.HTTPRequest) (any, error) {
		manager.mu.Lock()
		asked = append(asked, r)
		a := answer
		manager.mu.Unlock()
		return a, nil
	}
	manager.mu.Unlock()
	same("taken", todoRouteBody)
	manager.mu.Lock()
	if len(asked) != 2 || asked[0].Path != asked[1].Path || !bytes.Equal(asked[0].Body, asked[1].Body) || !maps.EqualFunc(asked[0].Header, asked[1].Header, slices.Equal[[]string]) {
		manager.mu.Unlock()
		t.Fatalf("the manager was asked differently: %+v", asked)
	}
	answer = assistantwire.HTTPResponse{Status: 409, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: []byte(`{"error":"Not this chat's task","code":"conflict"}`)}
	manager.mu.Unlock()
	same("refused", todoRouteBody)

	// Cut off while the manager has it: each relay on a link of its own, dropped once the manager has the request.
	cut := func(link *rawBus) {
		got := make(chan struct{}, 1)
		manager.mu.Lock()
		manager.http = func(ctx context.Context, _ assistantwire.HTTPRequest) (any, error) {
			got <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		manager.mu.Unlock()
		go func() {
			<-got
			_ = link.ws.CloseNow()
		}()
	}
	cut(link)
	was := post(plain, "", todoRouteBody)
	eventually(t, 5*time.Second, "the cut link let go", func() bool { return h.busConnOf(assistantwire.AssistantID) == nil })
	manager, link = connectInboxManager(t, h)
	cut(link)
	now := post(routed, "", todoRouteBody)
	if was.Code != now.Code || !bytes.Equal(was.Body.Bytes(), now.Body.Bytes()) || strings.Contains(now.Body.String(), assistantwire.CodeAssistantInDoubt) {
		t.Fatalf("cut off: was %d %s, now %d %s", was.Code, was.Body, now.Code, now.Body)
	}

	// With an op id but no to-do in the body: the manager's refusal, and nothing said of a to-do.
	eventually(t, 5*time.Second, "the cut link let go", func() bool { return h.busConnOf(assistantwire.AssistantID) == nil })
	manager, _ = connectInboxManager(t, h)
	manager.mu.Lock()
	manager.http = func(context.Context, assistantwire.HTTPRequest) (any, error) {
		return assistantwire.HTTPResponse{Status: 400, Header: map[string][]string{"Content-Type": {"application/json"}},
			Body: []byte(`{"error":"todo_id and text are required","code":"bad_request"}`)}, nil
	}
	manager.mu.Unlock()
	if w := post(routed, "00000000-0000-4000-8000-000000100465", `{"text":"Hello"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("no to-do named: %d %s", w.Code, w.Body)
	}
	for who, c := range map[string]*client{"knows": apps.knows, "hello": apps.hello, "watch": apps.watch, "other": apps.other} {
		noTodoSendState(t, c, who)
	}
	if got := receiptStates(store); len(got) != 1 {
		t.Fatalf("receipts: %v", got)
	}
}

// todo_send_state is the hub's: declared once, never a manager's message (a forged one reaches no app, whatever an
// extension report declares), its version written into the hello by the hub alone, and its key no field the iOS
// Envelope already reads.
func TestTodoSendStateIsTheHubs(t *testing.T) {
	if n := len(slices.DeleteFunc(slices.Clone(proto.Features), func(f string) bool { return f != proto.FeatureTodoSendState })); n != 1 ||
		!hubMessageTypes[proto.FeatureTodoSendState] || managerMessageTypes[proto.FeatureTodoSendState] {
		t.Fatalf("todo_send_state must be declared once and the hub's: %d", n)
	}
	for _, declared := range []bool{false, true} {
		h := newTestHub(t)
		h.keepExtReport(assistantwire.AssistantID, &extwire.Report{EventsDeclared: declared, Features: []string{proto.FeatureTodoSendState},
			APIs: []extwire.APIPrefix{{Prefix: "fixture", Events: []string{proto.FeatureTodoSendState}}}})
		app := &client{out: make(chan []byte, 8), role: roleFull, urlFeatures: map[string]bool{proto.FeatureTodoSendState: true}}
		addTestClient(h, app)
		forged := `{"type":"todo_send_state","todo_id":"todo_a1","client_op_id":"00000000-0000-4000-8000-000000110465","state":"delivered","at":"2026-11-19T10:00:00Z"}`
		if err := h.relayBroadcastFrom(assistantwire.AssistantID, json.RawMessage(forged)); err == nil {
			t.Fatalf("declared=%t: a manager's todo_send_state was passed on", declared)
		}
		noTodoSendState(t, app, "an app")
		raw, err := json.Marshal(h.hello())
		if err != nil {
			t.Fatal(err)
		}
		var hello map[string]json.RawMessage
		if err := json.Unmarshal(raw, &hello); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(hello[proto.FeatureTodoSendState], []byte("1")) {
			t.Fatalf("hello's todo_send_state: %s", raw)
		}
	}
	if slices.Contains(iosEnvelopeKeys, proto.FeatureTodoSendState) {
		t.Fatal("the hello's todo_send_state is a key the iOS Envelope reads")
	}
}

// The conversation route keeps a todo_route receipt; one that reached no manager is asked again under its op id. How
// a relay's answer reads as a final state, and as the receipt's: failed only when it shows nothing was taken.
func TestTodoRouteReceiptKindAndOutcome(t *testing.T) {
	offline, _ := json.Marshal(map[string]string{"code": assistantwire.CodeAssistantOffline})
	doubt, _ := json.Marshal(map[string]string{"code": assistantwire.CodeAssistantInDoubt})
	if !clientOpRetry(todoRouteOp, offline) || clientOpRetry(todoRouteOp, doubt) || clientOpRetry(todoRouteOp, []byte(`{"code":"conflict"}`)) {
		t.Fatal("only an offline manager's refusal may be asked again under the same op id")
	}
	const route = "POST /api/v1/assistant/conversations/{id}/route"
	if kind := todoReceiptRoutes[route]; kind != todoRouteOp || !slices.Contains(assistantwire.Routes, route) {
		t.Fatalf("the conversation route's receipt kind %q", kind)
	}
	for _, tc := range []struct {
		status        int
		body          string
		want, receipt string
	}{
		{200, `{"item":{"id":"f1"}}`, proto.MessageSendDelivered, "done"},
		{0, ``, proto.MessageSendDelivered, "done"},
		{200, `{"outcome":"answered","delivered":"none"}`, proto.MessageSendDelivered, "done"},
		{502, `{"code":"assistant_in_doubt","delivered":"unknown"}`, proto.MessageSendUnknown, "unknown"},
		{409, `{"code":"conflict"}`, proto.MessageSendUnknown, "unknown"}, // the conversation route's answer to any error
		{502, `{"code":"accept_not_taken"}`, proto.MessageSendUnknown, "unknown"},
		{502, `{"error":"bus: closed"}`, proto.MessageSendUnknown, "unknown"},
		{409, `{"code":"ask_stale"}`, proto.MessageSendUnknown, "unknown"},
		{409, `{"code":"input_draft","delivered":"none"}`, proto.MessageSendFailed, "failed"},
		{409, `{"code":"conflict","delivered":"yes"}`, proto.MessageSendUnknown, "unknown"},
		{423, `{"code":"dialog_open","delivered":"none"}`, proto.MessageSendFailed, "failed"},
		{503, `{"code":"assistant_offline"}`, proto.MessageSendFailed, "failed"},
		{404, `{"code":"no_manager"}`, proto.MessageSendFailed, "failed"},
		{400, `{"code":"bad_request"}`, proto.MessageSendFailed, "failed"},
		{403, `{"code":"forbidden","delivered":"none"}`, proto.MessageSendFailed, "failed"},
		{400, `{"code":"bad_request","delivered":"unknown"}`, proto.MessageSendUnknown, "unknown"},
		// A newer manager says delivered on every refusal of a to-do's write (spec/fixtures/todo-writes).
		{409, `{"code":"` + assistantwire.CodeSendInDoubt + `","delivered":"unknown","retryable":false}`, proto.MessageSendUnknown, "unknown"},
		{502, `{"code":"` + assistantwire.CodeSendInDoubt + `","delivered":"yes","retryable":false}`, proto.MessageSendUnknown, "unknown"},
		{404, `{"code":"todo_not_found","delivered":"yes"}`, proto.MessageSendUnknown, "unknown"},
		{409, `{"code":"conflict","delivered":"none"}`, proto.MessageSendFailed, "failed"},
		{409, `{"code":"ask_stale","delivered":"none"}`, proto.MessageSendFailed, "failed"},
		{502, `{"code":"accept_not_taken","delivered":"none"}`, proto.MessageSendFailed, "failed"},
		{503, `{"code":"internal","delivered":"none"}`, proto.MessageSendFailed, "failed"},
	} {
		if got := todoReceiptState(tc.status, []byte(tc.body)); got != tc.receipt {
			t.Errorf("receipt %d %s: %s, want %s", tc.status, tc.body, got, tc.receipt)
		}
		if got := todoSendOutcome(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("%d %s: %s, want %s", tc.status, tc.body, got, tc.want)
		}
	}
}

// A to-do write under an op id is relayed with todoRelayWait, past the manager's own longest wait, not the plain
// relay's five minutes; without an op id the request goes on as it came.
func TestTodoRelayWaitsPastTheManager(t *testing.T) {
	if managerLongest := 6*time.Minute + 90*time.Second; todoRelayWait <= managerLongest { // a new Agent's start, then its send
		t.Fatalf("todoRelayWait %s is not past the manager's %s", todoRelayWait, managerLongest)
	}
	h := newTestHub(t)
	h.store = &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	var left time.Duration
	var bounded bool
	relay := h.withTodoReceipt(todoRouteOp, func(w http.ResponseWriter, r *http.Request) {
		deadline, ok := r.Context().Deadline()
		left, bounded = time.Until(deadline), ok
		w.WriteHeader(http.StatusOK)
	})
	for _, op := range []string{"00000000-0000-4000-8000-000000140465", ""} {
		r := httptest.NewRequest(http.MethodPost, todoRoutePath, strings.NewReader(todoRouteBody))
		if op != "" {
			r.Header.Set("X-Persona-Op-Id", op)
		}
		relay(httptest.NewRecorder(), r)
		if op != "" && (!bounded || left <= todoRelayWait-time.Minute) {
			t.Fatalf("under an op id: deadline %t in %s", bounded, left)
		}
		if op == "" && bounded {
			t.Fatalf("without an op id: a deadline in %s", left)
		}
	}
}

// spec/fixtures/todo-writes, the hub's half: each answer a manager gives a to-do's write that carries the owner's
// words, which the manager answers for each case (assistant/todo_write_outcome_test.go), relayed to an app that sent
// the write under an op id. The final todo_send_state and the receipt are what the case names: failed only for an
// answer that says nothing was typed, unknown for words that may be with the Agent or are, delivered for a 2xx; asked
// again, the same reply and no second state.
func TestTodoWriteOutcomeFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "fixtures", "todo-writes", "outcomes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version int
		Cases   []struct {
			Name  string
			Input struct{ Route string }
			// Expected: the manager's answer (status, code, delivered), and what the hub makes of it.
			Expected struct {
				Status                          int
				Code, Delivered, State, Receipt string
			}
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Version != 1 || len(doc.Cases) == 0 {
		t.Fatalf("spec/fixtures/todo-writes/outcomes.json: version %d, %d cases, %v", doc.Version, len(doc.Cases), err)
	}
	h := newTestHub(t)
	store := &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	h.store = store
	apps := newTodoSendApps(t, h)
	manager, _ := connectInboxManager(t, h)
	answer := assistantwire.HTTPResponse{}
	manager.mu.Lock()
	manager.http = func(context.Context, assistantwire.HTTPRequest) (any, error) {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return answer, nil
	}
	manager.mu.Unlock()
	mux := h.routes()
	for i, c := range doc.Cases {
		want := c.Expected
		body := map[string]any{"error": "the manager's words"}
		path, sent := todoRoutePath, todoRouteBody
		switch {
		case c.Input.Route == "answer":
			path, sent = todoAnswerPath, `{"text":"Ryokan Hanami, please"}`
			if want.Status/100 == 2 {
				body = map[string]any{"ask": "todo:decision@2026-11-10T09:00:00Z", "outcome": "answered"}
			}
		case want.Status/100 == 2:
			body = map[string]any{"item": map[string]string{"id": "f1"}}
		}
		for key, value := range map[string]string{"code": want.Code, "delivered": want.Delivered} {
			if value != "" {
				body[key] = value
			}
		}
		reply, _ := json.Marshal(body)
		manager.mu.Lock()
		answer = assistantwire.HTTPResponse{Status: want.Status, Header: map[string][]string{"Content-Type": {"application/json"}}, Body: reply}
		manager.mu.Unlock()
		op := fmt.Sprintf("00000000-0000-4000-8000-%012x", 0x10465300+i)
		for range 2 {
			if code, out := postTodo(t, context.Background(), h, mux, path, op, sent); code != want.Status || out != string(reply) {
				t.Fatalf("%s: %d %s, want %d %s", c.Name, code, out, want.Status, reply)
			}
		}
		if event := nextTodoSendState(t, apps.knows); event.State != proto.MessageSendProcessing || event.ClientOpID != op {
			t.Fatalf("%s: processing %+v", c.Name, event)
		}
		if event := nextTodoSendState(t, apps.knows); event.State != want.State || event.ClientOpID != op {
			t.Fatalf("%s: %+v, want %s", c.Name, event, want.State)
		}
		if got := receiptStates(store)[op]; got != want.Receipt {
			t.Fatalf("%s: receipt %q, want %q", c.Name, got, want.Receipt)
		}
		noTodoSendState(t, apps.knows, c.Name+" asked again")
	}
}
