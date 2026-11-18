package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A planned task whose Agent start stopped on its new tab's startup question (shell_waiting) is answered from the app
// (#10365, POST /api/v1/todos/{id}/start/answer): the node types one offered answer into the kept tab by its pane
// (start.answer, its own guards), and the task starts again in that tab, not a new one. The computer, the question and
// the screen are placeholders.

const omzLine = "[oh-my-zsh] Would you like to update? [Y/n]"

// shellWaitingRefusal is the node's refusal of a start whose new tab stopped on question line, offered choices, its
// tab w1:p2 kept until until.
func shellWaitingRefusal(line, choices string, until time.Time, extra map[string]string) *rpc.Error {
	data := map[string]string{"delivered": "none", "pane": "w1:p2", "line": line, "screen": "Last login: today\n" + line,
		"kind": "update", "choices": choices, "kept_until": until.UTC().Format(time.RFC3339)}
	for k, v := range extra {
		data[k] = v
	}
	raw, _ := json.Marshal(data)
	return &rpc.Error{Code: proto.CodeShellWaiting, Message: "the new tab's shell is waiting for an answer: " + line, Data: raw}
}

// answeredNode has box's node answer start.answer with answer (nil: it types and says so), and returns what it was
// asked, in order.
func answeredNode(f *fakeHub, answer func(p proto.StartAnswerParams) error) func() []proto.StartAnswerParams {
	return replyingNode(f, func(p proto.StartAnswerParams) (any, error) {
		if answer != nil {
			if err := answer(p); err != nil {
				return nil, err
			}
		}
		return map[string]any{"ok": true, "typed": true}, nil
	})
}

// replyingNode has box's node answer start.answer with reply's, and returns what it was asked, in order.
func replyingNode(f *fakeHub, reply func(p proto.StartAnswerParams) (any, error)) func() []proto.StartAnswerParams {
	var mu sync.Mutex
	var asked []proto.StartAnswerParams
	f.answerNode(proto.MethodStartAnswer, func(machine string, raw json.RawMessage) (any, error) {
		var p proto.StartAnswerParams
		_ = json.Unmarshal(raw, &p)
		mu.Lock()
		asked = append(asked, p)
		mu.Unlock()
		if machine != "box" {
			return nil, errors.New("asked the wrong computer: " + machine)
		}
		return reply(p)
	})
	return func() []proto.StartAnswerParams {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(asked)
	}
}

// postAnswer posts the answer choice to todo id's question as a device of role ("" : full) and returns the status and
// body.
func postAnswer(t *testing.T, s *Service, id, role, body string) (int, map[string]any) {
	t.Helper()
	header := map[string][]string{"Accept-Language": {"en"}}
	if role != "" {
		header[RoleHeader] = []string{role}
	}
	res, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{Method: "POST", Path: "/api/v1/todos/" + id + "/start/answer",
		Header: header, Body: []byte(body)}, s.lang)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(res.Body, &out)
	return res.Status, out
}

// startsSoFar is how many times session.start reached the computers.
func startsSoFar(f *fakeHub) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

// The owner's "n": the node is asked to type it into the kept tab, with the question the plan task listed; the same
// to-do is reopened and its agent starts in that tab (in_pane), no new one; the task's history says the owner
// answered, by the answer's id. The plan task carries the question and its answers for the app until then.
func TestTaskStartAnswerTypesAndStartsInTheKeptTab(t *testing.T) {
	s, f := launcherRetryHub(t)
	asked := answeredNode(f, nil)
	todo, p := failOnWork(t, s, f, shellWaitingRefusal(omzLine, "y,n,enter", time.Now().Add(5*time.Minute), map[string]string{"default": "y"}))
	if a := p.Tasks[0].ErrorArgs; p.Tasks[0].ErrorCode != proto.CodeShellWaiting || a["line"] != omzLine || a["choices"] != "y,n,enter" ||
		a["pane"] != "w1:p2" || a["kept_until"] == "" || a["kind"] != "update" || a["default"] != "y" {
		t.Fatalf("plan task %+v", p.Tasks[0])
	}
	status, body := postAnswer(t, s, todo.ID, "", `{"choice":"n"}`)
	if status != http.StatusOK || body["todo"] == nil {
		t.Fatalf("answer: %d %v", status, body)
	}
	if got := asked(); len(got) != 1 || got[0] != (proto.StartAnswerParams{Pane: "w1:p2", Line: omzLine, Choice: "n"}) {
		t.Fatalf("the node was asked %+v", got)
	}
	waitFor(t, "the agent to start", func() bool { return len(onlyTodo(s).Agents) == 1 })
	f.mu.Lock()
	starts := slices.Clone(f.starts)
	f.mu.Unlock()
	if len(starts) != 2 || starts[1]["in_pane"] != "w1:p2" || starts[1]["launcher"] != "work" {
		t.Fatalf("starts %v", starts)
	}
	if got := onlyTodo(s); got.ID != todo.ID || got.DoneAt != nil || got.StartError != "" || todoCount(s) != 1 {
		t.Fatalf("not the same to-do, reopened: %+v", got)
	}
	items, _, _ := s.feed(context.Background(), "conv1", time.Time{})
	if !slices.ContainsFunc(items, func(it FeedItem) bool {
		var b startRemedyBody
		_ = json.Unmarshal(it.Body, &b)
		return it.Kind == feedStartRemedy && it.Ref == todo.ID && b.Action == "answer" && b.By == "user" && b.Choice == "n" && b.Text == "" && len(b.Keys) == 0
	}) {
		t.Fatalf("no answer step in the task's history: %s", feedKinds(s, "conv1"))
	}
}

// remedySteps is the start_remedy steps of todo id's history in conv1, by action.
func remedySteps(s *Service, id string) map[string][]startRemedyBody {
	items, _, _ := s.feed(context.Background(), "conv1", time.Time{})
	steps := map[string][]startRemedyBody{}
	for _, it := range items {
		var b startRemedyBody
		if it.Kind == feedStartRemedy && it.Ref == id && json.Unmarshal(it.Body, &b) == nil {
			steps[b.Action] = append(steps[b.Action], b)
		}
	}
	return steps
}

// The question had left the tab's last line before the owner's answer came (answered at the computer): the node types
// nothing and says so (typed false), and the task still starts in the tab, but its history does not say the owner
// answered: its step is answer_not_typed, with the answer they picked. A node whose reply does not say typed (the first
// start.answer) typed, as the hub took it before.
func TestTaskStartAnswerNotTypedIsNotTheOwnersAnswer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply map[string]any
		step  string
	}{
		{"answered at the computer", map[string]any{"ok": true, "typed": false}, "answer_not_typed"},
		{"typed", map[string]any{"ok": true, "typed": true}, "answer"},
		{"a reply without typed", map[string]any{"ok": true}, "answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := launcherRetryHub(t)
			asked := replyingNode(f, func(proto.StartAnswerParams) (any, error) { return tc.reply, nil })
			todo, _ := failOnWork(t, s, f, shellWaitingRefusal(omzLine, "y,n,enter", time.Now().Add(5*time.Minute), nil))
			if status, body := postAnswer(t, s, todo.ID, "", `{"choice":"n"}`); status != http.StatusOK || body["todo"] == nil {
				t.Fatalf("answer: %d %v", status, body)
			}
			waitFor(t, "the agent to start", func() bool { return len(onlyTodo(s).Agents) == 1 })
			f.mu.Lock()
			starts := slices.Clone(f.starts)
			f.mu.Unlock()
			if len(asked()) != 1 || len(starts) != 2 || starts[1]["in_pane"] != "w1:p2" {
				t.Fatalf("asked %v, starts %v", asked(), starts)
			}
			steps := remedySteps(s, todo.ID)
			other := map[string]string{"answer": "answer_not_typed", "answer_not_typed": "answer"}[tc.step]
			if got := steps[tc.step]; len(got) != 1 || got[0].By != "user" || got[0].Choice != "n" || len(steps[other]) != 0 {
				t.Fatalf("steps %+v: want one %s", steps, tc.step)
			}
		})
	}
}

// Only a full device answers; a read-only one, or an extension's token, is refused before anything is asked. A body
// without a choice is a 400.
func TestTaskStartAnswerIsFullRoleOnly(t *testing.T) {
	s, f := launcherRetryHub(t)
	asked := answeredNode(f, nil)
	todo, _ := failOnWork(t, s, f, shellWaitingRefusal(omzLine, "y,n,enter", time.Now().Add(5*time.Minute), nil))
	for _, role := range []string{"read", roleExtension} {
		if status, body := postAnswer(t, s, todo.ID, role, `{"choice":"n"}`); status != http.StatusForbidden || body["code"] != proto.CodeReadOnly ||
			body["error"] != "This device can’t answer a question in a computer’s terminal." {
			t.Errorf("%s: %d %v", role, status, body)
		}
	}
	for _, in := range []string{`{}`, `{"choice":"n; rm"}`, `nope`} {
		if status, body := postAnswer(t, s, todo.ID, "", in); status != http.StatusBadRequest || body["error"] != "Choose one of the answers." {
			t.Errorf("%s: %d %v", in, status, body)
		}
	}
	if got := asked(); len(got) != 0 || startsSoFar(f) != 1 || onlyTodo(s).StartError == "" {
		t.Fatalf("asked %v, %d starts, to-do %+v", got, startsSoFar(f), onlyTodo(s))
	}
}

// Nothing is typed and nothing changes for a task whose start did not stop on a question, whose tab its computer no
// longer keeps, or for an answer the question was not offered (a password prompt is offered Cancel alone): 409 with
// the code and the words.
func TestTaskStartAnswerRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		choice  string
		code    string
		words   string
	}{
		{"not a question", terminalRefusal(proto.CodeLauncherMissing, "work"), "n", "start_not_waiting", "This agent's start isn't waiting on a question any more. Nothing was typed."},
		{"tab gone", shellWaitingRefusal(omzLine, "y,n,enter", time.Now().Add(-time.Minute), nil), "n", "start_tab_gone",
			"That terminal tab isn't waiting any more: it was closed after a few minutes, or an agent runs there now. Nothing was typed; start the agent again."},
		{"not offered", shellWaitingRefusal(omzLine, "y,n,enter", time.Now().Add(5*time.Minute), nil), "q", "choice_not_offered",
			"That isn't one of the answers this question offers. Nothing was typed."},
		{"a password", shellWaitingRefusal("Enter passphrase for key '/home/me/.ssh/id_ed25519':", "cancel", time.Now().Add(5*time.Minute),
			map[string]string{"kind": "secret", "secret": "true", "line_mode": "true"}), "enter", "choice_not_offered", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := launcherRetryHub(t)
			asked := answeredNode(f, nil)
			todo, _ := failOnWork(t, s, f, tc.failure)
			status, body := postAnswer(t, s, todo.ID, "", `{"choice":"`+tc.choice+`"}`)
			if status != http.StatusConflict || body["code"] != tc.code || tc.words != "" && body["error"] != tc.words {
				t.Fatalf("%d %v", status, body)
			}
			if got := asked(); len(got) != 0 || startsSoFar(f) != 1 || onlyTodo(s).StartError == "" || onlyTodo(s).DoneAt == nil {
				t.Fatalf("asked %v, %d starts, to-do %+v", got, startsSoFar(f), onlyTodo(s))
			}
		})
	}
	// An open to-do (it started, or is starting again) has no question to answer.
	s, f := launcherRetryHub(t)
	answeredNode(f, nil)
	todo, _ := failOnWork(t, s, f, shellWaitingRefusal(omzLine, "y,n,enter", time.Now().Add(5*time.Minute), nil))
	if _, err := s.retryTask(context.Background(), todo.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the retry to start", func() bool { return len(onlyTodo(s).Agents) == 1 })
	if status, body := postAnswer(t, s, todo.ID, "", `{"choice":"n"}`); status != http.StatusConflict || body["code"] != "start_not_waiting" {
		t.Fatalf("an open to-do: %d %v", status, body)
	}
}

// The node's own refusals come back as it gave them, nothing typed and nothing started: the tab went meanwhile, an
// answer it does not take for that question (Ctrl-C alone into a password prompt). (A question answered at the
// computer is no refusal: the node types nothing and the task starts in the tab.)
func TestTaskStartAnswerPassesTheNodesRefusalOn(t *testing.T) {
	for _, code := range []string{"start_tab_gone", "choice_not_offered"} {
		t.Run(code, func(t *testing.T) {
			s, f := launcherRetryHub(t)
			asked := answeredNode(f, func(proto.StartAnswerParams) error {
				return &rpc.Error{Code: code, Message: "refused: " + code}
			})
			todo, _ := failOnWork(t, s, f, shellWaitingRefusal(omzLine, "y,n,enter", time.Now().Add(5*time.Minute), nil))
			status, body := postAnswer(t, s, todo.ID, "", `{"choice":"y"}`)
			if status != http.StatusConflict || body["code"] != code {
				t.Fatalf("%d %v", status, body)
			}
			if code == "start_tab_gone" && body["error"] != "That terminal tab isn't waiting any more: it was closed after a few minutes, or an agent runs there now. Nothing was typed; start the agent again." {
				t.Errorf("words %v", body)
			}
			if got := asked(); len(got) != 1 || startsSoFar(f) != 1 || onlyTodo(s).StartError == "" {
				t.Fatalf("asked %v, %d starts, to-do %+v", got, startsSoFar(f), onlyTodo(s))
			}
			// it can be retried as before
			if _, err := s.retryTask(context.Background(), todo.ID); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the retry to start", func() bool { return len(onlyTodo(s).Agents) == 1 })
		})
	}
}

// A computer whose node is older than start.answer is told to update, nothing typed: the node answered the method
// unknown (through a hub that relays it), or its connection did not announce the capability.
func TestTaskStartAnswerNeedsANewerNode(t *testing.T) {
	s, f := launcherRetryHub(t)
	answeredNode(f, func(proto.StartAnswerParams) error { return errors.New("unknown method start.answer") })
	todo, _ := failOnWork(t, s, f, shellWaitingRefusal(omzLine, "y,n,enter", time.Now().Add(5*time.Minute), nil))
	status, body := postAnswer(t, s, todo.ID, "", `{"choice":"n"}`)
	if status != http.StatusConflict || body["code"] != proto.CodeNodeOutdated ||
		body["error"] != "Update Persona on box to answer its terminal’s question from here. Nothing was typed." {
		t.Fatalf("%d %v", status, body)
	}
	if startsSoFar(f) != 1 || onlyTodo(s).StartError == "" {
		t.Fatalf("%d starts, to-do %+v", startsSoFar(f), onlyTodo(s))
	}

	old := &Service{devs: oldNodeDevs{}}
	pt := PlanTask{ErrorCode: proto.CodeShellWaiting, ErrorArgs: map[string]string{"line": omzLine, "choices": "y,n,enter"}}
	var refused answerRefused
	if _, err := old.answerKeptTab(context.Background(), Device{ID: "box", Name: "box"}, pt, "w1:p2", "n"); !errors.As(err, &refused) ||
		refused.code != proto.CodeNodeOutdated {
		t.Fatalf("a node that did not announce %s: %v", nodeproto.StartAnswer, err)
	}
}

// oldNodeDevs are the manager's computers, whose nodes announced nothing (older than start.answer).
type oldNodeDevs struct{ Devices }

func (oldNodeDevs) Has(string) bool                   { return true }
func (oldNodeDevs) NodeAnnounced(string, string) bool { return false }
