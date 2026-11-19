package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func overviewHub(t *testing.T, publicURL string) (*Service, *fleetDevs, *[]map[string]any) {
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, PublicURL: publicURL,
			Features: FeishuFeatures{Main: true, Cards: true, Reports: true}}, nil
	})
	d := newFleetDevs()
	d.add("server", "srv")
	s := withSettings(t, f, New(Options{Hub: f, Logf: testLogf(t), Devices: d}))
	s.fleet.interactions = map[string]*pending{}
	s.todos.loaded, s.todos.todos, s.todos.activeBySession = true, map[string]*Todo{}, map[[2]string]string{}
	return s, d, posts
}

func addPending(s *Service, id, machine, session, question string) {
	s.fleet.interactions[id] = &pending{ix: proto.Interaction{ID: id, MachineID: machine, SessionID: session,
		Kind: proto.IxQuestion, Questions: []proto.Question{{Question: question}}}}
}

func addActiveTodo(s *Service, td Todo, sessions ...[2]string) {
	s.todos.todos[td.ID] = &td
	for _, x := range sessions {
		s.todos.activeBySession[x] = td.ID
	}
}

// /tasks and both aliases post one display-only card with both sections, the pending links and a hand-made todo.
func TestTasksCommandShowsOverviewCard(t *testing.T) {
	for _, cmd := range []string{"/tasks", "/进展", "/总览"} {
		t.Run(cmd, func(t *testing.T) {
			s, d, posts := overviewHub(t, "https://hub.example.com")
			d.setSessions("server", []proto.Session{
				{SessionID: "asm", Cwd: "/home/u/.persona/assistant"},
				{SessionID: "pane", Cwd: "/code", PaneID: "%1"}})
			addPending(s, "ixA", "server", "asm", "主对话的问题")
			addPending(s, "ixB", "server", "pane", "Agent 的问题")
			addActiveTodo(s, Todo{ID: "todo_1", Title: "手工建的任务"}, [2]string{"server", "s1"}) // Origin ""
			if h, rest := s.botCommand(context.Background(), FeishuFeatures{}, "om", cmd, "", ""); !h || rest != "" {
				t.Fatalf("botCommand(%q) = %v, %q", cmd, h, rest)
			}
			if len(*posts) != 1 {
				t.Fatalf("posted %d cards, want 1", len(*posts))
			}
			card := (*posts)[0]["card"].(string)
			for _, want := range []string{"等你拍板", "在途任务", "主对话的问题", "手工建的任务",
				`"default_url":"https://hub.example.com/persona/asm"`,
				`"default_url":"https://hub.example.com/s/server/pane"`,
				`"default_url":"https://hub.example.com/todos/todo_1"`} {
				if !strings.Contains(card, want) {
					t.Fatalf("card missing %q:\n%s", want, card)
				}
			}
			if strings.Contains(card, `"type":"callback"`) || strings.Contains(card, `"tag":"input"`) ||
				strings.Contains(card, `select_static`) {
				t.Fatalf("overview card must be display-only:\n%s", card)
			}
		})
	}
}

// Empty sections show their empty text on one unnumbered card (done todo hidden); with no public URL rows still
// render but carry no link.
func TestTasksOverviewEmptyAndNoLinks(t *testing.T) {
	doneAt, _ := time.Parse(time.RFC3339, "2026-11-06T10:00:00Z")
	s, _, posts := overviewHub(t, "https://hub.example.com")
	addActiveTodo(s, Todo{ID: "finished", Title: "做完的任务", Origin: "c2", DoneAt: &doneAt}, [2]string{"server", "s9"})
	s.botCommand(context.Background(), FeishuFeatures{}, "om", "/tasks", "", "")
	card := (*posts)[0]["card"].(string)
	for _, want := range []string{"暂时没有等你拍板的事。", "暂时没有在途任务。"} {
		if !strings.Contains(card, want) {
			t.Fatalf("empty state missing %q", want)
		}
	}
	if strings.Contains(card, "做完的任务") || strings.Contains(card, "/1）") {
		t.Fatalf("done todo listed or part suffix on one card:\n%s", card)
	}
	s2, _, posts2 := overviewHub(t, "")
	addPending(s2, "ix1", "server", "s1", "要发布吗？")
	addActiveTodo(s2, Todo{ID: "todo_1", Title: "没有网页版地址的任务"}, [2]string{"server", "s1"})
	s2.botCommand(context.Background(), FeishuFeatures{}, "om", "/tasks", "", "")
	card = (*posts2)[0]["card"].(string)
	if strings.Contains(card, "default_url") {
		t.Fatalf("card links despite an empty public_url:\n%s", card)
	}
	if !strings.Contains(card, "要发布吗？") || !strings.Contains(card, "没有网页版地址的任务") {
		t.Fatalf("rows missing despite no links:\n%s", card)
	}
}

// Many heavy todos split into numbered cards, each inside the byte/element budget, each with a fallback.
func TestTasksOverviewPaginatesWithinBudget(t *testing.T) {
	s, _, posts := overviewHub(t, "https://hub.example.com")
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("todo_%02d", i)
		addActiveTodo(s, Todo{ID: id, Origin: "conv1", Stage: "review",
			Title: fmt.Sprintf("第%02d件在途任务标题", i), PRURL: fmt.Sprintf("https://git.example.com/team/repo/pull/%02d", i)},
			[2]string{"server", id})
	}
	s.botCommand(context.Background(), FeishuFeatures{}, "om", "/tasks", "", "")
	if len(*posts) < 2 {
		t.Fatalf("wanted several cards, got %d", len(*posts))
	}
	var all strings.Builder
	for i, p := range *posts {
		card := p["card"].(string)
		var v struct {
			Body struct {
				Elements []json.RawMessage `json:"elements"`
			} `json:"body"`
		}
		_ = json.Unmarshal([]byte(card), &v)
		if len(card) > reportCardMaxBytes || len(v.Body.Elements) > reportCardMaxElements ||
			!strings.Contains(card, fmt.Sprintf("（%d/%d）", i+1, len(*posts))) || p["fallback"].(string) == "" {
			t.Fatalf("card %d over budget, unnumbered or without fallback: %d bytes, %d elements", i+1, len(card), len(v.Body.Elements))
		}
		all.WriteString(card)
	}
	if !strings.Contains(all.String(), "todos/todo_00") || !strings.Contains(all.String(), "todos/todo_24") {
		t.Fatal("first/last task lost across the cards")
	}
}

// Manual todos (Origin "") bound to no working session are absent from todoReport's links, yet they must be listed;
// a done manual todo stays hidden and a session-bound todo keeps its Agent link (the manual one has none).
func TestTasksOverviewListsManualTodos(t *testing.T) {
	s, d, posts := overviewHub(t, "https://hub.example.com")
	d.setSessions("server", []proto.Session{{SessionID: "pane", Cwd: "/code", PaneID: "%1"}})
	s.todos.todos["todo_manual"] = &Todo{ID: "todo_manual", Title: "随手记下的手工任务"}
	doneAt, _ := time.Parse(time.RFC3339, "2026-11-06T10:00:00Z")
	s.todos.todos["todo_done"] = &Todo{ID: "todo_done", Title: "做完的手工任务", DoneAt: &doneAt}
	addActiveTodo(s, Todo{ID: "todo_agent", Title: "Agent 在做的任务"}, [2]string{"server", "pane"})
	s.botCommand(context.Background(), FeishuFeatures{}, "om", "/tasks", "", "")
	card := (*posts)[0]["card"].(string)
	for _, want := range []string{"随手记下的手工任务", "Agent 在做的任务",
		`"default_url":"https://hub.example.com/todos/todo_manual"`,
		`"default_url":"https://hub.example.com/todos/todo_agent"`,
		`"default_url":"https://hub.example.com/s/server/pane"`} {
		if !strings.Contains(card, want) {
			t.Fatalf("card missing %q:\n%s", want, card)
		}
	}
	if strings.Contains(card, "做完的手工任务") {
		t.Fatalf("done manual todo listed:\n%s", card)
	}
	agentLink := `"default_url":"https://hub.example.com/s/server/pane"`
	if n := strings.Count(card, agentLink); n != 1 {
		t.Fatalf("agent link count = %d, want 1 (the session-less manual todo must have none)", n)
	}
}

// A public_url without a scheme is a dirty value: rows still render, but no open_url button goes out on either a
// pending row or a todo row.
func TestTasksOverviewDirtyPublicURLHasNoButtons(t *testing.T) {
	s, _, posts := overviewHub(t, "hub.example.com")
	addPending(s, "ix1", "server", "pane", "要发布吗？")
	s.todos.todos["todo_1"] = &Todo{ID: "todo_1", Title: "脏地址的任务"}
	s.botCommand(context.Background(), FeishuFeatures{}, "om", "/tasks", "", "")
	card := (*posts)[0]["card"].(string)
	if strings.Contains(card, "default_url") {
		t.Fatalf("card carries link buttons with a scheme-less public_url:\n%s", card)
	}
	if !strings.Contains(card, "要发布吗？") || !strings.Contains(card, "脏地址的任务") {
		t.Fatalf("rows missing despite the dirty public_url:\n%s", card)
	}
}
