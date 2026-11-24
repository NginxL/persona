package recording

import "github.com/nwverse/persona/server/internal/assistantwire"

// What crosses the bus is assistantwire's: the names the assistant's package gives it, kept so the code reads as it did.
type (
	Hello        = assistantwire.Hello
	Notification = assistantwire.Notification
	SessionRef   = assistantwire.SessionRef
	StartParams  = assistantwire.StartParams
	SendParams   = assistantwire.SendParams
)

const (
	NotifyPush                  = assistantwire.NotifyPush
	DeviceAssistantSend         = assistantwire.DeviceAssistantSend
	DeviceSessionSend           = assistantwire.DeviceSessionSend
	DeviceSessionItems          = assistantwire.DeviceSessionItems
	DeviceAssistantCompact      = assistantwire.DeviceAssistantCompact
	DeviceAssistantGuestPrepare = assistantwire.DeviceAssistantGuestPrepare
	DeviceAssistantGuestDelete  = assistantwire.DeviceAssistantGuestDelete
	CodeMachineOffline          = assistantwire.CodeMachineOffline
	CodeNodeClosed              = assistantwire.CodeNodeClosed
	CodeMeetingEnded            = assistantwire.CodeMeetingEnded
	CodeBadRequest              = assistantwire.CodeBadRequest
)

// ErrNoDevice: no computer runs the assistant (assistantwire's).
var ErrNoDevice = assistantwire.ErrNoDevice
