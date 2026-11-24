package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/nwverse/persona/server/internal/modelhttp"
)

const modelSourceEnv = "PERSONA_MODEL_SOURCE"

func sourceSession(source modelhttp.Source, id string) modelhttp.Source {
	if id != "" {
		source.Session = id
	}
	return source.Safe()
}

func assistantModelSource(e modelExecution) modelhttp.Source {
	if e.Source.Role != "" {
		return e.Source.Safe()
	}
	role := "main"
	switch {
	case e.IsMain:
	case e.Origin == originInbox:
		role = "background"
	case e.Strict:
		role = "routine"
	case e.Origin == "task":
		role = "background"
	}
	return modelhttp.Source{Role: role}
}

// sourceClaudeSettings writes only Persona's generated settings. The effective launcher's
// header value (settings before process environment) is merged instead of overwritten.
func (n *Node) sourceClaudeSettings(ctx context.Context, launcher, cwd, settings, profile string, source modelhttp.Source) (string, error) {
	if os.Getenv(modelhttp.SourceHostsEnv) == "" {
		return settings, nil
	}
	var probe Probe
	if managed, ok := n.managedLauncher(launcher); ok {
		probe = Probe{Env: providerEnv(managed, "", "", false), Args: managed.Args}
	} else {
		var err error
		probe, err = n.keptProbe(ctx, launcher)
		if err != nil {
			return settings, nil // no verified endpoint: do not guess from the launcher's name
		}
	}
	if dir := probe.Env["CLAUDE_CONFIG_DIR"]; profile == "" && dir != "" {
		profile = dir
	}
	if profile == "" {
		profile = n.primaryClaudeDir()
	}
	var own map[string]any
	if arg := settingsArg(probe.Args); arg != "" {
		var err error
		own, err = readLauncherSettings(arg, cwd, n.home)
		if err != nil {
			return settings, nil // a dynamic/missing settings file makes the endpoint unknown
		}
	}
	mine := map[string]any{}
	if settings != "" {
		var err error
		mine, err = readLauncherSettings(settings, cwd, n.home)
		if err != nil {
			return "", errors.New("cannot read generated model-source settings")
		}
	}
	merged := mergeClaudeSettings(own, mine)
	env := map[string]any{}
	for _, settings := range []map[string]any{own, mine} {
		if vars, ok := settings["env"].(map[string]any); ok {
			for key, value := range vars {
				env[key] = value
			}
		}
	}
	layers, shell := claudeEnvLayers(probe, settingsArg(probe.Args), profile, cwd, n.home)
	// Persona's final settings precede the launcher's; administrator policy still precedes ours.
	adminCount := len(claudeManagedEnvLayers())
	ours := map[string]string{}
	for key, value := range env {
		if text, ok := value.(string); ok {
			ours[key] = text
		}
	}
	layers = append(layers[:adminCount], append([]map[string]string{ours}, layers[adminCount:]...)...)
	get := func(key string) string {
		for _, layer := range layers {
			if value, ok := layer[key]; ok {
				return value
			}
		}
		return shell[key]
	}
	base, headers := get("ANTHROPIC_BASE_URL"), get("ANTHROPIC_CUSTOM_HEADERS")
	if !modelhttp.SourceAllowed(base) {
		return settings, nil
	}
	env["ANTHROPIC_CUSTOM_HEADERS"] = source.MergeClaudeForURL(headers, base)
	env["ANTHROPIC_BASE_URL"] = base
	merged["env"] = env
	raw, err := json.Marshal(merged)
	if err != nil {
		return "", errors.New("cannot encode generated model-source settings")
	}
	return writeSettings(n.dir, "model-source-"+filepath.Base(cwd)+"-"+newUUID()+".json", raw)
}
