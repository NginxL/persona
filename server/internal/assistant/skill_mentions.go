package assistant

import (
	"context"
	"errors"
	"net/http"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/promptcontent"
	"github.com/nwverse/persona/server/internal/rpc"
)

func (s *Service) skillOwner() string {
	if s.id != "" {
		return s.id
	}
	return assistantwire.AssistantID
}

// skillCatalogFor uses only manager-owned conversation/directory facts. The old GET /skills inventory
// remains unchanged for old clients; catalog mode must be explicitly acknowledged by the node.
func (s *Service) skillCatalogFor(ctx context.Context, machine, sid string, params map[string]any) (assistantskills.Catalog, error) {
	d, known := s.device(ctx, machine)
	if !known || s.isWorkMachine(machine) {
		return assistantskills.Catalog{}, errors.New("assistant execution environment is unavailable")
	}
	cwd := s.assistantCwd(d)
	if params["task_execution"] == true {
		cwd, _ = params["cwd"].(string)
	}
	if sid != "" {
		c, ok := s.session(ctx, machine, sid)
		cfg := s.settings(ctx)
		main := cfg.Main == sid && cfg.mainRouteMachine() == machine
		if !main && (!ok || !c.Assistant) {
			return assistantskills.Catalog{}, &rpc.Error{Code: "not_found", Message: "assistant conversation is unavailable"}
		}
		if ok && c.Cwd != "" {
			cwd = c.Cwd
		}
		if s.isGuest(ctx, machine, sid) {
			return assistantskills.Catalog{}, errors.New("skill selection is unavailable for this conversation")
		}
	}
	request := map[string]any{"catalog": true, "cwd": cwd, "session_id": sid, "machine_id": machine, "skill_owner": s.skillOwner()}
	for _, key := range []string{"launcher", "config_dir"} {
		if value, ok := params[key]; ok {
			request[key] = value
		}
	}
	var catalog assistantskills.Catalog
	if err := s.call(ctx, machine, "skills.list", request, &catalog); err != nil {
		return catalog, err
	}
	if catalog.Version != 1 {
		return catalog, errors.New("this computer does not support skill selection; update Persona on it")
	}
	return catalog, nil
}

func (s *Service) skillRoutes() {
	// Full credentials only (no extension scope): this is the person's explicit per-message picker.
	s.handle("GET /api/v1/assistant/skills", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		machine, sid := r.URL.Query().Get("machine_id"), r.URL.Query().Get("session_id")
		if machine == "" {
			machine = s.settings(ctx).MachineID
		}
		params := map[string]any{}
		if launcher := r.URL.Query().Get("launcher"); launcher != "" {
			params["launcher"] = launcher
		}
		if sid != "" {
			if c, ok := s.session(ctx, machine, sid); !ok || !c.Assistant {
				cfg := s.settings(ctx)
				if cfg.Main != sid || cfg.mainRouteMachine() != machine {
					apierr.Write(w, http.StatusNotFound, errors.New("assistant conversation is unavailable"))
					return
				}
			}
		}
		if err := s.sendModel(ctx, machine, sid, params, s.requestCtxLang(ctx)); err != nil {
			apierr.Call(w, err)
			return
		}
		catalog, err := s.skillCatalogFor(ctx, machine, sid, params)
		if err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, catalog)
	})
}

// validateSkillSelections runs only for explicit selections. Ordinary messages never depend on inventory availability.
func (s *Service) validateSkillSelections(ctx context.Context, machine, sid string, params map[string]any) error {
	delete(params, "skill_owner")
	text, _ := params["text"].(string)
	refs, err := assistantskills.References(text)
	if err != nil {
		return skillReferenceError()
	}
	if len(refs) == 0 {
		return nil
	}
	catalog, err := s.skillCatalogFor(ctx, machine, sid, params)
	if err != nil {
		return skillReferenceError()
	}
	if _, err := assistantskills.Resolve(text, catalog.Skills); err != nil {
		return skillReferenceError()
	}
	params["skill_owner"] = s.skillOwner()
	return nil
}

// A scheduled selection requires a node that understands the complete scheduled prompt contract.
func (s *Service) validateTaskSelections(ctx context.Context, machine, sid string, params map[string]any, promptSelections bool) error {
	if err := s.validateSkillSelections(ctx, machine, sid, params); err != nil {
		return err
	}
	if !promptSelections {
		return nil
	}
	text, _ := params["text"].(string)
	files, err := promptcontent.Files(text)
	if err != nil {
		return &rpc.Error{Code: "file_unavailable", Message: i18n.T(i18n.From(ctx), "assistant-schedule.selected_file_unavailable"), Data: []byte(`{"delivered":"none","retryable":false}`)}
	}
	if len(files) > 0 || params["skill_owner"] != nil {
		params["task_input"] = true
	}
	if len(files) > 0 {
		params["prompt_language"] = s.requestCtxLang(ctx).Name()
	}
	return nil
}

func skillReferenceError() error {
	return &rpc.Error{Code: "skill_unavailable", Message: assistantskills.ErrReference.Error(),
		Data: []byte(`{"delivered":"none","retryable":false}`)}
}
