package devices

// Answering a start's startup question from the app (#10365, docs/protocol/protocol.md "Answering a start's
// question"): POST /api/v1/sessions/{mid}/{sid}/start/continue {"choice"} relayed to the node's start.continue, which
// types the answer into the kept tab only while the same question is still there and starts the session again in it.
// Typing into a computer's shell is for a device with full credentials: the hub keeps answer-only devices and
// extensions' tokens out, and the manager checks the role it relays again. What the node refuses (the question went,
// the tab expired, an answer not offered) goes back coded, nothing typed.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

// startContinueTimeout bounds the node's answer: it reads the tab, types the answer and watches its echo for a moment,
// then starts the session in the background.
const startContinueTimeout = 20 * time.Second

// startChoice is what a choice can look like (the node checks it against the question's own answers).
var startChoice = lazyre.New(`^[a-z0-9]{1,8}$`)

// postStartContinue: POST /api/v1/sessions/{mid}/{sid}/start/continue {"choice":"n"} → {"session": Session}.
func (d *Devices) postStartContinue(w http.ResponseWriter, r *http.Request, m *Machine) {
	if r.Header.Get(assistantwire.RoleHeader) != "full" {
		writeErrCode(w, http.StatusForbidden, proto.CodeReadOnly, i18n.Err("devices-start_continue.read_only"))
		return
	}
	var in proto.StartContinueParams
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil || !startChoice.MatchString(in.Choice) {
		writeErrCode(w, http.StatusBadRequest, codeBadRequest, i18n.Err("devices-start_continue.need_choice"))
		return
	}
	m.mu.Lock()
	reachable := m.online || m.warmLocked(time.Now())
	info := m.info
	m.mu.Unlock()
	if !reachable {
		writeErrCode(w, http.StatusServiceUnavailable, devicewire.CodeMachineOffline, i18n.Err("devices-start_continue.offline", i18n.Args{"name": m.name()}))
		return
	}
	if !nodeHas(info, nodeproto.StartContinue) {
		writeErrCode(w, http.StatusConflict, proto.CodeNodeOutdated, i18n.Err("devices-start_continue.node_outdated"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), startContinueTimeout)
	defer cancel()
	var out struct {
		Session *proto.Session `json:"session"`
	}
	sid := r.PathValue("sid")
	if err := m.call(ctx, proto.MethodStartContinue, proto.StartContinueParams{SessionID: sid, Choice: in.Choice}, &out); err != nil {
		apierr.Call(w, err)
		return
	}
	if out.Session != nil {
		out.Session.MachineID = m.cfg.ID
	}
	d.logf("[%s] %s: the start's question answered from the app", m.cfg.ID, sid)
	writeJSON(w, out)
}
