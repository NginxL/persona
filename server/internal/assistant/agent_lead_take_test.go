package assistant

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// leadTakeEnv is a quiet service whose conversation lead0001 is a direction's lead beside the main conversation conv1.
func leadTakeEnv(t *testing.T, box ...proto.Session) (*Service, *fakeHub) {
	t.Helper()
	s, f := newQuietService(t)
	f.known["lead0001"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead0001", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead0001"} })
	f.setSessions("box", box...)
	return s, f
}

// A lead resumes, with its own task, an agent whose earlier task (the main conversation's here, any direction's alike)
// is closed (#12953): the agent is resumed on a new task the lead originates and supervises (#13362), and the
// closed to-do and its supervisor are left as they were. No main conversation was involved in the hand-off.
func TestALeadResumesAFinishedAgentOfAnotherDirection(t *testing.T) {
	s, f := leadTakeEnv(t, stoppedBoxAgent("done0001"))
	resumingNode(f)
	earlier := onBoxTask(t, s, "done0001", "conv1", true, "the first check passes")
	res, err := runJSON(t, s, "lead0001", "agent_send", map[string]any{"machine": "box", "session_id": "done0001",
		"text": "run the check again, this time with a full context", "resume": true, "resume_why": "it knows this check",
		"task": map[string]any{"title": "Recheck", "done_when": []string{"the check passes with a full context"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(startCalls(f)) != 1 || !strings.Contains(res, "Resumed done0001 on box") {
		t.Fatalf("%s %v", res, startCalls(f))
	}
	now, ok := s.openTodoOfAgent("box", "done0001")
	if !ok || now.ID == earlier.ID || now.Origin != "lead0001" || now.Supervision == nil || now.Supervision.Supervisor != "lead0001" {
		t.Fatalf("not the lead's task: %+v", now)
	}
	before, _ := s.todoNow(earlier.ID)
	if before.DoneAt == nil || before.Origin != "conv1" || before.Supervision == nil || before.Supervision.Supervisor != "conv1" {
		t.Fatalf("the earlier to-do changed: %+v", before)
	}
}

// What stays out of a lead's reach, each refused before anything is started, sent or made: an agent on an open task
// another conversation supervises (the refusal says whose), on the user's own to-do, waiting for the user, or busy on
// untracked work that Persona did not start. Only the lead is held to this: the main conversation is not.
func TestALeadTakesNoAgentOutsideItsBoundary(t *testing.T) {
	waiting := boxAgent("asks0001")
	waiting.State, waiting.PendingInteractions = proto.StateNeedsYou, 1
	busy := boxAgent("busy0001")
	busy.Status, busy.State = proto.StatusWorking, proto.StateWorking
	for _, c := range []struct {
		name, sid string
		resume    bool
		want      []string
	}{
		{"another conversation's open task, resumed", "peer0001", true, []string{"works for task", "supervised by the main conversation (conv1)", "stays theirs"}},
		{"another conversation's open task", "peer0002", false, []string{"supervised by the direction lead lead0002"}},
		{"the user's own to-do", "user0001", false, []string{"user's own todo"}},
		{"waiting for the user", "asks0001", false, []string{"is waiting for the user"}},
		{"busy on untracked work", "busy0001", false, []string{"busy on work no task tracks"}},
	} {
		for _, tool := range []string{"agent_send", "agent_supervise"} {
			if c.resume && tool == "agent_supervise" {
				continue
			}
			t.Run(c.name+"/"+tool, func(t *testing.T) {
				s, f := leadTakeEnv(t, stoppedBoxAgent("peer0001"), boxAgent("peer0002"), boxAgent("user0001"), waiting, busy)
				setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead0001", "lead0002"} })
				resumingNode(f)
				onBoxTask(t, s, "peer0001", "conv1", false, "main's criteria")
				onBoxTask(t, s, "peer0002", "lead0002", false, "the other lead's criteria")
				onBoxTask(t, s, "user0001", "", false)
				n := todoCount(s)
				args := map[string]any{"machine": "box", "session_id": c.sid, "title": "Take it"}
				if tool == "agent_send" {
					args = map[string]any{"machine": "box", "session_id": c.sid, "text": "go on", "task": map[string]any{"title": "Take it"}}
					if c.resume {
						args["resume"], args["resume_why"] = true, "needs its context"
					}
				}
				_, err := runJSON(t, s, "lead0001", tool, args)
				if err == nil {
					t.Fatal("taken")
				}
				for _, want := range c.want {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("refusal lacks %q: %v", want, err)
					}
				}
				if strings.Contains(err.Error(), "main conversation to") || strings.Contains(err.Error(), "from the main conversation") {
					t.Fatalf("the refusal sends the lead to the main conversation: %v", err)
				}
				if len(startCalls(f)) != 0 || len(sentToAgent(f)) != 0 || todoCount(s) != n {
					t.Fatalf("started %v, sent %q, %d todos (was %d)", startCalls(f), sentToAgent(f), todoCount(s), n)
				}
			})
		}
	}
}

// A lead adopts the agents within its boundary itself (#12953): one it started with agent_start that runs on no task
// (Persona started it, so busy is no bar), and an idle one whose earlier task is closed; both become its tasks.
func TestALeadAdoptsItsOwnAgents(t *testing.T) {
	mine := boxAgent("mine0001")
	mine.Status, mine.State, mine.StartedByPersona = proto.StatusWorking, proto.StateWorking, true
	s, _ := leadTakeEnv(t, mine, boxAgent("idle0001"))
	onBoxTask(t, s, "idle0001", "conv1", true, "earlier criteria")
	for _, sid := range []string{"mine0001", "idle0001"} {
		res, err := runJSON(t, s, "lead0001", "agent_supervise", map[string]any{"machine": "box", "session_id": sid,
			"title": "Own work", "done_when": []string{"it is done"}})
		if err != nil {
			t.Fatalf("%s: %v", sid, err)
		}
		todo, ok := s.openTodoOfAgent("box", sid)
		if !ok || todo.Supervision == nil || todo.Supervision.Supervisor != "lead0001" || !strings.Contains(res, todo.ID) {
			t.Fatalf("%s: %q %+v", sid, res, todo)
		}
	}
}

// The main conversation is held to none of the lead's bounds: it still adopts an agent waiting for the user or busy on
// untracked work, as before.
func TestTheMainConversationKeepsAdoptingAsBefore(t *testing.T) {
	waiting := boxAgent("asks0001")
	waiting.State, waiting.PendingInteractions = proto.StateNeedsYou, 1
	busy := boxAgent("busy0001")
	busy.Status, busy.State = proto.StatusWorking, proto.StateWorking
	s, _ := leadTakeEnv(t, waiting, busy)
	for _, sid := range []string{"asks0001", "busy0001"} {
		if _, err := runJSON(t, s, "conv1", "agent_supervise", map[string]any{"machine": "box", "session_id": sid}); err != nil {
			t.Fatalf("%s: %v", sid, err)
		}
		if todo, ok := s.openTodoOfAgent("box", sid); !ok || todo.Supervision == nil || todo.Supervision.Supervisor != "conv1" {
			t.Fatalf("%s: %+v", sid, todo)
		}
	}
}
