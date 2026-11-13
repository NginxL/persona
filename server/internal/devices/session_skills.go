package devices

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

func (d *Devices) sessionSkillOwner() string {
	id := d.opts.ManagerID
	if id == "" {
		id = assistantwire.AssistantID
	}
	return "agents:" + id
}

func (d *Devices) getSessionSkills(w http.ResponseWriter, r *http.Request, m *Machine) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get(assistantwire.RoleHeader) != "full" {
		writeErr(w, http.StatusForbidden, assistantskills.ErrReference)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var catalog assistantskills.Catalog
	err := m.call(ctx, "session.skills", map[string]any{"session_id": r.PathValue("sid"), "skill_owner": d.sessionSkillOwner()}, &catalog)
	if err != nil {
		apierr.Call(w, err)
		return
	}
	if catalog.Version != 1 {
		apierr.Call(w, &rpc.Error{Code: "skill_unavailable", Message: assistantskills.ErrReference.Error()})
		return
	}
	writeJSON(w, catalog)
}

// Only explicit selections need a catalog-capable node. Plain text remains independent of skill availability.
func (d *Devices) prepareSessionSkills(params json.RawMessage) (json.RawMessage, error) {
	var fields struct {
		Text  string `json:"text"`
		Owner string `json:"skill_owner"`
	}
	if err := json.Unmarshal(params, &fields); err != nil {
		return nil, err
	}
	refs, err := assistantskills.References(fields.Text)
	if err != nil {
		return nil, &rpc.Error{Code: "skill_unavailable", Message: err.Error(), Data: json.RawMessage(`{"delivered":"none"}`)}
	}
	if len(refs) == 0 || fields.Owner != "" {
		return params, nil
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	p["skill_owner"], _ = json.Marshal(d.sessionSkillOwner())
	return json.Marshal(p)
}

func refusalSessionSkillNode() error {
	return &rpc.Error{Code: "skill_unavailable", Message: assistantskills.ErrReference.Error(), Data: json.RawMessage(`{"delivered":"none"}`)}
}
