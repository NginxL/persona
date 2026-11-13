package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

type modelPatch struct {
	Revision *int64          `json:"revision"`
	Provider json.RawMessage `json:"provider"`
	Model    json.RawMessage `json:"model"`
	Effort   json.RawMessage `json:"effort"`
	Runtime  json.RawMessage `json:"runtime"`
	// ConfigDir: the main chat's profile for new conversations (#4633), private (stored as none) or legacy.
	ConfigDir json.RawMessage `json:"config_dir"`
}

func patchModel(b ModelBinding, in modelPatch) (ModelBinding, error) {
	if in.Provider != nil {
		var provider string
		if string(in.Provider) != "null" {
			if err := json.Unmarshal(in.Provider, &provider); err != nil {
				return b, i18n.Err("assistant-models.err_provider_type")
			}
		}
		if provider != b.Provider {
			b.Model, b.Effort = "", ""
		}
	}
	for _, f := range []struct {
		raw json.RawMessage
		dst *string
	}{{in.Provider, &b.Provider}, {in.Model, &b.Model}, {in.Effort, &b.Effort}, {in.Runtime, &b.Runtime}, {in.ConfigDir, &b.ConfigDir}} {
		if f.raw == nil {
			continue
		}
		if string(f.raw) == "null" {
			*f.dst = ""
			continue
		}
		var v string
		if err := json.Unmarshal(f.raw, &v); err != nil {
			return b, i18n.Err("assistant-models.err_field_type")
		}
		if v == "" || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\r\n\x00") || len(v) > 512 {
			return b, i18n.Err("assistant-models.err_field_invalid")
		}
		*f.dst = v
	}
	if b.Runtime != "" && !slices.Contains(assistantRuntimes, b.Runtime) {
		return b, i18n.Err("assistant-models.err_runtime_invalid")
	}
	if b.Runtime == proto.AssistantRuntimeCLI {
		b.Runtime = "" // the default, stored as none: an explicit cli fingerprints like no choice
	}
	if b.ConfigDir != "" && !slices.Contains(assistantConfigDirs, b.ConfigDir) {
		return b, i18n.Err("assistant-models.err_config_dir_invalid")
	}
	if b.ConfigDir == assistantConfigDirPrivate {
		b.ConfigDir = "" // the default, stored as none
	}
	return b, nil
}

// modelProviderCompatible is the shared purpose and machine scope used by model settings and launcher pickers.
func (s *Service) modelProviderCompatible(p ModelPurpose, v ModelProvider, stored []storedLauncher) bool {
	compatible := v.Kind == p.Kind
	if p.Kind == "launcher" && v.Kind == "api" { // a direct Messages API provider starts Claude on its gateway (#2455)
		compatible = messagesAPI(v.API)
	}
	if p.Kind == "api" && v.Kind == "launcher" {
		// Claude Code answering on the main chat's computer (models_run.go); a managed launcher with a Messages gateway is
		// called over HTTP, as before. With no such computer it stays offered, and its status says to choose one.
		compatible = (v.Agent == "" || v.Agent == "claude") && (p.MachineID == "" || len(v.Machines) == 0 || slices.Contains(v.Machines, p.MachineID))
		for _, l := range stored {
			if v.ID == "launcher."+l.Name {
				compatible = messagesAPI(l.API) || compatible && (p.MachineID == "" || s.scopeReason(l, p.MachineID) == "")
				break
			}
		}
	}
	if p.Kind == "plugin" {
		compatible = v.ID == p.ID
	}
	if compatible && p.CategoryID == "assistant" && v.Kind == "launcher" {
		compatible = slices.Contains([]string{"", "claude", "codex", "agy", "omp"}, v.Agent)
		// A managed Codex launcher runs through `personad launch`, which execs codex itself, past the `app-server`
		// wrapper the assistant puts around a launcher's codex (node codexShell): the main chat would wait on an
		// interactive Codex that never answers.
		if v.Agent == "codex" && slices.ContainsFunc(stored, func(l storedLauncher) bool { return v.ID == "launcher."+l.Name && l.Kind != "api" }) {
			compatible = false
		}
	}
	if compatible && p.Kind == "launcher" && len(v.Machines) > 0 && p.MachineID != "" {
		compatible = slices.Contains(v.Machines, p.MachineID)
	}
	if compatible && p.Kind == "launcher" && p.MachineID != "" {
		for _, l := range stored {
			if v.ID == "launcher."+l.Name {
				compatible = s.scopeReason(l, p.MachineID) == ""
				break
			}
		}
	}
	return compatible
}

// modelLauncherCompatible applies the shared provider rule to the current, merged launcher on one machine. Unlike
// the global discovery table, this includes new shell launchers, hub.json wrappers and a runtime's current caps.
func (s *Service) modelLauncherCompatible(p ModelPurpose, l ConfiguredLauncher, stored []storedLauncher) bool {
	v := ModelProvider{ID: "launcher." + l.Name, Kind: "launcher", Agent: cmp.Or(l.AgentType, l.Agent), Model: l.Model}
	for _, managed := range stored {
		if managed.Name == l.Name {
			v.Kind, v.API = managed.Kind, managed.API
			if v.Kind == "" {
				v.Kind = "launcher"
			}
			break
		}
	}
	source := compat.Source(l.Source)
	runtime := source != "" && source != "persona" && source != "builtin" && !machineSource(source)
	if p.CategoryID == "assistant" && runtime {
		if !l.Assistant {
			return false
		}
		// A runtime's assistant capability is explicit and does not depend on which agent it embeds.
		v.Agent = ""
	}
	return s.modelProviderCompatible(p, v, stored)
}

func effortLevelsForKind(kind string) []string {
	if kind == "api" {
		return append(slices.Clone(proto.EffortLevels), "off", "none")
	}
	return proto.EffortLevels
}

func (s *Service) validateModel(ctx context.Context, p ModelPurpose, b ModelBinding, providers []ModelProvider) error {
	var provider *ModelProvider
	for i := range providers {
		if providers[i].ID == b.Provider {
			provider = &providers[i]
			break
		}
	}
	if p.Kind == "launcher" && p.CategoryID == "assistant" && p.MachineID != "" {
		// Model settings and the picker share the current machine's merged launchers, not the global discovery cache.
		name := strings.TrimPrefix(b.Provider, "launcher.")
		if provider == nil {
			// Not in the global discovery table yet: only a configured wrapper or a launcher on the machine's own list.
			_, configured := s.configuredLauncher(ctx, p.MachineID, name)
			if _, known := s.launchersHere(ctx, p.MachineID); !configured && !known {
				return i18n.Err("assistant-models.err_provider_missing")
			}
		}
		fit, refusal := s.mainChatFit(ctx, p.MachineID, name)
		switch fit {
		case mainChatGone:
			return i18n.Err("assistant-models.err_provider_missing")
		case mainChatRefused:
			return refusal
		}
	} else {
		if provider == nil {
			return i18n.Err("assistant-models.err_provider_missing")
		}
		if !slices.Contains(p.ProviderIDs, b.Provider) {
			return i18n.Err("assistant-models.err_provider_unsupported")
		}
	}
	if b.Effort != "" && !slices.Contains(effortLevelsForKind(p.Kind), b.Effort) {
		return i18n.Err("assistant-models.err_effort_unsupported")
	}
	if p.Kind == "api" {
		c, err := s.apiConfig(ctx, b)
		if err == nil && c.RunLauncher != "" {
			return checkModelID(b.Model) // passed to the CLI as --model
		}
		return err
	}
	if p.Kind == "plugin" {
		if b.Effort != "" {
			return i18n.Err("assistant-models.err_extension_effort")
		}
		if b.Model != "" {
			if provider.Status != "" {
				return errors.New(provider.Status)
			}
			if !slices.ContainsFunc(provider.Models, func(m ModelChoice) bool { return m.ID == b.Model }) {
				return i18n.Err("assistant-models.err_extension_model")
			}
		}
		return nil
	}
	if p.Kind == "launcher" {
		if provider != nil && len(provider.Machines) > 0 && p.MachineID != "" && !slices.Contains(provider.Machines, p.MachineID) {
			return i18n.Err("assistant-models.err_launcher_not_on_machine")
		}
		if b.Model != "" {
			machine := ""
			if p.MachineID == "" {
				// Like the model picker, unbound purposes use an online candidate;
				// with none available, saving leaves the model unverified.
				machine, _ = s.pickMachine(ctx, provider.Machines, "")
			} else if _, ok := s.device(ctx, p.MachineID); ok {
				machine = p.MachineID
			} else if len(provider.Machines) > 0 {
				if _, ok := s.device(ctx, provider.Machines[0]); ok {
					machine = provider.Machines[0]
				}
			}
			if machine != "" {
				var result struct {
					Agent string `json:"agent"`
				}
				if err := s.call(ctx, machine, assistantwire.DeviceLaunchersModelCheck, s.launcherParams(ctx, machine, strings.TrimPrefix(b.Provider, "launcher."), map[string]any{"model": b.Model}), &result); err != nil {
					return i18n.Err("assistant-models.err_check_model", i18n.Args{"error": i18n.Localize(err, s.requestCtxLang(ctx))})
				}
				if p.CategoryID == "agent.new" && result.Agent != "claude" && result.Agent != "codex" && result.Agent != "agy" && result.Agent != "omp" {
					return i18n.Err("assistant-models.err_agent_launcher")
				}
			}
		}
	}
	return nil
}

// providerModelUses are the current configurable purposes assigned to provider: a provider in use is not deleted underneath them.
// Automatic selection by the built-in API categories is not an assignment; current extension purposes also protect an inherited provider.
// The background supervisor's use is one of them (modelConfigIDs, #5806): its own provider, or the main chat's while it follows it.
func (s *Service) providerModelUses(ctx context.Context, provider string) ([]string, error) {
	uses := []string{}
	ids := slices.Clone(modelConfigIDs)
	for _, x := range s.modelExtensions(ctx) {
		ids = append(ids, assistantwire.ExtensionPurposePrefix+x.StorageKey())
	}
	for _, id := range ids {
		p, err := s.modelPurposeDef(ctx, id)
		if err != nil {
			return nil, err
		}
		b, err := s.resolveModel(ctx, id)
		if err != nil {
			return nil, err
		}
		if b.Provider != provider {
			continue
		}
		if p.Kind == "api" && slices.Contains(modelCategoryIDs, id) {
			// Automatic selection follows the remaining providers; only an assignment that still names this provider
			// without its automatic fallback blocks deletion. Current extension purposes keep their inherited-use guard.
			without := slices.DeleteFunc(slices.Clone(s.storedLaunchers(ctx)), func(l storedLauncher) bool {
				return launcherProvider(l.Name) == provider
			})
			withoutCtx := withManagedSnapshot(ctx, without)
			fallback, err := s.resolveModel(withoutCtx, id)
			if err != nil {
				return nil, err
			}
			if fallback.Provider != "" && fallback.Provider != provider {
				if strings.HasPrefix(fallback.Provider, "launcher.") || s.legacyModelConfig(withoutCtx, fallback.Provider).BaseURL != "" {
					continue
				}
			}
		}
		uses = append(uses, p.Name)
	}
	return uses, nil
}

// triageInUse refuses a change that leaves inbox.triage uncallable while plugins that need it are on: 422
// triage_model_in_use, naming them (not 409: the apps read a 409 from model-config as an edit conflict and would hide
// the message; a 422 they show as it is). nil when the change may go (docs/extensions/extensions.md "General rules for the Inbox").
func (s *Service) triageInUse(ctx context.Context, id string, effective ModelBinding) (map[string]any, error) {
	if id != "inbox.triage" || s.modelUsable(ctx, effective) {
		return nil, nil
	}
	var res struct {
		Names []string `json:"names"`
	}
	if err := s.appCall(ctx, AppPlugins, assistantwire.PluginsNeedTriage, map[string]any{}, &res); err != nil {
		return nil, err
	}
	if len(res.Names) == 0 {
		return nil, nil
	}
	l := s.requestCtxLang(ctx)
	return map[string]any{"code": "triage_model_in_use", "plugins": res.Names,
		"error": i18n.T(l, "assistant-models.triage_in_use", i18n.Args{"names": strings.Join(res.Names, i18n.T(l, "assistant-models.list_separator"))})}, nil
}
