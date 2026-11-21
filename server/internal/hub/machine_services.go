package hub

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// #12353: machine user services (systemd/launchd) and Docker container management
// with bounded log viewing and confirmation-gated lifecycle actions. All routes
// require full device credentials; answer-only devices and extension tokens are
// refused before the node is contacted.

const (
	machineServicesListTimeout   = 25 * time.Second
	machineServicesLogsTimeout   = 20 * time.Second
	machineServicesActionTimeout = 30 * time.Second
	machineServicesBodyLimit     = 8 * 1024
)

func (h *Hub) machineServicesRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/machines/{mid}/services", h.machineServicesList)
	api.HandleFunc("GET /api/v1/machines/{mid}/services/logs", h.machineServicesLogs)
	api.HandleFunc("POST /api/v1/machines/{mid}/services/action", h.machineServicesAction)
}

func requireFullForServices(w http.ResponseWriter, r *http.Request) bool {
	if principalOf(r.Context()).Role != roleFull {
		writeErr(w, http.StatusForbidden, errors.New("managing a computer's services requires a full device"))
		return false
	}
	return true
}

func handleServiceRPCErr(w http.ResponseWriter) func(error) bool {
	return func(err error) bool {
		var re *rpc.Error
		if !errors.As(err, &re) {
			return false
		}
		switch re.Code {
		case "bad_request":
			writeErrCode(w, http.StatusBadRequest, "bad_request", errors.New(re.Message))
			return true
		case proto.CodeServiceProtected:
			writeErrCode(w, http.StatusForbidden, proto.CodeServiceProtected, errors.New(re.Message))
			return true
		}
		return false
	}
}

func (h *Hub) machineServicesList(w http.ResponseWriter, r *http.Request) {
	h.withMachine(func(w http.ResponseWriter, r *http.Request, m *Machine) {
		if !requireFullForServices(w, r) {
			return
		}
		out, ok := h.callNode(w, r, m, proto.MethodMachineServices, proto.MachineServicesParams{},
			machineServicesListTimeout, "", nil, handleServiceRPCErr(w))
		if !ok {
			return
		}
		writeRawJSON(w, out)
	})(w, r)
}

func (h *Hub) machineServicesLogs(w http.ResponseWriter, r *http.Request) {
	h.withMachine(func(w http.ResponseWriter, r *http.Request, m *Machine) {
		if !requireFullForServices(w, r) {
			return
		}
		q := r.URL.Query()
		kind := q.Get("kind")
		id := q.Get("id")
		if err := proto.ValidateServiceID(kind, id); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		lines := proto.ServiceLogsDefaultLines
		if raw := q.Get("lines"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				writeErr(w, http.StatusBadRequest, errors.New("lines must be a positive integer"))
				return
			}
			lines = proto.ClampServiceLogLines(n)
		}
		params := proto.MachineServiceLogsParams{
			Kind:  kind,
			ID:    id,
			Lines: lines,
		}
		out, ok := h.callNode(w, r, m, proto.MethodMachineServiceLogs, params,
			machineServicesLogsTimeout, "", nil, handleServiceRPCErr(w))
		if !ok {
			return
		}
		writeRawJSON(w, out)
	})(w, r)
}

func (h *Hub) machineServicesAction(w http.ResponseWriter, r *http.Request) {
	h.withMachine(func(w http.ResponseWriter, r *http.Request, m *Machine) {
		if !requireFullForServices(w, r) {
			return
		}
		var req proto.MachineServiceActionParams
		if err := decodeJSONBody(w, r, &req, machineServicesBodyLimit); err != nil {
			writeBodyErr(w, err, errors.New("invalid request body"))
			return
		}
		if err := proto.ValidateServiceID(req.Kind, req.ID); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := proto.ValidateServiceAction(req.Action); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if !req.Confirmed {
			writeErr(w, http.StatusBadRequest, errors.New("explicit confirmation is required"))
			return
		}
		out, ok := h.callNode(w, r, m, proto.MethodMachineServiceAction, req,
			machineServicesActionTimeout, "", nil, handleServiceRPCErr(w))
		if !ok {
			return
		}
		writeRawJSON(w, out)
	})(w, r)
}
