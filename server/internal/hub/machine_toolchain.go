package hub

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const machineToolchainTimeout = 20 * time.Second

// machineToolchainRoutes exposes the on-demand read-only development toolchain
// and environment profile for a machine. Full devices only: the response names
// local executable paths and runtime capabilities, so answer-only devices and
// extension tokens are refused before the node hears of the request.
func (h *Hub) machineToolchainRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/machines/{mid}/toolchain", h.machineToolchain)
}

func (h *Hub) machineToolchain(w http.ResponseWriter, r *http.Request) {
	h.withMachine(func(w http.ResponseWriter, r *http.Request, m *Machine) {
		if principalOf(r.Context()).Role != roleFull {
			writeErr(w, http.StatusForbidden, errors.New("a computer's toolchain profile needs a full device"))
			return
		}
		refresh := parseToolchainRefresh(r.URL.Query().Get("refresh"))
		params := proto.MachineToolchainParams{Refresh: refresh}
		out, ok := h.callNode(w, r, m, proto.MethodMachineToolchain, params, machineToolchainTimeout, "", nil)
		if !ok {
			return
		}
		writeRawJSON(w, out)
	})(w, r)
}

func parseToolchainRefresh(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
