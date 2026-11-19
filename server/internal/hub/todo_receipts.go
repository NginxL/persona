package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// A to-do's relayed writes that keep an operation receipt (#10465, docs/protocol/protocol.md "Operation receipts"):
// the answer to one of its asks, which a notification action or a phone that lost the reply asks again with the same
// X-Persona-Op-Id, gets the first reply back instead of a second effect or a 409 for the round it already ended; a
// message to the to-do on its conversation's route is not typed to its Agent twice. The id is the header
// (withClientOp, whose digest covers the path and the body), never the body; a request without it goes through as
// before. The manager decides the answer; the hub only keeps the reply. With an op id both are relayed as a send
// (forwardSend): a write the link lost while the manager had it may have been taken, so it is 502
// assistant_in_doubt, kept and replayed, never assistant_offline, which says nothing reached a manager and keeps no
// receipt.
//
// While such a write is under way the hub says how it stands (todo_send_state, docs/SUPER_RULES.md 5): processing as
// soon as it has claimed the op id, before the manager has the write (a message may wait for its Agent's computer, or
// for a new Agent to start), then how the relay ended. Like message_send_state for a session's message it is
// transport only: it never says what the to-do or its Agent is doing.

const (
	// todoAnswerOp is the receipt kind of an answer to a to-do's ask.
	todoAnswerOp = "todo_answer"
	// todoRouteOp is the receipt kind of a message to a to-do on its conversation's route.
	todoRouteOp = "todo_route"
)

// todoReceiptRoutes are the relayed to-do routes with a receipt, and the receipt's kind.
var todoReceiptRoutes = map[string]string{
	"POST /api/v1/todos/{id}/asks/{ask}/answer":       todoAnswerOp,
	"POST /api/v1/assistant/conversations/{id}/route": todoRouteOp,
}

// todoRelayWait bounds how long the hub waits for the manager's answer to a to-do write under an op id. It is longer
// than the manager's own longest wait, a message that waits for a new Agent's start (six minutes) and then for its
// send (ninety seconds), so a slow start ends delivered rather than in doubt. Without an op id the plain relay's five
// minutes stay.
const todoRelayWait = 10 * time.Minute

// todoRelay relays a to-do write with a receipt, as a send, to the manager by names. A message on the conversation
// route without an op id, which is all an older app sends, goes as it always went.
func (h *Hub) todoRelay(kind string, by ownerOf) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := h.owner(w, r, by)
		if !ok {
			return
		}
		if kind == todoRouteOp && r.Header.Get("X-Persona-Op-Id") == "" {
			h.forwardHTTP(w, r, id)
			return
		}
		h.forwardSend(w, r, id)
	}
}

// todoFlight is one claimed to-do write under an op id, as withTodoReceipt follows it: the to-do it names, the op id,
// how the relay ended, and whether the receipt was stored.
type todoFlight struct {
	todo, op, state string
	stored          bool
}

// todoFlightKey carries a request's *todoFlight from withTodoReceipt's outer wrapper to the handler withClientOp runs.
type todoFlightKey struct{}

// withTodoReceipt gives next, a relayed to-do write, a receipt of kind under X-Persona-Op-Id. Once the receipt is
// claimed the relay no longer ends with the caller: a write the manager takes is recorded, and its reply kept, even
// when the phone stopped waiting. Only the claimed write says how it stands: a retry of its op id waits for the reply
// or gets it back (withClientOp) and sends nothing, so an older final state never follows a newer one. The final
// state goes out once withClientOp has stored the receipt and answered, so it never says more than a Check of the op
// id would: unknown when the receipt could not be stored.
func (h *Hub) withTodoReceipt(kind string, next http.HandlerFunc) http.HandlerFunc {
	claimed := h.withClientOp(kind, func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), todoRelayWait)
		defer cancel()
		r = r.WithContext(ctx)
		f := ctx.Value(todoFlightKey{}).(*todoFlight)
		f.op, f.todo = strings.ToLower(r.Header.Get("X-Persona-Op-Id")), todoOfWrite(kind, r)
		if f.todo == "" { // it names no to-do: the manager refuses it
			next(w, r)
			return
		}
		h.todoSendState(f.todo, f.op, proto.MessageSendProcessing)
		seen := &keptRecorder{ResponseWriter: w}
		next(seen, r)
		f.state = todoSendOutcome(seen.status, seen.body)
	})
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Persona-Op-Id") == "" {
			next(w, r)
			return
		}
		f := &todoFlight{state: proto.MessageSendUnknown} // a relay that never answered may have been taken
		defer func() {
			if f.todo == "" { // not claimed here, or about no to-do
				return
			}
			if !f.stored {
				f.state = proto.MessageSendUnknown
			}
			h.todoSendState(f.todo, f.op, f.state)
		}()
		ctx := context.WithValue(r.Context(), todoFlightKey{}, f)
		claimed(w, r.WithContext(context.WithValue(ctx, clientOpStoredKey{}, &f.stored)))
	}
}

// todoOfWrite is the to-do a write under a receipt is about: the path's for an answer, the body's todo_id, read as the
// manager reads it (the first JSON value), for a message on the conversation route ("" when it names none).
func todoOfWrite(kind string, r *http.Request) string {
	if kind != todoRouteOp {
		return r.PathValue("id")
	}
	raw, err := io.ReadAll(r.Body) // withClientOp has read it whole into memory already
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var in struct {
		TodoID string `json:"todo_id"`
	}
	if err != nil || json.NewDecoder(bytes.NewReader(raw)).Decode(&in) != nil {
		return ""
	}
	return in.TodoID
}

// todoSendOutcome is how a to-do write's relay ended, for its final todo_send_state and its receipt alike. Delivered
// on a 2xx: its Agent took the message, or the manager took the answer (one it recorded for the supervisor too, a 200
// that says delivered "none"). Failed only when the answer shows nothing was taken: it says delivered "none", it never
// reached a manager (assistant_offline, whose receipt is let go, so the op id may go again), or the request was refused
// before anything ran (400, 401, 403, 404). Anything else is unknown, since it may have been taken: 502
// assistant_in_doubt, and a manager's 409 or 502 that says nothing of delivery (the conversation route answers 409 for
// a send whose result it does not know too). The same op id gets the recorded answer back; only a new one could type
// it twice.
func todoSendOutcome(status int, body []byte) string {
	var out struct {
		Delivered string `json:"delivered"`
		Code      string `json:"code"`
	}
	_ = json.Unmarshal(body, &out)
	switch {
	case status == 0 || status >= 200 && status < 300:
		return proto.MessageSendDelivered
	case out.Delivered == "none" || out.Code == assistantwire.CodeAssistantOffline:
		return proto.MessageSendFailed
	case out.Delivered == "" && slices.Contains(todoRefusedBeforeRun, status):
		return proto.MessageSendFailed
	}
	return proto.MessageSendUnknown
}

// todoRefusedBeforeRun are the statuses a to-do write is refused with before anything runs: a body the manager does
// not take, a device that may not write, a to-do, ask or manager that is not there.
var todoRefusedBeforeRun = []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound}

// todoReceiptState is the receipt state of a to-do write the relay answered: the same reading as its final
// todo_send_state, so the receipt never says failed for a write that may have been typed.
func todoReceiptState(status int, body []byte) string {
	switch todoSendOutcome(status, body) {
	case proto.MessageSendDelivered:
		return "done"
	case proto.MessageSendFailed:
		return "failed"
	}
	return "unknown"
}

// todoSendState tells the full devices that asked for todo_send_state (client_hello or ?features=) where the write
// under op id op for to-do todo stands. Outside the event log: a phone that missed it reads the HTTP answer, or asks
// the op id again.
func (h *Hub) todoSendState(todo, op, state string) {
	b, err := json.Marshal(proto.TodoSendState{Type: "todo_send_state", TodoID: todo, ClientOpID: op, State: state, At: time.Now().UTC()})
	if err == nil {
		h.toFullWanting(proto.FeatureTodoSendState, b)
	}
}
