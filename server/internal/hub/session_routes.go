package hub

import (
	"net/http"
)

// sessionRoutes are the conversations: the list, starting one, its items, and what the user does in it.
func (h *Hub) sessionRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/sessions", h.listSessions)
	api.HandleFunc("POST /api/v1/sessions", h.withoutTerminalControls(h.withClientOp("start", h.startSession)))
	// extensionReadable: an extension's token never reads a health-space conversation (extension_private.go).
	api.HandleFunc("GET /api/v1/sessions/{mid}/{sid}/items", h.extensionReadable(h.withMachine(h.sessionItems)))
	api.HandleFunc("GET /api/v1/sessions/{mid}/{sid}/items/{iid}/image", h.extensionReadable(h.withMachine(h.sessionImage)))
	api.HandleFunc("GET /api/v1/sessions/{mid}/{sid}/items/{iid}/visualization", h.extensionReadable(h.withMachine(h.sessionVisualization)))
	api.HandleFunc("GET /api/v1/sessions/{mid}/{sid}/items/{iid}/full", h.extensionReadable(h.withMachine(h.sessionItemFull)))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/messages", h.extensionControllable(h.withoutTerminalControls(h.withClientOp("message", h.withMachine(h.sendMessage)))))
	api.HandleFunc("GET /api/v1/sessions/{mid}/{sid}/agent-messages", h.withTerminalAgentTarget(h.terminalAgentCheck))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/agent-messages", h.withTerminalAgentTarget(h.withClientOp("message", h.withMachine(h.sendMessage))))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/attach", h.proxyBody("session.attach"))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/interrupt", h.extensionControllable(h.withClientOp("interrupt", h.withMachine(h.interruptSession))))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/keys", h.withClientOp("keys", h.proxyBody("session.keys"))) // keys pressed twice are not undone (#12532)
	// #2436: rewind a Claude session to before one of its messages (rewind.go); a full device only, it restores files
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/rewind", h.withClientOp("rewind", h.withMachine(h.rewindSession)))
	// #2528: back to the configured model after a refusal fallback (restore_model.go); a full device only, a relay
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/restore-model", h.withClientOp("restore_model", h.withMachine(h.restoreSessionModel)))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/continue-in-new-chat", h.withClientOp("start", h.withMachine(h.continueInNewChat)))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/fork", h.withClientOp("fork", h.proxyBody("session.fork"))) // "Branch to new chat" (node/fork.go)
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/close", h.withClientOp("close", h.proxyBody("session.close")))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/queue", h.extensionControllable(h.withoutTerminalControls(h.proxyBody("session.queue.edit"))))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/queue/send-now", h.extensionControllable(h.withoutTerminalControls(h.withClientOp("send_now", h.proxyBody("session.queue.send_now"))))) // #6088, #12532
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/rename", h.extensionControllable(h.withoutTerminalControls(h.withClientOp("rename", h.withMachine(h.renameSession)))))
	api.HandleFunc("DELETE /api/v1/sessions/{mid}/{sid}", h.withMachine(h.deleteSession))
	// Model, permission, effort and the independent ultracode switch may each be left out (ultracode.go).
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/modes", h.withClientOp("modes", h.withMachine(h.sessionModes)))
	api.HandleFunc("POST /api/v1/machines/{mid}/spaces/{wid}/rename", h.withMachine(h.renameSpace))
	api.HandleFunc("GET /api/v1/sessions/{mid}/{sid}/info", h.extensionReadable(h.proxy("session.info", func(r *http.Request) any {
		return map[string]any{"session_id": r.PathValue("sid")}
	})))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/screen/before", paneAddressFullOnly(h.proxyBody("session.screen.before")))
	api.HandleFunc("GET /api/v1/sessions/{mid}/{sid}/screen", h.extensionReadable(paneAddressFullOnly(h.proxy("session.screen", func(r *http.Request) any {
		q := r.URL.Query()
		return map[string]any{"session_id": r.PathValue("sid"), "lines": atoi(q.Get("lines")), "source": q.Get("source"), "format": q.Get("format")}
	}))))
}
