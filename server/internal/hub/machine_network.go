package hub

import (
	"errors"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const machineNetworkTimeout = 15 * time.Second

// machineNetwork relays a read-only on-demand network and proxy diagnostic
// probe (machine.network) to the machine's node. Full devices only: the report
// exposes non-loopback interface IPs and sanitized proxy hostnames. Always
// sends empty params so callers cannot inject arbitrary SSRF probe targets.
func (h *Hub) machineNetwork() http.HandlerFunc {
	return h.withMachine(func(w http.ResponseWriter, r *http.Request, m *Machine) {
		if principalOf(r.Context()).Role != roleFull {
			writeErr(w, http.StatusForbidden, errors.New("machine network diagnostics require a full device"))
			return
		}
		out, ok := h.callNode(w, r, m, proto.MethodMachineNetwork, proto.MachineNetworkParams{}, machineNetworkTimeout, "", nil)
		if !ok {
			return
		}
		writeRawJSON(w, out)
	})
}

// machineNetworkRoutes registers the on-demand read-only machine network diagnostics endpoint.
func (h *Hub) machineNetworkRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/machines/{mid}/network", h.machineNetwork())
}
