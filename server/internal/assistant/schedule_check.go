package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

type routineCheck struct {
	proto.RoutineCheckParams
	OnError string `json:"on_error,omitempty"`
}

func parseRoutineCheck(raw json.RawMessage) (*routineCheck, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	check := &routineCheck{RoutineCheckParams: proto.RoutineCheckParams{Timeout: proto.RoutineCheckDefaultTimeout}}
	if err := json.Unmarshal(raw, check); err != nil {
		return nil, err
	}
	if strings.TrimSpace(check.Command) == "" || len(check.Command) > proto.RoutineCheckMaxCommand ||
		strings.ContainsRune(check.Command, 0) {
		return nil, errors.New("run_if.command must be non-empty and at most 4096 bytes")
	}
	if check.Timeout < 1 || check.Timeout > proto.RoutineCheckMaxTimeout {
		return nil, fmt.Errorf("run_if.timeout_s must be between 1 and %d", proto.RoutineCheckMaxTimeout)
	}
	switch check.OnError {
	case "", "run", "skip":
		return check, nil
	default:
		return nil, errors.New("run_if.on_error must be run or skip")
	}
}

func (s *Service) approveRoutineCheck(ctx context.Context, machine, self string, check *routineCheck) (bool, error) {
	now := time.Now().UTC()
	input, err := json.Marshal(check)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, settingCardWait)
	defer cancel()
	reply, err := s.ask(ctx, proto.Interaction{
		ID: "ix_" + randomHex(6), MachineID: machine, SessionID: self, Kind: proto.IxPermission,
		ToolName: "schedule_routine", ToolInput: input, SessionTitle: "Persona",
		Summary:   i18n.T(s.lang(ctx), "assistant-schedule.run_if_approval"),
		CreatedAt: now, ExpiresAt: now.Add(settingCardWait),
	})
	return reply.UserAllowed(), err
}

// checkRoutine returns the log entry and whether the scheduled run should be skipped.
func (s *Service) checkRoutine(ctx context.Context, t Task) (string, bool) {
	machine := t.ExecutionMachineID
	if machine == "" {
		machine = t.RunMachineID
	}
	if machine == "" {
		machine = s.settings(ctx).MachineID
		if t.SessionID != "" {
			machine = t.MachineID
		}
	}
	l := s.lang(ctx)
	cctx, cancel := context.WithTimeout(ctx, time.Duration(t.RunIf.Timeout+3)*time.Second)
	defer cancel()
	result := proto.RoutineCheckResult{ExitCode: -1}
	call := s.call
	if s.devs != nil {
		call = s.nodeCall
	}
	err := call(cctx, machine, proto.MethodRoutineCheck, t.RunIf.RoutineCheckParams, &result)
	if err == nil {
		switch result.ExitCode {
		case 0:
			return "", false
		case 1:
			if result.Reason != "" {
				return result.Reason, true
			}
			return i18n.T(l, "assistant-schedule.run_if_skipped"), true
		default:
			err = fmt.Errorf("exit %d", result.ExitCode)
			if result.Reason != "" {
				err = fmt.Errorf("%w: %s", err, result.Reason)
			}
		}
	}
	return i18n.T(l, "assistant-schedule.run_if_failed", i18n.Args{"error": err.Error()}), t.RunIf.OnError == "skip"
}
