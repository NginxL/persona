package iphone

import "errors"

// Error is a failure with a code the caller can act on: the bridge's own codes (its wire contract, #3451) pass
// through, plus the session's below.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// beforeSend: the failure came while connecting, before the operation was sent (Sent).
	beforeSend bool
}

// unsent is err as a failure to connect, before anything was sent.
func unsent(err error) error {
	var e *Error
	if errors.As(err, &e) {
		c := *e
		c.beforeSend = true
		return &c
	}
	return &Error{Code: CodeUnreachable, Message: err.Error(), beforeSend: true}
}

// ErrorCode is e's code, for callers that read it through an interface rather than this type.
func (e *Error) ErrorCode() string { return e.Code }

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// The session's own codes.
const (
	// CodeNotPaired: no pairing record for this phone on this computer.
	CodeNotPaired = "not_paired"
	// CodeBridgeMissing: the device bridge isn't installed on this computer.
	CodeBridgeMissing = "bridge_missing"
	// CodeUnreachable: the phone couldn't be found or connected before the caller's deadline.
	CodeUnreachable = "unreachable"
	// CodeUncertain: an input operation was handed to the bridge, and then the link dropped or the deadline passed
	// before the answer. It may or may not have happened on the phone; it is never sent again. Look first.
	CodeUncertain = "uncertain"
	// CodeBusy: another pairing job is already running.
	CodeBusy = "busy"
)

// The bridge's codes the session looks at.
const (
	bridgeDisconnected = "disconnected"
	bridgeTimeout      = "timeout"
)

// Code is err's code: an *Error's, or "" for any other error.
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
