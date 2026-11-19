package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const runtimePlan = `{"tasks":[{"title":"Check the build","prompt":"Check the build result","machine":"box","cwd":"/code/app","readonly":true}]}`

func TestRuntimeTaskPlanDeliversCompletionToCompatibleSupervisor(t *testing.T) {
	for _, route := range []string{"main", "background"} {
		t.Run(route, func(t *testing.T) {
			ctx := context.Background()
			s, f := newQuietService(t)
			useMain := route == "main"
			s.noBackground = false
			setSettings(t, s, func(v *Settings) {
				v.MainSupervisesOwnTasks = &useMain
				v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "background", "server"
			})
			persona := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Agent: "claude", Status: proto.StatusIdle}
			background := proto.Session{SessionID: "background", MachineID: "server", Assistant: true, Agent: "codex", Status: proto.StatusIdle}
			side := proto.Session{SessionID: "agy-side", MachineID: "server", Assistant: true, SourceKind: proto.SourceRuntime, Agent: "agy", Launcher: "agy", Status: proto.StatusIdle}
			f.setSessions("server", persona, background, side)
			f.mu.Lock()
			f.known[background.SessionID] = true
			f.mu.Unlock()
			guardRuntimeFollowUp(f, side.SessionID)

			result, err := s.toolTaskPlan(ctx, side.SessionID, json.RawMessage(runtimePlan))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result, "conversation supervises them") && !strings.Contains(result, "background supervisor follows them up") {
				t.Fatalf("plan did not explain who reviews the task: %s", result)
			}
			waitFor(t, "the worker to link", func() bool { return len(onlyTodo(s).Agents) == 1 })
			task := onlyTodo(s)
			if task.Origin != side.SessionID || task.Supervision == nil || task.Supervision.Supervisor != persona.SessionID {
				t.Fatalf("task origin or supervisor is wrong: %q, %+v", task.Origin, task.Supervision)
			}
			reviewer := persona.SessionID
			if !useMain {
				reviewer = background.SessionID
			}
			worker := task.Agents[0]
			const report = "Build finished successfully."
			if _, err := s.reportToSupervisor(ctx, worker.MachineID, worker.SessionID, json.RawMessage(`{"kind":"done","text":"`+report+`"}`), time.Now()); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the worker's report with policy at the reviewer", func() bool {
				f.mu.Lock()
				defer f.mu.Unlock()
				return slices.ContainsFunc(f.sends, func(p map[string]any) bool {
					text, _ := p["text"].(string)
					return p["session_id"] == reviewer && strings.Contains(text, report) &&
						p["supervision_instructions"] == prompts.RenderSupervisionInstructions()
				})
			})
			if f.sentTo(side.SessionID) != 0 || s.queuedFor(side.SessionID) != 0 {
				t.Fatal("the runtime side chat received or queued a supervision wake")
			}
			sendRuntimeOwnerFollowUp(t, s, side.SessionID)
			if _, err := runJSON(t, s, reviewer, "task_review", map[string]any{
				"todo_id": task.ID, "verdict": "accepted", "note": report,
			}); err != nil {
				t.Fatalf("the selected supervisor could not review the task: %v", err)
			}
			if todoNow(s, task.ID).DoneAt == nil {
				t.Fatal("the supervisor's review did not close the task")
			}
		})
	}
}

func TestRuntimeScheduledLeaderAcceptsOwnerFollowUp(t *testing.T) {
	ctx := context.Background()
	s, f := newQuietService(t)
	side := proto.Session{SessionID: "scheduled-run", MachineID: "server", Assistant: true, SourceKind: proto.SourceRuntime, Agent: "dsh", Launcher: "dsh", Status: proto.StatusIdle}
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{side.SessionID} })
	f.setSessions("server", side)
	s.originSet(ctx, side.MachineID, side.SessionID, OriginTask, "scheduled-check", "")
	guardRuntimeFollowUp(f, side.SessionID)

	sendRuntimeOwnerFollowUp(t, s, side.SessionID)
	if !s.isLeader(ctx, side.SessionID) || f.sentTo(side.SessionID) != 1 {
		t.Fatal("the scheduled leader did not receive the owner's reply")
	}
}

func TestRuntimeTaskPlanWithoutCompatibleReviewerStaysOpen(t *testing.T) {
	ctx := context.Background()
	s, f := newQuietService(t)
	side := proto.Session{SessionID: "runtime-side", MachineID: "server", Assistant: true, SourceKind: proto.SourceRuntime}
	main := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, SourceKind: proto.SourceRuntime}
	f.setSessions("server", main, side)
	_, err := s.toolTaskPlan(ctx, side.SessionID, json.RawMessage(runtimePlan))
	if invalid, ok := err.(planInvalid); !ok || !strings.Contains(i18n.Localize(invalid.error, i18n.En), "Claude Code or Codex") {
		t.Fatalf("dispatch without a reviewer = %v", err)
	}
	if todoCount(s) != 0 {
		t.Fatal("a task started without a reviewer")
	}
	s.tasks.mu.Lock()
	var planID string
	for id, plan := range s.tasks.plans {
		if plan.Conversation == side.SessionID && plan.State == planOpen {
			planID = id
		}
	}
	s.tasks.mu.Unlock()
	if planID == "" {
		t.Fatal("the plan did not remain open")
	}
	main.SourceKind, main.Agent = "", "claude"
	f.setSessions("server", main, side)
	started, err := s.startPlan(ctx, planID, nil, false)
	if err != nil || started.State != planStarted {
		t.Fatalf("retry with compatible reviewer = %+v, %v", started, err)
	}
	waitFor(t, "the retried plan's worker", func() bool { return len(onlyTodo(s).Agents) == 1 })
	if task := onlyTodo(s); task.Supervision == nil || task.Supervision.Supervisor != main.SessionID {
		t.Fatalf("retried plan supervisor = %+v", task.Supervision)
	}
}

func guardRuntimeFollowUp(f *fakeHub, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.known[id] = true
	f.onAssistantSend = func(f *fakeHub, p map[string]any) {
		if p["session_id"] == id && p["supervision_instructions"] != "" {
			f.failAssistant = &rpc.Error{Code: assistantwire.CodeSupervisionRuntime, Message: "The runtime cannot receive separate instructions."}
		}
	}
}

func sendRuntimeOwnerFollowUp(t *testing.T, s *Service, id string) {
	t.Helper()
	ctx := context.Background()
	p, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: id, Human: true,
		Params: map[string]any{"text": "Continue with the suggested next step.", "client_msg_id": "follow-up", "supervision_instructions": "caller input"}})
	if err != nil {
		t.Fatal(err)
	}
	if p["supervision_instructions"] != "" || p["launcher"] != nil {
		t.Fatalf("the reply needs the runtime's own launcher and empty supervision policy: %v", p)
	}
	p["session_id"] = id
	if err := s.call(ctx, "server", DeviceAssistantSend, p, nil); err != nil {
		t.Fatalf("owner follow-up: %v", err)
	}
}

func TestSupervisionRuntimeRefusalStopsRelay(t *testing.T) {
	for _, delivery := range []string{"immediate", "queued"} {
		t.Run(delivery, func(t *testing.T) {
			ctx := context.Background()
			s, f, task := newSupervisedService(t)
			f.mu.Lock()
			f.hello.Locale = "en"
			f.failAssistant = &rpc.Error{Code: assistantwire.CodeSupervisionRuntime, Message: "The runtime cannot receive separate instructions."}
			f.mu.Unlock()
			tries := relayTries(f, "conv1")
			idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
			if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
				v.State = reviewPending
				return true
			}); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			if delivery == "queued" {
				s.queueRelay("conv1", relayMsg{Tool: superviseTool, Text: "A task is ready for review.", At: now})
				s.drainRelays("server", []proto.Session{idle})
			} else {
				task = todoNow(s, task.ID)
				if !s.deliverDigest(ctx, s.settings(ctx), "conv1", watchSignals(task, nil, now), "", now, time.Time{}) {
					t.Fatal("the immediate refusal was not handled")
				}
			}
			waitFor(t, "the delivery failure to reach the owner", func() bool {
				f.mu.Lock()
				defer f.mu.Unlock()
				return slices.ContainsFunc(f.pushes, func(p Notification) bool {
					return p.SessionID == "conv1" && strings.Contains(p.Body, "Open the task")
				})
			})
			for range 3 {
				s.drainRelays("server", []proto.Session{idle})
				if s.queuedFor("conv1") != 0 || drainingNow(s, "conv1") {
					t.Fatal("the permanently refused message is still eligible for delivery")
				}
			}
			if got := triesOf(f, tries); len(got) != 1 {
				t.Fatalf("attempts = %v, want one", got)
			}
			if got := todoNow(s, task.ID); got.DoneAt != nil || got.Supervision.State != reviewPending {
				t.Fatalf("the task must remain available for review: %+v", got)
			}
		})
	}
}
