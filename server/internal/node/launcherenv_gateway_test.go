package node

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A managed provider behind a gateway runs claude with tool search off, every tool loaded up front (#10365), in the
// launch environment and the settings env alike (settings env beats the process environment); its own environment
// opts back in. Anthropic's own API is left to Claude Code, and to the assistant's start.
func TestProviderEnvToolSearchBehindAGateway(t *testing.T) {
	for _, tc := range []struct {
		name string
		l    proto.ManagedLauncher
		want string // "-": not set
	}{
		{"a gateway provider", proto.ManagedLauncher{Name: "gw", Kind: "api", BaseURL: "https://gw.example.com", APIKey: "sk-test-k", Model: "m1"}, "false"},
		{"one from before #2455", proto.ManagedLauncher{Name: "gw", BaseURL: "https://gw.example.com"}, "false"},
		{"a gateway its own env names", proto.ManagedLauncher{Name: "gw", Kind: "api", Env: "ANTHROPIC_BASE_URL=https://gw.example.com"}, "false"},
		{"opted in by its own env", proto.ManagedLauncher{Name: "gw", Kind: "api", BaseURL: "https://gw.example.com", Env: "ENABLE_TOOL_SEARCH=true"}, "true"},
		{"Anthropic's own API", proto.ManagedLauncher{Name: "a", Kind: "api", BaseURL: "https://api.anthropic.com/v1", APIKey: "sk-test-k"}, "-"},
		{"no Base URL", proto.ManagedLauncher{Name: "a", Kind: "api", APIKey: "sk-test-k", Model: "claude-opus-5-5"}, "-"},
		{"its own env goes back to Anthropic's API", proto.ManagedLauncher{Name: "a", Kind: "api", BaseURL: "https://gw.example.com", Env: "ANTHROPIC_BASE_URL="}, "-"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, set := providerEnv(tc.l, "", "", false)[toolSearchEnv]
			if !set {
				got = "-"
			}
			if got != tc.want {
				t.Fatalf("providerEnv %s=%q, want %q", toolSearchEnv, got, tc.want)
			}
			inList := "-"
			if slices.ContainsFunc(managedEnv(tc.l), func(kv string) bool { return strings.HasPrefix(kv, toolSearchEnv+"=") }) {
				inList = envValue(managedEnv(tc.l), toolSearchEnv)
			}
			settings, inSettings := managedSettingsEnv(tc.l)[toolSearchEnv]
			if !inSettings {
				settings = "-"
			}
			if inList != tc.want || settings != tc.want {
				t.Fatalf("launch env %q, settings env %q, want %q in both", inList, settings, tc.want)
			}
		})
	}
}

// Behind a gateway no model variable ever holds a tier alias (#10365): Claude Code 2.1.291 sends a tier variable's value
// as the model's name, so ANTHROPIC_DEFAULT_OPUS_MODEL=opus asked the gateway for "opus" (404). An alias in a tier
// field, the default model, the model asked for or the provider's own env counts as unset there: the tier's model, the
// default model, or nothing (Claude Code's own). Anthropic's own API keeps the alias as it is.
func TestProviderEnvNeverWritesTierAlias(t *testing.T) {
	const gw = "https://gw.example.com"
	models := []proto.ProviderModel{{ID: "route/claude-opus-5-5", ContextWindow: 1000000}, {ID: "small", ContextWindow: 128000}}
	aliasFree := func(t *testing.T, env map[string]string) {
		t.Helper()
		for _, k := range tierModelVars {
			if v, ok := env[k]; ok && isTierAlias(v) {
				t.Errorf("%s=%q is a tier alias: %v", k, v, env)
			}
		}
	}
	for _, tc := range []struct {
		name  string
		l     proto.ManagedLauncher
		model string
		want  map[string]string // "-": unset
	}{
		{"alias tier fields are the default model", proto.ManagedLauncher{Name: "gw", BaseURL: gw, Model: "route/claude-opus-5-5", OpusModel: "opus", FableModel: "fable", Models: models}, "",
			map[string]string{"ANTHROPIC_MODEL": "route/claude-opus-5-5[1m]", "ANTHROPIC_DEFAULT_OPUS_MODEL": "route/claude-opus-5-5[1m]", "ANTHROPIC_DEFAULT_FABLE_MODEL": "route/claude-opus-5-5[1m]"}},
		{"an alias default model with no model at all", proto.ManagedLauncher{Name: "gw", BaseURL: gw, Model: "opus"}, "",
			map[string]string{"ANTHROPIC_MODEL": "-", "ANTHROPIC_DEFAULT_OPUS_MODEL": "-", "ANTHROPIC_SMALL_FAST_MODEL": "-", "CLAUDE_CODE_SUBAGENT_MODEL": "-"}},
		{"an alias default model with an Opus tier", proto.ManagedLauncher{Name: "gw", BaseURL: gw, Model: "opus", OpusModel: "small", Models: models}, "",
			map[string]string{"ANTHROPIC_MODEL": "small", "ANTHROPIC_DEFAULT_OPUS_MODEL": "small", "ANTHROPIC_DEFAULT_SONNET_MODEL": "small"}},
		{"the model asked for is an alias", proto.ManagedLauncher{Name: "gw", BaseURL: gw, Model: "route/claude-opus-5-5", HaikuModel: "small", Models: models}, "haiku",
			map[string]string{"ANTHROPIC_MODEL": "small", "ANTHROPIC_DEFAULT_HAIKU_MODEL": "small"}},
		{"a concrete tier stays", proto.ManagedLauncher{Name: "gw", BaseURL: gw, Model: "route/claude-opus-5-5", OpusModel: "small", Models: models}, "",
			map[string]string{"ANTHROPIC_DEFAULT_OPUS_MODEL": "small", "ANTHROPIC_MODEL": "route/claude-opus-5-5[1m]"}},
		{"the provider's env names an alias", proto.ManagedLauncher{Name: "gw", BaseURL: gw, Model: "m1",
			Env: "ANTHROPIC_DEFAULT_OPUS_MODEL=opus\nANTHROPIC_DEFAULT_SONNET_MODEL=m2\nANTHROPIC_MODEL=sonnet\nCLAUDE_CODE_SUBAGENT_MODEL=inherit"}, "",
			map[string]string{"ANTHROPIC_DEFAULT_OPUS_MODEL": "m1", "ANTHROPIC_DEFAULT_SONNET_MODEL": "m2", "ANTHROPIC_MODEL": "m2", "CLAUDE_CODE_SUBAGENT_MODEL": "m1"}},
		{"best and opusplan are the Opus tier", proto.ManagedLauncher{Name: "gw", BaseURL: gw, Model: "m1", OpusModel: "m2"}, "opusplan",
			map[string]string{"ANTHROPIC_MODEL": "m2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, main := range []bool{false, true} {
				env := providerEnv(tc.l, tc.model, "", main)
				aliasFree(t, env)
				for k, want := range tc.want {
					got, ok := env[k]
					if !ok {
						got = "-"
					}
					if got != want {
						t.Errorf("main=%v: %s=%q, want %q", main, k, got, want)
					}
				}
			}
			aliasFree(t, managedSettingsEnv(tc.l))
		})
	}
	// Anthropic's own API resolves its aliases: kept as they were.
	l := proto.ManagedLauncher{Name: "a", Kind: "api", APIKey: "sk-test-k", Model: "opus", OpusModel: "opus"}
	if env := providerEnv(l, "", "", false); env["ANTHROPIC_MODEL"] != "opus" || env["ANTHROPIC_DEFAULT_OPUS_MODEL"] != "opus" {
		t.Fatalf("Anthropic's API: %v", env)
	}
}

// --model on a managed provider behind a gateway (#10365): a tier alias is the model the provider's environment gives
// that tier, or none (its default model) when it gives none; a model ID, plain claude, an Anthropic-API provider and
// another agent keep theirs. The launch's last guard does the same to what reaches `personad launch`.
func TestProviderModelArg(t *testing.T) {
	const gw = "https://gw.example.com"
	withTiers := proto.ManagedLauncher{Name: "gw", BaseURL: gw, Model: "route/claude-opus-5-5", HaikuModel: "small", OpusModel: "opus"}
	none := proto.ManagedLauncher{Name: "none", BaseURL: gw, Model: "opus"}
	marked := proto.ManagedLauncher{Name: "marked", BaseURL: gw, Model: "m1", OpusModel: "route/claude-opus-4-6",
		Models: []proto.ProviderModel{{ID: "route/claude-opus-4-6", ContextWindow: 1000000}}}
	anthropic := proto.ManagedLauncher{Name: "a", Kind: "api", APIKey: "sk-test-k"}
	codex := proto.ManagedLauncher{Name: "cx", Agent: "codex", BaseURL: gw}
	for _, tc := range []struct {
		l           proto.ManagedLauncher
		model, want string
	}{
		{withTiers, "opus", "route/claude-opus-5-5"},
		{withTiers, "opus[1m]", "route/claude-opus-5-5[1m]"}, // the 1M window asked for stays (Claude Code's own resolution)
		{withTiers, "opusplan[1m]", "route/claude-opus-5-5[1m]"},
		{marked, "opus[1m]", "route/claude-opus-4-6[1m]"}, // a tier already marked by its declared window: once
		{marked, "opus", "route/claude-opus-4-6[1m]"},
		{withTiers, "best", "route/claude-opus-5-5"},
		{withTiers, "opusplan", "route/claude-opus-5-5"},
		{withTiers, "haiku", "small"},
		{withTiers, "Sonnet", "route/claude-opus-5-5"},
		{withTiers, "default", ""},
		{withTiers, "inherit", ""},
		{withTiers, "small", "small"},
		{withTiers, "", ""},
		{none, "opus", ""},
		{none, "fable", ""},
		{anthropic, "opus", "opus"},
		{codex, "opus", "opus"},
	} {
		if got := providerModelArg(tc.l, tc.model); got != tc.want {
			t.Errorf("%s: providerModelArg(%q) = %q, want %q", tc.l.Name, tc.model, got, tc.want)
		}
	}
	n := managedNode(withTiers)
	if got := n.claudeModelArg("gw", "opus"); got != "route/claude-opus-5-5" {
		t.Errorf("claudeModelArg(gw, opus) = %q", got)
	}
	if got := n.claudeModelArg("claude", "opus"); got != "opus" {
		t.Errorf("plain claude: %q", got)
	}

	warned := []string{}
	warn := func(m string) { warned = append(warned, m) }
	env := managedEnv(withTiers)
	if got := gatewayModelArgs([]string{"-p", "--model", "haiku", "--model=opus", "--", "--model", "opus"}, env, warn); !slices.Equal(got,
		[]string{"-p", "--model", "small", "--model=route/claude-opus-5-5", "--", "--model", "opus"}) || len(warned) != 0 {
		t.Errorf("mapped: %q, warned %q", got, warned)
	}
	if got := gatewayModelArgs([]string{"--model", "opus[1m]", "--model=opusplan[1m]"}, env, warn); !slices.Equal(got,
		[]string{"--model", "route/claude-opus-5-5[1m]", "--model=route/claude-opus-5-5[1m]"}) || len(warned) != 0 {
		t.Errorf("[1m] mapped: %q, warned %q", got, warned)
	}
	if got := gatewayModelArgs([]string{"--resume", "x", "--model", "opus", "-p"}, managedEnv(none), warn); !slices.Equal(got,
		[]string{"--resume", "x", "-p"}) || len(warned) != 1 || !strings.Contains(warned[0], "opus") {
		t.Errorf("dropped: %q, warned %q", got, warned)
	}
	if args := []string{"--model", "opus"}; !slices.Equal(gatewayModelArgs(args, managedEnv(anthropic), warn), args) ||
		!slices.Equal(gatewayModelArgs(args, nil, warn), args) {
		t.Error("Anthropic's own API, or an older node's answer without a Base URL, keeps the alias")
	}
}

// providerAliasModel, the rule providerEnv maps a tier alias asked for by: the provider's field for that tier when it
// names a model, else its default model; an alias in a field counts as empty.
func TestProviderAliasModel(t *testing.T) {
	l := proto.ManagedLauncher{Model: "m1", OpusModel: "o1", SonnetModel: "sonnet", HaikuModel: "", SmallModel: "s1", FableModel: "f1"}
	want := map[string]string{"opus": "o1", "best": "o1", "opusplan": "o1", "sonnet": "m1", "haiku": "s1", "fable": "f1",
		"default": "m1", "inherit": "m1", "m9": "m9", "": "", "opus[1m]": "o1[1m]", "sonnet[1m]": "m1[1m]", "m9[1m]": "m9[1m]"}
	got := map[string]string{}
	for m := range want {
		got[m] = providerAliasModel(l, m)
	}
	if !maps.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := providerAliasModel(proto.ManagedLauncher{Model: "opus"}, "sonnet"); got != "" {
		t.Fatalf("nothing concrete: %q", got)
	}
	if got := providerAliasModel(proto.ManagedLauncher{OpusModel: "o1[1m]"}, "opus[1m]"); got != "o1[1m]" {
		t.Fatalf("a field marked already: %q", got)
	}
	// The conversation's model as the process runs it: opus[1m] on a gateway keeps its 1M window though the
	// provider declares none for the model.
	l2 := proto.ManagedLauncher{Name: "gw", BaseURL: "https://gw.example.com", Model: "m1", OpusModel: "route/claude-opus-4-6"}
	if env := providerEnv(l2, "opus[1m]", "", false); env["ANTHROPIC_MODEL"] != "route/claude-opus-4-6[1m]" {
		t.Fatalf("ANTHROPIC_MODEL=%q", env["ANTHROPIC_MODEL"])
	}
}
