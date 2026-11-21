package voice

import "net/http"

// Codes of the error frames (docs/assistant/voice.md "Extension → app"): a client shows its own words for them, the message is the
// detail.
const (
	codeNoUpstream          = "no_upstream"          // fatal: no realtime voice service is set up yet
	codeModelUnavailable    = "model_unavailable"    // not fatal: the app's model is gone, the default one is used instead
	codeUpstreamForbidden   = "upstream_forbidden"   // fatal: the service refused the key (401, 403)
	codeUpstreamUnavailable = "upstream_unavailable" // fatal: the service is down for the moment (503, or says so)
	codeUpstreamFailed      = "upstream_failed"      // fatal: the session failed, or a call's reconnects did not work
	codeTargetGone          = "target_gone"          // fatal: the conversation to bind is not listed
	codeTargetReadOnly      = "target_read_only"     // fatal: it can only be read
	codeTargetForbidden     = "target_forbidden"     // fatal: this caller may not bind one (another extension)
	codeMachineOffline      = "machine_offline"      // not fatal: its computer is offline, what is said waits for it
	// A meeting call's (meeting.go): fatal, its target is not its recording's room, or that room is not ready.
	codeNotMeetingRoom = "not_meeting_room"
	// fatal: an ordinary call bound to a meeting's room, which takes only meeting calls.
	codeMeetingRoom = "meeting_room"
)

// infoProtocol is the voice wire this plugin speaks (docs/assistant/voice.md "Version negotiation"): 1 is the frames of
// docs/extensions/extensions.md "Voice"; 2 adds the bound conversation (start.target), the run frames and resume, for a client
// whose start says protocol 2.
const infoProtocol = 2

// serveInfo is GET /info: the wire it speaks and its features, what is missing before a conversation can start and the
// settings it did not take. Never a key, nor the upstreams.
func (pl *Plugin) serveInfo(w http.ResponseWriter, _ *http.Request) {
	out := map[string]any{"protocol": infoProtocol, "features": features}
	if pl.cfg.Setup != "" {
		out["setup"] = pl.cfg.Setup
	}
	if len(pl.cfg.Warnings) > 0 {
		out["warnings"] = pl.cfg.Warnings
	}
	writeHTTPJSON(w, http.StatusOK, out)
}
