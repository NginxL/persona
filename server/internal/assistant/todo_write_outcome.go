package assistant

import (
	"errors"
	"net/http"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// What a to-do write that failed did with the owner's words (#10465, docs/protocol/protocol.md "To-do writes"): a
// message on the conversation route, an answer on the asks route or the accept route (and the Lark card's button or an
// "OK" reply, which take the same paths) says it in its error's delivered, so the hub's todo_send_state and its receipt
// say failed only for a write that typed nothing, and a phone never sends again under a new op id what may be with the
// Agent already. Words reach an Agent only through sendToTaskAgent's session.send or startTaskSuccessor's
// session.start. An error that came after the node answered either OK typed them: Service.call's own checks or decoding
// of the answer (completedNodeWriteError, afterNodeWrite), or the route failing to record what was sent (wordsSent).
// One that came before the call went out typed nothing (rpc.NotSent, beforeNodeWrite). An error of the call itself may
// have typed them when sendUnknown says so or it reads as a timeout or a dropped link, and typed nothing otherwise. The
// reading is broader than sendUnknown's (the rule agentSendNote.sent and the successor's start follow): apierr.NodeCode
// also takes a connection that ended mid-answer (io.EOF, io.ErrUnexpectedEOF) or any other network error as a dropped
// link, and here saying "none" wrongly is the unsafe side.

// codeSendInDoubt is the code of a to-do write whose words may be with its Agent already, or are: it is never sent
// again by itself, and its words say to look first.
const codeSendInDoubt = assistantwire.CodeSendInDoubt

// wordsSent is a to-do write that failed after its words reached the Agent: the routed item, or the decision they
// answer, could not be recorded (the to-do may have been deleted meanwhile).
type wordsSent struct{ error }

func (e wordsSent) Unwrap() error { return e.error }

// writeDelivered is what err, a to-do write's failure, did with its words, as its answer's delivered says it: "yes"
// when they reached the Agent before what followed failed (the node answered the send or the start OK), "unknown" when
// the send or the start that carried them may have reached it, "none" when nothing was typed. Besides sendUnknown, any
// transport failure the API reads as a timeout or a dropped link is unknown (apierr.NodeCode), unless it never went
// out, its computer was offline, or the asks could not be read or saved (errAcceptStore, which comes before any send,
// or instead of one).
func writeDelivered(err error) string {
	var sent wordsSent
	var wrote *completedNodeWriteError
	switch {
	case errors.As(err, &sent), errors.As(err, &wrote):
		return "yes"
	case errors.Is(err, rpc.ErrNotSent), errors.Is(err, apierr.ErrOffline), errors.Is(err, errAcceptStore):
		return "none"
	case sendUnknown(err), apierr.NodeCode(err) == "timeout", apierr.NodeCode(err) == "node_unreachable":
		return "unknown"
	}
	return "none"
}

// The words of a to-do write in doubt: a message's or an answer's, and whether it may have reached the Agent
// (delivered "unknown") or did, with what followed failed ("yes").
var (
	routeInDoubtKeys = map[string]string{
		"unknown": "assistant-tasks.route_in_doubt", "yes": "assistant-tasks.route_sent_not_kept"}
	answerInDoubtKeys = map[string]string{
		"unknown": "assistant-tasks.answer_in_doubt", "yes": "assistant-tasks.answer_sent_not_kept"}
)

// writeInDoubt writes err, a to-do write whose words may be with its Agent already (delivered "unknown") or are
// ("yes"), with status: send_in_doubt, never retryable, in words from keys that say so in the request's language,
// and what failed as the detail. Never a 2xx: the hub reads a 2xx as delivered.
func writeInDoubt(w http.ResponseWriter, status int, err error, delivered string, keys map[string]string) {
	l := i18n.OfWriter(w)
	body := apierr.LocalizedBody(status, codeSendInDoubt, i18n.ErrEnglish(keys[delivered]), l)
	body["delivered"], body["retryable"] = delivered, false
	body["detail"] = clip(oneLine(i18n.Localize(err, l)), 300)
	apierr.WriteBody(w, status, body)
}

// writeTypedNothing writes err, a refusal of a to-do write that typed nothing, as apierr.WriteCode does (code "" for
// the status's), with delivered "none": the hub's receipt of it is failed, and the phone may send it again under a new
// op id.
func writeTypedNothing(w http.ResponseWriter, status int, code string, err error) {
	body := apierr.CodeBody(w, status, code, err)
	body["delivered"] = "none"
	apierr.WriteBody(w, status, body)
}

// writeNodeRefusal writes err, a node's coded refusal of a to-do's answer (nodeRefusal: nothing was typed), as 423
// with the node's code and details, as the accept route answers it (#7343), and delivered "none" unless the node said
// it.
func writeNodeRefusal(w http.ResponseWriter, err error) {
	body := apierr.CallStatusBody(w, http.StatusLocked, err)
	if _, said := body["delivered"]; !said {
		body["delivered"] = "none"
	}
	apierr.WriteBody(w, http.StatusLocked, body)
}

// writeRouteError writes err, a message to a to-do the conversation route did not deliver: 409, as every refusal of
// this route has been (the apps installed show its words), with delivered; words that may be with the Agent already
// are send_in_doubt and say so in words from keys (a message's, or an answer's for one that answers a decision),
// instead of the refusal's words.
func writeRouteError(w http.ResponseWriter, err error, keys map[string]string) {
	if delivered := writeDelivered(err); delivered != "none" {
		writeInDoubt(w, http.StatusConflict, err, delivered, keys)
		return
	}
	writeTypedNothing(w, http.StatusConflict, "", err)
}

// inDoubtNote is what a Lark reply or toast says of err, an answer that failed on its way to the task's Agent, when its
// words may be with the Agent already or are, in l ("" when nothing was typed: the caller says why it was not taken).
func inDoubtNote(l i18n.Lang, err error) string {
	if delivered := writeDelivered(err); delivered != "none" {
		return i18n.T(l, answerInDoubtKeys[delivered])
	}
	return ""
}
