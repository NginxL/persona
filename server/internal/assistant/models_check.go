package assistant

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/textfmt"
)

const launcherCheckTimeout = 100 * time.Second

var errNoLauncherCheckMachine = errors.New("no machine to test on")

// checkModelProvider is Model Settings' existing "Test connection": one bounded real request for a direct provider,
// or the node launcher check on the requested (otherwise the assistant's) computer. It never changes a model binding.
// A direct provider's request failure is returned in check, as the HTTP route has always reported it.
func (s *Service) checkModelProvider(ctx context.Context, provider ModelProvider, machine, purpose string) (string, proto.LauncherCheck, error) {
	name := strings.TrimPrefix(provider.ID, "launcher.")
	if provider.Kind == "api" {
		started := time.Now()
		config, err := s.apiConfig(ctx, ModelBinding{Provider: provider.ID, Model: provider.Model})
		answer := ""
		if err == nil {
			answer, err = s.aiClient(config).Complete(ctx, "", prompts.RenderLauncherCheck(), 128)
		}
		result := proto.LauncherCheck{Agent: "api", Model: config.Model, Answer: textfmt.Clip(textfmt.OneLine(answer), 200), OK: err == nil,
			Millis: int(time.Since(started).Milliseconds())}
		if err != nil {
			message := err.Error()
			if explanation := directCallFailureExplanation(s.requestCtxLang(ctx), err); explanation != "" {
				message = explanation + ": " + message
			}
			result.Error = textfmt.Clip(textfmt.OneLine(message), 400)
		}
		return "", result, nil
	}
	if proto.IsExternalLauncher(name) {
		return "", proto.LauncherCheck{}, i18n.Err("external-adapter.scope_required")
	}
	if _, ok := s.device(ctx, machine); !ok {
		machine = s.settings(ctx).MachineID
	}
	if _, ok := s.device(ctx, machine); !ok {
		return "", proto.LauncherCheck{}, errNoLauncherCheckMachine
	}
	if err := s.launcherScopeErr(ctx, machine, name); err != nil {
		return "", proto.LauncherCheck{}, err
	}
	observed, _ := s.launcherHealth(ctx, machine, name)
	var res proto.LauncherCheck
	params := map[string]any{"name": name}
	if purpose == "assistant" {
		params["purpose"] = purpose
	}
	if err := s.call(ctx, machine, assistantwire.DeviceLaunchersCheck, params, &res); err != nil {
		return "", proto.LauncherCheck{}, err
	}
	// A node from before the cleanup still answers with the terminal's escapes and startup noise in it.
	cleanCheck(&res)
	if res.OK {
		s.recordTestPassed(ctx, machine, name, res, observed)
	}
	return machine, res, nil
}
