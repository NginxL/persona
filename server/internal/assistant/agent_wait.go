package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Stay below the transport's normal tool deadline. This budget observes an already
// dispatched input; expiring it never cancels, resends or reviews the task.
const agentWaitMost = 50 * time.Second

var agentWaitPoll = time.Second

func agentWaitDuration(args json.RawMessage) (time.Duration, error) {
	var a struct {
		Seconds int `json:"wait_seconds"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return 0, fmt.Errorf("wait_seconds: %w", err)
	}
	if a.Seconds < 0 {
		return 0, fmt.Errorf("wait_seconds must be a nonnegative integer")
	}
	// Clamp before multiplying to avoid integer overflow on untrusted input.
	return time.Duration(min(a.Seconds, int(agentWaitMost/time.Second))) * time.Second, nil
}

type agentWaitTarget func(context.Context) (string, string, error)

func fixedWaitTarget(machine, session string) agentWaitTarget {
	return func(context.Context) (string, string, error) { return machine, session, nil }
}

type agentWaitResult struct {
	State       string `json:"state"`
	AgentState  string `json:"agent_state,omitempty"`
	InputID     string `json:"input_id"`
	Machine     string `json:"machine,omitempty"`
	Session     string `json:"session_id,omitempty"`
	Link        string `json:"link,omitempty"`
	Reply       string `json:"reply,omitempty"`
	Question    string `json:"question,omitempty"`
	Interaction string `json:"interaction_id,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

func (s *Service) waitAgentReply(ctx context.Context, receipt, inputID string, wait time.Duration, target agentWaitTarget) string {
	if wait <= 0 || inputID == "" {
		return receipt
	}
	wctx, cancel := context.WithTimeout(ctx, min(wait, agentWaitMost))
	defer cancel()
	result := agentWaitResult{State: "timeout", InputID: inputID}
	loggedListErr := false
	decided := false // the loop ended on an outcome, not on the deadline or cancellation
	ticker := time.NewTicker(agentWaitPoll)
	defer ticker.Stop()
	for {
		if wctx.Err() != nil {
			break
		}
		machine, session, failure := target(wctx)
		if failure != nil {
			result.State, result.Detail, decided = "failed", failure.Error(), wctx.Err() == nil
			break
		}
		if machine != "" && session != "" {
			result.Machine, result.Session = machine, session
			result.Link = sessionLink(s.webBase(wctx), machine, session, false)
			var page struct {
				Items []proto.Item `json:"items"`
			}
			// Read bounded recent exchanges, without folding away the input correlator.
			// If an older input has fallen outside this window, report unknown/timeout,
			// never substitute the most recent reply.
			err := s.call(wctx, machine, DeviceSessionItems, map[string]any{"session_id": session, "turns": 10, "lazy_images": true}, &page)
			if err != nil {
				result.State, result.Detail = "unavailable", "The dispatched input was not resent; read the agent before trying again."
				decided = wctx.Err() == nil
				break
			}
			c, ok := s.session(wctx, machine, session)
			if ok {
				result.AgentState = c.Status
				if observed, found := agentWaitObservation(page.Items, inputID, c); found {
					observed.InputID, observed.Machine, observed.Session, observed.Link = inputID, machine, session, result.Link
					observed.AgentState = c.Status
					result, decided = observed, true
					break
				}
				// Only this input's transcript line can open the question path. A question
				// from the previous turn must not satisfy a send still in the runtime queue.
				if at, found := agentWaitInput(page.Items, inputID); found {
					var pending struct {
						Interactions []proto.Interaction `json:"interactions"`
					}
					if err := s.call(wctx, DeviceAll, DeviceInteractionsList, map[string]any{}, &pending); err == nil {
						for _, ix := range pending.Interactions {
							if ix.SessionID == session && (ix.MachineID == "" || ix.MachineID == machine) &&
								(ix.Kind == proto.IxQuestion || ix.Kind == proto.IxElicitation) && !ix.Private &&
								!at.TS.IsZero() && !ix.CreatedAt.Before(at.TS) {
								result.State, result.Question, result.Interaction = "question", questionSummary(ix), ix.ID
								if result.Question == "" {
									result.Question = ix.Summary
								}
								break
							}
						}
						if result.State == "question" {
							decided = true
							break
						}
					} else if !loggedListErr && wctx.Err() == nil {
						loggedListErr = true
						s.logf("assistant: agent_wait %s/%s: %s: %v", machine, session, DeviceInteractionsList, err)
					}
				}
			}
		}
		select {
		case <-wctx.Done():
		case <-ticker.C:
		}
	}
	switch {
	case decided: // an outcome found before the deadline stands even when the deadline has passed by now
	case ctx.Err() != nil:
		result.State = "cancelled"
	case wctx.Err() != nil:
		result.State = "timeout"
	}
	raw, _ := json.Marshal(result)
	return receipt + "\n\nInline observation (normal supervision and task_review still apply):\n" + string(raw)
}

func agentWaitInput(items []proto.Item, inputID string) (proto.Item, bool) {
	var input proto.Item
	for _, it := range items {
		if it.Role != "user" {
			continue
		}
		if it.ClientMsgID == inputID {
			input = it
		} else if it.Kind == proto.KindText && input.ID != "" {
			return proto.Item{}, false
		}
	}
	return input, input.ID != ""
}

// A cached idle snapshot alone is not completion: first find this exact input,
// then a reply after it and an idle snapshot strictly newer than that reply.
// A later independent input makes attribution ambiguous, so it cannot satisfy us.
func agentWaitObservation(items []proto.Item, inputID string, c proto.Session) (agentWaitResult, bool) {
	seen := false
	var reply proto.Item
	for _, it := range items {
		if !seen {
			if it.Role == "user" && it.ClientMsgID == inputID {
				seen = true
			}
			continue
		}
		if it.Role == "user" && it.Kind == proto.KindText && it.ClientMsgID != inputID {
			return agentWaitResult{}, false
		}
		if it.Role == "assistant" && it.Kind == proto.KindText && !it.Narration && strings.TrimSpace(it.Text) != "" {
			reply = it
		}
	}
	if !seen || reply.ID == "" || reply.TS.IsZero() || !c.UpdatedAt.After(reply.TS) {
		return agentWaitResult{}, false
	}
	switch c.Status {
	case proto.StatusIdle, proto.StatusDone:
		return agentWaitResult{State: "completed", Reply: clip(reply.Text, 40000)}, true
	case proto.StatusInterrupted, proto.StatusOffline:
		return agentWaitResult{State: "interrupted", Detail: "The agent stopped; its latest text is not confirmed as a completed reply."}, true
	}
	return agentWaitResult{}, false
}
