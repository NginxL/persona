package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Agents are told the closeout's form before they work (docs/assistant/agent-work.md "Closeout"), so a turn that did
// work ends with one without the held stop and the extra turn it costs: after a task's prompt (any runtime, Codex
// included), and as the starting context of a session that has Persona's hooks (node/closeout_start.go). Both only
// under the triage's main switch and its closeout switch.

// closeoutBrief is what follows a task's prompt: the closeout's form and when to write it; "" while the switches are
// off. afterDelivery: the same block also carries delivery disposition and responsibility.
func (s *Service) closeoutBrief(ctx context.Context, afterDelivery bool) string {
	if sw := s.triageSwitches(ctx); !sw.Enabled || !sw.Closeout {
		return ""
	}
	return prompts.RenderAgentCloseoutBrief(prompts.AgentCloseoutBrief{Form: closeout.Example,
		Language: answerLanguage(s.hello(ctx).Locale), AfterDelivery: afterDelivery})
}

// withCloseoutBrief is prompt followed by the closeout's form, or prompt as it is while the switches are off.
func (s *Service) withCloseoutBrief(ctx context.Context, prompt string, afterDelivery bool) string {
	if brief := s.closeoutBrief(ctx, afterDelivery); brief != "" {
		return prompt + "\n\n" + brief
	}
	return prompt
}

// closeoutStart answers a node whose agent's session is starting: the form as starting context, or none. Never for a
// review's reviewer: by its session once it is linked, and by the review's own worktree before that (the session
// starts before the start that made it answers). The node leaves out assistant conversations, subagents and the
// sessions that only report; the manager is not asked about the session itself, which it may not know yet.
func (s *Service) closeoutStart(ctx context.Context, machine string, p assistantwire.CloseoutBriefRequest) assistantwire.CloseoutBriefResponse {
	var out assistantwire.CloseoutBriefResponse
	if sw := s.triageSwitches(ctx); !sw.Enabled || !sw.Closeout {
		return out
	}
	if p.SessionID == "" || s.isReviewer(machine, p.SessionID) || s.reviewWorktree(machine, p.Cwd) {
		return out
	}
	out.Context = prompts.RenderAgentCloseoutBrief(prompts.AgentCloseoutBrief{Form: closeout.Example,
		Language: answerLanguage(s.hello(ctx).Locale), Session: true})
	return out
}

// reviewWorktree: cwd on machine is the copy a running review's reviewer works in (supervise_autoreview.go).
func (s *Service) reviewWorktree(machine, cwd string) bool {
	if cwd == "" {
		return false
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for _, t := range s.todos.todos {
		if t.Supervision == nil {
			continue
		}
		if a := t.Supervision.AutoReview; a != nil && a.State == autoRunning && a.MachineID == machine && a.Worktree == cwd {
			return true
		}
	}
	return false
}

// closeoutLine is the list line of a turn that has a closeout and no phase: one nobody can call finished
// (docs/assistant/agent-work.md "Closeout"). A claim the hub disproved comes first, naming the claim; else what the
// agent says is missing (else what it waits for, else what it did), behind "Not finished". In lang, the user's.
func closeoutLine(c proto.Closeout, lang i18n.Lang) string {
	if k := c.Checked; k != nil {
		if c.Merged == proto.CloseoutYes && k.Merged == proto.CloseoutNo {
			return i18n.T(lang, "assistant-agents_triage.closeout_not_merged")
		}
		if c.Deployed == proto.CloseoutYes && k.Deployed == proto.CloseoutNo {
			return i18n.T(lang, "assistant-agents_triage.closeout_not_deployed")
		}
	}
	text := cmpOr(c.Missing, c.Waiting)
	if text == "" && len(c.Did) > 0 {
		text = c.Did[0]
	}
	if text = oneLine(text); text == "" {
		return i18n.T(lang, "assistant-agents_triage.closeout_not_finished")
	}
	return clip(i18n.T(lang, "assistant-agents_triage.closeout_not_finished_text", i18n.Args{"text": text}), 60)
}

// unfinished gives a work state that has a closeout and no phase its list line; any other state is returned as it is.
func unfinished(w *proto.WorkState, lang i18n.Lang) *proto.WorkState {
	if w != nil && w.Phase == "" && w.Closeout != nil {
		w.Line = closeoutLine(*w.Closeout, lang)
	}
	return w
}

// workShown: a stored work state the lists are told of. One with a phase, and one without that has a closeout: the
// turn stays Unread (fleet_work.go workState goes by the phase) and its row says why nobody calls it finished. An
// empty phase is what every client already draws as a plain line, where a new phase value would be unknown to them.
func workShown(w *proto.WorkState) bool {
	return w.Phase != "" || w.Closeout != nil
}

// snapshotCloseout is the tail of an agent's line in the live snapshot: what its closeout claims of merging and
// deploying, and what the hub found where that differs. nil when there is nothing to say.
func snapshotCloseout(w *proto.WorkState) *prompts.SnapshotAgentCloseout {
	if w == nil || w.Closeout == nil {
		return nil
	}
	c := w.Closeout
	out := prompts.SnapshotAgentCloseout{Merged: c.Merged, Deployed: c.Deployed}
	if k := c.Checked; k != nil {
		if k.Merged != "" && k.Merged != c.Merged {
			out.FoundMerged = k.Merged
		}
		if k.Deployed != "" && k.Deployed != c.Deployed {
			out.FoundDeployed = k.Deployed
		}
	}
	if out == (prompts.SnapshotAgentCloseout{}) {
		return nil
	}
	return &out
}
