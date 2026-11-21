package agentsend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/hubclient"
)

type state string

const (
	delivered   state = "delivered"
	queued      state = "queued"
	held        state = "held"
	pending     state = "pending"
	refused     state = "refused"
	conflict    state = "conflict"
	unknown     state = "unknown"
	unavailable state = "unavailable"
)

type result struct {
	ID         string `json:"request_id"`
	State      state  `json:"state"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// interpretReply reads delivery separately from HTTP completion. A receipt may
// be complete while the message is still being held or could not be confirmed.
func interpretReply(id string, status int, body []byte) result {
	out := result{ID: id, State: unknown, HTTPStatus: status}
	reply := decodeReply(body)
	if reply == nil {
		return out
	}
	switch {
	case status == http.StatusConflict && reply.Code == "op_id_reused":
		out.State = conflict
	case reply.Delivered == "unknown" || reply.State == "unknown":
	case status == http.StatusOK && reply.Error == "" && reply.Code == "" && reply.State == "" &&
		(reply.OK != nil && *reply.OK || reply.OK == nil && (reply.Delivered == "yes" || emptyObject(body))) &&
		(reply.Delivered == "" || reply.Delivered == "yes"):
		out.State = delivered
		if reply.WaitsOnComputer || reply.Queued {
			out.State = queued
		}
	case status == http.StatusAccepted && reply.Delivered == "later" && reply.State == "" && reply.Error == "":
		out.State = held
	case status == http.StatusAccepted && reply.State == "pending":
		out.State = pending
	case reply.Delivered == "yes":
		// Something was delivered before a later failure. Keep the ambiguity.
	case reply.Delivered == "none" && status >= 400:
		out.State = refused
	case status == http.StatusBadRequest || status == http.StatusUnauthorized ||
		status == http.StatusForbidden:
		out.State = refused
	}
	return out
}

// The hub historically returns an empty object when replaying a confirmed send.
func emptyObject(body []byte) bool {
	text := strings.TrimSpace(string(body))
	return len(text) >= 2 && text[0] == '{' && strings.TrimSpace(text[1:len(text)-1]) == ""
}

type sendReply struct {
	OK              *bool  `json:"ok"`
	Delivered       string `json:"delivered"`
	State           string `json:"state"`
	Code            string `json:"code"`
	Error           string `json:"error"`
	WaitsOnComputer bool   `json:"waits_on_computer"`
	Queued          bool   `json:"queued"`
}

func decodeReply(body []byte) *sendReply {
	var reply *sendReply
	if json.Unmarshal(body, &reply) != nil {
		return nil
	}
	return reply
}

type receiptTarget struct {
	path   string
	digest string
}

// readReceipt also reports whether the recorded operation was usable. The send
// path derives its own target and digest so an old receipt cannot confirm a
// different send after a reset or timeout.
func readReceipt(ctx context.Context, client *hubclient.Client, id string, expected *receiptTarget) (result, bool) {
	var op struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Digest string `json:"digest"`
		State  string `json:"state"`
		Target struct {
			Path  string `json:"path"`
			Query string `json:"query"`
		} `json:"target"`
		Result struct {
			Status int    `json:"status"`
			Body   []byte `json:"body"`
		} `json:"result"`
	}
	out := result{ID: id, State: unknown}
	if _, err := client.JSON(ctx, http.MethodGet, "/api/v1/ops/"+id, nil, maxReplyBytes, &op); err != nil {
		var failure *hubclient.HTTPError
		if errors.As(err, &failure) {
			out.HTTPStatus = failure.Status
			if failure.Status == http.StatusNotFound || failure.Status == http.StatusUnauthorized ||
				failure.Status == http.StatusForbidden {
				out.State = unavailable
			}
		}
		return out, false
	}
	if op.ID != id || op.Kind != "message" ||
		!strings.HasPrefix(op.Target.Path, "/api/v1/sessions/") || !strings.HasSuffix(op.Target.Path, "/agent-messages") {
		out.State = unavailable
		return out, false
	}
	if expected != nil && (op.Target.Path != expected.path || op.Target.Query != "" || op.Digest != expected.digest) {
		return out, false
	}
	switch op.State {
	case "pending":
		out.State = pending
	case "done":
		if op.Result.Status >= 200 && op.Result.Status < 300 {
			out = interpretReply(id, op.Result.Status, op.Result.Body)
			if out.State == pending {
				out.State = unknown
			}
		}
	case "failed":
		if op.Result.Status >= 400 && op.Result.Status <= 599 {
			out = interpretReply(id, op.Result.Status, op.Result.Body)
		}
	case "unknown":
		if op.Result.Status >= 200 && op.Result.Status <= 599 {
			out.HTTPStatus = op.Result.Status
			reply := decodeReply(op.Result.Body)
			// Some server errors are stored as an unknown operation even
			// though the Agent authoritatively said nothing was delivered.
			if op.Result.Status >= 400 && reply != nil && reply.Delivered == "none" && reply.State != "unknown" {
				out.State = refused
			}
		}
	default:
		return out, false
	}
	return out, true
}

func check(ctx context.Context, client *hubclient.Client, opts options) result {
	out, _ := readReceipt(ctx, client, opts.id, nil)
	if !opts.wait || out.State != pending {
		return out
	}
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return waitEnd(out, ctx.Err())
		case <-ticker.C:
			fresh, present := readReceipt(ctx, client, opts.id, nil)
			if ctx.Err() != nil {
				return waitEnd(out, ctx.Err())
			}
			if !present && fresh.State == unknown {
				continue // a transient read error cannot erase the last known pending receipt
			}
			if fresh.State != pending {
				return fresh
			}
			out = fresh
		}
	}
}

func waitEnd(out result, err error) result {
	out.Reason = "timeout"
	if errors.Is(err, context.Canceled) {
		out.Reason = "canceled"
	}
	return out
}
