package node

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

var modelProbeCache = struct {
	sync.Mutex
	results map[string]modelProbeResult
}{results: map[string]modelProbeResult{}}

type modelProbeResult struct {
	at    time.Time
	agent string
	err   error
}

// launcherModelCheck verifies argument forwarding without contacting a model API.
// A fixed --model flag must not silently win over the user's purpose-specific choice.
func (n *Node) launcherModelCheck(ctx context.Context, name, model string) (string, error) {
	if err := (modelExecution{Model: model}).check(name); err != nil {
		return "", err
	}
	if m, managed := n.managedLauncher(name); managed {
		return m.AgentType(), nil
	}
	if _, ok := n.codexLauncher(ctx, name); ok {
		return "codex", nil
	}
	if res, ok := n.runtimeLauncherModels(name); ok {
		return res.Agent, nil
	}
	key := n.home + "\x00" + name + "\x00" + model
	modelProbeCache.Lock()
	cached, ok := modelProbeCache.results[key]
	modelProbeCache.Unlock()
	if ok && time.Since(cached.at) < 10*time.Minute {
		return cached.agent, cached.err
	}
	probe, err := n.launcherProbe(ctx, name, "--model", model)
	agent := probe.Agent
	switch {
	case errors.Is(err, errProbeUnseen):
		// What it forwards cannot be seen: start it as before the check, the model given as --model, rather than refuse
		// every send and start through it (the assistant's own launcher: nothing could be sent at all).
		agent, err = launcherAgentType(name), nil
	case err == nil:
		err = forwardedModel(probe, model)
	}
	modelProbeCache.Lock()
	if len(modelProbeCache.results) > 256 {
		modelProbeCache.results = map[string]modelProbeResult{}
	}
	modelProbeCache.results[key] = modelProbeResult{at: time.Now(), agent: agent, err: err}
	modelProbeCache.Unlock()
	return agent, err
}

func forwardedModel(probe Probe, model string) error {
	if probe.Agent == "codex" {
		return nil
	} // app-server's thread/turn fields override the launcher's CLI default.
	matches := 0
	for i, arg := range probe.Args {
		if arg == "--model" || arg == "-m" {
			if i+1 < len(probe.Args) && probe.Args[i+1] == model {
				matches++
			} else {
				return errors.New("启动器固定了模型参数，不能覆盖具体模型")
			}
		}
		if strings.HasPrefix(arg, "--model=") {
			if strings.TrimPrefix(arg, "--model=") == model {
				matches++
			} else {
				return errors.New("启动器固定了模型参数，不能覆盖具体模型")
			}
		}
	}
	// Every model flag names the model asked for: a wrapper that pins that model and also forwards its arguments
	// passes it twice, and claude runs it either way (#6205). None at all is a launcher that dropped it.
	if matches == 0 {
		return errors.New("启动器没有可靠转发模型参数，请在模型供应商中修正它")
	}
	return nil
}

// withoutModelArgs leaves one unambiguous model flag: the purpose-specific caller's.
func withoutModelArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" { // prompt words after the separator are not the launcher's model options
			out = append(out, args[i:]...)
			break
		}
		if arg == "--model" || arg == "-m" {
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "--model=") {
			continue
		}
		out = append(out, arg)
	}
	return out
}
