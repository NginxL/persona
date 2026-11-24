package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/proto"
)

// Equal engine kinds do not prove that a runtime can resume another connection's
// transcript. Ordinary model picks never grant permission to create a new segment.
func (s *Service) logicalAdapterPick(ctx context.Context, machine string, row proto.Session, launcher string) error {
	if row.Launcher == launcher || proto.IsExternalLauncher(row.Launcher) || proto.IsExternalLauncher(launcher) {
		return nil // External picks retain their separate, scoped-main binding path.
	}
	runtime := row.SourceKind == proto.SourceRuntime
	if !runtime {
		list, err := s.pickerLaunchers(ctx, machine)
		if err != nil {
			return err
		}
		for _, l := range list {
			if l.Name == launcher {
				runtime = runtimeSource(l.Source)
				break
			}
		}
	}
	if !runtime {
		return nil
	}
	source := proto.AssistantExecutionIdentity(sessionEngineAgent(row), row.SourceKind, row.SourceInstanceID, row.Launcher)
	if source == "" {
		return logicalRefusal(ctx, "unknown_engine")
	}
	target, err := s.logicalTarget(ctx, machine, launcher)
	if err != nil {
		return err
	}
	if target.Engine != source {
		return logicalRefusal(ctx, "explicit_switch_required")
	}
	return nil
}
