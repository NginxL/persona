package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The four background API categories have their own model_config records. Uses that only inherit from a category stay
// out of persona_settings: changing the category here has the same effect as changing it in Model Settings.
var backgroundModelGroupIDs = []string{"kb", "inbox.triage", "agent.delegate", "translate"}

// modelGroupOwnSettings exposes the existing model configuration through the same approval gate as other own settings.
// Provider IDs, effective bindings and revisions come from modelPurpose; no credential field is part of either view.
func (s *Service) modelGroupOwnSettings() []ownSetting {
	actions := []string{"get", "set", "test"}
	out := make([]ownSetting, 0, len(backgroundModelGroupIDs))
	for _, id := range backgroundModelGroupIDs {
		id := id
		out = append(out, ownSetting{
			Setting: Setting{
				Key:     modelKey(id),
				Title:   purposeName(i18n.En, id),
				Actions: actions,
				About:   "provider for this background model group; get returns the effective provider, model, effort and revision plus valid provider IDs; set waits for phone approval and a provider change clears the old provider's model, effort and permission mode; test makes one bounded connection request and saves no model-group setting",
				Value:   "provider ID string from get's purpose.provider_ids (set and test)",
			},
			get: func(ctx context.Context) (string, error) { return s.getModelGroupSetting(ctx, id) },
			set: func(ctx context.Context, v json.RawMessage) (*ownChange, error) {
				return s.setModelGroupProvider(ctx, id, v)
			},
			change: func(ctx context.Context, action string, v json.RawMessage) (*ownChange, error) {
				return s.testModelGroupProvider(ctx, id, action, v)
			},
			cardTitle: func(l i18n.Lang) string { return purposeName(l, id) },
		})
	}
	return out
}

// getModelGroupSetting returns only providers compatible with this purpose. ModelProvider and ModelPurpose contain
// connection metadata and effective bindings, never API keys, sealed keys, tokens or secret header values.
func (s *Service) getModelGroupSetting(ctx context.Context, id string) (string, error) {
	providers := s.modelProviders(ctx)
	p, err := s.modelPurpose(ctx, id, providers)
	if err != nil {
		return "", err
	}
	compatible := make([]ModelProvider, 0, len(p.ProviderIDs))
	for _, provider := range providers {
		if slices.Contains(p.ProviderIDs, provider.ID) {
			compatible = append(compatible, provider)
		}
	}
	p = p.localized(s.requestCtxLang(ctx))
	raw, err := json.Marshal(map[string]any{"key": modelKey(id), "purpose": p, "providers": compatible})
	return string(raw), err
}

func modelGroupProviderValue(v json.RawMessage) (string, error) {
	var provider string
	if len(v) == 0 || json.Unmarshal(v, &provider) != nil || strings.TrimSpace(provider) != provider || provider == "" || len(provider) > 512 || strings.ContainsAny(provider, "\r\n\x00") {
		return "", errors.New("value must be one provider ID string from get's purpose.provider_ids")
	}
	return provider, nil
}

// modelGroupProviderChoice is deliberately checked before a card or a probe. An unknown or incompatible value can
// neither create an approval nor cause a paid request, and the refusal tells the assistant exactly what is configured.
func modelGroupProviderChoice(p ModelPurpose, providers []ModelProvider, id string) (ModelProvider, error) {
	for _, provider := range providers {
		if provider.ID == id && slices.Contains(p.ProviderIDs, id) {
			return provider, nil
		}
	}
	choices := strings.Join(p.ProviderIDs, ", ")
	if choices == "" {
		choices = "(none configured)"
	}
	return ModelProvider{}, fmt.Errorf("%q is not a valid provider for %s; valid configured providers: %s", id, modelKey(p.ID), choices)
}

// testModelGroupProvider is the read-only test action. It uses the existing launcher check and returns an ordinary
// result even when the remote check fails: the structured check, not a tool transport error, is what failed.
func (s *Service) testModelGroupProvider(ctx context.Context, id, action string, v json.RawMessage) (*ownChange, error) {
	if action != "test" {
		return nil, fmt.Errorf("%s does not take %q", modelKey(id), action)
	}
	providerID, err := modelGroupProviderValue(v)
	if err != nil {
		return nil, err
	}
	providers := s.modelProviders(ctx)
	p, err := s.modelPurpose(ctx, id, providers)
	if err != nil {
		return nil, err
	}
	provider, err := modelGroupProviderChoice(p, providers, providerID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, launcherCheckTimeout)
	defer cancel()
	machine, check, err := s.checkModelProvider(ctx, provider, "", "")
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{"key": modelKey(id), "provider": providerID, "machine": machine, "check": check})
	if err != nil {
		return nil, err
	}
	return &ownChange{summary: string(raw)}, nil
}

// modelGroupProviderPreparation is one validated provider edit at one model-record revision.
type modelGroupProviderPreparation struct {
	purpose         ModelPurpose
	binding         ModelBinding
	before, after   ModelBinding
	inheritedBefore bool
	unchanged       bool
}

func (p modelGroupProviderPreparation) card(l i18n.Lang) string {
	before := modelGroupCardBinding(l, p.before)
	after := modelGroupCardBinding(l, p.after)
	if p.inheritedBefore {
		before += " (" + i18n.T(l, "assistant-settings_tool.model_group_inherited") + ")"
	} else {
		before += " (" + i18n.T(l, "assistant-settings_tool.model_group_explicit") + ")"
	}
	after += " (" + i18n.T(l, "assistant-settings_tool.model_group_explicit") + ")"
	return i18n.T(l, "assistant-settings_tool.change_model_group_provider", i18n.Args{
		"purpose": purposeName(l, p.purpose.ID) + " (" + modelKey(p.purpose.ID) + ")",
		"before":  before,
		"after":   after,
	})
}

func modelGroupCardBinding(l i18n.Lang, b ModelBinding) string {
	provider, model := b.Provider, b.Model
	if provider == "" {
		provider = i18n.T(l, "assistant-settings_tool.none")
	}
	if model == "" {
		model = i18n.T(l, "assistant-settings_tool.none")
	}
	text := provider + " · " + model
	if b.Effort != "" {
		text += " · effort=" + b.Effort
	}
	return text
}

// prepareModelGroupProviderLocked follows the HTTP model-config update's compatibility, inheritance, triage and
// validation path. The caller holds models.mu so the validated revision and the eventual write cannot drift apart.
func (s *Service) prepareModelGroupProviderLocked(ctx context.Context, id, providerID string) (modelGroupProviderPreparation, error) {
	var out modelGroupProviderPreparation
	if s.store() == nil {
		return out, i18n.Err("assistant-models.err_no_database")
	}
	providers := s.modelProviders(ctx)
	p, err := s.modelPurpose(ctx, id, providers)
	if err != nil {
		return out, err
	}
	selected, err := modelGroupProviderChoice(p, providers, providerID)
	if err != nil {
		return out, err
	}
	out.purpose, out.before, out.inheritedBefore = p, p.Effective, p.Override.Provider == ""
	if p.Override.Provider == providerID {
		out.unchanged = true
		return out, nil
	}
	raw, _ := json.Marshal(providerID)
	b, err := patchModel(p.Override, modelPatch{Provider: raw})
	if err != nil {
		return out, err
	}
	base, _, err := s.modelBaseline(ctx, p)
	if err != nil {
		return out, err
	}
	effective := effectiveModel(p, base, b)
	if body, err := s.triageInUse(ctx, id, effective); err != nil {
		return out, err
	} else if body != nil {
		return out, fmt.Errorf("%v", body["error"])
	}
	if err := s.validateModel(ctx, p, effective, providers); err != nil {
		return out, err
	}
	out.binding, out.after = b, effective
	if out.after.Model == "" {
		out.after.Model = selected.Model
	}
	return out, nil
}

func (s *Service) setModelGroupProvider(ctx context.Context, id string, v json.RawMessage) (*ownChange, error) {
	providerID, err := modelGroupProviderValue(v)
	if err != nil {
		return nil, err
	}
	s.models.mu.Lock()
	prep, err := s.prepareModelGroupProviderLocked(ctx, id, providerID)
	s.models.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if prep.unchanged {
		return &ownChange{summary: fmt.Sprintf("%s already has provider %s; nothing to change.", modelKey(id), providerID)}, nil
	}
	l := s.lang(ctx)
	approved := prep.card(l)
	revision := prep.purpose.Revision
	return &ownChange{
		applyChecksProposal: true,
		cardText:            func(lang i18n.Lang) string { return prep.card(lang) },
		apply: func(ctx context.Context) (string, error) {
			return s.applyModelGroupProvider(ctx, id, providerID, revision, l, approved)
		},
	}, nil
}

// applyModelGroupProvider revalidates the exact approved card under the model-configuration lock. A record revision
// or approved effective-binding change invalidates the approval instead of rebasing it onto something else.
func (s *Service) applyModelGroupProvider(ctx context.Context, id, providerID string, revision int64, l i18n.Lang, approved string) (string, error) {
	s.models.mu.Lock()
	fresh, err := s.prepareModelGroupProviderLocked(ctx, id, providerID)
	if err != nil {
		s.models.mu.Unlock()
		return "", fmt.Errorf("%w: revalidate the approved model-group provider: %v", approvalops.ErrNotApplied, err)
	}
	if fresh.unchanged || fresh.purpose.Revision != revision || fresh.card(l) != approved {
		s.models.mu.Unlock()
		return "", fmt.Errorf("the model group or its providers changed after the card was shown, so nothing was saved: %w", approvalops.ErrStale)
	}
	err = s.writeModelRecord(ctx, id, fresh.purpose.Revision, fresh.binding, nil, false)
	s.models.mu.Unlock()
	if err != nil {
		return "", err
	}
	text, err := s.getModelGroupSetting(ctx, id)
	if err != nil {
		return fmt.Sprintf("Saved %s, but reading back the saved view failed: %v", modelKey(id), err), nil
	}
	return text, nil
}
