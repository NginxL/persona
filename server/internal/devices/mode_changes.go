package devices

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The record of permission mode changes (#10297, docs/protocol/protocol.md「Session mode changes」). A mode picked
// through the hub's /modes route reaches the node as session.modes with the hub's NodeCall.Actor: the manager keeps who
// asked, from which mode to which, and whether it took effect at once or by a restart into Full access that waits for
// the turn to end (the node's restart_queued). What became of such a restart comes as the node's session.mode_restart
// event (node/mode_restart.go). The apps read one session's rows to draw a line in its chat.

// errModeChangesRole: the record is the owner's devices' to read, not a watch's or an extension's.
var errModeChangesRole = errors.New("only the owner's devices can read a session's mode changes")

// modeChangesShown is how many of a session's latest changes the apps get.
const modeChangesShown = 200

// modeChangeStore keeps the permission mode changes of sessions (db.DB); a store without it keeps none.
type modeChangeStore interface {
	AddSessionModeChange(ctx context.Context, c db.SessionModeChange) error
	SessionModeChanges(ctx context.Context, machine, session string, limit int) ([]db.SessionModeChange, error)
}

// modeSetFrom is the permission mode a session.modes call with an actor asks for, the session it names and the mode
// that session runs in now (the change's from), or ok false when the call is nothing to record.
func (m *Machine) modeSetFrom(p devicewire.NodeCall, params json.RawMessage) (sid, from, to string, ok bool) {
	if p.Method != "session.modes" || p.Actor == nil {
		return "", "", "", false
	}
	var a struct {
		SessionID      string `json:"session_id"`
		PermissionMode string `json:"permission_mode"`
	}
	if json.Unmarshal(params, &a) != nil || a.SessionID == "" || strings.TrimSpace(a.PermissionMode) == "" {
		return "", "", "", false
	}
	if s, found := m.session(a.SessionID); found {
		from = s.PermissionMode
	}
	return a.SessionID, from, a.PermissionMode, true
}

// recordModeSet keeps a session.modes call the node took: who asked, from and to, and how it took effect (its answer's
// applied: "now" or "restart"; an older node says neither, kept as ""). A pick of the mode the session runs in already
// changed nothing and is not kept: with a restart into Full access waiting, it called that off, and the node's
// session.mode_restart event (cancelled) is the record of it (#11089).
func (m *Machine) recordModeSet(actor *devicewire.Actor, sid, from, to string, out json.RawMessage) {
	st, ok := m.d.opts.Store.(modeChangeStore)
	if !ok || actor == nil {
		return
	}
	var answer struct {
		Applied string `json:"applied"`
	}
	_ = json.Unmarshal(out, &answer)
	applied := ""
	if answer.Applied == db.ModeAppliedNow || answer.Applied == db.ModeAppliedRestart {
		applied = answer.Applied
	}
	if from != "" && from == to && applied != db.ModeAppliedRestart {
		return
	}
	c := db.SessionModeChange{MachineID: m.cfg.ID, SessionID: sid, Kind: db.ModeChangeSet, From: from, To: to, Applied: applied,
		ActorKind: actor.Kind, DeviceID: actor.DeviceID, DeviceName: actor.DeviceName}
	if actor.Kind == "extension" {
		c.DeviceName = actor.Extension
	}
	ctx, cancel := m.storeCtx()
	defer cancel()
	if err := st.AddSessionModeChange(ctx, c); err != nil {
		m.d.logf("machine %s: session %s: mode change not recorded: %v", m.cfg.ID, sid, err)
	}
}

// onModeRestart keeps what became of a queued restart into Full access (the node's session.mode_restart event).
func (m *Machine) onModeRestart(params json.RawMessage) {
	st, ok := m.d.opts.Store.(modeChangeStore)
	if !ok {
		return
	}
	var e proto.ModeRestartEvent
	if json.Unmarshal(params, &e) != nil || e.SessionID == "" {
		return
	}
	kind := ""
	switch e.Outcome {
	case proto.ModeRestartDone:
		kind = db.ModeChangeRestartDone
	case proto.ModeRestartFailed:
		kind = db.ModeChangeRestartFailed
	case proto.ModeRestartDropped:
		kind = db.ModeChangeRestartDropped
	default:
		return
	}
	ctx, cancel := m.storeCtx()
	defer cancel()
	if err := st.AddSessionModeChange(ctx, db.SessionModeChange{MachineID: m.cfg.ID, SessionID: e.SessionID, Kind: kind,
		To: e.Mode, Reason: clip(e.Reason, 300)}); err != nil {
		m.d.logf("machine %s: session %s: restart outcome not recorded: %v", m.cfg.ID, e.SessionID, err)
	}
}

// getModeChanges is GET /api/v1/sessions/{mid}/{sid}/mode-changes: a session's latest mode changes, oldest first, for
// the owner's devices (a full role).
func (d *Devices) getModeChanges(w http.ResponseWriter, r *http.Request, m *Machine) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get(assistantwire.RoleHeader) != "full" {
		writeErr(w, http.StatusForbidden, errModeChangesRole)
		return
	}
	changes := []db.SessionModeChange{}
	if st, ok := d.opts.Store.(modeChangeStore); ok {
		list, err := st.SessionModeChanges(r.Context(), m.cfg.ID, r.PathValue("sid"), modeChangesShown)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		changes = append(changes, list...)
	}
	writeJSON(w, map[string]any{"changes": changes})
}
