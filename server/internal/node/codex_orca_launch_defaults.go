package node

import (
	"context"
	"slices"
	"strings"

	"github.com/anmitsu/go-shlex"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart"
)

// stockOrcaCodexArgv distinguishes the built-in stock launcher from a configured no-op alias
// with the same words. Discovery records alias provenance beside the shared launcher argv map.
func stockOrcaCodexArgv(launcher string, argv []string) bool {
	if launcher != "codex" || len(argv) != 1 || argv[0] != "codex" {
		return false
	}
	launcherCache.Lock()
	configured := launcherCache.codexAliases["codex"]
	if launcherCache.codexAliases == nil {
		// Test fixtures and a start before the first discovery carry no provenance map.
		_, configured = launcherCache.codex["codex"]
	}
	launcherCache.Unlock()
	return !configured
}

// codexOrcaLaunchDefaults follows Orca's stock Codex launch profile. Configured launchers and explicit
// Persona modes do not call this: their own complete launch configuration stays authoritative.
func codexOrcaLaunchDefaults(ctx context.Context, launch host.Launcher, argv []string) ([]string, error) {
	h, ok := launch.(*orcaHost)
	if !ok || len(argv) != 1 || argv[0] != "codex" {
		return argv, nil
	}
	var res struct {
		Settings *struct {
			Commands map[string]string   `json:"agentCmdOverrides"`
			Args     map[string]string   `json:"agentDefaultArgs"`
			Env      map[string][]string `json:"environmentNames"`
		} `json:"settings"`
	}
	if err := h.c.Call(ctx, orcart.CallTimeout, "settings.getAgentLaunch", nil, &res); err != nil || res.Settings == nil {
		// No fallback: an old or unavailable host cannot confirm which preferences it would use.
		return nil, refusal("codex_launch_preferences", "Could not read Orca's launch preferences. Update Orca or select an explicitly configured launcher.", nil)
	}
	settings := res.Settings
	if command := settings.Commands["codex"]; (command != "" && command != "codex") || len(settings.Env["codex"]) > 0 {
		return nil, refusal("codex_launch_preferences", "Orca's Codex launcher has a custom command or environment. Select that configured launcher in Persona.", nil)
	}
	raw := settings.Args["codex"]
	if strings.ContainsAny(raw, "\n\r") {
		return nil, refusal("codex_launch_preferences", "Orca launch arguments require a shell", nil)
	}
	if _, err := shlex.Split(raw, true); err != nil {
		return nil, refusal("codex_launch_preferences", "Orca launch arguments have incomplete quoting. Correct them before starting Codex.", nil)
	}
	args := shellWords(raw)
	// Execute literal argv, never evaluate shell operators, substitutions or variables.
	for _, arg := range args {
		if strings.ContainsAny(arg, "$`<>\x00~*?[") || slices.Contains([]string{";", "&", "&&", "|", "||", ">", "<"}, arg) {
			return nil, refusal("codex_launch_preferences", "Orca's launch arguments require a shell. Select an explicitly configured launcher.", nil)
		}
	}
	// A cached launcher's slice belongs to every later start; never append to it directly.
	return append(slices.Clone(argv), args...), nil
}
