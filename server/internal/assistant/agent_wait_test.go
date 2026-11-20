package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nwverse/persona/server/internal/proto"
	"strings"
	"testing"
	"time"
)

func inlineItems(id string, at time.Time) []proto.Item {
	return []proto.Item{
		{ID: "old", Role: "assistant", Kind: proto.KindText, Text: "old answer", TS: at.Add(-time.Second)},
		{ID: "input", Role: "user", Kind: proto.KindText, ClientMsgID: id, Text: "read sample", TS: at},
		{ID: "reply", Role: "assistant", Kind: proto.KindText, Text: "sample checked", TS: at.Add(time.Millisecond)},
	}
}
func TestAgentWaitValidatesAndCaps(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
		bad  bool
	}{
		{`{}`, 0, false}, {`{"wait_seconds":0}`, 0, false}, {`{"wait_seconds":3}`, 3 * time.Second, false},
		{`{"wait_seconds":9223372036854775807}`, agentWaitMost, false},
		{`{"wait_seconds":-1}`, 0, true}, {`{"wait_seconds":0.5}`, 0, true}, {`{"wait_seconds":"1"}`, 0, true},
	} {
		got, err := agentWaitDuration(json.RawMessage(tc.raw))
		if got != tc.want || (err != nil) != tc.bad {
			t.Fatalf("%s: %v %v", tc.raw, got, err)
		}
	}
}
func TestAgentWaitExactInputAndFreshCompletion(t *testing.T) {
	now := time.Now()
	items := inlineItems("mine", now)
	for _, tc := range []struct {
		name, id, status string
		updated          time.Time
		items            []proto.Item
		done             bool
	}{
		{"done", "mine", proto.StatusDone, now.Add(time.Second), items, true},
		{"idle", "mine", proto.StatusIdle, now.Add(time.Second), items, true},
		{"wrong input", "other", proto.StatusIdle, now.Add(time.Second), items, false},
		{"cached idle", "mine", proto.StatusIdle, now, items, false},
		{"idle at reply time", "mine", proto.StatusIdle, items[2].TS, items, false},
		{"done at reply time", "mine", proto.StatusDone, items[2].TS, items, false},
		{"working", "mine", proto.StatusWorking, now.Add(time.Second), items, false},
		{"no reply", "mine", proto.StatusIdle, now.Add(time.Second), items[:2], false},
		{"later input", "mine", proto.StatusIdle, now.Add(time.Second), append(append([]proto.Item{}, items...), proto.Item{ID: "later", Role: "user", Kind: proto.KindText}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, ok := agentWaitObservation(tc.items, tc.id, proto.Session{Status: tc.status, UpdatedAt: tc.updated})
			if ok != tc.done || ok && r.Reply != "sample checked" {
				t.Fatalf("%+v %v", r, ok)
			}
		})
	}
}
func TestAgentWaitQuestion(t *testing.T) {
	s, f := newTaskService(t)
	now := time.Now()
	f.sessions["box"] = []proto.Session{{SessionID: "s1", Status: proto.StatusBlocked, UpdatedAt: now}}
	f.items = inlineItems("mine", now)[:2]
	cards := []proto.Interaction{{ID: "question1", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion, CreatedAt: now, Questions: []proto.Question{{Question: "Sample A or B?"}}}}
	f.nodes = map[string]nodeAnswer{DeviceInteractionsList: func(string, json.RawMessage) (any, error) { return map[string]any{"interactions": cards}, nil }}
	got := s.waitAgentReply(context.Background(), "Sent.", "mine", time.Second, fixedWaitTarget("box", "s1"))
	if !strings.Contains(got, `"state":"question"`) || !strings.Contains(got, `"interaction_id":"question1"`) || !strings.Contains(got, "Sample A or B?") {
		t.Fatal(got)
	}
	if len(f.agentSent) != 0 {
		t.Fatal("wait sent input")
	}
}
func TestAgentWaitTimeoutCancellationAndUnavailable(t *testing.T) {
	for _, state := range []string{"timeout", "cancelled", "unavailable"} {
		t.Run(state, func(t *testing.T) {
			s, f := newTaskService(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.sessions["box"] = []proto.Session{{SessionID: "s1", Status: proto.StatusIdle, UpdatedAt: time.Now()}}
			f.items = inlineItems("old", time.Now())
			if state == "cancelled" {
				cancel()
			}
			if state == "unavailable" {
				f.itemsFail = 1
			}
			got := s.waitAgentReply(ctx, "Queued receipt.", "mine", time.Millisecond, fixedWaitTarget("box", "s1"))
			if !strings.HasPrefix(got, "Queued receipt.") || !strings.Contains(got, `"state":"`+state+`"`) || strings.Contains(got, "old answer") {
				t.Fatal(got)
			}
			if len(f.agentSent) != 0 || len(f.starts) != 0 {
				t.Fatal("wait dispatched work")
			}
		})
	}
}
func TestAgentStartInlineReply(t *testing.T) {
	for _, wait := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipt", true: "inline"}[wait], func(t *testing.T) {
			s, f := newTaskService(t)
			f.onStart = func(f *fakeHub, p map[string]any) {
				now := time.Now()
				id, _ := p["client_msg_id"].(string)
				if wait && id == "" {
					t.Error("no input id")
				}
				if !wait && id != "" {
					t.Error("changed omitted wait")
				}
				f.items = inlineItems(id, now)
				f.sessions["box"] = []proto.Session{{SessionID: "s1", Status: proto.StatusDone, UpdatedAt: now.Add(time.Second)}}
			}
			raw := `{"machine":"box","cwd":"/code/app","prompt":"read sample"}`
			if wait {
				raw = strings.TrimSuffix(raw, "}") + `,"wait_seconds":1}`
			}
			before := time.Now()
			got, err := s.runTool(context.Background(), "server", "conv9", "agent_start", json.RawMessage(raw))
			elapsed := time.Since(before)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(got, `"state":"completed"`) != wait {
				t.Fatal(got)
			}
			if wait && !strings.Contains(got, "sample checked") {
				t.Fatal(got)
			}
			if len(f.starts) != 1 {
				t.Fatalf("starts %d", len(f.starts))
			}
			t.Logf("wait=%v result receipt after %s", wait, elapsed)
		})
	}
}

func shortInlinePoll(t *testing.T) {
	t.Helper()
	old := agentWaitPoll
	agentWaitPoll = time.Millisecond
	t.Cleanup(func() { agentWaitPoll = old })
}
func TestAgentSendWaitsForItsQueuedInput(t *testing.T) {
	shortInlinePoll(t)
	s, f := newTaskService(t)
	now := time.Now()
	id := ""
	reads := 0
	f.sessions["box"] = []proto.Session{{SessionID: "session-0001", Cwd: "/code/app", Status: proto.StatusWorking, UpdatedAt: now}}
	var delivered time.Time
	f.nodes = map[string]nodeAnswer{DeviceSessionSend: func(_ string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		id, _ = p["client_msg_id"].(string)
		delivered = time.Now()
		f.agentSent = append(f.agentSent, p)
		return map[string]any{"queued": true}, nil
	}}
	f.page = func(int) []proto.Item {
		reads++
		if reads == 1 {
			return inlineItems("previous", now)
		}
		f.sessions["box"][0].Status = proto.StatusDone
		f.sessions["box"][0].UpdatedAt = now.Add(time.Second)
		return inlineItems(id, now)
	}
	before := time.Now()
	got, err := s.runTool(context.Background(), "server", "conv9", "agent_send", json.RawMessage(`{"machine":"box","session_id":"session-0001","text":"read sample","wait_seconds":1}`))
	ended := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	if reads < 2 || id == "" || !strings.HasPrefix(got, "Queued for") || !strings.Contains(got, `"state":"completed"`) || !strings.Contains(got, "sample checked") {
		t.Fatal(reads, got)
	}
	if len(f.agentSent) != 1 {
		t.Fatal("input resent", len(f.agentSent))
	}
	t.Logf("synthetic delivery feedback=%s; result=%s", delivered.Sub(before), ended.Sub(before))
}
func TestTaskPlanSingleInlineAndApprovalReturn(t *testing.T) {
	shortInlinePoll(t)
	for _, readonly := range []bool{true, false} {
		t.Run(map[bool]string{true: "read-only", false: "approval"}[readonly], func(t *testing.T) {
			s, f := newTaskService(t)
			now := time.Now()
			f.onStart = func(f *fakeHub, p map[string]any) {
				id, _ := p["client_msg_id"].(string)
				f.items = inlineItems(id, now)
				f.sessions["box"] = []proto.Session{{SessionID: "s1", Cwd: "/code/app", Status: proto.StatusDone, UpdatedAt: now.Add(time.Second)}}
			}
			var args map[string]any
			_ = json.Unmarshal(planArgs(readonly), &args)
			args["wait_seconds"] = 1
			raw, _ := json.Marshal(args)
			got, err := s.toolTaskPlan(context.Background(), "conv1", raw)
			if err != nil {
				t.Fatal(err)
			}
			if readonly {
				if !strings.Contains(got, `"state":"completed"`) || !strings.Contains(got, "task_review") {
					t.Fatal(got)
				}
				if len(f.starts) != 1 {
					t.Fatal("wrong dispatch count", len(f.starts))
				}
				todos := s.todosOf(context.Background(), "*")
				if len(todos) != 1 || todos[0].DoneAt != nil {
					t.Fatal("inline reply accepted task", todos)
				}
			} else if !strings.Contains(got, "nothing has started") || strings.Contains(got, "Inline observation") || len(f.starts) != 0 {
				t.Fatal(got)
			}
		})
	}
}

func TestAgentWaitRejectsOldAndLaterQuestions(t *testing.T) {
	shortInlinePoll(t)
	for _, tc := range []string{"queued input not seen", "old card", "later input", "private"} {
		t.Run(tc, func(t *testing.T) {
			s, f := newTaskService(t)
			now := time.Now()
			f.sessions["box"] = []proto.Session{{SessionID: "s1", Status: proto.StatusBlocked}}
			f.items = inlineItems("mine", now)[:2]
			ix := proto.Interaction{ID: "question", SessionID: "s1", Kind: proto.IxQuestion, CreatedAt: now, Summary: "Question?"}
			switch tc {
			case "queued input not seen":
				f.items = inlineItems("old", now)
			case "old card":
				ix.CreatedAt = now.Add(-time.Second)
			case "later input":
				f.items = append(f.items, proto.Item{ID: "next", Role: "user", Kind: proto.KindText, TS: now.Add(time.Second)})
			case "private":
				ix.Private = true
			}
			f.nodes = map[string]nodeAnswer{DeviceInteractionsList: func(string, json.RawMessage) (any, error) {
				return map[string]any{"interactions": []proto.Interaction{ix}}, nil
			}}
			got := s.waitAgentReply(context.Background(), "Sent.", "mine", 2*time.Millisecond, fixedWaitTarget("box", "s1"))
			if !strings.Contains(got, `"state":"timeout"`) || strings.Contains(got, "Question?") {
				t.Fatal(got)
			}
		})
	}
}
func TestTaskPlanMultiKeepsImmediateReceipt(t *testing.T) {
	s, _ := newTaskService(t)
	var args map[string]any
	_ = json.Unmarshal(planArgs(false), &args)
	tasks := args["tasks"].([]any)
	second := map[string]any{}
	for k, v := range tasks[0].(map[string]any) {
		second[k] = v
	}
	second["title"] = "second task"
	args["tasks"] = append(tasks, second)
	args["wait_seconds"] = 50
	raw, _ := json.Marshal(args)
	got, err := s.toolTaskPlan(context.Background(), "conv1", raw)
	if err != nil || strings.Contains(got, "Inline observation") || !strings.Contains(got, "nothing has started") {
		t.Fatal(got, err)
	}
}

func TestAgentSendCancelledWaitDoesNotResend(t *testing.T) {
	s, f := newTaskService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.sessions["box"] = []proto.Session{{SessionID: "session-0001", Cwd: "/code/app", Status: proto.StatusWorking}}
	f.onSend = func(*fakeHub, map[string]any) { cancel() }
	got, err := s.runTool(ctx, "server", "conv9", "agent_send", json.RawMessage(`{"machine":"box","session_id":"session-0001","text":"read sample","wait_seconds":1}`))
	if err != nil || !strings.Contains(got, `"state":"cancelled"`) || len(f.agentSent) != 1 {
		t.Fatal(got, err, len(f.agentSent))
	}
}

func TestAgentResumeInlineCorrelatesItsInput(t *testing.T) {
	s, f := newQuietService(t)
	f.setSessions("box", stoppedBoxAgent("stop0001"))
	onBoxTask(t, s, "stop0001", "conv1", false, "the fix is merged")
	calls := 0
	f.answerNode(DeviceSessionStart, func(_ string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		calls++
		id, _ := p["client_msg_id"].(string)
		if id == "" {
			t.Error("resume lost correlator")
		}
		now := time.Now()
		f.items = inlineItems(id, now)
		f.sessions["box"] = []proto.Session{{SessionID: "stop0001", Status: proto.StatusDone, UpdatedAt: now.Add(time.Second)}}
		return map[string]any{"session": proto.Session{SessionID: "stop0001"}}, nil
	})
	got, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "stop0001", "text": "read sample", "resume": true, "resume_why": "continue the same investigation", "wait_seconds": 1})
	if err != nil || calls != 1 || !strings.Contains(got, `"state":"completed"`) || len(f.agentSent) != 0 {
		t.Fatal(got, err, calls)
	}
}

func TestTaskPlanInlineReportsDispatchFailure(t *testing.T) {
	shortInlinePoll(t)
	s, f := newTaskService(t)
	f.failStart = errors.New("synthetic launch refused")
	var args map[string]any
	_ = json.Unmarshal(planArgs(true), &args)
	args["wait_seconds"] = 1
	raw, _ := json.Marshal(args)
	got, err := s.toolTaskPlan(context.Background(), "conv1", raw)
	if err != nil || !strings.Contains(got, `"state":"failed"`) || !strings.Contains(got, "synthetic launch refused") {
		t.Fatal(got, err)
	}
}
