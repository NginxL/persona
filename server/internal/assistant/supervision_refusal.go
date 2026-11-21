package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func supervisionRefusalCode(code string) bool {
	return code == assistantwire.CodeSupervisionRuntime || code == assistantwire.CodeSupervisionAccount
}

func (s *Service) supervisionLauncherNames(ctx context.Context, machine string) []string {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	picker, err := s.pickerLaunchers(cctx, machine)
	names := []string{}
	if err == nil {
		fits, fitErr := s.assistantLaunchersOf(cctx, machine, picker)
		if fitErr == nil {
			list := make([]proto.Launcher, 0, len(fits))
			for _, l := range fits {
				list = append(list, l.Launcher)
			}
			names = proto.SupervisionLauncherNames(list)
		}
	}
	return names
}

func supervisionNamesArgs(lang i18n.Lang, names []string) i18n.Args {
	label := strings.Join(names, ", ")
	if label == "" {
		label = i18n.T(lang, "rpc-errors.assistant_supervision_none")
	}
	return i18n.Args{"launchers": label}
}

func (s *Service) supervisionLauncherArgs(ctx context.Context, machine string, lang i18n.Lang) i18n.Args {
	return supervisionNamesArgs(lang, s.supervisionLauncherNames(ctx, machine))
}

func (s *Service) supervisionRuntimeRefusal(ctx context.Context, machine string) error {
	names := s.supervisionLauncherNames(ctx, machine)
	args := supervisionNamesArgs(i18n.From(ctx), names)
	data, _ := json.Marshal(map[string]any{"delivered": "none", "launchers": names})
	return &rpc.Error{Code: assistantwire.CodeSupervisionRuntime,
		Message: i18n.T(i18n.From(ctx), "rpc-errors.assistant_supervision_runtime", args), Data: data}
}

// A supervision selector keeps unsupported connections visible with a reason;
// filtering never grants permission to execute or starts a provider to probe it.
func (s *Service) supervisionPickerLaunchers(list []ConfiguredLauncher, unavailable []unavailableLauncher, lang i18n.Lang) ([]ConfiguredLauncher, []unavailableLauncher) {
	inventory := make([]proto.Launcher, 0, len(list))
	for _, l := range list {
		inventory = append(inventory, l.Launcher)
	}
	args := supervisionNamesArgs(lang, proto.SupervisionLauncherNames(inventory))
	fits := []ConfiguredLauncher{}
	for _, l := range list {
		if proto.LauncherSupervisionPolicy(l.Launcher) {
			fits = append(fits, l)
			continue
		}
		unavailable = append(unavailable, unavailableLauncher{Name: l.Name, DisplayName: l.DisplayName, Kind: "launcher", Agent: l.Agent, Code: assistantwire.CodeSupervisionRuntime, Reason: i18n.T(lang, "rpc-errors.assistant_supervision_runtime", args)})
	}
	return fits, unavailable
}
