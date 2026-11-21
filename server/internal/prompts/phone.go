package prompts

import "strings"

// RenderPhoneQueuedWrite adds the hub's optional delivery contract to a phone's description.
func RenderPhoneQueuedWrite(description string) string {
	return render("tools/phone_queued_write", description)
}

// RenderPhoneQueueOption describes the argument consumed by the hub before forwarding to a phone.
func RenderPhoneQueueOption() string { return render("tools/phone_queue_option", nil) }

// PhoneClipboardFile is phone_clipboard_set's description with its file argument, which `personad mcp` reads on the
// agent's machine (mcpserver, #3947): the phone's own description and the limits that argument has.
type PhoneClipboardFile struct {
	Description string
	MaxKiB      int // the largest file
	TailMin     int // the shortest text whose last 4 characters the result shows
}

func RenderPhoneClipboardFile(p PhoneClipboardFile) string {
	return render("tools/phone_clipboard_file", p)
}

// RenderPhoneClipboardFileOption and RenderPhoneClipboardTrimOption describe phone_clipboard_set's file and trim.
func RenderPhoneClipboardFileOption() string { return render("tools/phone_clipboard_file_option", nil) }
func RenderPhoneClipboardTrimOption() string { return render("tools/phone_clipboard_trim_option", nil) }

// PhoneStandIn is the note on a phone call's result when a primary device is chosen but another device answered
// (docs/apple/phone-tools.md "Which device takes a call"), so the model can say where the answer came from.
type PhoneStandIn struct {
	Device  string // the device that answered, as it named itself (else its platform); empty when unknown
	Primary string // the primary device's name; empty when the hub does not know it
}

// RenderPhoneStandIn is the note without the template file's final newline: it goes into a JSON field or a text block.
func RenderPhoneStandIn(p PhoneStandIn) string {
	return strings.TrimSuffix(render("tools/phone_stand_in", p), "\n")
}

// PhonePrimaryOffline is a phone call's answer when the primary device is not connected, did not wake up, and no
// other device can take the call.
type PhonePrimaryOffline struct {
	Primary string // the primary device's name; empty when the hub does not know it
}

func RenderPhonePrimaryOffline(p PhonePrimaryOffline) string {
	return strings.TrimSuffix(render("tools/phone_primary_offline", p), "\n")
}
