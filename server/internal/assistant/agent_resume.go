package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The assistant's resume of a stopped agent (#8099). A stopped agent is read-only (node stopped.go, #4754): no message
// wakes it, and Persona's own follow-ups (rework, relayed answers, digests) are still never delivered to it. What the
// assistant may do is resume it on purpose, as the user's Resume on its terminal page does, when the next work needs
// that agent's context (it has just found the cause and should now fix it, rework, the user asked for "the one with
// the context"): agent_send with resume true and resume_why. The resume is
//
//   - the same node call as the user's Resume: session.start with resume_session_id and a node-side recovery guard
//     (node node.go fillResume: the session's own launcher, directory, model and permission mode; the shell it left
//     when that is still there, else a new tab), the text as its first message;
//   - never unsupervised: the agent is on an open task of this conversation already, or the call gives task (main
//     conversation), which makes it one as agent_send with a task does, the card marked resumed (wakeResumed);
//   - visible: the phone's confirmation and the task's card say which agent was resumed and why (resume_note);
//   - refused for user/unknown stops, offline computers, nearly full context, or the node's own start refusals.

// resumeContextFull is the share of its model's window an agent's context may fill for the assistant to resume it:
// above it the resumed agent would compact or run out at once, so a new agent with a summary goes on instead.
const resumeContextFull = 0.85

// resumeRequest is what agent_send's resume asks for: why its context is needed.
type resumeRequest struct {
	why string
}

// stoppedRefusal is agent_send's answer for a stopped agent without resume: nothing was sent, and how to choose.
func stoppedRefusal(c proto.Session) error {
	return fmt.Errorf("%s has stopped: its conversation and terminal stay, and a plain message does not start it again. "+
		"Nothing was sent. Choose how to go on: when the next work depends on what this agent already knows (it has just "+
		"investigated this and should now fix it, rework of its own work, the user asked for the agent with the context), "+
		"send again with resume true and resume_why, which resumes this same session with its context on its own "+
		"launcher and permission mode (from the main conversation also give task, unless it is on an open task of yours). "+
		"For unrelated work, or when its context is nearly full or it went off track, start a replacement and give it what "+
		"it needs (read this one with agent_read): for its task, task_plan with continues (the task's todo id) and "+
		"continues_why, which keeps the task's todo and brief; for work on no task, agent_start in its directory. "+
		"When you cannot tell, ask the owner (ask_user, or task_review needs_user): reuse this agent, or start a new one", agentLabel(c))
}

// resumeAllowed checks a resume before anything is asked or started: a reason, the computer reachable, room left in
// the agent's context, and a task of self's it will be on (one it is on, or task, which the send's checks go on with).
func (s *Service) resumeAllowed(ctx context.Context, self string, am Device, c proto.Session, why string, task *sendTask) error {
	cause := proto.StopUnknown
	if c.Capabilities != nil {
		cause = c.Capabilities.StopCause
	}
	if c.Archived {
		// Archive snapshots may predate the stop or a later user close. Only the node's saved cause authorizes resume.
		var info struct {
			StopCause proto.StopCause `json:"stop_cause"`
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := s.call(cctx, am.ID, "session.info", map[string]any{"session_id": c.SessionID}, &info)
		cancel()
		if err != nil {
			return resumeFailed(c, am, fmt.Errorf("could not verify the node's stop cause: %w", err))
		}
		cause = info.StopCause
	}
	if !cause.AllowsSupervisorResume() {
		return fmt.Errorf("%s cannot be resumed by the assistant: stop cause is %s. User stops and unknown causes require "+
			"the user's Resume on its terminal page. Nothing was started or sent", agentLabel(c), cause)
	}
	if why == "" {
		return fmt.Errorf("resume_why is required: say why this agent's own context is needed (the user sees it). Nothing was started or sent")
	}
	if d, ok := s.device(ctx, am.ID); !ok || !d.Reachable {
		return resumeFailed(c, am, machineOffline(am.Name))
	}
	if contextFull(c) {
		return resumeFailed(c, am, fmt.Errorf("its context is nearly full (%d of %d tokens): resumed, it would have to compact or "+
			"run out at once", c.ContextTokens, c.ContextWindow))
	}
	if task != nil {
		return nil // sendTaskAllowed checks it next, as for any hand-off
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	t, ok := s.openTodoOfAgent(am.ID, c.SessionID)
	if !ok {
		return fmt.Errorf("%s is on no open task of yours: a resumed agent must be supervised. Give task (from the main "+
			"conversation) to make it your task as you resume it. Nothing was started or sent", agentLabel(c))
	}
	if err := othersTask(c, t, self); err != nil {
		return fmt.Errorf("%w; it is not yours to resume. Nothing was started or sent", err)
	}
	return nil
}

// contextFull: what the model saw in c's latest reply fills more than resumeContextFull of its window (both known).
func contextFull(c proto.Session) bool {
	return c.ContextWindow > 0 && c.ContextTokens > 0 && float64(c.ContextTokens) >= resumeContextFull*float64(c.ContextWindow)
}

// resumeFailed is a resume that did not happen, with why and what to do instead.
func resumeFailed(c proto.Session, am Device, err error) error {
	return fmt.Errorf("could not resume %s on %s: %w. Nothing was sent; start a replacement instead (read this one with "+
		"agent_read and give it what it needs): for its task, task_plan with continues (the task's todo id) and continues_why; "+
		"for work on no task, agent_start in its directory", agentLabel(c), am.Name, err)
}

// resumeParams are the node's session.start params for resuming c with text as its first message: the user's Resume
// (the node fills in what the session ran with) plus the prompt and an eligibility recheck on that node.
func resumeParams(c proto.Session, text string) map[string]any {
	return map[string]any{"resume_session_id": c.SessionID, "prompt": text, "resume_automatic": true}
}

// resumeAndSend resumes stopped agent c of am with params' text as its first message (resumeParams) and says so in
// conversation self: on task's card when the send made or found one, else on the open task of self's it is on.
// handed is what the receipt says of the attachments. The text is noted before it goes, as any agent_send's
// (noteAgentSend, #7539).
func (s *Service) resumeAndSend(ctx context.Context, self string, am Device, c proto.Session, params map[string]any, why, told, handed string,
	task *handedTask) (string, error) {
	text, _ := params["text"].(string)
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute) // a resume can take minutes
	defer cancel()
	sendNote, err := s.noteAgentSend(sctx, self, am.ID, c.SessionID, "", text) // more from its task's conversation reopens an accepted task (#6726, #7539)
	if err != nil {
		return "", task.sendFailed(sctx, s, err)
	}
	var res struct {
		Session proto.Session `json:"session"`
	}
	err = s.call(sctx, am.ID, DeviceSessionStart, resumeParams(c, text), &res)
	sendNote.sent(sctx, err) // an unknown answer counts for the agent's next turn: it may still come up and take it
	if err != nil {
		if startResultUnknown(err) { // it may still come up and take the message
			return "", task.sendFailed(sctx, s, err)
		}
		return "", task.sendFailed(sctx, s, resumeFailed(c, am, err))
	}
	s.logf("resume %s on %s for %s: %s", short(c.SessionID), am.ID, self, why)
	note := s.resumeNote(sctx, c, am, why)
	said := ""
	if task != nil {
		said = task.sent(sctx, s, told)
		s.noteResume(sctx, self, task.todo.ID, am.ID, c.SessionID, note)
		s.ensureSupervisor(sctx, task.todo.ID) // made or kept by adoptAgent; never left without a supervisor
	} else if t, ok := s.openTodoOfAgent(am.ID, c.SessionID); ok {
		s.ensureSupervisor(sctx, t.ID) // its task goes on with a supervisor, as a successor's does (tasks_successor.go)
		s.releaseHold(sctx, t.ID)      // its supervisor sent it on: follow-ups go again (decision.go)
		if _, err := s.appendFeed(sctx, self, feedRouted, t.ID, map[string]string{"text": told, "from": routedFromAssistant,
			"machine_id": am.ID, "session_id": c.SessionID}); err != nil {
			s.logf("task %s: routed feed: %v", t.ID, err)
		}
		s.noteResume(sctx, self, t.ID, am.ID, c.SessionID, note)
		said = fmt.Sprintf(" It works for your task %s (%q).", t.ID, t.Title)
	}
	return fmt.Sprintf("Resumed %s on %s (same session, with its context, on its own launcher and permission mode) and sent.",
		agentLabel(c), am.Name) + handed + said, nil
}

// resumeNote is the line the user reads about a resume: which agent, where, and why.
func (s *Service) resumeNote(ctx context.Context, c proto.Session, am Device, why string) string {
	return i18n.T(s.lang(ctx), "assistant-tasks.resume_note", i18n.Args{"agent": agentLabel(c), "machine": am.Name, "why": why})
}

// noteResume puts the resume's note on task id's card in conversation self, as Persona's, naming the run it resumed
// (machine, sid; #10465).
func (s *Service) noteResume(ctx context.Context, self, id, machine, sid, note string) {
	if _, err := s.appendFeed(ctx, self, feedRouted, id, map[string]string{"text": note, "from": routedFromAssistant,
		"machine_id": machine, "session_id": sid}); err != nil {
		s.logf("task %s: resume feed: %v", id, err)
	}
}

// cleanWhy is resume_why as kept: one line, clipped.
func cleanWhy(why string) string {
	return clip(oneLine(strings.TrimSpace(why)), 300)
}
