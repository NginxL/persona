package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	featureAgentSessionArchives     = assistantwire.CapabilityAgentSessionArchives
	featureAgentSessionArchiveClose = assistantwire.CapabilityAgentSessionArchiveClose
)

// Archive state belongs to managers. The hub only gates a rolling upgrade,
// routes to the owner and projects its pre-existing delete tombstones.
func (h *Hub) archiveManagerStatus(id string) int {
	return h.archiveManagerHas(id, assistantwire.CapabilityAgentSessionArchives)
}

// archiveManagerHas is 200 when manager id is connected and named capability in its hello, 503 when it is not
// connected, 501 when it is too old.
func (h *Hub) archiveManagerHas(id, capability string) int {
	if h.busConnOf(id) == nil {
		return http.StatusServiceUnavailable
	}
	for _, manager := range h.busRoster() {
		if manager.ID == id && slices.Contains(manager.Capabilities, capability) {
			return http.StatusOK
		}
	}
	return http.StatusNotImplemented
}

func (h *Hub) archiveManagers(ctx context.Context) []string {
	ids := h.listManagers(ctx)
	for _, m := range h.machinesNow() {
		owner := h.machineOwner(m.cfg.ID)
		if !slices.Contains(ids, owner) {
			ids = append(ids, owner)
		}
	}
	return ids
}

// agentSessionArchiveFeatures are the archive features every owner supports now: archives, and "Archive and close"
// with them (one look at the managers for both). A Hub-wide feature cannot promise support for only part of its
// fleet: mixed versions or an offline owner disable the controls until every owner is ready.
func (h *Hub) agentSessionArchiveFeatures() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ids := h.archiveManagers(ctx)
	if len(ids) == 0 {
		return nil
	}
	closeToo := true
	for _, id := range ids {
		if h.archiveManagerStatus(id) != http.StatusOK {
			return nil
		}
		closeToo = closeToo && h.archiveManagerHas(id, assistantwire.CapabilityAgentSessionArchiveClose) == http.StatusOK
	}
	if closeToo {
		return []string{featureAgentSessionArchives, featureAgentSessionArchiveClose}
	}
	return []string{featureAgentSessionArchives}
}

func (h *Hub) archiveManagerReady(w http.ResponseWriter, id string) bool {
	status := h.archiveManagerStatus(id)
	if status == http.StatusOK {
		return true
	}
	if status == http.StatusServiceUnavailable {
		writeErrCode(w, status, assistantwire.CodeAssistantOffline, errAssistantOffline)
	} else {
		writeErrCode(w, status, "agent_archives_unsupported", i18n.Err("hub-agent_archives.upgrade"))
	}
	return false
}

func (h *Hub) forwardAgentArchives(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		if principalOf(r.Context()).Role != roleFull {
			writeErr(w, http.StatusForbidden, i18n.Err("hub-agent_archives.forbidden"))
			return
		}
		owner, ok := h.owner(w, r, h.byMachine)
		if !ok || !h.archiveManagerReady(w, owner) {
			return
		}
		// An older manager would take ?close=1 for a plain archive and leave the agent open while the app says closed.
		if r.URL.Query().Get("close") == "1" && h.archiveManagerHas(owner, assistantwire.CapabilityAgentSessionArchiveClose) != http.StatusOK {
			writeErrCode(w, http.StatusNotImplemented, "agent_archive_close_unsupported", i18n.Err("hub-agent_archives.close_upgrade"))
			return
		}
		h.forwardHTTP(w, r, owner)
		return
	}
	mid := r.URL.Query().Get("machine_id")
	if mid != "" {
		if em := extensionManager(r); em != "" && h.machineOwner(mid) != em {
			writeErrCode(w, http.StatusForbidden, codeOtherManager, errOtherManager)
			return
		}
		if h.localMachine(mid) == nil {
			writeErr(w, http.StatusNotFound, i18n.Err("devices-api.unknown_machine"))
			return
		}
		owner := h.machineOwner(mid)
		if !h.archiveManagerReady(w, owner) {
			return
		}
		h.forwardArchiveList(w, r, owner)
		return
	}
	named, err := impliedManager(r)
	if err != nil {
		writeErrCode(w, http.StatusForbidden, codeOtherManager, err)
		return
	}
	if named != "" {
		owner, ok := h.owner(w, r, h.byRequest)
		if !ok || !h.archiveManagerReady(w, owner) {
			return
		}
	} else {
		for _, owner := range h.archiveManagers(r.Context()) {
			if !h.archiveManagerReady(w, owner) {
				return
			}
		}
	}
	// The general merged-list API permits partial results. Archives must not:
	// a failed manager is not an empty archive, even after readiness was checked.
	merged := newBufferedWriter()
	var failed *bufferedWriter
	h.mergeList(merged, r, "sessions", func(dst http.ResponseWriter, req *http.Request, owner string) {
		reply := newBufferedWriter()
		h.forwardArchiveList(reply, req, owner)
		if reply.status >= 400 && failed == nil {
			failed = reply
		}
		h.copyBufferedArchive(dst, reply)
	})
	if failed != nil {
		h.copyBufferedArchive(w, failed)
		return
	}
	h.copyBufferedArchive(w, merged)
}

// forwardArchiveList filters a manager's actual response with exactly the same
// legacy deletion projection used by normal session lists. It stores no state.
func (h *Hub) forwardArchiveList(w http.ResponseWriter, r *http.Request, owner string) {
	response := newBufferedWriter()
	h.forwardHTTP(response, r, owner)
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	if status < 200 || status >= 300 {
		h.copyBufferedArchive(w, response)
		return
	}
	var body map[string]json.RawMessage
	var rows []proto.Session
	if err := json.Unmarshal(response.body.Bytes(), &body); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if err := json.Unmarshal(body["sessions"], &rows); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	out := make([]proto.Session, 0, len(rows))
	deletes := map[string]map[string]time.Time{}
	for _, row := range rows {
		if h.spacesHidden(r) && spaceSession(&row) {
			continue
		}
		if h.machineOwner(row.MachineID) != owner {
			continue
		}
		row.Manager = owner
		del, ok := deletes[row.MachineID]
		if !ok {
			del = h.deletedAt(row.MachineID)
			deletes[row.MachineID] = del
		}
		if !sessionHiddenByDeletion(row, del) {
			out = append(out, row)
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	body["sessions"] = raw
	writeJSON(w, body)
}

func (h *Hub) copyBufferedArchive(w http.ResponseWriter, response *bufferedWriter) {
	copyHeader(w.Header(), response.header)
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if _, err := w.Write(response.body.Bytes()); err != nil {
		h.logf("write agent archive response: %v", err)
	}
}
