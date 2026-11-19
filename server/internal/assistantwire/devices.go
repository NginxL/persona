package assistantwire

// HubDeviceMethods are the device.call methods the hub answers itself, from what it holds (the rest are a node's own
// and go to that machine's node as they are). The manager that connects the computers (internal/devices) calls the
// nodes itself and sends the hub only these.
var HubDeviceMethods = map[string]bool{
	DeviceDevicesList:          true,
	DeviceInteractionsList:     true,
	DeviceSessionsList:         true,
	DeviceSessionGet:           true,
	DeviceSessionFollow:        true,
	DeviceSessionUnfollow:      true,
	DeviceSessionSendOnce:      true,
	DeviceAssistantAnswerAgain: true,
}
