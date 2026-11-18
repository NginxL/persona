package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// gatewayCatalog is Claude Code's /model list on a Persona-managed gateway provider whose tiers all name one model, as
// captured live (2026-11-18, Claude Code 2.1.291; IDs, names and resolutions only): every tier alias reads "Opus 5.5",
// and the gateway's own entry for another route of it reads the same.
func gatewayCatalog() []proto.AgentModel {
	return []proto.AgentModel{
		{ID: "default", Name: "Default (recommended)", Resolved: "claude-opus-5-5", Default: true},
		{ID: "opus", Name: "Opus 5.5", Description: "Custom Opus model (claude-opus-5-5)", Resolved: "claude-opus-5-5", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "high"},
		{ID: "fable", Name: "Opus 5.5", Description: "Custom Fable model (claude-opus-5-5)", Resolved: "claude-opus-5-5"},
		{ID: "sonnet", Name: "Opus 5.5", Description: "Custom Sonnet model (claude-opus-5-5)", Resolved: "claude-opus-5-5"},
		{ID: "haiku", Name: "Opus 5.5", Description: "Custom Haiku model (claude-opus-5-5)", Resolved: "claude-opus-5-5"},
		{ID: "kiro/claude-opus-5-5", Name: "Opus 5.5", Description: "From gateway"},
	}
}

// rowsText is a list as one line a row: id, name, what it resolves to and its aliases.
func rowsText(models []proto.AgentModel) []string {
	var out []string
	for _, m := range models {
		s := m.ID + " " + m.Name
		if m.Resolved != "" {
			s += " → " + m.Resolved
		}
		if len(m.Aliases) > 0 {
			s += fmt.Sprintf(" %v", m.Aliases)
		}
		out = append(out, s)
	}
	return out
}

// One row per model (#10365): the rows of one launcher that run the same model with the same window fold into one, the
// concrete model's on a gateway provider; Default, other routes and other windows stay apart.
func TestFoldModels(t *testing.T) {
	plain := []proto.AgentModel{
		{ID: "default", Name: "Default (recommended)", Resolved: "claude-opus-5-5", Default: true},
		{ID: "opus", Name: "Opus 5.5", Resolved: "claude-opus-5-5"},
		{ID: "fable", Name: "Fable 5.2", Resolved: "claude-fable-5-2"},
		{ID: "sonnet", Name: "Sonnet 5.5", Resolved: "claude-sonnet-5-5"},
		{ID: "haiku", Name: "Haiku 5.5", Resolved: "claude-haiku-5-5"},
	}
	for _, c := range []struct {
		name     string
		models   []proto.AgentModel
		concrete bool
		declared []proto.ProviderModel
		want     []string
	}{
		{"a gateway provider: the four aliases are the concrete model's row", gatewayCatalog(), true, nil, []string{
			"default Default (recommended) → claude-opus-5-5",
			"claude-opus-5-5 Opus 5.5 [opus fable sonnet haiku]",
			"kiro/claude-opus-5-5 Opus 5.5",
		}},
		{"a shell launcher on that gateway: the first alias is the row", gatewayCatalog(), false, nil, []string{
			"default Default (recommended) → claude-opus-5-5",
			"opus Opus 5.5 → claude-opus-5-5 [fable sonnet haiku]",
			"kiro/claude-opus-5-5 Opus 5.5",
		}},
		{"plain claude: aliases of different models stay", plain, false, nil, rowsText(plain)},
		{"a 1M window is another row", []proto.AgentModel{
			{ID: "opus", Name: "Opus 5.5", Resolved: "claude-opus-5-5"},
			{ID: "opus[1m]", Name: "Opus 5.5 (1M context)", Resolved: "claude-opus-5-5[1m]"},
			{ID: "claude-opus-5-5[1m]", Name: "claude-opus-5-5[1m]"},
		}, true, nil, []string{
			"opus Opus 5.5 → claude-opus-5-5",
			"claude-opus-5-5[1m] Opus 5.5 (1M context) [opus[1m]]",
		}},
		{"declared 1M: the provider's entry runs as [1m] too", []proto.AgentModel{
			{ID: "opus", Name: "Opus 5.5", Resolved: "claude-opus-5-5[1m]"},
			{ID: "sonnet", Name: "Opus 5.5", Resolved: "claude-opus-5-5[1m]"},
			{ID: "claude-opus-5-5", Name: "claude-opus-5-5"},
		}, true, []proto.ProviderModel{{ID: "claude-opus-5-5", ContextWindow: 1000000}}, []string{
			"claude-opus-5-5 Opus 5.5 → claude-opus-5-5[1m] [opus sonnet]",
		}},
		{"declared 200K: the [1m] spelling is another window", []proto.AgentModel{
			{ID: "glm-5", Name: "GLM 5"},
			{ID: "opus", Name: "GLM 5", Resolved: "glm-5[1m]"},
		}, true, []proto.ProviderModel{{ID: "glm-5", ContextWindow: 200000}}, []string{
			"glm-5 GLM 5",
			"opus GLM 5 → glm-5[1m]",
		}},
		{"opusplan runs two models: its own row", []proto.AgentModel{
			{ID: "opus", Name: "Opus 5.5", Resolved: "claude-opus-5-5"},
			{ID: "opusplan", Name: "Opus Plan Mode", Resolved: "claude-opus-5-5"},
		}, false, nil, []string{
			"opus Opus 5.5 → claude-opus-5-5",
			"opusplan Opus Plan Mode → claude-opus-5-5",
		}},
		{"typed IDs never fold", []proto.AgentModel{
			{ID: "gw/x", Name: "gw/x"},
			{ID: "gw/x", Name: "gw/x", Custom: true},
		}, true, nil, []string{"gw/x gw/x", "gw/x gw/x"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := slices.Clone(c.models)
			got := rowsText(foldModels(in, c.concrete, c.declared))
			if !slices.Equal(got, c.want) {
				t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
			if !slices.EqualFunc(in, c.models, func(a, b proto.AgentModel) bool { return a.ID == b.ID && a.Name == b.Name }) {
				t.Fatal("the list given was changed")
			}
		})
	}

	// The folded row keeps the first member's description and the efforts of the first that has any.
	got := foldModels(gatewayCatalog(), true, nil)[1]
	if got.Description != "Custom Opus model (claude-opus-5-5)" || fmt.Sprint(got.Efforts) != "[low medium high]" || got.DefaultEffort != "high" {
		t.Fatalf("folded row: %+v", got)
	}
	reordered := gatewayCatalog()
	reordered[1], reordered[4] = reordered[4], reordered[1] // haiku first, without efforts
	if got := foldModels(reordered, true, nil)[1]; got.ID != "claude-opus-5-5" || fmt.Sprint(got.Efforts) != "[low medium high]" ||
		fmt.Sprint(got.Aliases) != "[haiku fable sonnet opus]" {
		t.Fatalf("another order: %+v", got)
	}
}

// Rows of one list that still read the same are told apart by the route their ID goes through; the one without keeps
// the plain name, Default and rows with names of their own are left alone.
func TestRouteNames(t *testing.T) {
	models := foldModels(gatewayCatalog(), true, nil)
	models = append(models, proto.AgentModel{ID: "anthropic/claude-fable-5-2", Name: "Fable 5.2"})
	for _, c := range []struct {
		lang i18n.Lang
		want string
	}{{i18n.En, "Opus 5.5 (kiro)"}, {i18n.ZhHans, "Opus 5.5（kiro）"}, {i18n.Ja, "Opus 5.5（kiro）"}} {
		got := slices.Clone(models)
		routeNames(got, c.lang)
		if got[2].Name != c.want || got[1].Name != "Opus 5.5" || got[0].Name != "Default (recommended)" || got[3].Name != "Fable 5.2" {
			t.Fatalf("%s: %v", c.lang, rowsText(got))
		}
	}
}

// The models route a picker reads (purpose assistant and New Agent alike) folds a gateway provider's list, and every
// spelling folded away still counts as the launcher's: a pick saved as "opus" keeps working (#10365).
func TestGatewayProviderListOneRowPerModel(t *testing.T) {
	ctx := context.Background()
	s, f, _ := modelService(t)
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gateway-api", BaseURL: "https://gateway.example.com/v1",
		Model: "claude-opus-5-5", Models: []proto.ProviderModel{{ID: "claude-opus-5-5"}}}, DisplayName: "Gateway API"})
	f.agentModels = map[string][]proto.AgentModel{"gateway-api": gatewayCatalog()}
	want := []string{
		"default Default (recommended) → claude-opus-5-5",
		"claude-opus-5-5 Opus 5.5 [opus fable sonnet haiku]",
		"kiro/claude-opus-5-5 Opus 5.5 (kiro)",
	}
	for _, path := range []string{"/api/v1/machines/server/launchers/gateway-api/models?purpose=assistant", "/api/v1/machines/server/launchers/gateway-api/models",
		"/api/v1/machines/server/launchers/gateway-api/models?purpose=agent.new"} {
		r := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		s.ServeHTTP(r, req.WithContext(i18n.With(req.Context(), i18n.En))) // the hub's middleware sets the request's language
		var res proto.LauncherModels
		if err := json.Unmarshal(r.Body.Bytes(), &res); err != nil || r.Code != 200 {
			t.Fatalf("%s: %d %s", path, r.Code, r.Body.String())
		}
		if got := rowsText(res.Models); !slices.Equal(got, want) {
			t.Fatalf("%s:\n%s", path, strings.Join(got, "\n"))
		}
	}
	res, err := s.askLauncherModels(ctx, "server", "gateway-api", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"opus", "fable", "sonnet", "haiku", "claude-opus-5-5", "kiro/claude-opus-5-5"} {
		if !listsModel(res, model) { // Lark's /modelchange, an agent that takes no typed model
			t.Errorf("%s is not listed", model)
		}
		if !s.launcherHasModel(ctx, "server", "gateway-api", model) {
			t.Errorf("%s is not the launcher's", model)
		}
		if err := s.pickedModelRefusal(ctx, "server", "gateway-api", model); err != nil {
			t.Errorf("%s refused: %v", model, err)
		}
	}
	if aliases, def := s.launcherModelResolution(ctx, "server", "gateway-api"); aliases["opus"] != "claude-opus-5-5" || aliases["haiku"] != "claude-opus-5-5" || def != "claude-opus-5-5" {
		t.Fatalf("resolution: %v %q", aliases, def)
	}
	// A saved alias is the launcher's own, not a typed ID to offer again.
	s.rememberCustomModel(ctx, "server", "gateway-api", "sonnet")
	if got := s.customModels(ctx, "server", "gateway-api"); len(got) != 0 {
		t.Fatalf("remembered a folded alias: %v", got)
	}
	// An ID remembered as typed before that is one of the folded spellings now is not listed twice.
	if err := s.putSetting(ctx, launchersCustomModelsKey, []customModel{{Machine: "server", Launcher: "gateway-api", Model: "fable"},
		{Machine: "server", Launcher: "gateway-api", Model: "gw/typed"}}); err != nil {
		t.Fatal(err)
	}
	if got := customIDs(machineModels(t, s, "server", "gateway-api")); fmt.Sprint(got) != "[gw/typed]" {
		t.Fatalf("custom: %v", got)
	}
}

// Which row stands for the folded ones follows the provider's effective gateway: its env's ANTHROPIC_BASE_URL over its
// Base URL. Anthropic's own API keeps the alias as the row (the CLI resolves it there); a shell launcher too.
func TestFoldRepresentativeFollowsTheEffectiveGateway(t *testing.T) {
	ctx := context.Background()
	s, f, _ := modelService(t)
	env, err := s.sealKey("# the provider's own endpoint\nexport ANTHROPIC_BASE_URL=\"https://api.anthropic.com\" \n")
	if err != nil {
		t.Fatal(err)
	}
	toGateway, err := s.sealKey("ANTHROPIC_BASE_URL=https://gateway.example.com # its relay")
	if err != nil {
		t.Fatal(err)
	}
	putLaunchers(t, s,
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "official", BaseURL: "https://gateway.example.com", Model: "claude-opus-5-5"}, EnvSealed: env},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "account", Model: "claude-opus-5-5"}},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "relayed", Model: "claude-opus-5-5"}, EnvSealed: toGateway},
		// A Messages API provider is started as Claude Code too, with the same tier alias rows.
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "messages-gw", Kind: "api", API: "messages", BaseURL: "https://gateway.example.com", Model: "claude-opus-5-5"}},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "messages-official", Kind: "api", Model: "claude-opus-5-5"}},
	)
	f.agentModels = map[string][]proto.AgentModel{"official": gatewayCatalog(), "account": gatewayCatalog(), "relayed": gatewayCatalog(), "zsh-gw": gatewayCatalog(),
		"messages-gw": gatewayCatalog(), "messages-official": gatewayCatalog()}
	for name, want := range map[string]string{"official": "opus", "account": "opus", "relayed": "claude-opus-5-5", "zsh-gw": "opus",
		"messages-gw": "claude-opus-5-5", "messages-official": "opus"} {
		res, err := s.askLauncherModels(ctx, "server", name, false)
		if err != nil || len(res.Models) != 3 || res.Models[1].ID != want {
			t.Errorf("%s: %v %v", name, rowsText(res.Models), err)
		}
	}
	for _, c := range []struct{ env, want string }{
		{"ANTHROPIC_BASE_URL=https://a.example.com", "https://a.example.com"},
		{"export ANTHROPIC_BASE_URL='https://b.example.com' # note", "https://b.example.com"},
		{"ANTHROPIC_BASE_URL= # none", ""},
		{"ANTHROPIC_BASE_URL=https://c.example.com\nANTHROPIC_BASE_URL=https://d.example.com", "https://d.example.com"},
	} {
		if got, ok := envValue(c.env, "ANTHROPIC_BASE_URL"); !ok || got != c.want {
			t.Errorf("%q: %q %v", c.env, got, ok)
		}
	}
	if _, ok := envValue("OTHER=1", "ANTHROPIC_BASE_URL"); ok {
		t.Error("found a variable the env does not set")
	}
}
