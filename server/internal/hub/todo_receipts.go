package hub

import (
	"context"
	"net/http"
)

// A to-do's relayed writes that keep an operation receipt (#10465, docs/protocol/protocol.md "Operation receipts"):
// the answer to one of its asks, which a notification action or a phone that lost the reply asks again with the same
// X-Persona-Op-Id, gets the first reply back instead of a second effect or a 409 for the round it already ended. The
// id is the header (withClientOp, whose digest covers the path and the body), never the body; a request without it
// goes through as before. The manager decides the answer; the hub only keeps the reply. It is relayed as a send
// (forwardSend): an answer the link lost while the manager had it may have been taken, so it is 502
// assistant_in_doubt, kept and replayed, never assistant_offline, which says nothing reached a manager and keeps no
// receipt.

// todoAnswerOp is the receipt kind of an answer to a to-do's ask.
const todoAnswerOp = "todo_answer"

// todoReceiptRoutes are the relayed to-do routes with a receipt, and the receipt's kind.
var todoReceiptRoutes = map[string]string{
	"POST /api/v1/todos/{id}/asks/{ask}/answer": todoAnswerOp,
}

// withTodoReceipt gives next, a relayed to-do write, a receipt of kind under X-Persona-Op-Id. Once the receipt is
// claimed the relay no longer ends with the caller: an answer the manager takes is recorded, and its reply kept, even
// when the phone stopped waiting.
func (h *Hub) withTodoReceipt(kind string, next http.HandlerFunc) http.HandlerFunc {
	return h.withClientOp(kind, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Persona-Op-Id") != "" {
			r = r.WithContext(context.WithoutCancel(r.Context()))
		}
		next(w, r)
	})
}
