package hub

import (
	"net/http"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// health answers GET /api/v1/health, the one route without authentication.
func (h *Hub) health(w http.ResponseWriter, r *http.Request) {
	h.healthWithSource(w, r, h.sourceOf)
}

func (h *Hub) healthWithSource(w http.ResponseWriter, r *http.Request, sourceOf func(*http.Request) source) {
	port, fp := h.tlsPin()
	response := map[string]any{"ok": true, "hub_id": h.hubID(), "hub_name": h.hubName(), "tls_port": port, "fp": fp}
	if sourceOf(r) != sourcePublic {
		response["version"], response["features"] = h.version, h.features()
		response["channel"], response["build"] = h.build.Channel, h.build.Number
		// The commit it was built from ("-dirty" after it with changes not committed; "" when the build did not say):
		// a task is deployed only once this contains its PR's merge commit (assistant deploy_check.go, #3205).
		response["commit"] = h.build.Commit
		response["protocol"], response["min_app_build"] = proto.Protocol, proto.MinAppBuild
		response["update"] = map[string]any{"confirmed": h.upd == nil || h.upd.isConfirmed()} // the watchdog waits for this
	}
	writeJSON(w, response)
}

// hello is the first message on every WebSocket. Fields added here must not reuse a key the iOS app decodes with a
// different type (its LiveConnection Envelope), or the whole hello is dropped there (hub-changes.md 1.2).
func (h *Hub) hello() map[string]any {
	port, fp := h.tlsPin()
	m := map[string]any{
		"type": "hello", "server_version": h.version, "client_id": randomHex(6), "features": h.features(),
		// Hub-authored trust markers, never merged from a manager's feature report: a session message's receipts
		// (send.go) and a to-do write's (todo_receipts.go). Legacy hubs reserve hello but can forward arbitrary
		// extension feature strings.
		proto.FeatureMessageSendState: proto.MessageSendStateVersion,
		proto.FeatureTodoSendState:    proto.TodoSendStateVersion,
		"channel":                     h.build.Channel, "build": h.build.Number, "protocol": proto.Protocol, "min_app_build": proto.MinAppBuild,
		"hub_id": h.hubID(), "hub_name": h.hubName(), "endpoints": h.endpointList(), "tls_port": port, "fp": fp,
		// The hubs of this hub's mesh group, itself included (docs/archive/operations/plan-hub-mesh.md 2.6): only after authentication.
		"mesh": map[string]any{"hubs": h.meshHubs()},
		// Push grading switch (hub.ln_mode / push_grading): synced on hello and on switch toggles.
		"push_grading": h.push.pushGradingOn(),
		// the user's language (feature locale, locale.go)
		"locale": string(h.userLang()),
	}
	// The address the hub's own links use (hub.json public_url; the assistant's /s/…, /persona/…, /todos links, #2194): the
	// apps tell those links from other sites by it even when they reach the hub at another address (#2696). Only when set.
	if h.cfg == nil {
		return m
	}
	if u := strings.TrimRight(strings.TrimSpace(h.cfg.PublicURL), "/"); u != "" {
		m["public_url"] = u
	}
	return m
}
