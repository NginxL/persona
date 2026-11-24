package iphonectl

import (
	"context"
	"errors"
	"slices"
	"time"
)

// Bridge is the device bridge's operations on one phone, named by an opaque device id (the node fills in what the
// bridge needs to reach it). Every method gets a context with its deadline and should give up at it; the controller
// never waits past the deadline either way (see bounded).
//
// DeviceInfo, Screenshot, AXTree and ListApps only read and may be retried. The rest change the phone, answer only
// after the phone took the input, and are never sent again when their answer did not arrive.
type Bridge interface {
	DeviceInfo(ctx context.Context, device string) (DeviceInfo, error)
	Screenshot(ctx context.Context, device string) (Image, error)
	AXTree(ctx context.Context, device string) (Tree, error)
	ListApps(ctx context.Context, device string) ([]AppInfo, error)

	Tap(ctx context.Context, device string, p Point, count int) error
	LongPress(ctx context.Context, device string, p Point, d time.Duration) error
	Drag(ctx context.Context, device string, from, to Point, d, hold time.Duration) error
	Swipe(ctx context.Context, device string, from, to Point, d time.Duration) error
	// Keys types text into what has the keyboard.
	Keys(ctx context.Context, device, text string) error
	// Key presses a key of the keyboard (return, backspace, delete, tab, escape, space, arrows) repeat times.
	Key(ctx context.Context, device, key string, repeat int) error
	// Button presses a hardware button (home, lock, volume_up, volume_down); d 0 presses briefly.
	Button(ctx context.Context, device, name string, d time.Duration) error
	LaunchApp(ctx context.Context, device, bundleID string) error
	TerminateApp(ctx context.Context, device, bundleID string) error
}

// AppInfo is an installed app.
type AppInfo struct {
	BundleID string `json:"bundle_id"`
	Name     string `json:"name,omitempty"`
	Version  string `json:"version,omitempty"`
	Type     string `json:"type,omitempty"` // user | system
}

// Error is a bridge error: its code and its English message. The caller's session may know more than the code says:
// NotSent, the operation certainly did not reach the phone (refused before anything was written); MaybeSent, it may
// have, whatever the code (handed over, then the answer was lost). MaybeSent wins.
type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	NotSent   bool   `json:"not_sent,omitempty"`
	MaybeSent bool   `json:"maybe_sent,omitempty"`
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// The bridge's error codes.
const (
	CodeBadRequest         = "bad_request"
	CodePairingMissing     = "pairing_missing"
	CodePairingRejected    = "pairing_rejected"
	CodeUnreachable        = "unreachable"
	CodeWrongDevice        = "wrong_device"
	CodeTunnelFailed       = "tunnel_failed"
	CodeDeveloperModeOff   = "developer_mode_off"
	CodeDDINotMounted      = "ddi_not_mounted"
	CodeServiceUnavailable = "service_unavailable"
	CodeLocked             = "locked"
	CodeAppNotFound        = "app_not_found"
	CodeUnsupported        = "unsupported"
	CodeDisconnected       = "disconnected"
	CodeTimeout            = "timeout"
	CodeInternal           = "internal"
)

// refusedCodes fail before any input is written: the input certainly did not reach the phone.
var refusedCodes = []string{CodeBadRequest, CodePairingMissing, CodePairingRejected, CodeUnreachable,
	CodeWrongDevice, CodeTunnelFailed, CodeDeveloperModeOff, CodeDDINotMounted, CodeServiceUnavailable, CodeLocked,
	CodeAppNotFound, CodeUnsupported}

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// refused says the bridge failed before sending anything.
func refused(err error) bool {
	var e *Error
	if errors.As(err, &e) && (e.MaybeSent || e.NotSent) {
		return !e.MaybeSent
	}
	return slices.Contains(refusedCodes, code(err))
}
