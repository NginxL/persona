package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// successorGateHub holds session.start at a gate, outside the fake's own lock, the way a node that is still building
// the session holds its answer; fail answers that many of the held starts with an error once the gate opens.
type successorGateHub struct {
	*fakeHub
	arrived  chan struct{}
	gate     chan struct{}
	mu       sync.Mutex
	fail     int
	startErr error
}

func (h *successorGateHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method == DeviceSessionStart {
		h.arrived <- struct{}{}
		<-h.gate
		h.mu.Lock()
		failing := h.fail > 0
		if failing {
			h.fail--
		}
		h.mu.Unlock()
		if failing {
			if h.startErr != nil {
				return nil, h.startErr
			}
			return nil, errors.New("the node could not start the agent")
		}
	}
	return h.fakeHub.DeviceCall(ctx, p)
}

// stoppedTaskOnGate is newQuietService with h as the service's hub from the start, and a task whose only agent has
// stopped (read-only, #4754).
func stoppedTaskOnGate(t *testing.T) (*Service, *successorGateHub, Todo) {
	t.Helper()
	old := quietNoticeDelay
	quietNoticeDelay = 30 * time.Millisecond
	t.Cleanup(func() { quietNoticeDelay = old })
	reachA, reachB, reachM := agentReachAttempts, agentReachBackoff, agentReachMax
	agentReachAttempts, agentReachBackoff, agentReachMax = 2, time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { agentReachAttempts, agentReachBackoff, agentReachMax = reachA, reachB, reachM })
	f := newFakeHub()
	f.sessions["box"] = nil
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	// No fake model server: its listener would never count as durably blocked in the test's bubble, and nothing here
	// asks a model.
	f.ownModels = true
	h := &successorGateHub{fakeHub: f, arrived: make(chan struct{}, 4), gate: make(chan struct{})}
	s := withSettings(t, f, New(Options{Hub: h, Logf: testLogf(t)}))
	shortSupervise(t, s)
	f.hello.Assistant.MachineID = "server"
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	f.known["conv1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	secretary(f)
	if _, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "sec00001", "text": "把周报数据拉一下",
		"task": map[string]any{"title": "拉周报数据"}}); err != nil {
		t.Fatal(err)
	}
	todo := onlyTodo(s)
	f.setSessions("box", proto.Session{SessionID: "sec00001", MachineID: "box", Cwd: "/code/desk", Agent: "claude", Title: "秘书",
		Launcher: "work", PermissionMode: "acceptEdits", LastText: "表格在 /code/desk/out.xlsx",
		Status: proto.StatusOffline, State: proto.StateOffline, UpdatedAt: time.Now(),
		Capabilities: &proto.SessionCapabilities{Read: true, Terminal: true}, ReadOnlyReason: "stopped"})
	return s, h, todo
}

type routed struct {
	text string
	item FeedItem
	err  error
}

// newAgentOf is the new agent a routed feed line names ("" for a message sent to an agent already there).
func newAgentOf(item FeedItem) string {
	var body struct {
		NewAgent string `json:"new_agent"`
	}
	_ = json.Unmarshal(item.Body, &body)
	return body.NewAgent
}

// linkedSuccessors are the task's agents other than the stopped first one.
func linkedSuccessors(s *Service, todo string) []string {
	var out []string
	for _, a := range todoNow(s, todo).Agents {
		if a.SessionID != "sec00001" && a.active() {
			out = append(out, a.SessionID)
		}
	}
	return out
}

// Two messages on the card of a task whose agents have all stopped, the second while the new agent the first one
// starts is still starting (the owner's next line, or the same one sent again after the app stopped waiting): one new
// agent starts, with the first message, and the second message goes to it once it is there. Nothing is lost and no
// second agent works in the same directory.
func TestStoppedTaskSecondMessageGoesToTheStartingAgent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, h, todo := stoppedTaskOnGate(t)
		results := make(chan routed, 2)
		route := func(text string) {
			item, err := s.routeToTask(context.Background(), "conv1", todo.ID, text)
			results <- routed{text, item, err}
		}
		go route("接着把环比也算上")
		<-h.arrived // the first message's session.start is at the node

		go route("顺便把同比也算上")
		synctest.Wait() // the second message either reached the node too or waits for the first start
		select {
		case <-h.arrived:
			t.Error("the second message reached session.start while the first new agent was still starting")
		default:
		}
		close(h.gate)
		got := map[string]routed{}
		for range 2 {
			r := <-results
			if r.err != nil {
				t.Fatalf("%q: %v", r.text, r.err)
			}
			got[r.text] = r
		}
		f := h.fakeHub
		f.mu.Lock()
		starts := len(f.starts)
		var prompt string
		if starts > 0 {
			prompt, _ = f.starts[0]["prompt"].(string)
		}
		f.mu.Unlock()
		if starts != 1 {
			t.Fatalf("%d agents were started for one stopped task, want 1", starts)
		}
		if !strings.HasSuffix(prompt, "接着把环比也算上") {
			t.Fatalf("the new agent's first message is not the first one's: %q", prompt)
		}
		agent := newAgentOf(got["接着把环比也算上"].item)
		if agent == "" || newAgentOf(got["顺便把同比也算上"].item) != "" {
			t.Fatalf("feed lines: first %s, second %s", got["接着把环比也算上"].item.Body, got["顺便把同比也算上"].item.Body)
		}
		f.mu.Lock()
		var sent []map[string]any
		sent = append(sent, f.agentSent...)
		f.mu.Unlock()
		delivered := false
		for _, a := range sent {
			delivered = delivered || (a["session_id"] == agent && a["text"] == "顺便把同比也算上")
		}
		if !delivered {
			t.Fatalf("the second message was not sent to the new agent %s: %v", agent, sent)
		}
		if linked := linkedSuccessors(s, todo.ID); len(linked) != 1 || linked[0] != agent {
			t.Fatalf("new agents linked to the task = %v, want [%s]", linked, agent)
		}
	})
}

// The first message's start fails: the message that waited for it starts the new agent itself, with its own words, so
// it is not lost either.
func TestStoppedTaskMessageAfterAFailedStartStartsTheAgent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, h, todo := stoppedTaskOnGate(t)
		h.fail = 1
		results := make(chan routed, 2)
		route := func(text string) {
			item, err := s.routeToTask(context.Background(), "conv1", todo.ID, text)
			results <- routed{text, item, err}
		}
		go route("接着把环比也算上")
		<-h.arrived
		go route("顺便把同比也算上")
		synctest.Wait()
		select {
		case <-h.arrived:
			t.Error("the second message reached session.start while the first new agent was still starting")
		default:
		}
		close(h.gate)
		got := map[string]routed{}
		for range 2 {
			r := <-results
			got[r.text] = r
		}
		if got["接着把环比也算上"].err == nil {
			t.Fatal("the first message's failed start was not answered as failed")
		}
		second := got["顺便把同比也算上"]
		if second.err != nil {
			t.Fatalf("the waiting message: %v", second.err)
		}
		agent := newAgentOf(second.item)
		h.fakeHub.mu.Lock()
		starts := len(h.fakeHub.starts)
		var prompt string
		if starts > 0 {
			prompt, _ = h.fakeHub.starts[starts-1]["prompt"].(string)
		}
		h.fakeHub.mu.Unlock()
		if starts != 1 || agent == "" || !strings.HasSuffix(prompt, "顺便把同比也算上") {
			t.Fatalf("after the failed start: %d starts reached the fake, new agent %q, prompt %q", starts, agent, prompt)
		}
		if linked := linkedSuccessors(s, todo.ID); len(linked) != 1 || linked[0] != agent {
			t.Fatalf("new agents linked to the task = %v, want [%s]", linked, agent)
		}
	})
}

func TestStoppedTaskUncertainStartDoesNotRepeatTheMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"deadline across the bus", relayedDeadline},
		{"node failed after writing", nodeAfterWrite},
		{"unreadable start receipt", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, h, todo := stoppedTaskOnGate(t)
				if ok, err := s.autoDoneTodo(t.Context(), todo.ID, "finished", time.Now()); !ok || err != nil {
					t.Fatalf("complete task: %v, %v", ok, err)
				}
				if tc.err == nil {
					h.answerNode(DeviceSessionStart, unreadableWriteReceipt)
				} else {
					h.fail, h.startErr = 1, tc.err
				}
				first, waiting := make(chan error, 1), make(chan error, 1)
				route := func(result chan<- error) {
					_, err := s.routeToTask(t.Context(), "conv1", todo.ID, "Check the weekly figures")
					result <- err
				}
				go route(first)
				<-h.arrived
				go route(waiting)
				synctest.Wait()
				close(h.gate)
				for _, ch := range []chan error{first, waiting} {
					if err := <-ch; err == nil || !strings.Contains(i18n.Localize(err, i18n.En), "before sending again") {
						t.Errorf("route returned %v; want to check the agent before sending again", err)
					}
				}
				if extra := len(h.arrived); extra != 0 {
					t.Errorf("the waiting message started %d more agents", extra)
				}
				if got := todoNow(s, todo.ID); got.DoneAt != nil || got.ReopenedAt == nil {
					t.Errorf("an uncertain start did not reopen the task")
				}
			})
		})
	}
}

// A start that outlasts its own bound: the message that waited for it is answered as not sent, rather than waiting
// for good or starting a second agent beside it.
func TestStoppedTaskMessageStopsWaitingForAStuckStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, h, todo := stoppedTaskOnGate(t)
		results := make(chan routed, 2)
		route := func(text string) {
			item, err := s.routeToTask(context.Background(), "conv1", todo.ID, text)
			results <- routed{text, item, err}
		}
		go route("接着把环比也算上")
		<-h.arrived
		go route("顺便把同比也算上")
		r := <-results // the gate never opens until the second message is answered
		var m *i18n.Message
		if r.text != "顺便把同比也算上" || !errors.As(r.err, &m) || m.Key != "assistant-tasks.route_starting" {
			t.Fatalf("%q answered %v, want the second message refused while the start is still going", r.text, r.err)
		}
		select {
		case <-h.arrived:
			t.Error("the second message started a second agent")
		default:
		}
		close(h.gate)
		<-results
	})
}

// A bare /exit on the card while the new agent another message started is still starting is a second message like any
// other (#8272): it waits for that start and goes to the agent it brought up. Only when no agent is there or on its way
// is there nothing to exit: after the start failed, the waiting /exit is refused and starts no agent itself (#7539).
func TestStoppedTaskExitWaitsForTheStartingAgent(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
	}{{name: "the start comes up"}, {name: "the start fails", fail: true}} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, h, todo := stoppedTaskOnGate(t)
				if tc.fail {
					h.fail = 1
				}
				results := make(chan routed, 2)
				route := func(text string) {
					item, err := s.routeToTask(context.Background(), "conv1", todo.ID, text)
					results <- routed{text, item, err}
				}
				go route("接着把环比也算上")
				<-h.arrived
				go route("/exit")
				synctest.Wait()
				close(h.gate)
				got := map[string]routed{}
				for range 2 {
					r := <-results
					got[r.text] = r
				}
				select {
				case <-h.arrived:
					t.Fatal("/exit started an agent")
				default:
				}
				f := h.fakeHub
				f.mu.Lock()
				starts := len(f.starts) // a start the gate failed never reaches the fake
				var exits []map[string]any
				for _, a := range f.agentSent {
					if a["text"] == "/exit" {
						exits = append(exits, a)
					}
				}
				f.mu.Unlock()
				if want := map[bool]int{false: 1, true: 0}[tc.fail]; starts != want {
					t.Fatalf("%d starts reached the fake, want %d", starts, want)
				}
				exit := got["/exit"]
				if tc.fail {
					var m *i18n.Message
					if !errors.As(exit.err, &m) || m.Key != "assistant-tasks.route_agents_exited" || len(exits) != 0 {
						t.Fatalf("/exit after the failed start: %v, sent %v; want route_agents_exited, nothing sent", exit.err, exits)
					}
					return
				}
				agent := newAgentOf(got["接着把环比也算上"].item)
				if exit.err != nil || agent == "" || newAgentOf(exit.item) != "" {
					t.Fatalf("/exit: %v, feed %s; first feed %s", exit.err, exit.item.Body, got["接着把环比也算上"].item.Body)
				}
				if len(exits) != 1 || exits[0]["session_id"] != agent {
					t.Fatalf("/exit sent %v, want once to the new agent %s", exits, agent)
				}
			})
		})
	}
}
