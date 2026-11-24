package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Machine storage monitoring, on-demand bounded hotspot analysis, and confirmation-gated selective cleanup (#12349).
// All routes require full device credentials (never watch answer-only tokens or extension tokens).
// Cleanup requests are validated against the closed proto.StorageCleanupTargetIDs allowlist and must specify either
// dry_run=true (preview) or confirm=true before reaching the node.

const (
	machineStorageTimeout         = 15 * time.Second
	machineStorageHotspotsTimeout = 45 * time.Second
	// Must stay comfortably above the node's overall cleanup deadline (90s in internal/node)
	// so multi-target cleanups always truncate and return per-item results before the hub RPC times out.
	machineStorageCleanupTimeout = 120 * time.Second
)

func (h *Hub) machineStorageRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/machines/{mid}/storage", h.getMachineStorage)
	api.HandleFunc("GET /api/v1/machines/{mid}/storage/hotspots", h.getMachineStorageHotspots)
	api.HandleFunc("POST /api/v1/machines/{mid}/storage/cleanup", h.postMachineStorageCleanup)
}

func (h *Hub) requireStorageFullRole(w http.ResponseWriter, r *http.Request) bool {
	if principalOf(r.Context()).Role != roleFull {
		writeErrCode(w, http.StatusForbidden, proto.CodeReadOnly, errors.New("machine storage inspection and cleanup require full device credentials"))
		return false
	}
	return true
}

func (h *Hub) getMachineStorage(w http.ResponseWriter, r *http.Request) {
	h.withMachine(func(w http.ResponseWriter, r *http.Request, m *Machine) {
		if !h.requireStorageFullRole(w, r) {
			return
		}
		raw, ok := h.callNode(w, r, m, proto.MethodMachineStorage, map[string]any{}, machineStorageTimeout, "devices-diagnosis.node_outdated", nil)
		if !ok {
			return
		}
		var rep proto.MachineStorageReport
		if err := json.Unmarshal(raw, &rep); err != nil {
			writeRawJSON(w, raw)
			return
		}
		rep.MachineID = m.cfg.ID
		writeJSON(w, rep)
	})(w, r)
}

func (h *Hub) getMachineStorageHotspots(w http.ResponseWriter, r *http.Request) {
	h.withMachine(func(w http.ResponseWriter, r *http.Request, m *Machine) {
		if !h.requireStorageFullRole(w, r) {
			return
		}
		var params proto.StorageHotspotsParams
		if v := r.URL.Query().Get("timeout_ms"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				params.TimeoutMS = n
			}
		}
		if v := r.URL.Query().Get("max_depth"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				params.MaxDepth = n
			}
		}
		raw, ok := h.callNode(w, r, m, proto.MethodMachineStorageHotspots, params, machineStorageHotspotsTimeout, "devices-diagnosis.node_outdated", nil)
		if !ok {
			return
		}
		var rep proto.MachineStorageHotspotsReport
		if err := json.Unmarshal(raw, &rep); err != nil {
			writeRawJSON(w, raw)
			return
		}
		rep.MachineID = m.cfg.ID
		writeJSON(w, rep)
	})(w, r)
}

func (h *Hub) postMachineStorageCleanup(w http.ResponseWriter, r *http.Request) {
	h.withMachine(func(w http.ResponseWriter, r *http.Request, m *Machine) {
		if !h.requireStorageFullRole(w, r) {
			return
		}
		var in proto.StorageCleanupParams
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&in); err != nil || len(in.Targets) == 0 {
			writeErrCode(w, http.StatusBadRequest, "bad_request", errors.New("at least one allowlisted cleanup target is required"))
			return
		}
		for _, id := range in.Targets {
			if !slices.Contains(proto.StorageCleanupTargetIDs, id) {
				writeErrCode(w, http.StatusBadRequest, "bad_request", errors.New("unknown or disallowed cleanup target: "+id))
				return
			}
		}
		if !in.DryRun && !in.Confirm {
			writeErrCode(w, http.StatusBadRequest, "confirmation_required", errors.New("cleanup requires dry_run=true or confirm=true"))
			return
		}
		raw, ok := h.callNode(w, r, m, proto.MethodMachineStorageCleanup, in, machineStorageCleanupTimeout, "devices-diagnosis.node_outdated", nil)
		if !ok {
			return
		}
		var res proto.StorageCleanupResult
		if err := json.Unmarshal(raw, &res); err != nil {
			writeRawJSON(w, raw)
			return
		}
		res.MachineID = m.cfg.ID
		if !in.DryRun {
			cleaned := 0
			for _, it := range res.Items {
				if it.Status == proto.StorageCleanupCleaned {
					cleaned++
				}
			}
			h.logf("[%s] storage cleanup: %d of %d targets cleaned, %d bytes freed", m.cfg.ID, cleaned, len(in.Targets), res.FreedBytes)
		}
		writeJSON(w, res)
	})(w, r)
}
