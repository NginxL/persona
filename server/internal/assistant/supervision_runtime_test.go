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
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			ctx := context.Background()
			s, f := newQuietService(t)
			persona := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Agent: agent, Status: proto.StatusIdle}
			side := proto.Session{SessionID: "agy-side", MachineID: "server", Assistant: true, SourceKind: proto.SourceRuntime, Agent: "agy", Launcher: "agy", Status: proto.StatusIdle}
			f.setSessions("server", persona, side)
			guardRuntimeFollowUp(f, side.SessionID)

			result, err := s.toolTaskPlan(ctx, side.SessionID, json.RawMessage(runtimePlan))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result, "conversation supervises them") {
				t.Fatalf("plan did not explain who reviews the task: %s", result)
			}
			waitFor(t, "the worker to link", func() bool { return len(onlyTodo(s).Agents) == 1 })
			task := onlyTodo(s)
			if task.Origin != side.SessionID || task.Supervision == nil || task.Supervision.Supervisor != persona.SessionID {
				t.Fatalf("task origin or supervisor is wrong: %q, %+v", task.Origin, task.Supervision)
			}
			reviewer := persona.SessionID
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

// A task a runtime-backed chat already supervises (it was handed out before such a chat's tasks went to the main
// conversation's route) is followed up by that route as well: its supervision message neither goes to the chat nor ends
// in a "did not send" push, whether it goes out at once or waits in the queue.
func TestRuntimeSupervisedStockTaskGoesToOwnSupervisor(t *testing.T) {
	for _, route := range []string{"main", "background"} {
		for _, delivery := range []string{"immediate", "queued"} {
			t.Run(route+"/"+delivery, func(t *testing.T) {
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

				if _, err := s.toolTaskPlan(ctx, side.SessionID, json.RawMessage(runtimePlan)); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "the worker to link", func() bool { return len(onlyTodo(s).Agents) == 1 })
				task := onlyTodo(s)
				// What an earlier release recorded: the runtime chat itself as the supervisor.
				if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
					v.Supervisor = side.SessionID
					return true
				}); err != nil {
					t.Fatal(err)
				}
				reviewer := persona.SessionID
				if !useMain {
					reviewer = background.SessionID
				}
				if delivery == "queued" {
					f.setSessions("server", persona, background, func() proto.Session { w := side; w.Status = proto.StatusWorking; return w }())
				}
				worker := task.Agents[0]
				const report = "Build finished successfully."
				if _, err := s.reportToSupervisor(ctx, worker.MachineID, worker.SessionID, json.RawMessage(`{"kind":"done","text":"`+report+`"}`), time.Now()); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "the worker's report at the route's reviewer", func() bool {
					f.mu.Lock()
					defer f.mu.Unlock()
					return slices.ContainsFunc(f.sends, func(p map[string]any) bool {
						text, _ := p["text"].(string)
						return p["session_id"] == reviewer && strings.Contains(text, report) &&
							p["supervision_instructions"] == prompts.RenderSupervisionInstructions()
					})
				})
				if f.sentTo(side.SessionID) != 0 || s.queuedFor(side.SessionID) != 0 {
					t.Fatal("the runtime chat received or queued a supervision message")
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				for _, p := range f.pushes {
					if p.SessionID == side.SessionID {
						t.Fatalf("the owner was told a message was not sent: %+v", p)
					}
				}
			})
		}
	}
}

// A supervision message queued for a runtime-backed chat before the hub knew that chat cannot take it moves to the
// main conversation's route when it comes due, instead of being refused there.
func TestRuntimeQueuedSupervisionMovesToOwnSupervisor(t *testing.T) {
	ctx := context.Background()
	s, f, task := newSupervisedService(t)
	side := proto.Session{SessionID: "agy-side", MachineID: "server", Assistant: true, SourceKind: proto.SourceRuntime, Agent: "agy", Launcher: "agy", Status: proto.StatusIdle}
	main := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Agent: "claude", Status: proto.StatusIdle}
	f.setSessions("server", main, side)
	f.mu.Lock()
	f.known[side.SessionID] = true
	f.mu.Unlock()
	guardRuntimeFollowUp(f, side.SessionID)
	if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
		v.Supervisor, v.State = side.SessionID, reviewPending
		return true
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	task = todoNow(s, task.ID)
	signals := watchSignals(task, nil, now)
	if len(signals) == 0 {
		t.Fatal("the task raises no signal")
	}
	s.queueRelay(side.SessionID, relayMsg{Tool: superviseTool, Text: "A task is ready for review.", Signals: encodeDigestSignals(signals), At: now})
	s.drainRelays("server", []proto.Session{side})
	waitFor(t, "the message at the main conversation", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return slices.ContainsFunc(f.sends, func(p map[string]any) bool { return p["session_id"] == main.SessionID })
	})
	waitFor(t, "the runtime chat's queue to empty", func() bool { return s.queuedFor(side.SessionID) == 0 })
	if f.sentTo(side.SessionID) != 0 {
		t.Fatal("the runtime chat was sent a supervision message")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.pushes {
		if p.SessionID == side.SessionID {
			t.Fatalf("the owner was told a message was not sent: %+v", p)
		}
	}
}

// A leader conversation on a runtime (a scheduled chat listed in assistant.leaders) is routed around, so the route's
// supervisor has to be able to review what it supervised.
func TestRuntimeLeaderTasksAreReviewedByBackgroundSupervisor(t *testing.T) {
	ctx := context.Background()
	s, f := newQuietService(t)
	off := false
	s.noBackground = false
	setSettings(t, s, func(v *Settings) {
		v.MainSupervisesOwnTasks = &off
		v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "background", "server"
		v.Leaders = []string{"agy-lead"}
	})
	persona := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Agent: "claude", Status: proto.StatusIdle}
	background := proto.Session{SessionID: "background", MachineID: "server", Assistant: true, Agent: "codex", Status: proto.StatusIdle}
	lead := proto.Session{SessionID: "agy-lead", MachineID: "server", Assistant: true, SourceKind: proto.SourceRuntime, Agent: "agy", Launcher: "agy", Status: proto.StatusIdle}
	f.setSessions("server", persona, background, lead)
	f.mu.Lock()
	f.known[background.SessionID] = true
	f.mu.Unlock()
	todo, err := s.newTodo(ctx, "Check", "", lead.SessionID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.Supervisor = lead.SessionID; return true }); err != nil {
		t.Fatal(err)
	}
	if got := s.digestTarget(ctx, s.settings(ctx), superviseSignal{Todo: todo.ID, Kind: sigReview}); got != background.SessionID {
		t.Fatalf("signal goes to %q, want the background supervisor", got)
	}
	if !s.backgroundSupervises(ctx, background.SessionID, todoNow(s, todo.ID)) {
		t.Fatal("the background supervisor may not review the task it is told about")
	}
}

func capableRuntimeCaps() *proto.SessionCapabilities {
	return &proto.SessionCapabilities{Read: true, Send: true, Interrupt: true, Approve: true, SupervisionPolicy: true}
}

func capableRuntimeTestSide() proto.Session {
	return proto.Session{
		SessionID: "agy-side", MachineID: "server", Assistant: true, SourceKind: proto.SourceRuntime,
		Agent: "agy", Launcher: "agy", Status: proto.StatusIdle, Capabilities: capableRuntimeCaps(),
	}
}

func TestCapableRuntimeWithoutMainSupervisesOwnTasks(t *testing.T) {
	ctx := context.Background()
	s, f := newQuietService(t)
	side := capableRuntimeTestSide()
	f.setSessions("server", side)
	f.mu.Lock()
	f.known[side.SessionID] = true
	f.mu.Unlock()
	setSettings(t, s, func(v *Settings) { v.Main = "" })

	if _, err := s.toolTaskPlan(ctx, side.SessionID, json.RawMessage(runtimePlan)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the worker to link without main", func() bool { return len(onlyTodo(s).Agents) == 1 })
	task := onlyTodo(s)
	if task.Origin != side.SessionID || task.Supervision == nil || task.Supervision.Supervisor != side.SessionID {
		t.Fatalf("capable runtime without main lost its own supervision: %q, %+v", task.Origin, task.Supervision)
	}
	if _, err := runJSON(t, s, side.SessionID, "task_review", map[string]any{
		"todo_id": task.ID, "verdict": "accepted", "note": "verified without main",
	}); err != nil {
		t.Fatalf("capable runtime without main could not accept its own task: %v", err)
	}
	if todoNow(s, task.ID).DoneAt == nil {
		t.Fatal("capable runtime without main did not close its accepted task")
	}
}

func TestCapableRuntimeSideChatSurvivesRehome(t *testing.T) {
	ctx := context.Background()
	s, f := newQuietService(t)
	persona := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	side := capableRuntimeTestSide()
	f.setSessions("server", persona, side)
	f.mu.Lock()
	f.known[side.SessionID] = true
	f.mu.Unlock()
	if _, err := s.toolTaskPlan(ctx, side.SessionID, json.RawMessage(runtimePlan)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the worker to link before rehome", func() bool { return len(onlyTodo(s).Agents) == 1 })
	task := onlyTodo(s)
	s.backfillPlanSupervisors(ctx)
	if got := todoNow(s, task.ID).Supervision; got == nil || got.Supervisor != side.SessionID {
		t.Fatalf("rehome rewrote the explicit capable-runtime topology: %+v", got)
	}
	if _, err := runJSON(t, s, side.SessionID, "task_review", map[string]any{
		"todo_id": task.ID, "verdict": "accepted", "note": "verified after rehome",
	}); err != nil {
		t.Fatalf("capable runtime could not accept after rehome: %v", err)
	}
	if todoNow(s, task.ID).DoneAt == nil {
		t.Fatal("capable runtime acceptance after rehome did not close the task")
	}
}

// A runtime session that advertises capabilities.supervision_policy (e.g. Antigravity) supervises its own planned
// tasks, receives separate supervision_instructions on supervision deliveries, and loses the policy once its last
// open task closes.
func TestCapableRuntimeSideChatSupervisesOwnTasks(t *testing.T) {
	ctx := context.Background()
	s, f := newQuietService(t)
	persona := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Agent: "claude", Status: proto.StatusIdle}
	side := proto.Session{
		SessionID:    "agy-side",
		MachineID:    "server",
		Assistant:    true,
		SourceKind:   proto.SourceRuntime,
		Agent:        "agy",
		Launcher:     "agy",
		Status:       proto.StatusIdle,
		Capabilities: capableRuntimeCaps(),
	}
	f.setSessions("server", persona, side)
	f.mu.Lock()
	f.known[side.SessionID] = true
	f.mu.Unlock()

	result, err := s.toolTaskPlan(ctx, side.SessionID, json.RawMessage(runtimePlan))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "You supervise them") {
		t.Fatalf("capable runtime side chat was not kept as supervisor: %s", result)
	}
	waitFor(t, "the worker to link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	task := onlyTodo(s)
	if task.Origin != side.SessionID || task.Supervision == nil || task.Supervision.Supervisor != side.SessionID {
		t.Fatalf("task origin or supervisor is wrong: %q, %+v", task.Origin, task.Supervision)
	}
	worker := task.Agents[0]
	const report = "Build finished cleanly."
	if _, err := s.reportToSupervisor(ctx, worker.MachineID, worker.SessionID, json.RawMessage(`{"kind":"done","text":"`+report+`"}`), time.Now()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the worker report with policy at the capable runtime side chat", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return slices.ContainsFunc(f.sends, func(p map[string]any) bool {
			text, _ := p["text"].(string)
			return p["session_id"] == side.SessionID && strings.Contains(text, report) &&
				p["supervision_instructions"] == prompts.RenderSupervisionInstructions()
		})
	})
	if _, err := runJSON(t, s, side.SessionID, "task_review", map[string]any{
		"todo_id": task.ID, "verdict": "accepted", "note": report,
	}); err != nil {
		t.Fatalf("capable runtime side chat could not review its own task: %v", err)
	}
	if todoNow(s, task.ID).DoneAt == nil {
		t.Fatal("the capable runtime side chat's review did not close the task")
	}
	// Once its last open task closes, ordinary follow-ups receive empty supervision_instructions so the runtime clears the role.
	if got := mustSupervisionInstructions(t, s, ctx, "server", side.SessionID); got != "" {
		t.Fatalf("closed-task side chat retained supervision policy (%d bytes)", len(got))
	}
}

// A capable runtime leader keeps its own supervision signals (not diverted to the background supervisor), and a
// capable runtime background supervisor stays recognized by backgroundThere and reviews main-chat tasks with policy.
func TestCapableRuntimeLeaderAndBackgroundSupervisor(t *testing.T) {
	ctx := context.Background()
	s, f := newQuietService(t)
	off := false
	s.noBackground = false
	setSettings(t, s, func(v *Settings) {
		v.MainSupervisesOwnTasks = &off
		v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "agy-bg", "server"
		v.Leaders = []string{"agy-lead"}
	})
	main := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, SourceKind: proto.SourceRuntime, Agent: "agy", Launcher: "agy", Status: proto.StatusIdle, Capabilities: capableRuntimeCaps()}
	bg := proto.Session{SessionID: "agy-bg", MachineID: "server", Assistant: true, SourceKind: proto.SourceRuntime, Agent: "agy", Launcher: "agy", Status: proto.StatusIdle, Capabilities: capableRuntimeCaps()}
	lead := proto.Session{SessionID: "agy-lead", MachineID: "server", Assistant: true, SourceKind: proto.SourceRuntime, Agent: "agy", Launcher: "agy", Status: proto.StatusIdle, Capabilities: capableRuntimeCaps()}
	dshSide := proto.Session{SessionID: "dsh-side", MachineID: "server", Assistant: true, SourceKind: proto.SourceRuntime, Agent: "dsh", Launcher: "dsh", Status: proto.StatusIdle}
	f.setSessions("server", main, bg, lead, dshSide)
	f.mu.Lock()
	f.known[bg.SessionID] = true
	f.known[lead.SessionID] = true
	f.mu.Unlock()

	cfg := s.settings(ctx)
	if !s.backgroundThere(ctx, cfg, bg.SessionID) {
		t.Fatal("capable runtime background supervisor was treated as gone")
	}
	// A non-capable runtime background supervisor is still treated as gone so it can be replaced.
	if s.backgroundThere(ctx, Settings{MachineID: "server", BackgroundSupervisor: dshSide.SessionID, BackgroundSupervisorMachineID: "server"}, dshSide.SessionID) {
		t.Fatal("non-capable runtime background supervisor was treated as valid")
	}
	// Main keeps its policy for new main-supervised work (#5732); background and leader receive their own policies too.
	if got := mustSupervisionInstructions(t, s, ctx, "server", main.SessionID); got != prompts.RenderSupervisionInstructions() {
		t.Fatalf("main chat lost its supervision policy (%d bytes)", len(got))
	}
	for _, id := range []string{bg.SessionID, lead.SessionID} {
		if got := mustSupervisionInstructions(t, s, ctx, "server", id); got != prompts.RenderSupervisionInstructions() {
			t.Fatalf("%s did not receive supervision policy", id)
		}
	}
	// Leader task stays with the capable leader; background supervisor does not usurp it.
	leadTodo, err := s.newTodo(ctx, "Leader check", "", lead.SessionID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.updateSupervision(ctx, leadTodo.ID, func(v *Supervision) bool { v.Supervisor = lead.SessionID; return true }); err != nil {
		t.Fatal(err)
	}
	if got := s.digestTarget(ctx, cfg, superviseSignal{Todo: leadTodo.ID, Kind: sigReview}); got != lead.SessionID {
		t.Fatalf("capable leader signal went to %q, want %q", got, lead.SessionID)
	}
	if s.backgroundSupervises(ctx, bg.SessionID, todoNow(s, leadTodo.ID)) {
		t.Fatal("background supervisor usurped a capable runtime leader's task")
	}
	// A non-capable DSH side chat's task still routes to the compatible main/background route (here, capable AGY background).
	if _, err := s.toolTaskPlan(ctx, dshSide.SessionID, json.RawMessage(runtimePlan)); err != nil {
		t.Fatalf("non-capable runtime side chat could not route to capable AGY background supervisor: %v", err)
	}
	todoByOrigin := func(origin string) (Todo, bool) {
		s.todos.mu.RLock()
		defer s.todos.mu.RUnlock()
		for _, td := range s.todos.todos {
			if td.Origin == origin {
				return *td, true
			}
		}
		return Todo{}, false
	}
	waitFor(t, "the worker to link", func() bool {
		td, ok := todoByOrigin(dshSide.SessionID)
		return ok && len(td.Agents) == 1
	})
	routed, _ := todoByOrigin(dshSide.SessionID)
	if routed.Supervision == nil || routed.Supervision.Supervisor != main.SessionID {
		t.Fatalf("routed supervisor = %+v, want main route %q", routed.Supervision, main.SessionID)
	}
	if got := s.digestTarget(ctx, cfg, superviseSignal{Todo: routed.ID, Kind: sigReview}); got != bg.SessionID {
		t.Fatalf("routed digest target = %q, want capable background supervisor %q", got, bg.SessionID)
	}
}

// When a capable runtime fails closed at delivery time (e.g. older agy CLI lacking additionalDirectories or disk write
// failure returning assistant_supervision_runtime), the hub stops retrying and notifies the owner while leaving the
// task open for review.
func TestCapableRuntimeFailClosedRefusalStopsRelay(t *testing.T) {
	ctx := context.Background()
	s, f, task := newSupervisedService(t)
	agyMain := proto.Session{
		SessionID:    "conv1",
		MachineID:    "server",
		Assistant:    true,
		SourceKind:   proto.SourceRuntime,
		Agent:        "agy",
		Launcher:     "agy",
		Status:       proto.StatusIdle,
		Capabilities: capableRuntimeCaps(),
	}
	f.setSessions("server", agyMain)
	f.mu.Lock()
	f.hello.Locale = "en"
	f.failAssistant = &rpc.Error{Code: assistantwire.CodeSupervisionRuntime, Message: "Antigravity CLI does not support separate supervision policy directories"}
	f.mu.Unlock()
	tries := relayTries(f, "conv1")
	if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
		v.State = reviewPending
		return true
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	task = todoNow(s, task.ID)
	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", watchSignals(task, nil, now), "", now, time.Time{}) {
		t.Fatal("the capable runtime fail-closed refusal was not handled")
	}
	waitFor(t, "the delivery failure notice", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return slices.ContainsFunc(f.pushes, func(p Notification) bool {
			return p.SessionID == "conv1" && strings.Contains(p.Body, "Open the task")
		})
	})
	if got := triesOf(f, tries); len(got) != 1 {
		t.Fatalf("attempts = %v, want one", got)
	}
	if got := todoNow(s, task.ID); got.DoneAt != nil || got.Supervision.State != reviewPending {
		t.Fatalf("task must remain pending review: %+v", got)
	}
}
