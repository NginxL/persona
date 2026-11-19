package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
)

// The answer to a to-do's ask keeps a receipt (todo_receipts.go, #10465), and the todo_asks feature is the managers'.

// todoAnswerPath is the answer route of a step's decision on to-do todo_a1's row, the step's / encoded as the app
// sends it.
const todoAnswerPath = "/api/v1/todos/todo_a1/asks/step:todo_s1%2Ftodo:decision@2026-11-10T09:00:00Z/answer"

// postTodoAnswer posts body to todoAnswerPath through h's routes, with op as X-Persona-Op-Id when set.
func postTodoAnswer(t *testing.T, ctx context.Context, h *Hub, mux http.Handler, op, body string) (int, string) {
	t.Helper()
	return postTodo(t, ctx, h, mux, todoAnswerPath, op, body)
}

// postTodo posts body to a to-do write's path through h's routes, with op as X-Persona-Op-Id when set.
func postTodo(t *testing.T, ctx context.Context, h *Hub, mux http.Handler, path, op, body string) (int, string) {
	t.Helper()
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "https://hub.example.com"+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	if op != "" {
		r.Header.Set("X-Persona-Op-Id", op)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

// The same op id answers once, however often it is asked, and gets the first reply back; another body under it is
// 409 op_id_reused; without the header each request reaches the manager as before. The manager gets the ask's path
// segment still encoded, and a caller that left before the reply still has the answer recorded.
func TestTodoAnswerReceipt(t *testing.T) {
	h := newTestHub(t)
	store := &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	h.store = store
	manager, _ := connectInboxManager(t, h)
	var calls atomic.Int32
	arrived, finish := make(chan struct{}, 1), make(chan struct{})
	manager.mu.Lock()
	manager.http = func(_ context.Context, r assistantwire.HTTPRequest) (any, error) {
		if r.Method != http.MethodPost || r.Path != todoAnswerPath {
			return assistantwire.HTTPResponse{Status: 404, Body: []byte(`{"error":"Not Found","code":"not_found"}`)}, nil
		}
		n := calls.Add(1)
		if n == 1 {
			arrived <- struct{}{}
			<-finish
		}
		return assistantwire.HTTPResponse{Status: 200, Header: map[string][]string{"Content-Type": {"application/json"}},
			Body: []byte(`{"ask":"step:todo_s1/todo:decision@2026-11-10T09:00:00Z","outcome":"answered","n":` + string(rune('0'+n)) + `}`)}, nil
	}
	manager.mu.Unlock()
	mux := h.routes()
	const op = "00000000-0000-4000-8000-000000010465"
	const body = `{"option":"Kaede"}`

	// The first: the phone goes away while the manager answers.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		postTodoAnswer(t, ctx, h, mux, op, body)
	}()
	<-arrived
	cancel()
	close(finish)
	<-done
	if got := receiptStates(store); got[op] != "done" {
		t.Fatalf("receipt after the caller went away = %v", got)
	}
	for range 2 {
		if code, out := postTodoAnswer(t, context.Background(), h, mux, op, body); code != 200 || !strings.Contains(out, `"n":1`) {
			t.Fatalf("asked again: %d %s", code, out)
		}
	}
	if code, out := postTodoAnswer(t, context.Background(), h, mux, op, `{"option":"Tsuki"}`); code != http.StatusConflict ||
		!strings.Contains(out, "op_id_reused") {
		t.Fatalf("another answer under the op id: %d %s", code, out)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("the manager answered %d times", n)
	}
	for range 2 {
		if code, out := postTodoAnswer(t, context.Background(), h, mux, "", body); code != 200 {
			t.Fatalf("without an op id: %d %s", code, out)
		}
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("without an op id the manager answered %d times in all", n)
	}
	if got := receiptStates(store); len(got) != 1 {
		t.Fatalf("receipts without an op id: %v", got)
	}
}

// A refusal the manager gave (nothing taken, a node's refusal) is replayed to the same op id, never 202 unknown, so
// a phone that checks learns the first outcome; an answer that never reached a manager keeps no receipt, and the same
// op id answers once one is back.
func TestTodoAnswerReceiptReplaysRefusalsAndForgetsOffline(t *testing.T) {
	h := newTestHub(t)
	store := &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	h.store = store
	mux := h.routes()
	const op = "00000000-0000-4000-8000-000000010466"
	if code, out := postTodoAnswer(t, context.Background(), h, mux, op, `{"text":"Hanami"}`); code != http.StatusServiceUnavailable ||
		!strings.Contains(out, assistantwire.CodeAssistantOffline) {
		t.Fatalf("offline: %d %s", code, out)
	}
	if got := receiptStates(store); len(got) != 0 {
		t.Fatalf("offline kept a receipt: %v", got)
	}
	manager, _ := connectInboxManager(t, h)
	var calls atomic.Int32
	manager.mu.Lock()
	manager.http = func(context.Context, assistantwire.HTTPRequest) (any, error) {
		calls.Add(1)
		return assistantwire.HTTPResponse{Status: http.StatusBadGateway, Header: map[string][]string{"Content-Type": {"application/json"}},
			Body: []byte(`{"error":"Not taken: the computer is offline","code":"accept_not_taken"}`)}, nil
	}
	manager.mu.Unlock()
	for range 2 {
		if code, out := postTodoAnswer(t, context.Background(), h, mux, op, `{"text":"Hanami"}`); code != http.StatusBadGateway ||
			!strings.Contains(out, "accept_not_taken") {
			t.Fatalf("a refusal: %d %s", code, out)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("the manager was asked %d times", n)
	}
}

// An answer cut off while the manager has it may have been taken: 502 assistant_in_doubt, delivered unknown, kept as
// unknown, so the same op id gets that reply again and the manager is asked once, never 503 assistant_offline, whose
// receipt is forgotten and whose retry would answer the round again.
func TestTodoAnswerCutOffStaysUnknown(t *testing.T) {
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
	mux := h.routes()
	const op = "00000000-0000-4000-8000-000000010467"
	answer := func() (int, map[string]any) {
		code, out := postTodoAnswer(t, context.Background(), h, mux, op, `{"option":"Kaede"}`)
		var body map[string]any
		if err := json.Unmarshal([]byte(out), &body); err != nil {
			t.Fatalf("reply %d %s", code, out)
		}
		return code, body
	}
	if code, out := answer(); code != http.StatusBadGateway || out["code"] != assistantwire.CodeAssistantInDoubt || out["delivered"] != "unknown" {
		t.Fatalf("cut off: %d %v", code, out)
	}
	if got := receiptStates(store); got[op] != "unknown" {
		t.Fatalf("receipt = %v", got)
	}
	connectInboxManager(t, h)
	if code, out := answer(); code != http.StatusBadGateway || out["code"] != assistantwire.CodeAssistantInDoubt {
		t.Fatalf("asked again: %d %v", code, out)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("the manager was asked %d times", n)
	}
}

// todo_asks is listed only while every manager whose to-dos the list merges is connected and names it, and an
// extension's report cannot add it.
func TestTodoAsksFeatureWhileEveryManagerNamesIt(t *testing.T) {
	h, _, a, b, _, _ := todoViewHub(t)
	listed := func() bool { return slices.Contains(h.features(), featureTodoAsks) }
	if listed() {
		t.Fatal("listed before any manager named it")
	}
	todoViewCapabilities(t, a, assistantwire.CapabilityTodoAsks)
	if listed() {
		t.Fatal("listed while m-0c0c0c does not name it")
	}
	todoViewCapabilities(t, b, assistantwire.CapabilityTodoView, assistantwire.CapabilityTodoAsks)
	if !listed() {
		t.Fatal("not listed while every manager names it")
	}
	todoViewCapabilities(t, a)
	if listed() {
		t.Fatal("listed after the default stopped naming it")
	}

	r := newTestHub(t)
	r.keepExtReport(assistantwire.AssistantID, &extwire.Report{Features: []string{featureTodoAsks, "fa"}})
	if f := r.features(); slices.Contains(f, featureTodoAsks) || !slices.Contains(f, "fa") {
		t.Fatalf("a report decided todo_asks: %v", f)
	}
}

// The receipt kinds: an answer that never reached the manager keeps no receipt; one the manager answered does.
func TestTodoAnswerReceiptKind(t *testing.T) {
	offline, _ := json.Marshal(map[string]string{"code": assistantwire.CodeAssistantOffline})
	doubt, _ := json.Marshal(map[string]string{"code": assistantwire.CodeAssistantInDoubt})
	if !clientOpRetry(todoAnswerOp, offline) || clientOpRetry(todoAnswerOp, doubt) || clientOpRetry(todoAnswerOp, []byte(`{"code":"accept_not_taken"}`)) {
		t.Fatal("only an offline manager's refusal may be asked again under the same op id")
	}
	if kind := todoReceiptRoutes["POST /api/v1/todos/{id}/asks/{ask}/answer"]; kind != todoAnswerOp ||
		!slices.Contains(assistantwire.Routes, "POST /api/v1/todos/{id}/asks/{ask}/answer") {
		t.Fatalf("the answer route's receipt kind %q", kind)
	}
}
