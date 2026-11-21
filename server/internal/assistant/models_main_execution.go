package assistant

import "context"

// An inherited default is not permission to resume another adapter's transcript.
// Explicit segment choices have already returned from logicalSendModel.
func (s *Service) mainDefaultExecution(ctx context.Context, machine, session string, params map[string]any) error {
	record, err := s.modelRecord(ctx, purposeMain)
	if err != nil {
		return err
	}
	if record.Binding == (ModelBinding{}) {
		return nil
	}
	launcher, _ := params["launcher"].(string)
	if launcher == "" {
		return nil
	}
	source, known := s.session(ctx, machine, session)
	if !known {
		return logicalRefusal(ctx, "unavailable")
	}
	current := assistantKind(source.Agent)
	target := assistantKind(s.launcherAgent(ctx, machine, launcher))
	if current != "" && target != "" && current != target {
		return logicalRefusal(ctx, "explicit_switch_required")
	}
	return s.logicalAdapterPick(ctx, machine, source, launcher)
}
