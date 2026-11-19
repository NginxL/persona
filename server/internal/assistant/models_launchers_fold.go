package assistant

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// One row per model in a launcher's /model list (#10365, docs/assistant/model-config.md "One row per model"). Claude
// Code lists each tier alias as a row of its own, and a gateway provider whose tiers all name one model (Persona fills
// the tiers it leaves empty with its model) showed "Opus 5.5" four times, then a fifth time for the same model behind
// another route of the gateway. The manager folds the list once, for every picker on every client; the node's list,
// which its fallback order reads, stays as Claude Code gives it.

// standardContextWindow: the window Claude Code counts for a model whose name has no "[1m]" (node/launcherenv.go).
const standardContextWindow = 200000

// unfoldedAliases run another model than the one they resolve to some of the time (opusplan: Opus in plan mode, Sonnet
// otherwise), so they stay rows of their own.
var unfoldedAliases = []string{"opusplan"}

// foldModels is a launcher's /model list (in its order) with the rows that run the same model with the same context
// window on it folded into one row, at its first member's place. The Default row stays as it is: it sends no model and
// follows the launcher's configuration. The folded row's ID is the concrete model when concrete (a Persona-managed
// provider whose gateway is not Anthropic's API: a pick then sends a model ID the gateway serves, not a tier alias),
// else its first member's; its name the first member's own (not an ID), its efforts and speeds the first member's that
// has any. Every other spelling of its members (IDs and what they resolve to) is in Aliases, and still picks it
// (listsModel, launcherModelSet). declared: the context windows the provider gives its models (none for a launcher
// Persona does not manage). Rows are never folded across routes ("kiro/x" and "x" are different IDs) or windows.
func foldModels(models []proto.AgentModel, concrete bool, declared []proto.ProviderModel) []proto.AgentModel {
	members := map[string][]int{}
	var order []string
	for i, m := range models {
		k, ok := modelFoldKey(m, declared)
		if !ok {
			continue
		}
		if _, seen := members[k]; !seen {
			order = append(order, k)
		}
		members[k] = append(members[k], i)
	}
	folded := map[int]proto.AgentModel{} // a group's row, at its first member's index
	gone := map[int]bool{}
	for _, k := range order {
		if at := members[k]; len(at) > 1 {
			folded[at[0]] = foldedRow(models, at, concrete)
			for _, i := range at[1:] {
				gone[i] = true
			}
		}
	}
	if len(gone) == 0 {
		return models
	}
	out := make([]proto.AgentModel, 0, len(models)-len(gone))
	for i, m := range models {
		if row, ok := folded[i]; ok {
			out = append(out, row)
		} else if !gone[i] {
			out = append(out, m)
		}
	}
	return out
}

// modelFoldKey is what a row runs on its launcher: the model (what an alias resolves to, without "[1m]") and the
// context window it runs with (the provider's declared one, else 1M for a "[1m]" name, else 0: Claude Code's own).
// false for a row that never folds: the Default row, a typed ID, one with no ID, an unfoldedAliases one.
func modelFoldKey(m proto.AgentModel, declared []proto.ProviderModel) (string, bool) {
	alias, _ := strings.CutSuffix(strings.ToLower(m.ID), "[1m]")
	if m.ID == "" || m.Default || m.Custom || slices.Contains(unfoldedAliases, alias) {
		return "", false
	}
	id := m.Resolved
	if id == "" {
		id = m.ID
	}
	base, marked := strings.CutSuffix(id, "[1m]")
	w := 0
	for _, pm := range declared {
		if pm.ID == id || pm.ID == base {
			w = pm.ContextWindow
			break
		}
	}
	// a provider's model over 200K runs as "<id>[1m]" with its own window (node sdkModel), so both spellings count it;
	// a "[1m]" name counts 1M whatever is declared below that
	if marked && w <= standardContextWindow {
		w = contextWindow1M
	}
	return base + "\x00" + strconv.Itoa(w), true
}

// foldedRow is the one row for models[at...] (foldModels).
func foldedRow(models []proto.AgentModel, at []int, concrete bool) proto.AgentModel {
	row := models[at[0]]
	row.Aliases = nil
	id := row.ID
	if concrete {
		// a member that runs as itself (a gateway's or the provider's own entry), else what the aliases resolve to
		id = ""
		for _, i := range at {
			if m := models[i]; m.Resolved == "" {
				if _, alias := apiTierAliasKey(m.ID); !alias {
					id = m.ID
					break
				}
			}
		}
		id = cmp.Or(id, row.Resolved, row.ID)
	}
	row.ID = id
	if row.Resolved == id {
		row.Resolved = ""
	}
	row.Name = id
	for _, i := range at {
		if m := models[i]; m.Name != "" && m.Name != m.ID {
			row.Name = m.Name
			break
		}
	}
	for _, i := range at {
		if m := models[i]; len(row.Efforts) == 0 && len(m.Efforts) > 0 {
			row.Efforts, row.DefaultEffort = m.Efforts, m.DefaultEffort
		}
		if m := models[i]; len(row.ServiceTiers) == 0 && len(m.ServiceTiers) > 0 {
			row.ServiceTiers = m.ServiceTiers
		}
	}
	seen := map[string]bool{row.ID: true, row.Resolved: true}
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			row.Aliases = append(row.Aliases, v)
		}
	}
	for _, i := range at {
		add(models[i].ID)
		for _, a := range models[i].Aliases {
			add(a)
		}
	}
	for _, i := range at {
		add(models[i].Resolved)
	}
	return row
}

// routeNames tells apart the rows of one list that still read the same after foldModels: one whose ID goes through a
// route of the gateway ("kiro/claude-opus-5-5", the text before its "/") says that route after its name ("Opus 5.5
// (kiro)"); the one without a route keeps the plain name. The Default row is left as it is.
func routeNames(models []proto.AgentModel, lang i18n.Lang) {
	names := map[string]map[string]bool{}
	for _, m := range models {
		if m.Default {
			continue
		}
		if names[m.Name] == nil {
			names[m.Name] = map[string]bool{}
		}
		names[m.Name][m.ID] = true
	}
	for i, m := range models {
		if m.Default || len(names[m.Name]) < 2 {
			continue
		}
		if route, _, ok := strings.Cut(m.ID, "/"); ok && route != "" {
			models[i].Name = mc(lang, "model_route_name", i18n.Args{"name": m.Name, "route": route})
		}
	}
}

// oneRowPerModel is res's list folded (foldModels) and told apart (routeNames) for launcher name.
func (s *Service) oneRowPerModel(ctx context.Context, name string, res proto.LauncherModels) proto.LauncherModels {
	if len(res.Models) < 2 {
		return res
	}
	var declared []proto.ProviderModel
	concrete := false
	// A Messages API provider (kind api) is started as Claude Code too (with its address and key, as for the main
	// chat), so its /model list carries the same tier alias rows.
	if l, ok := s.managedNamed(ctx, name); ok && (l.Kind != "api" || messagesAPI(l.API)) {
		declared = l.Models
		concrete = !proto.AnthropicAPI(s.effectiveBaseURL(l))
	}
	res.Models = foldModels(res.Models, concrete, declared)
	routeNames(res.Models, s.requestCtxLang(ctx))
	return res
}

// effectiveBaseURL is the gateway l's launches reach: its env's ANTHROPIC_BASE_URL over its own Base URL (the node's
// providerEnv), Anthropic's API for none.
func (s *Service) effectiveBaseURL(l storedLauncher) string {
	base := l.LaunchBaseURL()
	if l.EnvSealed != "" {
		if env, err := s.openKey(l.EnvSealed); err == nil {
			if v, ok := envValue(env, "ANTHROPIC_BASE_URL"); ok {
				base = v
			}
		}
	}
	if base == "" {
		return "https://" + proto.AnthropicAPIHost
	}
	return base
}

// envValue is the last value a provider's env (one NAME=value a line, checkEnv) gives name, read as the node's envLine
// reads it: quotes dropped, a trailing " # …" comment of an unquoted value dropped.
func envValue(env, name string) (string, bool) {
	value, found := "", false
	for _, line := range strings.Split(env, "\n") {
		line = exportPrefix.ReplaceAllString(strings.TrimSpace(line), "")
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != name {
			continue
		}
		rest := strings.TrimLeft(v, " \t")
		switch {
		case rest != v && strings.HasPrefix(rest, "#"):
			v = "" // `A= # a note`: empty, as in a shell
		case rest != "" && (rest[0] == '"' || rest[0] == '\'') && strings.IndexByte(rest[1:], rest[0]) >= 0:
			v = rest[1 : strings.IndexByte(rest[1:], rest[0])+1]
		default:
			v = rest
			for _, sep := range []string{" #", "\t#"} {
				if i := strings.Index(v, sep); i >= 0 {
					v = v[:i]
				}
			}
		}
		value, found = strings.TrimSpace(v), true
	}
	return value, found
}
