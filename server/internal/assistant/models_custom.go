package assistant

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Model ids the user typed (#1644, docs/assistant/model-config.md "Typing a model ID"). New Agent, a running
// session's model switch and the Persona chat's chip take any model id (proto.ValidModelID), not only one from the
// launcher's /model list: a gateway's own models, or a model newer than the list. One that ran is remembered here, per
// machine and launcher, for every device: the hub tells the manager (models.used) once a start, a switch or a message
// with it went through, and the /model lists offer it after the agent's own entries (custom: true, most recent
// first) until the user forgets it. Only the apps' picks are remembered: a model the assistant chose itself
// (agent_start, task_plan) is not the user's typing.

const (
	// launchersCustomModelsKey holds []customModel, most recent first.
	launchersCustomModelsKey = "launchers.custom_models"
	// maxCustomModels: remembered ids per machine and launcher; the oldest goes.
	maxCustomModels = 8
	// maxCustomModelsAll bounds the whole list (machines and launchers come and go).
	maxCustomModelsAll = 256
)

type customModel struct {
	Machine  string    `json:"machine"`
	Launcher string    `json:"launcher"`
	Model    string    `json:"model"`
	UsedAt   time.Time `json:"used_at"`
}

func (c customModel) of(machine, launcher string) bool {
	return c.Machine == machine && c.Launcher == launcher
}

// errModelInvalid refuses a model that is not a model id, saying which.
func errModelInvalid(model string) error {
	return i18n.Err("assistant-models.err_model_invalid", i18n.Args{"model": strconv.Quote(clip(model, 80))})
}

// checkModelID: model is none ("") or a model id.
func checkModelID(model string) error {
	if model != "" && !proto.ValidModelID(model) {
		return errModelInvalid(model)
	}
	return nil
}

// readCustomModels is the remembered ids; none without a database.
func (s *Service) readCustomModels(ctx context.Context) ([]customModel, error) {
	var list []customModel
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, launchersCustomModelsKey, &list); err != nil {
			return nil, err
		}
	}
	return list, nil
}

// customModels are the ids remembered for launcher on machine, most recent first; none when they can't be read.
func (s *Service) customModels(ctx context.Context, machine, launcher string) []string {
	list, err := s.readCustomModels(ctx)
	if err != nil {
		s.logf("custom models: read: %v", err)
		return nil
	}
	var out []string
	for _, c := range list {
		if c.of(machine, launcher) {
			out = append(out, c.Model)
		}
	}
	return out
}

// isCustomModel: model is remembered for launcher on machine.
func (s *Service) isCustomModel(ctx context.Context, machine, launcher, model string) bool {
	return slices.Contains(s.customModels(ctx, machine, launcher), model)
}

// ModelUsed is models.used: the hub says an app's pick of p.Model ran with p.Launcher on p.MachineID.
func (s *Service) ModelUsed(ctx context.Context, p assistantwire.ModelUsed) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s.rememberCustomModel(ctx, p.MachineID, p.Launcher, strings.TrimSpace(p.Model))
}

// rememberCustomModel keeps model for launcher on machine, first, unless the launcher's own list has it (an alias, a
// listed id or what one resolves to): only what had to be typed is worth offering again. Nor for a launcher that takes
// no model (takesTypedModel). With the list unknown (a failed call) it is kept: the apps offer a typed id there too.
func (s *Service) rememberCustomModel(ctx context.Context, machine, launcher, model string) {
	if machine == "" || launcher == "" || proto.IsExternalLauncher(launcher) || !proto.ValidModelID(model) || s.store() == nil {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	res, err := s.askLauncherModels(lctx, machine, launcher, false) // the node keeps its list a while
	cancel()
	if err == nil && (!takesTypedModel(res) || listsModel(res, model)) {
		return
	}
	b := &s.models
	b.customMu.Lock()
	defer b.customMu.Unlock()
	list, err := s.readCustomModels(ctx)
	if err != nil {
		s.logf("custom models: read: %v", err)
		return // not over what could not be read
	}
	list = slices.DeleteFunc(list, func(c customModel) bool { return c.of(machine, launcher) && c.Model == model })
	list = slices.Insert(list, 0, customModel{Machine: machine, Launcher: launcher, Model: model, UsedAt: time.Now().UTC()})
	kept, n := list[:0], 0
	for _, c := range list {
		if c.of(machine, launcher) {
			if n++; n > maxCustomModels {
				continue
			}
		}
		kept = append(kept, c)
	}
	list = kept[:min(len(kept), maxCustomModelsAll)]
	if err := s.putSetting(ctx, launchersCustomModelsKey, list); err != nil {
		s.logf("custom models: save %s for %s on %s: %v", model, launcher, machine, err)
	}
}

// forgetCustomModel drops model for launcher on machine; nothing to drop is no error.
func (s *Service) forgetCustomModel(ctx context.Context, machine, launcher, model string) error {
	b := &s.models
	b.customMu.Lock()
	defer b.customMu.Unlock()
	list, err := s.readCustomModels(ctx)
	if err != nil {
		return err
	}
	kept := slices.DeleteFunc(slices.Clone(list), func(c customModel) bool { return c.of(machine, launcher) && c.Model == model })
	if len(kept) == len(list) {
		return nil
	}
	return s.putSetting(ctx, launchersCustomModelsKey, kept)
}

// takesTypedModel: res being a launcher's /model list, a model typed for that launcher would reach its agent: the
// agent is Claude Code or Codex (the node starts no other one with a model, control.go) and the launcher sets no
// --model of its own over it (Locked). The other agents' lists are empty, which tells the apps to show no model row.
func takesTypedModel(res proto.LauncherModels) bool {
	return res.Locked == "" && (res.Agent == "claude" || res.Agent == "codex")
}

// listsModel: model is one of res's ids, what one of its aliases resolves to, or a spelling folded into one of its rows
// (proto.AgentModel.Aliases: a pick saved as "opus" before its row became the concrete model's, #10365). A gateway echo
// of a listed model (routed under a prefix, or with/without the 1M marker) counts too (proto.SameLauncherModel), so it
// is not remembered again as a typed id.
func listsModel(res proto.LauncherModels, model string) bool {
	return slices.ContainsFunc(res.Models, func(m proto.AgentModel) bool {
		return m.ID == model || m.Resolved == model || slices.Contains(m.Aliases, model) ||
			proto.SameLauncherModel(model, m.ID) || proto.SameLauncherModel(model, m.Resolved) ||
			slices.ContainsFunc(m.Aliases, func(a string) bool { return proto.SameLauncherModel(model, a) })
	})
}

// withCustomModels adds the ids remembered for launcher name on machine to its /model list, after its own entries
// and without the ones it lists itself (custom: true; no efforts: they are not known). A list of a launcher that takes
// no typed model is left as it is.
func (s *Service) withCustomModels(ctx context.Context, machine, name string, res proto.LauncherModels) proto.LauncherModels {
	if proto.IsExternalLauncher(name) || !takesTypedModel(res) {
		return res
	}
	custom := s.customModels(ctx, machine, name)
	if len(custom) == 0 {
		return res
	}
	seen := map[string]bool{}
	for _, m := range res.Models {
		seen[m.ID] = true
		if m.Resolved != "" {
			seen[m.Resolved] = true
		}
		for _, a := range m.Aliases {
			seen[a] = true
		}
	}
	desc := mc(s.requestCtxLang(ctx), "custom_model_description")
	models := slices.Clone(res.Models)
	for _, id := range custom {
		if !seen[id] {
			seen[id] = true
			models = append(models, proto.AgentModel{ID: id, Name: id, Description: desc, Custom: true})
		}
	}
	res.Models = models
	return res
}

// customModelRoutes: forgetting a typed id (the list rows' "Forget").
func (s *Service) customModelRoutes() {
	s.handle("DELETE /api/v1/machines/{mid}/launchers/{name}/models/custom", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-models.err_role_forget"))
			return
		}
		model := r.URL.Query().Get("model")
		if !proto.ValidModelID(model) {
			apierr.Write(w, http.StatusBadRequest, errModelInvalid(model))
			return
		}
		if err := s.forgetCustomModel(r.Context(), r.PathValue("mid"), r.PathValue("name"), model); err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
}
