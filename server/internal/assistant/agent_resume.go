package assistant

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
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
//   - compacted before the text goes in when its context is nearly full (#11137): resumed without a first message,
//     then the text sent with compact_if_full, so the node runs the runtime's own /compact and sends once it is done
//     (node compact_before_send.go). Only a failed compaction, or a runtime Persona cannot compact, falls back to a
//     replacement, saying why;
//   - refused for user/unknown stops, offline computers, or the node's own start refusals.

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
		"A nearly full context is no reason to start over: the resume compacts it with the agent's own /compact before your "+
		"text goes in. For unrelated work, or when it went off track, start a replacement and give it what "+
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
	if contextFull(c) && !compactable(c) {
		return resumeFailed(c, am, fmt.Errorf("its context is nearly full (%d of %d tokens) and %s, so it cannot be compacted "+
			"before your text goes in", c.ContextTokens, c.ContextWindow, notCompactable(c)))
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

// contextFull: what the model saw in c's latest reply fills proto.ContextNearlyFullShare of its window (both known).
func contextFull(c proto.Session) bool {
	return proto.ContextNearlyFull(int(c.ContextTokens), int(c.ContextWindow))
}

// compactable: c runs on a runtime whose own compaction the node can run before a message (#11137): Claude Code in one
// of Persona's terminals, and Codex wherever Persona reaches it to resume or send: its terminal, a thread nothing runs
// that the node resumes there with codex resume (codex_history), or its app-server (codex_app_server). Not the Codex
// App's chats, a Claude outside Persona's terminals (claude_bg, claude_desktop, claude_cli: its inbox runs no command)
// or a runtime's session (Paseo and the like).
func compactable(c proto.Session) bool {
	switch c.Agent {
	case "", "claude":
		return c.SourceKind == ""
	case "codex":
		return c.SpaceKind != codexAppSpace && (c.SourceKind == "" || c.SourceKind == "codex_history" || c.SourceKind == "codex_app_server")
	}
	return false
}

// codexAppSpace is the space_kind of the Codex App's chats (node codex_desktop.go).
const codexAppSpace = "codex_app"

// notCompactable says why c cannot be compacted from Persona, and what then.
func notCompactable(c proto.Session) string {
	switch {
	case c.Agent == "codex" && c.SpaceKind == codexAppSpace:
		return "it is a Codex App chat, which takes no /compact from Persona (the App compacts on its own)"
	case c.SourceKind == proto.SourceClaudeBg || c.SourceKind == proto.SourceClaudeDesktop || c.SourceKind == proto.SourceClaudeTerminal:
		return "it is a Claude outside Persona's terminals, whose inbox runs no /compact"
	case c.SourceKind != "" && (c.Agent == "" || c.Agent == "claude" || c.Agent == "codex"):
		return "it is a " + c.SourceKind + " session, which Persona cannot compact"
	case c.SourceKind != "":
		return "it runs in " + c.SourceKind + ", which has no compaction Persona can run"
	}
	return c.Agent + " has no compaction Persona can run"
}

// compactFailed is a send whose compaction of a nearly full context failed (the node's compact_failed): nothing was
// sent; what to do instead. resumed: the agent was resumed for it and now rests with its context.
func compactFailed(c proto.Session, am Device, err error, resumed bool) error {
	state := "It keeps its context as it was"
	if resumed {
		state = "It was resumed and now rests with its context as it was"
	}
	return fmt.Errorf("could not compact %s on %s before your text: %w. %s; nothing was sent. Send again to try once "+
		"more, or, if it cannot be compacted, start a replacement (read this one with agent_read and give it what it "+
		"needs): for its task, task_plan with continues (the task's todo id) and continues_why; for work on no task, "+
		"agent_start in its directory", agentLabel(c), am.Name, err, state)
}

// compactSendWait bounds a send that may compact first: a compaction takes minutes (node compactBeforeSendDeadline).
const compactSendWait = 15 * time.Minute

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
	wait := 5 * time.Minute // a resume can take minutes
	if contextFull(c) {
		wait += compactSendWait // and then a compaction (#11137)
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wait)
	defer cancel()
	sendNote, err := s.noteAgentSend(sctx, self, am.ID, c.SessionID, "", text) // more from its task's conversation reopens an accepted task (#6726, #7539)
	if err != nil {
		return "", task.sendFailed(sctx, s, err)
	}
	var res struct {
		Session proto.Session `json:"session"`
	}
	full := contextFull(c) // resumeAllowed let only a compactable one through
	startParams := resumeParams(c, text)
	if full {
		// Resumed without a first message, then the text sent with compact_if_full: the node compacts it with its own
		// /compact first and sends once that is done (#11137).
		delete(startParams, "prompt")
	} else if id, ok := params["client_msg_id"]; ok {
		startParams["client_msg_id"] = id
	}
	err = s.call(sctx, am.ID, DeviceSessionStart, startParams, &res)
	if err != nil {
		if full && sendUnknown(err) {
			// This start carried no prompt. Keep supervision of the possibly resumed agent, but no turn is
			// attributed to text that the second call never sent.
			sendNote.sent(sctx, rpc.NotSent(err))
			if task != nil && task.made {
				s.taskCard(sctx, task.self, task.todo.ID, task.wake)
			}
			return "", fmt.Errorf("%w (the agent may have resumed without a first message; check with agent_read before resuming again)", err)
		}
		sendNote.sent(sctx, err)
		if sendUnknown(err) {
			return "", task.sendFailed(sctx, s, err)
		}
		return "", task.sendFailed(sctx, s, resumeFailed(c, am, err))
	}
	compacted := ""
	if full {
		send := maps.Clone(params)
		send["compact_if_full"] = true
		var sent struct {
			Compacted bool `json:"compacted"`
		}
		err = s.call(sctx, am.ID, DeviceSessionSend, send, &sent)
		sendNote.sent(sctx, err)
		if err != nil {
			if sendUnknown(err) {
				return "", task.sendFailed(sctx, s, err)
			}
			if errCode(err) == proto.CodeCompactFailed {
				return "", task.sendFailed(sctx, s, compactFailed(c, am, err, true))
			}
			return "", task.sendFailed(sctx, s, fmt.Errorf("resumed %s on %s, but the text did not go in: %w", agentLabel(c), am.Name, err))
		}
		s.logf("resume %s on %s: context %d of %d, compacted before the text: %v", short(c.SessionID), am.ID, c.ContextTokens, c.ContextWindow, sent.Compacted)
		if sent.Compacted {
			compacted = fmt.Sprintf(" Its context was nearly full (%d of %d tokens), so it compacted it with its own /compact first.", c.ContextTokens, c.ContextWindow)
		}
	} else {
		sendNote.sent(sctx, err)
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
		agentLabel(c), am.Name) + compacted + handed + said, nil
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
