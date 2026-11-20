package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// stoppedBoxAgent is agent sid of box as a node after #4754 lists it once it stopped: read-only, its terminal kept.
func stoppedBoxAgent(sid string) proto.Session {
	c := boxAgent(sid)
	c.Status, c.State = proto.StatusOffline, ""
	c.Capabilities, c.ReadOnlyReason = &proto.SessionCapabilities{Read: true, Terminal: true, StopCause: proto.StopIdle}, "stopped"
	return c
}

// resumingNode has box's node answer session.start as a resume does: the same session, running again.
func resumingNode(f *fakeHub) {
	f.answerNode(DeviceSessionStart, func(_ string, params json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		id, _ := p["resume_session_id"].(string)
		return map[string]any{"session": proto.Session{SessionID: id, Agent: "claude", Status: proto.StatusIdle}}, nil
	})
}

func TestAgentSendUncertainResume(t *testing.T) {
	for _, tc := range []struct {
		name       string
		full       bool
		failedCall string
	}{
		{"with first message", false, DeviceSessionStart},
		{"before first message", true, DeviceSessionStart},
		{"after compaction", true, DeviceSessionSend},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newQuietService(t)
			row := stoppedBoxAgent("sec00001")
			if tc.full {
				row.ContextTokens, row.ContextWindow = 190000, 200000
			}
			f.setSessions("box", row)
			resumingNode(f)
			f.answerNode(tc.failedCall, func(string, json.RawMessage) (any, error) { return nil, nodeAfterWrite })
			_, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": row.SessionID,
				"text": "Check the figures", "resume": true, "resume_why": "Use the existing context", "task": map[string]any{"title": "Figures"}})
			want := "check with agent_read before sending again"
			if tc.full && tc.failedCall == DeviceSessionStart {
				want = "may have resumed without a first message"
			}
			if !errors.Is(err, nodeAfterWrite) || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "start a replacement") {
				t.Fatalf("resume returned %v; want %q", err, want)
			}
			if starts, sends := len(startCalls(f)), sessionSends(f); starts != 1 || (sends == 1) != (tc.failedCall == DeviceSessionSend) || sends > 1 {
				t.Fatalf("starts = %d, sends = %d", starts, sends)
			}
			if _, kept := s.openTodoOfAgent("box", row.SessionID); !kept {
				t.Fatal("lost supervision of the possibly resumed agent")
			}
		})
	}
}

// startCalls are the session.start params the nodes were asked for.
func startCalls(f *fakeHub) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if c.Method != DeviceSessionStart {
			continue
		}
		var p map[string]any
		_ = json.Unmarshal(c.Params, &p)
		out = append(out, p)
	}
	return out
}

// routedTexts are the routed lines on task todoID's card in conversation conv, in order.
func routedTexts(s *Service, conv, todoID string) []string {
	items, _, _ := s.feed(context.Background(), conv, time.Time{})
	var out []string
	for _, it := range items {
		if it.Kind != feedRouted || it.Ref != todoID {
			continue
		}
		var body map[string]string
		_ = json.Unmarshal(it.Body, &body)
		out = append(out, body["from"]+": "+body["text"])
	}
	return out
}

// A plain message to a stopped agent is still refused with nothing sent or started (#4754), but the refusal now says
// how to choose: resume it with its context (resume, resume_why), start a new agent, or ask the user; it no longer
// says only the user can resume it (#8099).
func TestStoppedAgentRefusalSaysHowToChoose(t *testing.T) {
	s, f := newQuietService(t)
	f.setSessions("box", stoppedBoxAgent("stop0001"))
	_, err := s.runTool(t.Context(), "server", "conv1", "agent_send", sendArgs("stop0001", "go on", ""))
	if err == nil {
		t.Fatal("a plain message to a stopped agent went")
	}
	for _, want := range []string{"has stopped", "Nothing was sent", "resume true", "resume_why", "agent_start", "ask_user"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal lacks %q: %v", want, err)
		}
	}
	if strings.Contains(strings.ToLower(err.Error()), "only the user") {
		t.Fatalf("refusal still says only the user can resume: %v", err)
	}
	if len(sentToAgent(f)) != 0 || len(startCalls(f)) != 0 {
		t.Fatalf("sent %q, started %v", sentToAgent(f), startCalls(f))
	}
}

// From the main conversation, agent_send with resume and task resumes a stopped agent through the user's Resume's
// node call (session.start with resume_session_id, prompt, and resume_automatic, so the node keeps its launcher,
// directory and permission mode), the text as its first message; nothing goes through session.send. The agent becomes a supervised task whose
// card says it was resumed, and the card also says which agent was resumed and why.
func TestAssistantResumesAStoppedAgentOnATask(t *testing.T) {
	s, f := newQuietService(t)
	f.setSessions("box", stoppedBoxAgent("stop0001"))
	resumingNode(f)
	res, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "stop0001",
		"text": "根因清楚了，接着修", "resume": true, "resume_why": "它刚查清根因，修复要用它的上下文",
		"task": map[string]any{"title": "修复问题", "done_when": []string{"PR 合并"}, "expect_minutes": 60}})
	if err != nil {
		t.Fatal(err)
	}
	starts := startCalls(f)
	if len(starts) != 1 || !maps.Equal(starts[0], map[string]any{"resume_session_id": "stop0001", "prompt": "根因清楚了，接着修", "resume_automatic": true}) {
		t.Fatalf("session.start: %v", starts)
	}
	if sent := sentToAgent(f); len(sent) != 0 {
		t.Fatalf("session.send: %q", sent)
	}
	todo := onlyTodo(s)
	if todo.ID == "" || todo.Title != "修复问题" || len(todo.Agents) != 1 || todo.Agents[0].SessionID != "stop0001" ||
		todo.Supervision == nil || todo.Supervision.Supervisor != "conv1" {
		t.Fatalf("task: %+v", todo)
	}
	if !strings.Contains(res, "Resumed stop0001 on box") || !strings.Contains(res, todo.ID) {
		t.Fatalf("receipt: %s", res)
	}
	if got := taskCardWake(s, "conv1", todo.ID); got != wakeResumed {
		t.Fatalf("card wake %q", got)
	}
	routed := routedTexts(s, "conv1", todo.ID)
	if len(routed) != 2 || routed[0] != "assistant: 根因清楚了，接着修" || !strings.HasPrefix(routed[1], "assistant: ") ||
		!strings.Contains(routed[1], "stop0001") || !strings.Contains(routed[1], "它刚查清根因，修复要用它的上下文") {
		t.Fatalf("routed: %q", routed)
	}
}

// An idle-stopped agent still on an open task of this conversation is resumed without a new task: it stays on that task,
// whose card gets what it was told and the resume's note.
func TestAssistantResumesAStoppedAgentOnItsOwnTask(t *testing.T) {
	s, f := newQuietService(t)
	f.setSessions("box", stoppedBoxAgent("stop0001"))
	resumingNode(f)
	todo := onBoxTask(t, s, "stop0001", "conv1", false, "the fix is merged")
	res, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "stop0001",
		"text": "rework: the test is missing", "resume": true, "resume_why": "rework of its own change"})
	if err != nil {
		t.Fatal(err)
	}
	if len(startCalls(f)) != 1 || !strings.Contains(res, todo.ID) {
		t.Fatalf("%s %v", res, startCalls(f))
	}
	s.todos.mu.RLock()
	n := len(s.todos.todos)
	s.todos.mu.RUnlock()
	if n != 1 {
		t.Fatalf("%d todos", n)
	}
	routed := routedTexts(s, "conv1", todo.ID)
	if len(routed) != 2 || routed[0] != "assistant: rework: the test is missing" || !strings.Contains(routed[1], "rework of its own change") {
		t.Fatalf("routed: %q", routed)
	}
}

// A resume is refused before anything is asked, started or sent when it has no reason, would leave the agent
// unsupervised (no task given and none of its own), would take another conversation's or the user's task, its
// computer is offline, or the agent's context is nearly full on a runtime Persona cannot compact (#11137: Claude Code
// and Codex are compacted instead); each refusal says why, and the last two suggest a new agent.
func TestAssistantResumeRefusals(t *testing.T) {
	full := stoppedBoxAgent("full0001")
	full.ContextTokens, full.ContextWindow, full.Agent = 190_000, 200_000, "pi"
	roomy := stoppedBoxAgent("roomy001")
	roomy.ContextTokens, roomy.ContextWindow = 100_000, 200_000
	for _, c := range []struct {
		name, sid, why string
		task           bool
		offline        bool
		want           []string
	}{
		{"no reason", "free0001", "", true, false, []string{"resume_why is required"}},
		{"unsupervised", "free0001", "needs its context", false, false, []string{"on no open task of yours", "Give task"}},
		{"another conversation's task", "peer0001", "needs its context", false, false, []string{"of another conversation", "not yours to resume"}},
		{"the user's own todo", "user0001", "needs its context", false, false, []string{"user's own todo", "not yours to resume"}},
		{"offline", "free0001", "needs its context", true, true, []string{"could not resume free0001 on box", "agent_start"}},
		{"context nearly full", "full0001", "needs its context", true, false, []string{"context is nearly full (190000 of 200000 tokens)", "pi has no compaction", "agent_start"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := newQuietService(t)
			f.setSessions("box", stoppedBoxAgent("free0001"), stoppedBoxAgent("peer0001"), stoppedBoxAgent("user0001"), full, roomy)
			if c.offline {
				f.mu.Lock()
				f.down = map[string]bool{"box": true}
				f.mu.Unlock()
			}
			resumingNode(f)
			onBoxTask(t, s, "peer0001", "conv2", false, "peer criteria")
			onBoxTask(t, s, "user0001", "", false)
			args := map[string]any{"machine": "box", "session_id": c.sid, "text": "go on", "resume": true, "resume_why": c.why}
			if c.task {
				args["task"] = map[string]any{"title": "接着做"}
			}
			_, err := runJSON(t, s, "conv1", "agent_send", args)
			if err == nil {
				t.Fatal("resumed")
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal lacks %q: %v", want, err)
				}
			}
			if len(startCalls(f)) != 0 || len(sentToAgent(f)) != 0 {
				t.Fatalf("started %v, sent %q", startCalls(f), sentToAgent(f))
			}
		})
	}
	// Half full is room enough.
	if contextFull(roomy) || !contextFull(full) || contextFull(stoppedBoxAgent("unknown1")) {
		t.Fatal("contextFull")
	}
}

// When the node cannot resume the agent (no session record, launcher unavailable, open elsewhere), the tool says why,
// suggests a new agent, and the task made for the resume is taken back.
func TestAssistantResumeNodeRefusal(t *testing.T) {
	s, f := newQuietService(t)
	f.setSessions("box", stoppedBoxAgent("stop0001"))
	f.answerNode(DeviceSessionStart, func(string, json.RawMessage) (any, error) {
		return nil, errors.New("launcher work-claude is not available on this computer")
	})
	_, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "stop0001",
		"text": "go on", "resume": true, "resume_why": "needs its context", "task": map[string]any{"title": "接着做"}})
	if err == nil || !strings.Contains(err.Error(), "could not resume stop0001 on box: launcher work-claude is not available") ||
		!strings.Contains(err.Error(), "agent_start") || !strings.Contains(err.Error(), "no task was made") {
		t.Fatalf("node refusal: %v", err)
	}
	s.todos.mu.RLock()
	n := len(s.todos.todos)
	s.todos.mu.RUnlock()
	if n != 0 {
		t.Fatalf("%d todos left", n)
	}
}

// resume on an agent that runs changes nothing: the message goes through session.send as any other.
func TestResumeIsIgnoredForARunningAgent(t *testing.T) {
	s, f := newQuietService(t)
	f.setSessions("box", boxAgent("live0001"))
	if _, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "live0001",
		"text": "go on", "resume": true}); err != nil {
		t.Fatal(err)
	}
	if len(startCalls(f)) != 0 || len(sentToAgent(f)) != 1 {
		t.Fatalf("started %v, sent %q", startCalls(f), sentToAgent(f))
	}
}
