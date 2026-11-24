package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	defaultTaskExecutionWaitSeconds = 600
	maxTaskExecutionWaitSeconds     = 3600
	taskExecutionWaitPoll           = 2 * time.Second
)

type taskExecutionPolicy struct {
	wait           time.Duration
	legacyFallback bool
}

func taskExecutionPolicyOf(h Hello) taskExecutionPolicy {
	policy := taskExecutionPolicy{wait: defaultTaskExecutionWaitSeconds * time.Second, legacyFallback: true}
	if seconds := h.Assistant.TaskExecutionWaitSeconds; seconds != nil && *seconds >= 0 && *seconds <= maxTaskExecutionWaitSeconds {
		policy.wait = time.Duration(*seconds) * time.Second
	}
	if h.Assistant.TaskExecutionLegacyFallback != nil {
		policy.legacyFallback = *h.Assistant.TaskExecutionLegacyFallback
	}
	return policy
}

func nodeUpdateHandoffGraceOf(h Hello) time.Duration {
	if seconds := h.Assistant.NodeUpdateHandoffGraceSeconds; seconds != nil && *seconds >= 0 && *seconds <= proto.MaxNodeUpdateHandoffGraceSeconds {
		return time.Duration(*seconds) * time.Second
	}
	return time.Duration(proto.DefaultNodeUpdateHandoffGraceSeconds) * time.Second
}

// callDevice retries only the transport's explicit pre-delivery compatibility refusals. Everything before this point
// (model preparation, origin decisions and conversation bookkeeping) happens once, with the same request bytes.
func (s *Service) callDevice(ctx context.Context, call DeviceCall) (json.RawMessage, error) {
	once := func() (json.RawMessage, error) {
		var finish func(json.RawMessage)
		if devicewire.IsSessionStart(call.Method) {
			finish = s.beginPlacementBind(ctx, call.Device, call.Params)
		}
		res, ok, err := s.logicalCall(ctx, call.Device, call.Method, call.Params)
		if !ok {
			res, ok, err = s.localFleetDeviceCall(ctx, call)
		}
		if !ok {
			res, err = s.hub.DeviceCall(ctx, call)
		}
		if finish != nil {
			finish(res)
		}
		return res, err
	}
	if !devicewire.TaskExecutionWaitEnabled(ctx) || !taskExecutionRequest(call) {
		return once()
	}
	policy := taskExecutionPolicyOf(s.hello(ctx))
	deadline := time.Now().Add(policy.wait)
	for {
		res, err := once()
		if !taskExecutionCompatibilityRefusal(call, err) {
			return res, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, rpc.NotSent(fmt.Errorf("%w: %w", context.DeadlineExceeded, i18n.Err("assistant-schedule.task_execution_wait_timeout")))
		}
		timer := time.NewTimer(min(taskExecutionWaitPoll, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, rpc.NotSent(ctx.Err())
		case <-timer.C:
		}
	}
}

func taskExecutionRequest(call DeviceCall) bool {
	if call.Method == assistantwire.DeviceAssistantDirectory {
		return true
	}
	if call.Method != DeviceAssistantSend {
		return false
	}
	var fields map[string]json.RawMessage
	return json.Unmarshal(call.Params, &fields) == nil && string(fields["task_execution"]) == "true"
}

func taskExecutionCompatibilityRefusal(call DeviceCall, err error) bool {
	if !taskExecutionRequest(call) {
		return false
	}
	var coded *rpc.Error
	if !errors.As(err, &coded) {
		return false
	}
	if coded.Code == nodeproto.CodeRestarting {
		return true
	}
	if coded.Code != proto.CodeNodeOutdated {
		return false
	}
	if call.Method == assistantwire.DeviceAssistantDirectory {
		return true
	}
	var data struct {
		Capability string `json:"capability"`
	}
	if json.Unmarshal(coded.Data, &data) == nil && data.Capability == devicewire.TaskExecutionCapability {
		return true
	}
	// Compatibility with a manager that predates structured capability data, including the scheduler's isolated peers.
	return strings.Contains(coded.Message, devicewire.TaskExecutionCapability) || strings.Contains(coded.Message, "task execution")
}
