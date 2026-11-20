package devices

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
)

// ownerInputParams rebuilds owner_input on session.send from the Hub's verified full-device provenance.
func ownerInputParams(p devicewire.NodeCall, params json.RawMessage) (json.RawMessage, error) {
	return rewriteOwnerInput(params, trustedHumanInput(p))
}

func trustedHumanInput(p devicewire.NodeCall) bool {
	if p.Submission == nil || p.Submission.ID == "" || p.Caller == nil {
		return false
	}
	if p.Submission.Kind == devicewire.SubmissionApprovalClarification {
		// The hub creates this only after consuming the exact one-time proof. A
		// manager cannot attach a Submission to its own device.call envelope.
		return p.Caller.Kind == "system" && strings.TrimSpace(p.Submission.OriginalText) != ""
	}
	return p.Submission.Kind == "human_input" && p.Caller.Kind == "device"
}

type ownerControlKey struct{}

var errReservedOwnerControl = errors.New("owner control is reserved for the devices manager")

func reservedOwnerControl(method string) bool {
	return method == nodeproto.OwnerInterrupt || method == nodeproto.OwnerCardAnswer || method == nodeproto.SourceSend
}

// ownerControlMethod uses a distinct RPC with newer nodes. A new node cannot mistake parameters
// passed through an older manager for owner authorization.
func ownerControlMethod(ctx context.Context, method string) string {
	authorized, _ := ctx.Value(ownerControlKey{}).(string)
	if authorized == method {
		switch method {
		case "session.interrupt":
			return nodeproto.OwnerInterrupt
		case "codex.answer":
			return nodeproto.OwnerCardAnswer
		}
	}
	return method
}

func carriesOwnerInput(method string) bool {
	return method == "session.send" || method == "session.send_once" || method == "session.interrupt" || method == "codex.answer"
}

func rewriteOwnerInput(params json.RawMessage, owner bool) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, err
	}
	// Every key encoding/json would read as owner_input goes, not only the exact one: a node decodes the params into a
	// struct, whose field match ignores case, so OWNER_INPUT would otherwise pass for the mark (#9607).
	claimed := false
	for k := range fields {
		if strings.EqualFold(k, "owner_input") {
			delete(fields, k)
			claimed = true
		}
	}
	if owner {
		if fields == nil {
			fields = make(map[string]json.RawMessage)
		}
		fields["owner_input"] = json.RawMessage("true")
	} else if !claimed {
		return params, nil
	}
	return json.Marshal(fields)
}
