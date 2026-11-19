package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

// apiTierAliases: the Claude Code tier aliases a model field can hold (the picker offers them for a launcher's CLI,
// whose node resolves them through ANTHROPIC_DEFAULT_*_MODEL, node/launcherenv.go resolveTier), and which of a
// launcher's tier fields each runs. As on a computer (node's providerEnv), haiku falls back to the small model and
// best runs what opus does (Claude Code 2.1.321); opusplan is opus's tier too.
var apiTierAliases = map[string]func(storedLauncher) string{
	"opus":     func(l storedLauncher) string { return l.OpusModel },
	"best":     func(l storedLauncher) string { return l.OpusModel },
	"opusplan": func(l storedLauncher) string { return l.OpusModel },
	"sonnet":   func(l storedLauncher) string { return l.SonnetModel },
	"haiku":    func(l storedLauncher) string { return cmp.Or(l.HaikuModel, l.SmallModel) },
	"fable":    func(l storedLauncher) string { return l.FableModel },
}

// apiTierAliasKey is m's entry in apiTierAliases ("Sonnet", "opus[1m]" alike), and whether it has one.
func apiTierAliasKey(m string) (string, bool) {
	k := strings.ToLower(strings.TrimSpace(m))
	k = strings.TrimSuffix(k, "[1m]")
	_, ok := apiTierAliases[k]
	return k, ok
}

// resolveAPITierAlias is model as a direct API call to l's gateway sends it (#9165): a tier alias becomes the model
// l's tier field names, since no gateway serves a model called "sonnet"; any other model as it is. A tier alias l
// names no model for (or names another alias for) is an error, not a request the gateway refuses with a vague 4xx.
func resolveAPITierAlias(l storedLauncher, model string) (string, error) {
	k, ok := apiTierAliasKey(model)
	if !ok {
		return model, nil
	}
	if v := strings.TrimSpace(apiTierAliases[k](l)); v != "" {
		if _, alias := apiTierAliasKey(v); !alias {
			return v, nil
		}
	}
	return "", i18n.Err("assistant-models.err_tier_alias_unresolved", i18n.Args{"model": model, "provider": l.Name})
}

var errModelRecordEnumeration = errors.New("the store cannot enumerate model configuration")

type migratedTierAliases struct {
	settings       map[string]any
	profileChanged bool
	active         string
}

type tierAliasMigration struct {
	service  *Service
	ctx      context.Context
	provider string
	moved    map[string]string
	current  map[string]modelRecord
	hidden   []string
	hiddenOK bool
}

func modelRecordKeyID(key string) (string, bool) {
	for _, prefix := range []string{modelOverrideKeyPrefix, modelKeyPrefix} {
		if id, ok := strings.CutPrefix(key, prefix); ok && id != "" {
			return id, true
		}
	}
	return "", false
}

// tierAliasPurpose is the static inheritance rule for a persisted purpose. Disabled and deleted extensions and tasks
// keep their records, so migration cannot depend on their current catalogs.
func tierAliasPurpose(id string) (ModelPurpose, bool) {
	for _, p := range modelPurposes {
		if p.ID == id {
			return p, true
		}
	}
	if isTaskPurpose(id) {
		return ModelPurpose{ID: id, Kind: "launcher", Parent: "assistant"}, true
	}
	if isExtensionPurpose(id) {
		return ModelPurpose{ID: id, Kind: "api", Parent: "kb"}, true
	}
	if strings.HasPrefix(id, "plugin.") {
		return ModelPurpose{ID: id, Kind: "plugin"}, true
	}
	return ModelPurpose{}, false
}

// providerFor resolves only the provider, using a profile's bindings before the current records where the profile has
// one. Model and effort defaults do not affect whether an alias belongs to the Chat provider being changed.
func (m *tierAliasMigration) providerFor(id string, overlay map[string]ModelBinding, visiting map[string]bool) (string, error) {
	if visiting[id] {
		return "", fmt.Errorf("model purpose %s has an inheritance cycle", id)
	}
	visiting[id] = true
	defer delete(visiting, id)

	var binding ModelBinding
	if b, ok := overlay[id]; ok {
		binding = b
	} else if r, ok := m.current[id]; ok {
		binding = r.Binding
	}
	if binding.Provider != "" {
		return binding.Provider, nil
	}
	p, ok := tierAliasPurpose(id)
	if !ok {
		return "", fmt.Errorf("model purpose %s has no persisted inheritance rule", id)
	}
	if p.Parent != "" {
		return m.providerFor(p.Parent, overlay, visiting)
	}
	if p.ID == "agent.delegate" {
		return m.providerFor("inbox.triage", overlay, visiting)
	}
	if p.Kind == "api" {
		legacy := "legacy.ai"
		if p.ID == "inbox.triage" {
			legacy = "legacy.triage"
		} else if p.ID == "kb" {
			legacy = "legacy.kb"
		}
		if m.service.legacyModelConfig(m.ctx, legacy).BaseURL != "" {
			return legacy, nil
		}
		if !m.hiddenOK {
			hidden, err := m.service.readHiddenLaunchers(m.ctx)
			if err != nil {
				return "", err
			}
			m.hidden, m.hiddenOK = hidden, true
		}
		stored, known := m.service.storedLaunchersKnown(m.ctx)
		if !known {
			return "", errManagedLaunchersRead
		}
		return launcherProvider(fallbackAPIProviderFrom(stored, m.hidden)), nil
	}
	if p.ID == "assistant" {
		settings, ok := m.service.savedSettings(m.ctx)
		if !ok {
			return "", errors.New("the assistant's settings could not be read")
		}
		return launcherProvider(m.service.effective(m.ctx, settings).Launcher), nil
	}
	if p.Kind == "launcher" {
		return "launcher.claude", nil
	}
	return p.ID, nil
}

func (m *tierAliasMigration) binding(id string, binding ModelBinding, overlay map[string]ModelBinding, moved map[string]string) (ModelBinding, bool, error) {
	alias, ok := apiTierAliasKey(binding.Model)
	if !ok || moved[alias] == "" {
		return binding, false, nil
	}
	if binding.Provider != m.provider {
		if binding.Provider != "" {
			return binding, false, nil
		}
		provider, err := m.providerFor(id, overlay, map[string]bool{})
		if err != nil {
			return binding, false, err
		}
		if provider != m.provider {
			return binding, false, nil
		}
	}
	binding.Model = moved[alias]
	return binding, true, nil
}

func (m *tierAliasMigration) purposes(purposes map[string]ModelBinding, moved map[string]string) (bool, error) {
	changed := false
	for id, binding := range purposes {
		next, ok, err := m.binding(id, binding, purposes, moved)
		if err != nil {
			return false, err
		}
		if ok {
			purposes[id] = next
			changed = true
		}
	}
	return changed, nil
}

func (m *tierAliasMigration) launcherModels(models LauncherModels, moved map[string]string) (LauncherModels, bool) {
	out := models
	changed := false
	for _, field := range out.fields(nil) {
		if field.name == "effort" || *field.set == nil {
			continue
		}
		alias, ok := apiTierAliasKey(**field.set)
		if !ok || moved[alias] == "" {
			continue
		}
		*field.set = profileValue(moved[alias])
		changed = true
	}
	return out, changed
}

func launcherWithModels(l storedLauncher, models LauncherModels) storedLauncher {
	for _, field := range models.fields(&l.ManagedLauncher) {
		if *field.set != nil {
			*field.launcher = **field.set
		}
	}
	return l
}

func sameTierAliasTargets(a, b storedLauncher) bool {
	return a.OpusModel == b.OpusModel && a.SonnetModel == b.SonnetModel && a.HaikuModel == b.HaikuModel &&
		a.SmallModel == b.SmallModel && a.FableModel == b.FableModel
}

// movedTierAliases resolves aliases through the effective old and new launchers. An overlay that was already
// unresolvable keeps the provider-level fallback, preserving the legacy repair behavior for aliases in tier fields.
func movedTierAliases(old, next storedLauncher, fallback map[string]string) map[string]string {
	moved := map[string]string{}
	maps.Copy(moved, fallback)
	for alias := range apiTierAliases {
		before, err := resolveAPITierAlias(old, alias)
		if err != nil {
			continue
		}
		after, afterErr := resolveAPITierAlias(next, alias)
		if afterErr != nil || after != before {
			moved[alias] = before
		} else {
			delete(moved, alias)
		}
	}
	return moved
}

// migrateTierAliases keeps every persisted binding callable when a Chat save removes the tier fields it uses. Raw
// records, saved profiles and the rollback image are read completely; any failure aborts before the caller's single
// transaction. The caller holds models.mu and supplies the candidate post-save managed-launcher snapshot in ctx.
func (s *Service) migrateTierAliases(ctx context.Context, old storedLauncher, next *storedLauncher) (migratedTierAliases, error) {
	moved := movedTierAliases(old, *next, nil)
	if sameTierAliasTargets(old, *next) {
		// No raw binding or overlaid image can resolve differently when every alias target field stayed unchanged.
		return migratedTierAliases{}, nil
	}
	if alias, ok := apiTierAliasKey(next.Model); ok && moved[alias] != "" {
		next.Model = moved[alias]
	}

	store, ok := s.store().(settingsPrefixStore)
	if !ok {
		return migratedTierAliases{}, errModelRecordEnumeration
	}
	raw, err := store.SettingsPrefix(ctx, modelKeyPrefix)
	if err != nil {
		return migratedTierAliases{}, err
	}
	type record struct {
		key, id string
		value   modelRecord
	}
	records := make([]record, 0, len(raw))
	migration := tierAliasMigration{service: s, ctx: ctx, provider: launcherProvider(old.Name), moved: moved, current: map[string]modelRecord{}}
	for _, key := range slices.Sorted(maps.Keys(raw)) {
		var value modelRecord
		if err := json.Unmarshal(raw[key], &value); err != nil {
			return migratedTierAliases{}, fmt.Errorf("read %s: %w", key, err)
		}
		id, ok := modelRecordKeyID(key)
		if !ok {
			continue
		}
		records = append(records, record{key: key, id: id, value: value})
		if key == modelRecordKey(id) {
			migration.current[id] = value
		}
	}

	profiles, _, err := s.readModelProfiles(ctx)
	if err != nil {
		return migratedTierAliases{}, err
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return migratedTierAliases{}, err
	}

	writes := map[string]any{}
	for _, record := range records {
		binding, changed, err := migration.binding(record.id, record.value.Binding, map[string]ModelBinding{record.id: record.value.Binding}, migration.moved)
		if err != nil {
			return migratedTierAliases{}, err
		}
		if changed {
			record.value.Binding = binding
			record.value.Revision++
			writes[record.key] = record.value
		}
	}

	profilesChanged := false
	for i := range profiles.Profiles {
		profile := &profiles.Profiles[i]
		models, hasModels := profile.Launchers[old.Name]
		moved := migration.moved
		if hasModels {
			moved = movedTierAliases(launcherWithModels(old, models), launcherWithModels(*next, models), moved)
		}
		changed, err := migration.purposes(profile.Purposes, moved)
		if err != nil {
			return migratedTierAliases{}, err
		}
		if hasModels {
			nextModels, ok := migration.launcherModels(models, moved)
			if ok {
				profile.Launchers[old.Name] = nextModels
				changed = true
			}
		}
		profilesChanged = profilesChanged || changed
	}
	profileChanged := profilesChanged
	if profilesChanged {
		profiles.Revision++
		writes[modelProfilesKey] = profiles
	}
	if previous := state.Previous; previous != nil {
		models, hasModels := previous.Launchers[old.Name]
		moved := migration.moved
		if hasModels {
			moved = movedTierAliases(launcherWithModels(old, models), launcherWithModels(*next, models), moved)
		}
		changed, err := migration.purposes(previous.Purposes, moved)
		if err != nil {
			return migratedTierAliases{}, err
		}
		if hasModels {
			nextModels, ok := migration.launcherModels(models, moved)
			if ok {
				previous.Launchers[old.Name] = nextModels
				changed = true
			}
		}
		if changed {
			writes[modelProfileStateKey] = state
			profileChanged = true
		}
	}
	return migratedTierAliases{settings: writes, profileChanged: profileChanged, active: state.Active}, nil
}
