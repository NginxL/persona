package hub

import (
	"context"
	"net/http"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

type terminalAgentSendKey struct{}

func terminalAgentSend(ctx context.Context) bool {
	enabled, _ := ctx.Value(terminalAgentSendKey{}).(bool)
	return enabled
}

// withTerminalAgentTarget limits the terminal entry point before the receipt
// middleware can claim an operation. Use the manager's kept rows so an Agent
// on a disconnected computer still has an exact identity.
func (h *Hub) withTerminalAgentTarget(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !personSent(r.Context()) {
			writeCode(w, http.StatusForbidden, "full_device_required",
				i18n.T(h.requestLang(r), "agent-send.full_device_required"), map[string]any{"delivered": "none"})
			return
		}
		if r.Method == http.MethodPost && !clientOpUUID.MatchString(r.Header.Get("X-Persona-Op-Id")) {
			writeCode(w, http.StatusBadRequest, "operation_id_required",
				i18n.T(h.requestLang(r), "agent-send.invalid_id"), map[string]any{"delivered": "none"})
			return
		}
		m := h.localMachine(r.PathValue("mid"))
		if _, ok := h.terminalAgentTarget(m, r.PathValue("sid")); !ok {
			h.refuseTerminalAgent(w, r)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), terminalAgentSendKey{}, true)))
	}
}

func (h *Hub) terminalAgentTarget(m *Machine, id string) (proto.Session, bool) {
	if m == nil || id == "" || m.conversationDeleted(id) || h.isMainOn(m.cfg.ID, id) {
		return proto.Session{}, false
	}
	row, known := m.session(id)
	return row, known && !row.Archived && !row.Assistant && !m.isAssistant(row)
}

func (h *Hub) refuseTerminalAgent(w http.ResponseWriter, r *http.Request) {
	writeCode(w, http.StatusNotFound, "agent_target_required",
		i18n.T(h.requestLang(r), "agent-send.target_required"), map[string]any{"delivered": "none"})
}

func (h *Hub) terminalAgentCheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"machine_id": r.PathValue("mid"), "session_id": r.PathValue("sid")})
}
