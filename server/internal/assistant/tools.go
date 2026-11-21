package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Tools only the assistant (the "Chat" tab's headless Claude) gets: asking on the phone (headless claude has no
// AskUserQuestion), pushing, waking itself up later, its conversations, the tasks it hands out, the coding agents the
// hub already tracks, the knowledge base, the inbox's drafts, Feishu's /agent replies and Persona's own settings.
// The hub lists them with its own and sends their calls here (Handler.Tool). Those on the hub's knowledge base,
// interaction queue and inbox call those apps' data methods (tools_apps.go); one that is still an app's to run goes to
// that app (AppTool).

// toolNames are the tools only assistant conversations get; their descriptions and schemas are in prompts/mcp
// (docs/assistant/prompts.md).
var toolNames = []string{"ask_user", "notify_user", "supervision_finish", "schedule_followup", "schedule_routine", "schedule_state_set", "delete_routine", "list_followups",
	"cancel_followup", "list_places", "save_place", "machines_list", "machine_redeploy", "agents_list", "agent_read", "agent_send", "agent_switch", "agents_switch", "agent_interrupt", "agent_start", "agent_supervise", "agent_transfer", "agent_archive", "agent_unarchive", "agent_autoname", "agent_autoname_receipt", "interaction_answer", "ask_escalate", "inbox_suggest", "inbox_draft",
	"inbox_folders", "inbox_move", "inbox_post", "kb_search", "kb_list", "kb_get", "kb_update", "kb_history_plan", "kb_history_status", "kb_history_stop", "kb_history_resume", "persona_settings", "secret_run", "skill_import",
	"model_profiles_list", "model_profile_get", "model_profile_switch", "model_profile_rollback",
	"repositories_list", "repository_get", "repository_add", "view_show", "view_update", "view_get",
	"conversations_list", "conversation_read", "conversation_rename", "conversation_archive", "conversation_unarchive",
	"conversation_tags_set",
	"conversation_new", "conversation_send", "task_plan", "task_plan_withdraw", "task_review", "task_scope", "task_decided", "task_start_screen", "task_start_keys", "task_retry",
	"todo_workspace", "todo_repositories_get", "todo_repositories_set", "todo_repository_links_set", "todo_repository_links_add", "todo_list", "todo_get", "todo_create", "todo_update", "todo_complete", "todo_reopen", "todo_delete", "concern_report",
	"direction_name", "leader_progress"}

// Tools are the assistant's tools (assistant.tools).
func (s *Service) Tools(ctx context.Context) []proto.Tool {
	tools := toolsNamed(toolNames)
	// A bare Service has no settings store and is used by registry tests. With no
	// store the opt-in is false; do not call the nil store function just to hide it.
	if s.store != nil && ownerReplyApprovalOn(s.settings(ctx)) {
		tools = append(tools, toolsNamed([]string{ownerActionPrepareTool})...)
	}
	if s.urgentOn(ctx) {
		tools = append(tools, toolsNamed([]string{"urgent_call"})...)
	}
	return localizedMCPTools(append(tools, s.knowledgeFolderTools(ctx)...), s.toolLanguage(ctx))
}

// toolsNamed are the MCP tools of these names, as prompts/mcp has them.
func toolsNamed(names []string) []proto.Tool {
	tools := make([]proto.Tool, 0, len(names))
	for _, n := range names {
		t := prompts.Tool(n)
		tools = append(tools, proto.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return tools
}

// Tool runs one of the assistant's MCP tools for the conversation call.SessionID on machine call.MachineID.
func (s *Service) tool(ctx context.Context, call ToolCall) proto.ToolResult {
	if err := s.conversationAvailable(ctx, call.MachineID, call.SessionID); err != nil {
		return proto.TextResult(err.Error(), true)
	}
	call.Name = compat.Tool(call.Name)
	if err := s.guardRefusal(ctx, call.MachineID, call.SessionID, call.Name); err != nil {
		return proto.TextResult(err.Error(), true)
	}
	if res, refused := s.inboxModelRefusal(ctx, call.MachineID, call.SessionID, "", call.Name); refused {
		return res
	}
	args := call.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if res, refused := s.meetingRefuses(ctx, call.MachineID, call.SessionID, call.Name, true); refused {
		return res // a meeting's batch started this turn: only reading tools (meeting_turns.go)
	}
	switch call.Name {
	case "persona_settings":
		return s.toolPersonaSettings(ctx, call, args) // settings_tool.go
	case "task_plan", "agent_start", "conversation_new":
		// The optional dispatch policy is checked first: it can refuse a model's call where the task-agent refusal
		// below still lets a scheduled conversation through. The hub's own starts (an inbox hand-off) come through
		// runTool and never reach Tool, so they are not limited here.
		if err := s.dispatchToolGuard(ctx, call.MachineID, call.SessionID, call.Name); err != nil {
			return proto.TextResult(err.Error(), true)
		}
	}
	switch call.Name {
	case "task_plan", "agent_start":
		// A task's agent does the work itself (supervise_leader.go); the hub's own starts (an inbox hand-off) are not
		// a model's call and come through runTool.
		if err := s.workerRefusal(ctx, call.MachineID, call.SessionID); err != nil {
			return proto.TextResult(err.Error(), true)
		}
	}
	// A model's call says which conversation asked (task_brief.go callerOrigin); the hub's own runTool calls do not.
	ctx = withToolCaller(ctx, call.MachineID, call.SessionID)
	outward := s.outwardReader(ctx, call.MachineID, call.SessionID)
	if outward {
		ctx = withOutwardReader(ctx)
	}
	res, err := s.runTool(ctx, call.MachineID, call.SessionID, call.Name, args)
	if err != nil {
		return proto.TextResult(outwardText(outward, err.Error()), true)
	}
	// A conversation that writes to other people never reads the private parts of a profile, whichever tool would
	// give them (outward_reader.go).
	return proto.TextResult(outwardText(outward, res), false)
}

// appTool runs a tool that is app's (app.call app.tool).
func (s *Service) appTool(ctx context.Context, app string, call ToolCall, args json.RawMessage) proto.ToolResult {
	var res proto.ToolResult
	params := map[string]any{"name": compat.LegacyTool(call.Name), "arguments": args, "machine_id": call.MachineID,
		"session_id": call.SessionID}
	mcpWaitParams(ctx, params)
	if err := s.appCall(ctx, app, AppTool, params, &res); err != nil {
		return proto.TextResult(err.Error(), true)
	}
	return res
}

func (s *Service) runTool(ctx context.Context, machine, self, name string, args json.RawMessage) (string, error) {
	// Health stays in the health space (docs/assistant/health.md "How directories and memory are isolated"): it writes nothing to the knowledge base,
	// nor changes or starts what is imported into it (kb_history_status only reads).
	if name == "kb_update" || strings.HasPrefix(name, "kb_history_") && name != "kb_history_status" {
		if space, _ := s.selfSpace(ctx, self); space == SpaceHealth {
			return "", errors.New("the health space doesn't write the knowledge base: health information stays in its conversations and memory")
		}
	}
	switch name {
	case "repository_add":
		return s.toolRepositoryAdd(ctx, machine, self, args)
	case "repositories_list", "repository_get":
		return s.toolRepositories(ctx, name, args)
	case "supervision_finish":
		return s.toolSupervisionFinish(ctx, self, args)
	case "ask_user":
		return s.toolAskUser(ctx, machine, self, args)
	case ownerActionPrepareTool:
		return s.toolOwnerActionPrepare(ctx, machine, self, args)
	case "view_show", "view_update", "view_get":
		return s.toolView(ctx, machine, self, name, args)
	case "task_plan":
		return s.toolTaskPlanFrom(ctx, machine, self, args)
	case "task_plan_withdraw":
		return s.toolTaskPlanWithdraw(ctx, self, args) // tasks_start.go
	case "model_profiles_list", "model_profile_get", "model_profile_switch", "model_profile_rollback":
		return s.toolModelProfile(ctx, self, name, args)
	case "task_start_screen": // a failed start's kept tab (start_remedy.go)
		return s.toolTaskStartScreen(ctx, self, args)
	case "task_start_keys":
		return s.toolTaskStartKeys(ctx, self, args)
	case "task_retry":
		return s.toolTaskRetry(ctx, self, args)
	case "task_scope":
		return s.toolTaskScope(ctx, self, args) // task_brief_scope.go
	case "task_decided":
		return s.toolTaskDecided(ctx, machine, self, args) // task_decided.go
	case "task_review":
		res, err := s.toolTaskReview(ctx, self, args) // supervise_review.go
		if err == nil {
			var a struct {
				Verdict string `json:"verdict"`
			}
			if json.Unmarshal(args, &a) == nil && (a.Verdict == reviewAccepted || a.Verdict == reviewNeedsUser) {
				s.promoteSupervision(self)
			}
		}
		return res, err
	case "todo_workspace":
		return s.toolTodoWorkspace(ctx, machine, self, args)
	case "todo_repository_links_add":
		return s.toolTodoRepositoryLinksAdd(ctx, machine, self, name, args)
	case "todo_repositories_get", "todo_repositories_set", "todo_repository_links_set":
		return s.toolTodoRepositories(ctx, machine, self, name, args)
	case "todo_list", "todo_get", "todo_create", "todo_update", "todo_complete", "todo_reopen", "todo_delete":
		return s.toolTodo(ctx, machine, self, name, args) // todo_tools.go
	case "direction_name":
		return s.toolDirectionName(ctx, self, args) // direction_progress.go
	case "leader_progress":
		return s.toolLeaderProgress(ctx, self, args)
	case "concern_report":
		return s.toolConcernReport(ctx, machine, self, args) // concerns.go
	case knowledgeSearchTool, knowledgeReadTool:
		_, guest, err := s.guestOf(ctx, machine, self)
		if err != nil || guest || s.isWorkMachine(machine) || isOutwardReader(ctx) {
			return "", errors.New("remote knowledge folders are only available in private sessions")
		}
		return s.toolKnowledgeFolder(ctx, name, args)
	case "kb_search", "kb_list", "kb_get", "kb_update":
		return s.toolKB(ctx, name, args)
	case "kb_history_plan", "kb_history_status", "kb_history_stop", "kb_history_resume":
		return s.historyTools().Call(ctx, name, args) // kbhistory/tools.go
	case "interaction_answer":
		return s.toolInteractionAnswer(ctx, self, args)
	case "ask_escalate":
		return s.toolAskEscalate(ctx, self, args) // ask_route.go
	case "secret_run":
		return s.toolSecretRun(ctx, machine, self, args) // secret_run.go
	case "skill_import":
		return s.toolSkillImport(ctx, machine, self, args)
	case "list_places":
		return s.toolListPlaces(ctx) // places.go
	case "save_place":
		return s.toolSavePlace(ctx, args)
	case "inbox_draft":
		return s.toolInboxDraft(ctx, self, args)
	case "inbox_suggest":
		return s.toolInboxSuggest(ctx, self, args)
	case "inbox_folders":
		return s.toolInboxFolders(ctx, args) // inbox_folders.go
	case "inbox_move":
		return s.toolInboxMove(ctx, args)
	case "inbox_post":
		return s.toolInboxPost(ctx, machine, self, args) // inbox_post.go
	case "notify_user":
		var a struct {
			Title      string `json:"title"`
			Body       string `json:"body"`
			PushLevel  string `json:"push_level"`
			RelatedKey string `json:"related_key"`
		}
		if err := json.Unmarshal(args, &a); err != nil || strings.TrimSpace(a.Title+a.Body) == "" {
			return "", fmt.Errorf("title and body are required")
		}
		// An empty level is passed through, not normalised to P1: with ln mode on the hub decides by conversation
		// (the main conversation's ordinary notices still interrupt, fleet_link.go pushGradingNotifyLevel); with it off the
		// hub derives P1 for a NotifyPush anyway (hub/push_grading.go levelFor), so delivery there is unchanged.
		switch a.PushLevel {
		case "", assistantwire.LevelP0, assistantwire.LevelP1:
		default:
			return "", fmt.Errorf("push_level must be P0 or P1")
		}
		if s.justMeNow(machine, self) {
			// A Just me turn (meeting_justme.go): no push and no copy anywhere; the text stays in the conversation,
			// which the meeting page shows.
			return justMeNotice(s.lang(ctx)), nil
		}
		if s.inHealth(ctx, self) { // the lock screen and the channels hear there is news, not what it is (health.go)
			a.Title, a.Body = healthNotice(s.lang(ctx))
		}
		// Asked for explicitly: every device, the Feishu bot too, and no separate push when this turn ends.
		res, err := s.pushResult(ctx, Notification{Subtitle: "Persona", DeviceID: machine, SessionID: self, Title: a.Title, Body: a.Body,
			Asked: true, Level: a.PushLevel, RelatedKey: clip(strings.TrimSpace(a.RelatedKey), 200)})
		if err != nil {
			return "", err
		}
		// The Feishu bot and the channels with pushes on (channels_copies.go), unless the hub's low-distraction mode held
		// the notice for the briefing (a P1 one, or a repeat of a related key within 12 hours): a copy there would
		// interrupt where the phone does not, as a conclusion's card would (push_quiet.go). A held notice marks no
		// channel pushed, so a turn-end mirror goes as it would without it (#8268).
		var copies *copyReport
		if res.Held {
			s.logf("assistant: notify_user held by low-distraction mode (%s): no copy to the chat channels", res.Reason)
		} else {
			copies = s.channelsCopied(machine, self, a.Title, a.Body)
		}
		s.recordNotify(ctx, machine, self, notifyMissed(res)) // an interruption when a device took it (north_star.go)
		s.promoteSupervision(self)
		return notifyAnswerText(s.lang(ctx), res, copies.wait(notifyCopyWait)), nil
	case "urgent_call":
		return s.toolUrgentCall(ctx, machine, self, args) // urgent_call_tool.go
	case "conversations_list":
		return s.toolConversationsList(ctx, self, args)
	case "conversation_read":
		return s.toolConversationRead(ctx, self, args)
	case "conversation_rename":
		return s.toolConversationRename(ctx, self, args)
	case "conversation_archive", "conversation_unarchive":
		return s.toolConversationArchive(ctx, self, args, name == "conversation_archive")
	case "conversation_tags_set":
		return s.toolConversationTagsSet(ctx, self, args)
	case "conversation_new":
		return s.toolConversationNew(ctx, self, args)
	case "conversation_send":
		return s.toolConversationSend(ctx, self, args)
	case "schedule_state_set":
		return s.toolScheduleStateSet(ctx, machine, self, args)
	case "list_followups":
		space, _ := s.knownSpace(ctx, self)
		return s.describeSchedule(ctx, space), nil
	case "schedule_routine", "schedule_followup":
		// A conversation the policy limits may not change the schedule: new or changed tasks could alter its own
		// class or open another run.
		if err := s.dispatchScheduleGuard(ctx, machine, self); err != nil {
			return "", err
		}
		if name == "schedule_routine" {
			return s.toolScheduleRoutine(ctx, machine, self, args)
		}
		return s.toolScheduleFollowup(ctx, machine, self, args)
	case "delete_routine", "cancel_followup":
		// A conversation the policy limits may not delete its own scheduled task either (dispatch_policy.go).
		if err := s.dispatchScheduleGuard(ctx, machine, self); err != nil {
			return "", err
		}
		var a struct{ ID string }
		_ = json.Unmarshal(args, &a)
		space, _ := s.knownSpace(ctx, self)
		if !s.taskInSpace(ctx, a.ID, space) { // another space's task is as good as none (health.go)
			return "", fmt.Errorf("no task %q", a.ID)
		}
		ok, err := s.deleteTaskFrom(ctx, a.ID, self) // this conversation may ask for it again (scheduleOwn)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("no task %q", a.ID)
		}
		if name == "delete_routine" {
			return "Deleted.", nil
		}
		return "Cancelled.", nil
	case "machines_list":
		return s.toolMachinesList(ctx)
	case "machine_redeploy":
		return s.toolMachineRedeploy(ctx, machine, args)
	case "agents_list":
		var a struct{ State, Machine, Query string }
		_ = json.Unmarshal(args, &a)
		return s.describeAgents(ctx, a.State, a.Machine, a.Query, s.agentCallerSpace(ctx, machine, self)), nil
	case "agent_read":
		var a struct {
			Machine   string `json:"machine"`
			SessionID string `json:"session_id"`
			Turns     int    `json:"turns"`
		}
		_ = json.Unmarshal(args, &a)
		return s.toolAgentRead(ctx, a.Machine, a.SessionID, a.Turns, s.agentCallerSpace(ctx, machine, self))
	case "agent_send":
		wait, waitErr := agentWaitDuration(args)
		if waitErr != nil {
			return "", waitErr
		}
		var a struct {
			Machine   string `json:"machine"`
			SessionID string `json:"session_id"`
			Text      string `json:"text"`
			// DismissNotice: close a notice that Enter only closes (no options) before sending; any other dialog
			// still refuses the send.
			DismissNotice bool `json:"dismiss_notice"`
			// Task: new work handed to this agent becomes a supervised task of the main conversation (agent_control.go),
			// read from RawTask by givenTask (nil: a reply).
			Task    *sendTask           `json:"-"`
			RawTask json.RawMessage     `json:"task"`
			Handoff *taskHandoffRequest `json:"handoff"`
			// Attachments: the user's pictures and files, by their paths on this conversation's computer (#2437).
			Attachments []string `json:"attachments"`
			// Resume: resume a stopped agent with its context, as the user's Resume does, with ResumeWhy said to the
			// user (agent_resume.go, #8099). Ignored for an agent that runs.
			Resume    bool   `json:"resume"`
			ResumeWhy string `json:"resume_why"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", fmt.Errorf("arguments: %w", err)
		}
		a.Task = givenTask(a.RawTask)
		space := s.agentCallerSpace(ctx, machine, self)
		am, c, err := s.findAgentInSpace(ctx, a.Machine, a.SessionID, &space)
		if err != nil {
			return "", err
		}
		if c.Assistant {
			return "", fmt.Errorf("that is an assistant conversation, not a coding agent")
		}
		if c.SourceKind != "" && (c.Capabilities == nil || !c.Capabilities.Send) && !stoppedAgent(c) {
			// i18n-ignore: original MCP refusal for the model, not API error, push or approval-card copy; keep the source reason verbatim.
			return "", fmt.Errorf("这个 Agent 仅支持查看：%s", c.ReadOnlyReason)
		}
		if strings.TrimSpace(a.Text) == "" {
			return "", fmt.Errorf("text is empty")
		}
		var handoff *preparedHandoff
		if a.Handoff != nil {
			if a.Task != nil || a.Resume || c.Status == proto.StatusOffline || stoppedAgent(c) || exitText(a.Text) {
				return "", fmt.Errorf("a handoff needs an existing running Agent and a work message")
			}
			handoff, err = s.prepareTaskHandoff(ctx, self, am.ID, c.SessionID, *a.Handoff)
			if err != nil {
				return "", err
			}
		}
		resume := ""
		if stoppedAgent(c) { // read-only (#4754): a message does not start it; only a resume asked for does (#8099)
			if exitText(a.Text) {
				return fmt.Sprintf("%s has already stopped: nothing was sent.", agentLabel(c)), nil
			}
			if !a.Resume {
				return "", stoppedRefusal(c)
			}
			resume = cleanWhy(a.ResumeWhy)
			if err := s.resumeAllowed(ctx, self, am, c, resume, a.Task); err != nil {
				return "", err
			}
		}
		atts, err := cleanAttachments(a.Attachments)
		if err == nil {
			atts, err = s.checkAttachments(ctx, machine, self, atts)
		}
		if err != nil {
			return "", fmt.Errorf("%w; nothing was sent", err)
		}
		if a.Task != nil { // refused before the user is asked to confirm a send that would not go
			if err := s.sendTaskAllowed(ctx, self, am, c, *a.Task, a.Text); err != nil {
				return "", err
			}
		} else if err := s.exitAllowed(ctx, self, am, c, a.Text, len(atts)); err != nil {
			return "", err
		}
		summary := agentLabel(c) + " · " + am.Name + "\n\n" + a.Text
		if resume != "" { // the phone says it resumes the agent, and why
			summary = s.resumeNote(ctx, c, am, resume) + "\n\n" + a.Text
		}
		if ok, err := s.confirmOutbound(ctx, machine, self, name, summary, args); err != nil {
			return "", err
		} else if !ok {
			return "The user declined on the phone: nothing was sent to that agent.", nil
		}
		var task *handedTask
		if a.Task != nil {
			if task, err = s.handTask(ctx, self, am, c, *a.Task, a.Text); err != nil {
				return "", err
			}
		}
		text := a.Text
		if a.Task == nil {
			if t, theirs, err := s.supervisedElsewhere(ctx, self, am, c); err != nil {
				return "", err // Unknown task ownership must not strip message provenance.
			} else if theirs {
				// Read after the phone: the agent may have become another conversation's task while the user was asked.
				if err := exitRefused(c, t, a.Text, len(atts)); err != nil {
					return "", err
				}
				text = prompts.RenderSideConversation(a.Text)
			}
		}
		if a.Task != nil {
			text, err = s.withStandingAuthorizations(ctx, am.ID, c.Cwd, text)
			if err != nil {
				return "", task.sendFailed(ctx, s, rpc.NotSent(err))
			}
		}
		if handoff != nil {
			wire, _ := json.Marshal(taskHandoffReceipt{TodoID: handoff.origin, ID: handoff.ID, Head: handoff.Head, Check: handoff.Target.Check})
			text = prompts.RenderTodoHandoff(prompts.TodoHandoff{Text: text, ReceiptJSON: string(wire), Until: handoff.until.Format(time.RFC3339)})
		}
		// The user's attachments, copied to the agent's computer when it is another one: the text ends with them, the
		// pictures in the <attached_images> block a picture sent straight to the agent ends with (#2437).
		params := sendParams(c.SessionID, text, a.DismissNotice)
		inputID := ""
		if handoff != nil {
			inputID = handoff.ID
			params["client_msg_id"] = inputID
		} else if wait > 0 {
			inputID = "wait_" + randomHex(16)
			params["client_msg_id"] = inputID
		}
		finish := func(receipt string) string {
			return s.waitAgentReply(ctx, receipt, inputID, wait, fixedWaitTarget(am.ID, c.SessionID))
		}
		var handed handedAttachments
		if mentioned := s.mentionedAttachments(ctx, machine, self, a.Text, atts); len(atts)+len(mentioned) > 0 {
			handed = s.handAttachments(ctx, machine, am.ID, atts, mentioned)
			params["text"] = withAttachments(text, handed)
		}
		sending, _ := params["text"].(string)
		// A resume goes through the user's Resume's node call, the text as its first message (agent_resume.go).
		if resume != "" {
			receipt, err := s.resumeAndSend(ctx, self, am, c, params, resume, a.Text, handed.said(), task)
			if err != nil {
				return "", err
			}
			return finish(receipt), nil
		}
		if c.Status != proto.StatusOffline {
			// A running agent takes it at once. A send of more for an accepted task holds a turn of the agent that
			// starts meanwhile for its answer, so it is bounded however long the caller waits (a runtime's tool call
			// has no deadline) and the turn is not held for good; any other send waits as long as its caller (#7539).
			lctx, cancel := context.WithTimeout(ctx, liveSendWait)
			defer cancel()
			note, err := s.noteAgentSend(lctx, self, am.ID, c.SessionID, "", sending) // more from its task's conversation reopens an accepted task (#6726, #7539)
			if err != nil {
				return "", task.sendFailed(ctx, s, err)
			}
			if !note.reopens() && handoff == nil {
				lctx = ctx
			}
			// A nearly full Claude Code or Codex agent is compacted with its own /compact before the text goes in, by
			// its node as it delivers, which checks again then (#11137): that takes minutes, so the wait is not the live
			// send's. Any other send goes exactly as before.
			if contextFull(c) && compactable(c) {
				params["compact_if_full"] = true
				var ccancel context.CancelFunc
				lctx, ccancel = context.WithTimeout(context.WithoutCancel(ctx), compactSendWait)
				defer ccancel()
			}
			var sent agentSendReply
			if handoff != nil {
				err = s.sendTaskHandoff(lctx, handoff, params, &sent)
			} else {
				err = s.call(lctx, am.ID, DeviceSessionSend, params, &sent)
			}
			note.sent(lctx, err) // an unknown answer counts for the agent's next turn: it may still arrive
			if err == nil {
				s.progressMade(am.ID, c.SessionID, time.Now()) // the supervisor told it something: a new stretch (supervise_progress.go)
			}
			if err != nil {
				if errCode(err) == proto.CodeCompactFailed && !sendUnknown(err) {
					return "", task.sendFailed(ctx, s, compactFailed(c, am, err, false))
				}
				return "", task.sendFailed(ctx, s, err)
			}
			if handoff != nil {
				return finish(fmt.Sprintf("Handoff %s confirmed for %s. It is waiting for %s to report the result.",
					handoff.ID, handoff.origin, handoff.Target.Title) + handed.said()), nil
			}
			if sent.CompactAfterTurn {
				return finish(fmt.Sprintf("Queued for %s on %s: it is in the middle of a turn and its context is nearly full, so once "+
					"the turn ends it is compacted with its own /compact and then reads the message.", agentLabel(c), am.Name) +
					handed.said() + task.sent(ctx, s, a.Text)), nil
			}
			if sent.Compacted {
				return finish(fmt.Sprintf("Sent to %s on %s after compacting its nearly full context with its own /compact.",
					agentLabel(c), am.Name) + handed.said() + task.sent(ctx, s, a.Text)), nil
			}
			if sent.Queued {
				return finish(fmt.Sprintf("Queued for %s on %s: it is in the middle of a turn and reads the message once what it is doing now ends.",
					agentLabel(c), am.Name) + handed.said() + task.sent(ctx, s, a.Text)), nil
			}
			return finish(fmt.Sprintf("Sent to %s on %s.", agentLabel(c), am.Name) + handed.said() + task.sent(ctx, s, a.Text)), nil
		}
		// Not running and not read-only: a branch nothing has started yet (node fork.go), or a node from before #4754
		// that still resumes a stopped agent. Its node starts it first, which can take minutes and goes on if this call
		// stops waiting.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		note, err := s.noteAgentSend(sctx, self, am.ID, c.SessionID, "", sending)
		if err != nil {
			return "", task.sendFailed(sctx, s, err)
		}
		err = s.call(sctx, am.ID, DeviceSessionSend, params, nil)
		note.sent(sctx, err)
		if err != nil {
			return "", task.sendFailed(sctx, s, err)
		}
		return finish(fmt.Sprintf("Resumed %s on %s (same session, directory and launcher) and sent.", agentLabel(c), am.Name) + handed.said() + task.sent(sctx, s, a.Text)), nil
	case "agent_switch": // moves a running agent to another launcher and goes on with it (agent_switch.go, #5973)
		return s.toolAgentSwitch(ctx, machine, self, args)
	case "agents_switch": // moves many running agents to another launcher at once (agents_switch.go, #6008)
		return s.toolAgentsSwitch(ctx, machine, self, args)
	case "agent_interrupt":
		return s.toolAgentInterrupt(ctx, machine, self, args) // agent_control.go
	case "agent_supervise":
		return s.toolAgentSupervise(ctx, self, args)
	case "agent_transfer":
		return s.toolAgentTransfer(ctx, self, args) // agent_route.go: the App's Move to path
	case "agent_archive", "agent_unarchive":
		return s.toolAgentArchive(ctx, machine, self, args, name == "agent_archive") // agent_archive_tools.go
	case "agent_autoname":
		return s.toolAgentAutoname(ctx, machine, self, args) // agent_naming_tool.go
	case "agent_autoname_receipt":
		return s.toolAgentAutonameReceipt(ctx, machine, self, args) // agent_naming_receipt.go
	case "agent_start":
		wait, waitErr := agentWaitDuration(args)
		if waitErr != nil {
			return "", waitErr
		}
		var a struct {
			Machine        string `json:"machine"`
			Cwd            string `json:"cwd"`
			Prompt         string `json:"prompt"`
			Launcher       string `json:"launcher"`
			Model          string `json:"model"`
			Effort         string `json:"effort"`
			Speed          string `json:"speed"`
			PermissionMode string `json:"permission_mode"`
			Tier           string `json:"tier"`
			Worktree       struct {
				Branch string `json:"branch"`
			} `json:"worktree"`
			Ultracode bool `json:"ultracode"`
			// Attachments: the user's pictures and files, by their paths on this conversation's computer (#2437).
			Attachments []string `json:"attachments"`
			// SecretEnv: environment variables the Agent gets from secret providers, by reference, after the user
			// approves on the phone (secret_handoff.go).
			SecretEnv map[string]string `json:"secret_env"`
		}
		_ = json.Unmarshal(args, &a)
		if err := checkSecretEnvArg(args); err != nil {
			return "", fmt.Errorf("%w; no agent was started", err)
		}
		if a.PermissionMode = strings.TrimSpace(a.PermissionMode); a.PermissionMode != "" && !slices.Contains(proto.PermissionModes, a.PermissionMode) {
			return "", fmt.Errorf("invalid permission_mode %q; no agent was started", a.PermissionMode)
		}
		am, ok := s.machineByName(ctx, a.Machine)
		if !ok {
			return "", fmt.Errorf("unknown machine %q (machines: %s)", a.Machine, s.machineIDs(ctx))
		}
		// Refused before the user is asked: a model goes to the agent as --model (#1644).
		if a.Model = strings.TrimSpace(a.Model); a.Model != "" && !proto.ValidModelID(a.Model) {
			return "", fmt.Errorf("model %q is not a model id: %s; no agent was started", a.Model, proto.ModelIDRule)
		}
		atts, err := cleanAttachments(a.Attachments)
		if err == nil {
			atts, err = s.checkAttachments(ctx, machine, self, atts)
		}
		if err != nil {
			return "", fmt.Errorf("%w; no agent was started", err)
		}
		a.Cwd = proto.ExpandHome(a.Cwd, am.Home)
		own := startPick{launcher: strings.TrimSpace(a.Launcher), effort: strings.TrimSpace(a.Effort), speed: strings.TrimSpace(a.Speed), permission: a.PermissionMode} // before the tier
		// The tier is resolved first (an unknown one asks and starts nothing), then Model Settings' Agents laid over it as
		// the start lays it: what runs is said on the card (a hand-off from the health space asks) and in the answer.
		tier := strings.TrimSpace(a.Tier)
		if tier != "" {
			t, err := s.tierModel(ctx, tier)
			if err != nil {
				return "", fmt.Errorf("%w; no agent was started", err)
			}
			model := a.Model
			if !layTier(t, &a.Launcher, &a.Model, &a.Effort) {
				tier = "" // a launcher of its own: the tier gives it nothing
			} else {
				if a.Speed == "" && (model == "" || model == t.Model) {
					a.Speed = t.Speed
				}
				if a.PermissionMode == "" && t.PermissionMode != "" {
					a.PermissionMode = t.PermissionMode
				}
			}
		}
		// The tier that actually contributed a launcher is a caller choice: fitLauncher refuses where its launcher is
		// absent instead of substituting another one and dropping the preset (#12032). A tier that gave nothing
		// (a launcher of the caller's own) leaves this "".
		own.tier = tier
		own.ompModelDefault = a.Launcher == "omp" && a.Model == ""
		// What it runs on this computer, before the user is asked (#2640, launcher_fit.go): a launcher named here that
		// the computer lacks starts nothing; a default it lacks gives way to one it has, and the card and answer say so.
		run := map[string]any{"launcher": a.Launcher, "model": a.Model, "effort": a.Effort, "speed": a.Speed, "permission_mode": a.PermissionMode}
		if err := s.applyModel(ctx, "agent.new", run); err != nil {
			return "", err
		}
		swapped, fit, err := s.fitNewLauncher(ctx, am, own, run)
		if err != nil {
			return "", fmt.Errorf("%w; no agent was started", err)
		}
		a.Speed, _ = run["speed"].(string)
		if err := checkStartSpeed(run, fit, ""); err != nil {
			return "", fmt.Errorf("%w; no agent was started", err)
		}
		if swapped != "" {
			a.Launcher, _ = run["launcher"].(string)
			a.Model, _ = run["model"].(string)
			a.Effort, _ = run["effort"].(string)
			a.Speed, _ = run["speed"].(string)
			a.PermissionMode, _ = run["permission_mode"].(string)
			tier = "" // the tier's launcher is not what runs
		}
		// traex, omp…: no effort or permission mode (launcher_modes.go). One this call names is refused before the user is asked, rather
		// than by the computer after they said yes; one a tier filled in is dropped. pi, cursor: dropped either way.
		if !startTakesModes(fit.agent) {
			launcher, _ := run["launcher"].(string)
			if err := startModesRefused(launcher, fit.agent, own.permission, own.effort); err != nil {
				return "", fmt.Errorf("%w; no agent was started", err)
			}
			a.Effort = ""
			a.PermissionMode = ""
			delete(run, "effort")
			delete(run, "permission_mode")
		} else if mode, _ := run["permission_mode"].(string); mode != "" && own.permission == "" && !fit.supportsPermissionMode(mode) {
			delete(run, "permission_mode")
		}
		// The admission gate refuses a too-busy computer before the user is asked to approve anything.
		gated, _ := run["launcher"].(string)
		if err := s.checkAgentStart(ctx, am.ID, s.startLauncher(ctx, gated)); err != nil {
			return "", fmt.Errorf("%w; no agent was started", err)
		}
		branch := strings.TrimSpace(a.Worktree.Branch)
		where := am.Name + " · " + a.Cwd
		var runs string
		if branch != "" {
			where += i18n.T(s.lang(ctx), "assistant-tools.new_worktree", i18n.Args{"branch": branch})
		}
		switch { // the user sees what it will run on before saying yes
		case swapped != "":
			where += " · " + swapped
		case tier != "":
			selected := pickOf(run)
			runs = ModelTier{Launcher: selected.launcher, Model: selected.model, Effort: selected.effort, Speed: selected.speed, PermissionMode: selected.permission}.runs()
			where += i18n.T(s.lang(ctx), "assistant-tools.with_tier", i18n.Args{"tier": tier, "runs": runs})
		case a.Model != "":
			where += i18n.T(s.lang(ctx), "assistant-tools.with_model", i18n.Args{"model": a.Model})
		case proto.FasterSpeed(a.Speed):
			where += " · " + a.Speed
		}
		if tier == "" {
			if perm, _ := run["permission_mode"].(string); perm != "" {
				where += " · " + perm
			}
		}
		if a.Ultracode {
			where += " · ultracode"
		}
		if ok, err := s.confirmOutbound(ctx, machine, self, name, where+"\n\n"+a.Prompt, withoutSecretEnv(args)); err != nil {
			return "", err
		} else if !ok {
			return "The user declined on the phone: no agent was started.", nil
		}
		var res struct {
			Session proto.Session `json:"session"`
			// What an engine that runs only its own launchers ran in place of what was asked (#8140).
			Instead *proto.StartInstead `json:"instead"`
		}
		// The node builds the session and starts the agent before answering; a machine waking from sleep or a
		// launcher cold start can take minutes. Like agent_send's resume branch, this wait is detached from the
		// conversation's turn and given agentStartTimeout instead of the old 100s.
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentStartTimeout)
		defer cancel()
		// The tab is named from the prompt as written; the agent gets its project's background before it (#2820).
		title := s.tabName(sctx, a.Prompt)
		params := map[string]any{"cwd": a.Cwd, "launcher": a.Launcher, "model": a.Model, "effort": a.Effort, "speed": a.Speed, "permission_mode": a.PermissionMode, "tab_name": title, "title": title}
		prompt := s.withProjectContext(sctx, am.ID, a.Cwd, self, a.Prompt)
		if a.Ultracode {
			params["ultracode"] = true
		}
		if err := s.applyModel(sctx, "agent.new", params); err != nil {
			return "", err
		}
		// A launcher that failed its health check here runs on a machine where it works (launcher_health_check.go).
		picked, _ := params["launcher"].(string)
		launcher := s.startLauncher(ctx, picked)
		alt, note, moved := s.healthDetour(ctx, am, launcher)
		if moved {
			a.Cwd = rehome(a.Cwd, am, alt)
			params["cwd"], am = a.Cwd, alt
			// The same launcher name may mean a different agent on the destination. Reuse its list before choosing permissions.
			if _, fit, err = s.fitLauncher(ctx, am, own, params); err != nil {
				return "", fmt.Errorf("%w; no agent was started", err)
			}
		}
		if err := s.applyOmpStartDefaults(sctx, am, own, fit, params); err != nil {
			return "", err
		}
		params["prompt"] = s.withCloseoutBrief(sctx, am.ID, prompt, false)
		if err := checkStartSpeed(params, fit, ""); err != nil {
			return "", fmt.Errorf("%w; no agent was started", err)
		}
		// Precedence: explicit permission_mode > tier preset > agent.new binding > Allow all (Claude / Auto Accept) > CLI default.
		if !startTakesModes(fit.agent) { // the machine a health detour landed on may run another agent under this name
			if err := startModesRefused(picked, fit.agent, own.permission, own.effort); err != nil {
				return "", fmt.Errorf("%w; no agent was started", err)
			}
			delete(params, "effort")          // Model configuration's, not this call's
			delete(params, "permission_mode") // Model configuration's or tier's, not this call's
		} else {
			mode, _ := params["permission_mode"].(string)
			if mode != "" && own.permission == "" && !fit.supportsPermissionMode(mode) {
				delete(params, "permission_mode")
				mode = ""
			}
			if mode == "" && fit.allowAll() {
				// Allow all for Claude Code and for a provider whose approval is an Auto Accept switch (startAgent.allowAll, #8141).
				params["permission_mode"] = "bypassPermissions"
			} else if mode == "" {
				delete(params, "permission_mode")
			}
		}
		if !s.isWorkMachine(am.ID) {
			params["prompt"] = params["prompt"].(string) + "\n\n" + s.taskHandoff(sctx, am.ID, fit)
		}
		if note != "" {
			note = " Note: " + note + "."
		}
		// Secrets for this run (secret_handoff.go): checked, then asked on the card, for the computer and launcher it
		// starts with; resolved and staged right before the start.
		var hand *secretHandoff
		approvedBy := ""
		if len(a.SecretEnv) > 0 {
			if hand, err = s.handoffPrepare(ctx, am.ID, am.Name, params, a.SecretEnv); err != nil {
				return "", fmt.Errorf("%w; no agent was started", err)
			}
			tab, _ := params["tab_name"].(string)
			by, answer, err := s.handoffApprove(ctx, machine, self, hand, tab, a.Cwd, a.Prompt)
			if err != nil {
				return "", err
			}
			if by == "" {
				return answer, nil
			}
			approvedBy = by
		}
		if runs != "" {
			note = " Preset " + tier + ": runs on " + runs + "." + note
		}
		if swapped != "" {
			note = " Note: " + swapped + "." + note
		}
		if branch != "" { // the machine's engine makes the worktree (herdr's or Orca's own), the agent starts in it
			wt, err := s.newWorktree(sctx, am.ID, a.Cwd, branch)
			if err != nil {
				return "", fmt.Errorf("the worktree on branch %s was not made, no agent was started: %w", branch, err)
			}
			a.Cwd = wt.Path
			params["cwd"], params["workspace_id"] = wt.Path, wt.WorkspaceID
			note = fmt.Sprintf(" It runs in a new worktree on branch %s.", wt.Branch) + note
		}
		// The user's attachments, after any detour: to the machine the agent starts on (#2437).
		if mentioned := s.mentionedAttachments(ctx, machine, self, a.Prompt, atts); len(atts)+len(mentioned) > 0 {
			handed := s.handAttachments(sctx, machine, am.ID, atts, mentioned)
			params["prompt"] = withAttachments(params["prompt"].(string), handed)
			note += handed.said()
		}
		// Remember the sessions already there, so an unanswered start can adopt only the session it created:
		// UpdatedAt is last sign of life, so a cwd match alone could be another agent already busy there. The
		// list failing is non-fatal: it just means an unknown result cannot be turned into an adoption. It gets
		// its own short bound, so a slow list cannot spend the start's wait before the start is even sent.
		lctx, lcancel := context.WithTimeout(sctx, agentStartListTimeout)
		knownIDs := s.knownSessionIDs(lctx, am.ID)
		lcancel()
		// A Claude start takes the session id the hub picks (node session.start's session_id, used by --session-id):
		// if its answer never comes back, the reconciliation matches this exact id instead of guessing from launcher
		// and directory (a hand-started session and another workspace-none task look just like it). Other agents pick
		// their own ids, so they keep the heuristic fallback (session_adopt.go). The id is generated once, so an
		// ultracode refusal's retry still names the same session.
		expectID := ""
		if fit.agent == "claude" {
			expectID = newStartSessionID()
			params["session_id"] = expectID
		}
		// Last look on the machine it will really start on (a health detour may have changed am): re-check and hold
		// its admission slot across the session.start call.
		final, _ := params["launcher"].(string)
		release, err := s.admitAgentStart(sctx, am.ID, s.startLauncher(ctx, final))
		if err != nil {
			return "", fmt.Errorf("%w; no agent was started", err)
		}
		defer release()
		clearStandardSpeed(params)
		s.setPlanThen(ctx, am.ID, params)
		var values *handoffValues
		if hand != nil {
			var answer string
			if values, answer = s.handoffResolve(sctx, hand, approvedBy); values == nil {
				return answer, nil
			}
			defer values.wipe()
			if params["secret_ticket"], err = values.stage(sctx); err != nil {
				return "", fmt.Errorf("the secrets could not be staged on %s: %v; no agent was started", am.Name, err)
			}
			if params["ultracode"] != true {
				values.wipe() // no retry can need them again
			}
			note += hand.granted(approvedBy)
		}
		inputID := ""
		if wait > 0 {
			inputID = "wait_" + randomHex(16)
			params["client_msg_id"] = inputID
		}
		err = s.call(sctx, am.ID, DeviceSessionStart, params, &res)
		if reason, refused := s.ultracodeRefusal(ctx, err, params); refused {
			delete(params, "ultracode")                                  // an extra the agent it landed on cannot have: it starts without it (#3596)
			note += " Note: started without ultracode (" + reason + ")." // an answer lost on the way still says so
			err = s.call(sctx, am.ID, DeviceSessionStart, params, &res)
			// Refused before the node took the ticket, the same ticket starts it; one the refused start took and
			// dropped is gone, and the values are staged once more for this start.
			if values != nil && refusedWith(err, proto.CodeSecretEnvGone) {
				if params["secret_ticket"], err = values.stage(sctx); err != nil {
					return "", fmt.Errorf("the secrets could not be staged on %s again: %v; no agent was started", am.Name, err)
				}
				err = s.call(sctx, am.ID, DeviceSessionStart, params, &res)
			}
		}
		values.wipe()
		if err != nil {
			s.recordStartFailure(ctx, am.ID, launcher, err)
			if startResultUnknown(err) {
				// The node may already have the session and a running agent although the answer never came back:
				// do not report failure (the model would start it again, duplicating the run).
				return s.startedWithoutAnswer(ctx, self, am, picked, fit.snapshot, a.Cwd, a.Prompt, params["tab_name"], note, expectID, knownIDs), nil
			}
			return "", err
		}
		s.nameStartedAgent(sctx, am.ID, res.Session, title)
		s.recordStartSuccess(ctx, am.ID, ranLauncher(launcher, res.Instead, res.Session))
		if said := s.startInsteadNote(ctx, am, res.Instead, res.Session); said != "" {
			note += " Note: " + said + "." // the engine ran another of the same kind, its default model, a lower effort (#8140)
		}
		finish := func(receipt string) string {
			return s.waitAgentReply(ctx, receipt, inputID, wait, fixedWaitTarget(am.ID, res.Session.SessionID))
		}
		s.inboxAgentStarted(ctx, self, am.ID, res.Session.SessionID, a.Prompt) // an inbox event's conversation: its chain
		if told, ok := s.quietAgentTask(ctx, self, am, a.Cwd, a.Prompt, params["tab_name"], res.Session.SessionID, cmp.Or(res.Session.Agent, fit.snapshot)); ok {
			return finish(fmt.Sprintf("Started on %s in %s: session %s, a task of this conversation (its card is in the conversation; "+
				"%s).%s", am.Name, a.Cwd, res.Session.SessionID, told, note)), nil
		}
		return finish(fmt.Sprintf("Started on %s in %s: session %s. It shows up in the app's Agents tab.%s", am.Name, a.Cwd, res.Session.SessionID, note)), nil
	}
	return "", fmt.Errorf("unknown tool %s", name)
}

// agentStartTimeout bounds the wait for an agent_start answer: the node creates the session and launches the agent
// first, and a machine waking from sleep or a launcher cold start can take minutes. Same bound as a quiet task's
// own start (tasks_start.go's taskStartTimeout). A var so tests shorten it instead of sleeping past it.
var agentStartTimeout = taskStartTimeout

// agentStartAdoptWindow bounds how fresh a listed session's last sign of life must be to count as the one an
// unanswered start created.
var agentStartAdoptWindow = 10 * time.Minute

// agentStartListTimeout bounds the sessions.list calls around a start (the before snapshot and each adoption
// attempt), so they never spend the start's own wait.
var agentStartListTimeout = 15 * time.Second

// agentStartAdoptRetry is the one wait before a second adoption list: the hub's held list can lag the node.
var agentStartAdoptRetry = 200 * time.Millisecond

// startResultUnknown: the answer to session.start never came back (the wait ran out or the link dropped while it
// was out), though the node may already have built the session and launched the agent. An offline machine, a
// missing one or a request the node refused is definite and stays an error; rpc.ErrDropped answers the same words
// as rpc.ErrClosed (a relay passes them through), so the rpc.Error message form counts too.
func startResultUnknown(err error) bool {
	if p, ok := errors.AsType[*taskworkspace.Problem](err); ok {
		return p.Code == "operation_unknown"
	}
	var re *rpc.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		errors.Is(err, rpc.ErrClosed) || errors.As(err, &re) && re.Message == rpc.ErrClosed.Error()
}

// sendUnknown reports whether a failed node write may have taken effect. An explicit delivery result takes
// precedence over the transport's words. An uncoded error relayed across the bus keeps only its message.
func sendUnknown(err error) bool {
	if _, ok := errors.AsType[*completedNodeWriteError](err); ok {
		return true // a later validation error describes a different operation
	}
	if err == nil || errors.Is(err, rpc.ErrNotSent) {
		return false
	}
	switch nodeDelivered(err) {
	case "none":
		return false
	case "unknown", "yes":
		return true
	}
	var re *rpc.Error
	if errors.As(err, &re) {
		if re.Code != "" {
			return re.Code == devicewire.CodeDeadline || re.Code == devicewire.CodeNodeClosed
		}
		return noSendAnswer(re.Message)
	}
	return startResultUnknown(err) || noSendAnswer(err.Error())
}

// Only the transport's complete, canonical message substitutes for a lost Go error type.
func noSendAnswer(message string) bool {
	return message == context.DeadlineExceeded.Error() || message == context.Canceled.Error() || message == rpc.ErrClosed.Error()
}

// A node already answered a write successfully, but the manager could not validate or decode the answer.
// Keep the original error for diagnostics without turning it into permission to send again.
type completedNodeWriteError struct{ error }

func (e *completedNodeWriteError) Unwrap() error { return e.error }

func tracksNodeWrite(method string) bool {
	return method == DeviceSessionSend || method == DeviceSessionStart || method == devicewire.MethodCloseTaskTerminal ||
		method == "session.a2a.send" || method == "session.a2a.wake"
}

func beforeNodeWrite(method string, err error) error {
	if tracksNodeWrite(method) {
		return rpc.NotSent(err)
	}
	return err
}

func afterNodeWrite(method string, err error) error {
	if err != nil && tracksNodeWrite(method) {
		return &completedNodeWriteError{err}
	}
	return err
}

// nodeMayHaveTaken counts only a delivery the node itself marked as possible or confirmed.
func nodeMayHaveTaken(err error) bool {
	d := nodeDelivered(err)
	return d == "unknown" || d == "yes"
}

func nodeDelivered(err error) string {
	var re *rpc.Error
	if !errors.As(err, &re) || len(re.Data) == 0 {
		return ""
	}
	var d struct {
		Delivered string `json:"delivered"`
	}
	_ = json.Unmarshal(re.Data, &d)
	return d.Delivered
}

// liveSendWait bounds a send to an agent that runs when the send may reopen its accepted task (agent_send), and the
// owner's message on a task's card: its node types it in at once. A var so tests shorten it.
var liveSendWait = 90 * time.Second

// startedWithoutAnswer answers an agent_start whose result is unknown. A list showing the new session the node
// built (not present before the start) adopts and links it like an answered start; otherwise the model gets neutral
// text: check the Agents list first and never start a second copy. A failed pre-start list disables only the
// heuristic, not an exact expectID match. expectID is the session id the hub asked this start to use
// (session_adopt.go).
func (s *Service) startedWithoutAnswer(ctx context.Context, conversation string, m Device, launcher, launcherKind, cwd, prompt string, tab any, note, expectID string, knownIDs map[string]bool) string {
	if sess, ok := s.adoptStartedSession(ctx, m, launcher, cwd, expectID, knownIDs); ok {
		if told, ok := s.quietAgentTask(ctx, conversation, m, cwd, prompt, tab, sess.SessionID, cmp.Or(sess.Agent, launcherKind)); ok {
			return fmt.Sprintf("Started on %s in %s: session %s (the start took longer than the wait; the session was "+
				"confirmed from the machine's own list), a task of this conversation (its card is in the conversation; "+
				"%s).%s",
				m.Name, cwd, sess.SessionID, told, note)
		}
		return fmt.Sprintf("Started on %s in %s: session %s (the start took longer than the wait; the session was "+
			"confirmed from the machine's own list). It shows up in the app's Agents tab.%s", m.Name, cwd, sess.SessionID, note)
	}
	return fmt.Sprintf("The agent may already be starting on %s in %s, but the machine did not answer within the wait, "+
		"so the result is unknown. Do NOT start it again: check the app's Agents tab (or call agents_list) for a new "+
		"session there, and only start again if no new session appears.%s", m.Name, cwd, note)
}

// knownSessionIDs is the machine's session ids before a start; nil when the list could not be read (no heuristic
// adoption, though an exact expectID can still be reconciled).
func (s *Service) knownSessionIDs(ctx context.Context, machine string) map[string]bool {
	list, err := s.sessions(ctx, machine)
	if err != nil {
		return nil
	}
	ids := make(map[string]bool, len(list))
	for _, sess := range list {
		ids[sess.SessionID] = true
	}
	return ids
}

// adoptStartedSession lives in session_adopt.go: exact match on the start's own session id, then a heuristic that
// never takes a session already linked to an open todo.

// ---- ask_user ----

// askUserWait is how long an ask_user card waits for the user. It is longer than the Lark nudge's default wait
// (assistantwire.NudgeAfterDefault), so an owner who is away is nudged while the question still waits (#9); the
// turn that asked is held for as long, which is why the user speaking in the conversation withdraws the question
// (withdrawAskUser), and a message from elsewhere waiting for the conversation makes it let go of the turn
// (ask_release.go). A question nobody answers in it becomes a to-do (ask_todo.go). A variable for the tests.
var askUserWait = 2 * time.Hour

// question is an ask_user waiting for the user: the card the hub shows, and where the answer goes.
type question struct {
	ix     proto.Interaction
	answer chan proto.Resolution
	// userSpoke is set (under questions.mu) when the question was withdrawn because the user wrote in the conversation.
	userSpoke bool
	// later: asked with wait false (ask_later.go): no call waits on answer, its end is told to the conversation as a
	// message; telling (under questions.mu) while that is being done. A waiting one becomes later when it lets go of
	// its turn (ask_release.go): release is then closed, and the call returns.
	later, telling bool
	// release is closed when a waiting ask_user lets go of its turn because a message waits for the conversation
	// (ask_release.go); nil for any other card. releasing is set (under questions.mu) while that is being done, and
	// releaseCheck is the timer that looks again once the question is old enough to let go.
	release      chan struct{}
	releasing    bool
	releaseCheck *time.Timer
}

// errUserSpoke is what ask returns for a question withdrawn because the user wrote in the conversation.
var errUserSpoke = errors.New("the user wrote in the conversation")

// questions are the questions out to the user, by interaction id: they go to the hub in the snapshot.
type questions struct {
	mu sync.Mutex
	m  map[string]*question
}

func (s *Service) toolAskUser(ctx context.Context, machine, sessionID string, args json.RawMessage) (string, error) {
	var a struct {
		Questions []struct {
			Question    string                 `json:"question"`
			Header      string                 `json:"header"`
			MultiSelect bool                   `json:"multiSelect"`
			Options     []proto.QuestionOption `json:"options"`
		} `json:"questions"`
		// Wait false: the card goes up and the tool returns at once (ask_later.go). Unset is true.
		Wait *bool `json:"wait"`
	}
	if err := json.Unmarshal(args, &a); err != nil || len(a.Questions) == 0 {
		return "", fmt.Errorf("questions are required")
	}
	var da askDeadlineArgs
	if err := json.Unmarshal(args, &da); err != nil {
		return "", fmt.Errorf("arguments: %v", err) // a deadline or reversible of the wrong type is no deadline to ignore
	}
	now := time.Now()
	deadline, err := readAskDeadline(da, now)
	if err != nil {
		return "", err
	}
	expires := now.Add(askUserWait)
	if !deadline.At.IsZero() {
		// The deadline is kept as a time in the question's arguments (askDeadlineOf reads it back when it expires), and
		// the card waits no longer than it.
		var m map[string]any
		if err := json.Unmarshal(args, &m); err != nil {
			return "", fmt.Errorf("arguments: %v", err)
		}
		m["deadline"] = deadline.At.Format(time.RFC3339Nano)
		if args, err = json.Marshal(m); err != nil {
			return "", err
		}
		if deadline.At.Before(expires) {
			expires = deadline.At
		}
	}
	ix := proto.Interaction{
		ID: "ix_" + randomHex(6), MachineID: machine, SessionID: sessionID, SessionTitle: "Persona", Kind: proto.IxQuestion, CanCancel: true,
		ToolName: "ask_user", ToolInput: args, CreatedAt: now.UTC(), ExpiresAt: expires.UTC(),
	}
	if c, ok := s.session(ctx, machine, sessionID); ok {
		ix.Cwd = c.Cwd
		if t := transcript.Title(c.Title); t != "" {
			ix.SessionTitle = t
		}
	}
	for i, q := range a.Questions {
		ix.Questions = append(ix.Questions, proto.Question{Question: q.Question, Header: q.Header, MultiSelect: q.MultiSelect, Options: q.Options,
			Recommend: askRecommend(da, i, q.Options)})
	}
	keepAskDecision(&ix, da, deadline)
	markRecommended(s.lang(ctx), s.location(ctx), ix.Questions, deadline)
	// Answers come back keyed by question text: two questions with the same text would lose one answer.
	if proto.HasDuplicateQuestionText(ix.Questions) {
		return "", i18n.Err("hub-hub.duplicate_question")
	}
	s.promoteSupervision(sessionID)
	if a.Wait != nil && !*a.Wait {
		return s.askLaterStart(ctx, ix)
	}
	r, err := s.ask(ctx, ix)
	if errors.Is(err, errUserSpoke) {
		return prompts.RenderQuestionUserSpoke(), nil
	}
	if errors.Is(err, errReleased) { // a message waits for the conversation: the card stays up, unwaited (ask_release.go)
		return prompts.RenderQuestionReleased(prompts.QuestionAskedLater{ID: ix.ID,
			Until: ix.ExpiresAt.In(s.location(ctx)).Format("01-02 15:04")}), nil
	}
	if err != nil {
		return "", err
	}
	if r.Cancel {
		return prompts.RenderQuestionCancelled(), nil
	}
	if s.askExpiredResolution(ix, r) { // nobody answered in time: it waits as a to-do (ask_todo.go)
		t, err := s.askExpired(context.WithoutCancel(ctx), ix)
		if err == nil {
			return s.askExpiredResult(context.WithoutCancel(ctx), ix, t, deadline), nil
		}
		s.logf("ask_user %s: expired, and no to-do could be made: %v", ix.ID, err)
	}
	if r.Dismiss || len(r.Answers) == 0 {
		return "The user did not answer (dismissed or timed out). Don't go ahead with what you asked about; say what is waiting for them.", nil
	}
	var b strings.Builder
	b.WriteString("The user answered:\n")
	for _, q := range a.Questions {
		if ans, ok := r.Answers[q.Question]; ok {
			fmt.Fprintf(&b, "- %s → %s\n", q.Question, ans)
		}
	}
	return b.String(), nil
}

// ask puts ix on the phone (the snapshot's interactions: the hub shows it like any agent's card, pushes it and
// expires it) and waits for the answer (Action); when ctx ends first the question is withdrawn. An ask_user question
// may instead let go of the turn while a message waits for its conversation (errReleased, ask_release.go): its card
// then stays up, and its end is told to the conversation as a message.
func (s *Service) ask(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
	resume := pushMCPInteraction(ctx, ix)
	defer resume()
	ix = s.ownCard(ctx, ix)
	q := &question{ix: ix, answer: make(chan proto.Resolution, 1)}
	if ix.ToolName == "ask_user" && ix.Kind == proto.IxQuestion {
		q.release = make(chan struct{})
	}
	s.questions.mu.Lock()
	if s.questions.m == nil {
		s.questions.m = map[string]*question{}
	}
	s.questions.m[ix.ID] = q
	s.questions.mu.Unlock()
	s.publish(context.WithoutCancel(ctx))
	defer func() {
		s.questions.mu.Lock()
		if q.releaseCheck != nil {
			q.releaseCheck.Stop()
		}
		if !q.later { // one that let go of its turn stays out until its end is told (ask_later.go)
			delete(s.questions.m, ix.ID)
		}
		s.questions.mu.Unlock()
		s.publish(context.WithoutCancel(ctx))
	}()
	if q.release != nil {
		go s.releaseHeldAsks(ix.SessionID) // a message may be waiting behind this turn already
	}
	select {
	case r := <-q.answer:
		s.questions.mu.Lock()
		spoke := q.userSpoke
		s.questions.mu.Unlock()
		if spoke && r.Dismiss && r.Reason == proto.DismissedInChat { // not an answer that got in first
			return r, errUserSpoke
		}
		return r, nil
	case <-q.release:
		return proto.Resolution{}, errReleased
	case <-ctx.Done():
		return proto.Resolution{}, ctx.Err()
	}
}

// withdrawAskUser takes back the ask_user questions the conversation is waiting on, because the user wrote in it: the
// turn that asked is blocked on the answer, so what they write would wait behind it (Claude queues a message sent
// during a turn until the turn ends). The tool returns at once and says the new message follows. The card is closed
// as dismissed, with the reason "replied in the chat" (proto.DismissedInChat). Other cards, and Agents' questions, are
// not touched.
func (s *Service) withdrawAskUser(session string) {
	if session == "" {
		return
	}
	s.questions.mu.Lock()
	var mine []*question
	for _, q := range s.questions.m {
		// One asked without waiting holds no turn: its card stays up and can still be answered (ask_later.go).
		if q.ix.SessionID == session && q.ix.ToolName == "ask_user" && !q.userSpoke && !q.later {
			q.userSpoke = true
			mine = append(mine, q)
		}
	}
	s.questions.mu.Unlock()
	r := proto.Resolution{Dismiss: true, Reason: proto.DismissedInChat}
	for _, q := range mine {
		// The card is on the phone through the interaction queue, which hands the dismissal back to the question; before
		// it got there (or with no queue) the question is handed the dismissal directly.
		if err := s.resolve(q.ix.ID, r); err != nil {
			select {
			case q.answer <- r:
			default:
			}
		}
	}
}

// pendingQuestions are the questions out to the user, oldest first: the ones a call waits on, and the ones asked
// without waiting (those a manager before this one left are read back first, ask_later.go).
func (s *Service) pendingQuestions() []proto.Interaction {
	s.loadAskLater(context.Background())
	s.questions.mu.Lock()
	out := []proto.Interaction{}
	for _, q := range s.questions.m {
		out = append(out, q.ix)
	}
	s.questions.mu.Unlock()
	// Prepared assistant approvals use the interaction queue so clarification
	// can release the original tool without resolving the card.
	s.fleet.mu.Lock()
	for _, p := range s.fleet.interactions {
		if p.prepared != nil {
			out = append(out, p.ix)
		}
	}
	s.fleet.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Action takes the user's answer to one of the questions out, or to a card about an agent's turn (agents_work_act.go)
// (op answer, the answer a proto.Resolution).
func (s *Service) Action(ctx context.Context, a Action) (ActionResult, error) {
	if a.Op != "answer" {
		return ActionResult{}, fmt.Errorf("unknown op %q", a.Op)
	}
	var r proto.Resolution
	if len(a.Answer) > 0 {
		if err := json.Unmarshal(a.Answer, &r); err != nil {
			return ActionResult{}, err
		}
	}
	if mine, err := s.sessionReviewAction(ctx, a.InteractionID, r); mine {
		return ActionResult{OK: err == nil}, err
	}
	if mine, err := s.toolLoopAnswer(ctx, a.InteractionID, r); mine {
		return ActionResult{OK: err == nil}, err
	}
	if mine, err := s.workAnswer(ctx, a.InteractionID, r); mine {
		return ActionResult{OK: err == nil}, err
	}
	s.fleet.mu.Lock()
	prepared := s.fleet.interactions[a.InteractionID]
	isPrepared := prepared != nil && prepared.prepared != nil
	s.fleet.mu.Unlock()
	if isPrepared {
		if !r.Cancel {
			s.ownerHeard(prepared.ix.SessionID)
		}
		err := s.resolveAnswer(ctx, a.InteractionID, r, true, nil, nil)
		return ActionResult{OK: err == nil}, err
	}
	s.questions.mu.Lock()
	q := s.questions.m[a.InteractionID]
	s.questions.mu.Unlock()
	if q == nil {
		return ActionResult{}, errGone(a.InteractionID)
	}
	if r.Cancel && (q.ix.Kind != proto.IxQuestion || !q.ix.CanCancel || !r.IsQuestionCancel()) {
		return ActionResult{}, errInvalidQuestionCancel
	}
	r = honest(q.ix, r)
	if !r.Cancel {
		s.ownerHeard(q.ix.SessionID) // the owner's answer is their word in that conversation (ownerHeard)
	}
	// Whether a call waits on it is read, and the answer handed to that call, under the lock a question that lets go of
	// its turn takes to do so (ask_release.go): the answer goes to the call or is told as a message, never lost between.
	s.questions.mu.Lock()
	if !q.later {
		select {
		case q.answer <- r:
		default: // answered already
		}
		s.questions.mu.Unlock()
		return ActionResult{OK: true}, nil
	}
	s.questions.mu.Unlock()
	// Asked without waiting, or no longer holding its turn: its end goes to the conversation as a message (ask_later.go).
	err := s.askLaterEnded(ctx, q, r)
	return ActionResult{OK: err == nil}, err
}

// ---- agents ----

// machineByName is the machine by its id, else by its name or ssh alias (any case).
func (s *Service) machineByName(ctx context.Context, id string) (Device, bool) {
	devices := s.devices(ctx)
	for _, m := range devices {
		if m.ID == id {
			return m, true
		}
	}
	for _, m := range devices {
		if strings.EqualFold(m.Name, id) || strings.EqualFold(m.SSHHost, id) {
			return m, true
		}
	}
	return Device{}, false
}

func (s *Service) machineIDs(ctx context.Context) string {
	devices := s.devices(ctx)
	ids := make([]string, 0, len(devices))
	for _, m := range devices {
		ids = append(ids, m.ID)
	}
	return strings.Join(ids, ", ")
}

// agentSpace includes health subdirectories for the tools' access boundary.
func agentSpace(c proto.Session) string {
	if c.Space == SpaceHealth || identity.InHealthSpace(c.Cwd) {
		return SpaceHealth
	}
	return ""
}

// agentCallerSpace uses the calling conversation on its own machine. Calls without
// a known session can reach only the ordinary space, as with knownSpace.
func (s *Service) agentCallerSpace(ctx context.Context, machine, self string) string {
	if self != "" && machine != "" {
		if c, ok := s.session(ctx, machine, self); ok {
			return agentSpace(c)
		}
	}
	return ""
}

func (s *Service) findAgent(ctx context.Context, machine, id string) (Device, proto.Session, error) {
	return s.findAgentInSpace(ctx, machine, id, nil)
}

// findAgentInSpace resolves a full id or prefix among eligible sessions. An empty
// machine searches all of them. A nil space preserves the lookup for other callers.
func (s *Service) findAgentInSpace(ctx context.Context, machine, id string, space *string) (Device, proto.Session, error) {
	id = strings.TrimSpace(id)
	if len(id) < 6 {
		return Device{}, proto.Session{}, fmt.Errorf("session_id is required (at least its first 8 characters)")
	}
	var found *Device
	var session proto.Session
	for _, m := range s.devices(ctx) {
		if machine != "" {
			if want, ok := s.machineByName(ctx, machine); !ok || want.ID != m.ID {
				continue
			}
		}
		list, _ := s.sessions(ctx, m.ID)
		for _, c := range list {
			if c.HiddenByDirectory() || space != nil && (c.IsDeleted() || agentSpace(c) != *space) {
				continue
			}
			if c.SessionID == id || strings.HasPrefix(c.SessionID, id) {
				if found != nil {
					return Device{}, proto.Session{}, fmt.Errorf("session_id %s is ambiguous; use the full id and machine", id)
				}
				found, session = &m, c
			}
		}
	}
	if found != nil {
		return *found, session, nil
	}
	return Device{}, proto.Session{}, fmt.Errorf("no agent %s (use agents_list)", id)
}

func (s *Service) describeAgents(ctx context.Context, state, machine, query string, spaces ...string) string {
	space := ""
	if len(spaces) > 0 {
		space = spaces[0]
	}
	if state == "" {
		state = "active"
	}
	now := time.Now()
	var res struct {
		Interactions []proto.Interaction `json:"interactions"`
	}
	_ = s.call(ctx, DeviceAll, DeviceInteractionsList, map[string]any{}, &res)
	type pendingCard struct {
		text   string
		folded bool
		source [2]string
	}
	pending := map[[2]string][]pendingCard{}
	folded := map[[2]string]bool{}
	for _, ix := range res.Interactions {
		if identity.InHealthSpace(ix.Cwd) && space != SpaceHealth {
			continue
		}
		what := ix.Summary
		if ix.Kind == proto.IxQuestion && len(ix.Questions) > 0 {
			what = "提问：" + questionSummary(ix)
		} else if ix.Kind == proto.IxPlan {
			what = "计划待审批：" + planTitle(ix.Plan)
		} else if ix.Kind == proto.IxPermission {
			what = "要授权 " + ix.ToolName + "：" + ix.Summary
		} else if ix.Kind == proto.IxScreen {
			what = "终端对话框等你处理"
		}
		session := ix.SessionID
		source := [2]string{ix.MachineID, session}
		isFolded := ix.FoldedUnder != "" && ix.FoldedUnder != session
		if isFolded {
			folded[source] = false // include a folded card only after checking its own session
			session = ix.FoldedUnder
		}
		k := [2]string{ix.MachineID, session}
		// The id goes after the clip: a long request must not cut it off, or interaction_answer has nothing to answer.
		id := " (interaction_id=" + ix.ID + ")"
		pending[k] = append(pending[k], pendingCard{clip(what, max(400-utf8.RuneCountInString(id), 1)) + id, isFolded, source})
	}
	type row struct {
		m Device
		c proto.Session
	}
	var rows []row
	var offline []string
	want, wanted := Device{}, false
	if machine != "" {
		want, wanted = s.machineByName(ctx, machine)
	}
	for _, m := range s.devices(ctx) {
		if machine != "" && (!wanted || want.ID != m.ID) {
			continue
		}
		if !m.Online {
			offline = append(offline, m.Name)
		}
		list, muted, _ := s.sessionsMuted(ctx, m.ID)
		for _, c := range list {
			if c.HiddenByDirectory() || c.IsDeleted() || agentSpace(c) != space {
				continue
			}
			if source := [2]string{m.ID, c.SessionID}; c.FoldedUnder() != "" {
				if _, tracked := folded[source]; tracked {
					folded[source] = true
				}
			}
			if c.Assistant || c.FoldedUnder() != "" {
				continue
			}
			c.State = c.FoldedState()
			// Archived ones are out of the apps' lists: only "archived" lists them, and "all" with them marked.
			switch state {
			case "all":
			case "archived":
				if !c.Archived {
					continue
				}
			case "active":
				if st := agentsListState(c); st != proto.StateNeedsYou && st != proto.StateWorking && st != proto.StateUnread && !c.AwaitsReply {
					continue // a seen one still asking the user is active too (#5096)
				}
			case "done":
				if snapshotAgentGroup(c, now) != snapshotAgentDone || muted[c.SessionID] {
					continue
				}
			case "muted":
				if snapshotAgentGroup(c, now) == snapshotAgentOutside || !muted[c.SessionID] {
					continue
				}
			default:
				if agentsListState(c) != state {
					continue
				}
			}
			if c.Archived && state != "all" && state != "archived" {
				continue
			}
			if query != "" {
				hay := strings.ToLower(c.Title + " " + c.Cwd + " " + c.SpaceName + " " + c.SlotName)
				if !strings.Contains(hay, strings.ToLower(query)) {
					continue
				}
			}
			rows = append(rows, row{m, c})
		}
	}
	order := map[string]int{proto.StateNeedsYou: 0, proto.StateWorking: 1, proto.StateUnread: 2, proto.StateIdle: 3}
	sort.SliceStable(rows, func(i, j int) bool {
		oi, oj := order[agentsListState(rows[i].c)], order[agentsListState(rows[j].c)]
		if oi != oj {
			return oi < oj
		}
		return rows[i].c.UpdatedAt.After(rows[j].c.UpdatedAt)
	})
	if len(rows) > 60 {
		rows = rows[:60]
	}
	var b strings.Builder
	if len(rows) == 0 {
		b.WriteString("No agents match.\n")
	}
	for _, r := range rows {
		c := r.c
		fmt.Fprintf(&b, "- [%s] %s · machine=%s session_id=%s · %s", agentsListLabel(c), agentLabel(c), r.m.ID, c.SessionID, c.Cwd)
		if c.SourceKind != "" {
			fmt.Fprintf(&b, " · source=%s instance=%s", c.SourceKind, c.SourceInstanceID)
			if c.Capabilities == nil || !c.Capabilities.Send {
				fmt.Fprintf(&b, " · 仅查看：%s", c.ReadOnlyReason)
			}
		}
		if c.Archived {
			b.WriteString(" · archived")
		}
		if c.Launcher != "" && c.SourceKind == "" && !c.Assistant { // what agent_switch moves it from (agent_switch.go)
			fmt.Fprintf(&b, " · launcher=%s", c.Launcher)
			if c.OffLauncher { // its claude was resumed without Persona's command (#11792)
				fmt.Fprintf(&b, " (off_launcher: started on it, but its claude now runs without Persona's start command, "+
					"e.g. herdr's bare resume after a restart, so not on %s; agent_switch to %s brings it back)", c.Launcher, c.Launcher)
			}
		}
		if to := s.queuedSwitchOf(r.m.ID, c.SessionID); to != "" { // agents_switch queued it for the end of its turn
			fmt.Fprintf(&b, " · switch_queued=%s", to)
		}
		if reset, ok := usageLimitReset(c); ok { // stopped on its subscription's usage limit (usage_limit.go)
			fmt.Fprintf(&b, " · usage_limit resets_at=%s", reset.Format(time.RFC3339))
		} else if c.TurnErrorKind == proto.TurnErrorUsageLimit && c.TurnError != "" {
			b.WriteString(" · usage_limit")
		}
		if !c.UpdatedAt.IsZero() {
			fmt.Fprintf(&b, " · %s", ago(c.UpdatedAt))
		}
		if mode := ultracodeState(c.Ultracode); mode != "" {
			fmt.Fprintf(&b, " · ultracode=%s", mode)
		}
		if c.Loop != nil {
			b.WriteString(" · /loop")
		}
		if n := len(c.Background); n > 0 {
			fmt.Fprintf(&b, " · 后台任务 %d", n)
		}
		if state == "done" {
			b.WriteString(" · work=done")
		}
		if state == "muted" {
			b.WriteString(" · muted")
		}
		b.WriteString("\n")
		if (state == "done" || state == "muted") && c.Work != nil && c.Work.Phase != proto.WorkWorking && c.Work.Line != "" {
			fmt.Fprintf(&b, "    结论：%s\n", clip(oneLine(c.Work.Line), 200)) // i18n-ignore: agents_list output for the model, as the lines around it
		}
		for _, w := range runningWorkflows(c, now) { // its turn ended to wait for them: not done (#2935)
			fmt.Fprintf(&b, "    后台 workflow 在跑（在等它的结果，没做完）：%s\n", prompts.RenderSnapshotWorkflow(w))
		}
		for _, p := range pending[[2]string{r.m.ID, c.SessionID}] {
			if !p.folded || folded[p.source] {
				fmt.Fprintf(&b, "    等你：%s\n", p.text)
			}
		}
		if c.AwaitsReply && c.Asks != "" {
			fmt.Fprintf(&b, "    在问你、还没回：%s\n", c.Asks) // i18n-ignore: agents_list output for the model, as the lines around it
		}
		// Why it waits, while it does (a dialog's first line, a host's reason), as the apps' rows show it; else its last words.
		if last := c.LastText; c.State == proto.StateNeedsYou && strings.TrimSpace(c.AttentionReason) != "" {
			fmt.Fprintf(&b, "    最后说：%s\n", clip(oneLine(c.AttentionReason), 200))
		} else if last != "" {
			fmt.Fprintf(&b, "    最后说：%s\n", clip(oneLine(last), 200))
		}
	}
	if len(offline) > 0 {
		fmt.Fprintf(&b, "(offline machines: %s)\n", strings.Join(offline, ", "))
	}
	if machine == "" || wanted {
		b.WriteString(s.healthLines(ctx, want.ID))
	}
	return b.String()
}

func (s *Service) toolAgentRead(ctx context.Context, machine, id string, turns int, space string) (string, error) {
	m, c, err := s.findAgentInSpace(ctx, machine, id, &space)
	if err != nil {
		return "", err
	}
	if turns <= 0 {
		turns = 2
	}
	var res struct {
		Items []proto.Item `json:"items"`
	}
	if err := s.call(ctx, m.ID, DeviceSessionItems, map[string]any{"session_id": c.SessionID, "turns": min(turns, 10)}, &res); err != nil {
		return "", err
	}
	head := fmt.Sprintf("%s on %s (%s, %s)", agentLabel(c), m.Name, stateNames[c.State], c.Cwd)
	return transcriptText(head, sessionLink(s.webBase(ctx), m.ID, c.SessionID, false), res.Items), nil
}

// questionSummary is every question of an interaction with its options, so agents_list carries what answering one
// (interaction_answer) needs without a second call: "Send? (Send / Don't send)".
func questionSummary(ix proto.Interaction) string {
	parts := make([]string, 0, len(ix.Questions))
	for _, q := range ix.Questions {
		part := q.Question
		if len(q.Options) > 0 {
			part += "（" + optionLabels(q) + "）"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " / ")
}

// sendParams are an agent_send's session.send parameters.
func sendParams(session, text string, dismissNotice bool) map[string]any {
	p := map[string]any{"session_id": session, "text": text}
	if dismissNotice {
		p["dismiss_notice"] = true
	}
	return p
}

// silentRow: c stopped on the hub's own word that its turn had no output (silent_turns.go, #10187): not finished, and
// the owner's to continue, as the apps' Needs-you tray has an interrupted task.
func silentRow(c proto.Session) bool {
	return c.TurnError != "" && proto.SilentTurnKind(c.TurnErrorKind) && (c.State == proto.StateUnread || c.State == proto.StateIdle)
}

// agentsListState is c's state as agents_list filters and sorts it: a silentRow is needs_you, as the snapshot groups it
// (snapshotAgentGroup), never "done, not seen".
func agentsListState(c proto.Session) string {
	if silentRow(c) {
		return proto.StateNeedsYou
	}
	return c.State
}

// agentsListLabel is how agents_list names c's state: as the apps' rows do, a silentRow as what it is.
func agentsListLabel(c proto.Session) string {
	switch {
	case !silentRow(c):
		return stateNames[c.State]
	case c.TurnErrorKind == proto.TurnErrorStopped:
		return "被停下了，没做完" // i18n-ignore: agents_list output for the model, as the lines around it
	}
	return "中断了，没做完" // i18n-ignore: agents_list output for the model, as the lines around it
}
