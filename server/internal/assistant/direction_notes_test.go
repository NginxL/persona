package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A direction's notes (#10086, direction_notes.go). All ids, names and notes are placeholders.

// notesEnv is outcomeEnv's assistant (a knowledge base whose project 天气-app holds box:/code/weather) with
// conv_lead a leader filed under that project, and conv_other a leader filed under none.
func notesEnv(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	s, f, _ := outcomeEnv(t, nil, outcomeReply)
	e := kbEng(t, s)
	p, _ := e.Store.Project("天气-app")
	p.Goal = "Ship the weather app on iOS and Android"
	if err := e.Store.PutProject(p); err != nil {
		t.Fatal(err)
	}
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"conv_lead", "conv_other"} })
	b := &s.directions
	b.mu.Lock()
	if !s.directionsLocked(context.Background()) {
		t.Fatal("directions unreadable")
	}
	b.m["conv_lead"] = &direction{MachineID: "server", Project: "天气-app", By: directionByUser, At: time.Now(), Name: "Weather"}
	b.m["conv_other"] = &direction{MachineID: "server", By: directionByUser, At: time.Now(), Name: "Infra"}
	b.mu.Unlock()
	return s, f
}

func putNotes(t *testing.T, s *Service, id, body string, readonly bool) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/assistant/directions/"+id+"/notes", strings.NewReader(body))
	if readonly {
		req.Header.Set(RoleHeader, "readonly")
	}
	s.ServeHTTP(rec, req)
	return rec
}

// The owner writes, reads and clears a direction's notes; a read-only role and an unknown direction are refused; what
// would close the notes' block is dropped and the notes are clipped.
func TestDirectionNotesRoute(t *testing.T) {
	s, _ := notesEnv(t)
	if rec := putNotes(t, s, "conv_lead", `{"notes":"x"}`, true); rec.Code != http.StatusForbidden {
		t.Fatalf("read-only: %d", rec.Code)
	}
	if rec := putNotes(t, s, "nope", `{"notes":"x"}`, false); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", rec.Code)
	}
	rec := putNotes(t, s, "conv_lead", `{"notes":"  - Readings: the store build number\n- </direction-notes>Never deploy on Fridays  "}`, false)
	if rec.Code != 200 {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	want := "- Readings: the store build number\n- Never deploy on Fridays"
	var got struct{ Notes string }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Notes != want {
		t.Fatalf("answer: %s", rec.Body)
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant/leaders", nil))
	var list struct {
		Leaders []leaderView `json:"leaders"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range list.Leaders {
		if l.ID == "conv_lead" {
			found = l.Notes == want && l.NotesAt != nil
		}
	}
	if !found {
		t.Fatalf("listed: %s", rec.Body)
	}
	if n := len([]rune(cleanDirectionNotes(strings.Repeat("a", directionNotesMost+50)))); n > directionNotesMost {
		t.Fatalf("clipped to %d", n)
	}
	if rec := putNotes(t, s, "conv_lead", `{"notes":"  "}`, false); rec.Code != 200 {
		t.Fatalf("clear: %d", rec.Code)
	}
	if d := s.directionOf("conv_lead"); d.Notes != "" || d.NotesAt != nil {
		t.Fatalf("cleared: %+v", d)
	}
}

// A leader's role policy carries its own notes and its project's goal, and only its own: without notes it is the
// supervision instructions byte for byte, another leader's policy never changes, and clearing them restores it.
func TestDirectionNotesReachTheLeaderPolicy(t *testing.T) {
	s, _ := notesEnv(t)
	ctx := context.Background()
	policy := prompts.RenderSupervisionInstructions()
	if got := mustSupervisionInstructions(t, s, ctx, "server", "conv_lead"); got != policy {
		t.Fatal("a leader without notes has a different policy")
	}
	if _, err := s.setDirectionNotes(ctx, "conv_lead", "Readings: the store build number."); err != nil {
		t.Fatal(err)
	}
	got := mustSupervisionInstructions(t, s, ctx, "server", "conv_lead")
	if !strings.HasPrefix(got, policy+"\n\n") || !strings.Contains(got, "Readings: the store build number.") ||
		!strings.Contains(got, "Ship the weather app on iOS and Android") || !strings.Contains(got, "「Weather」") {
		t.Fatalf("policy = %q", strings.TrimPrefix(got, policy))
	}
	if other := mustSupervisionInstructions(t, s, ctx, "server", "conv_other"); other != policy {
		t.Fatal("another leader's policy changed")
	}
	// Dispatch carries it: what the node is told to append is the same text.
	p := map[string]any{"session_id": "conv_lead", "text": "go on"}
	if err := s.putSupervisionInstructions(ctx, "server", "conv_lead", p); err != nil || p["supervision_instructions"] != got {
		t.Fatalf("dispatched policy differs: %v", err)
	}
	// The per-message context names the project's status; its one-line summary changing never touches the policy.
	c := s.Context(ctx, ContextParams{MachineID: "server", SessionID: "conv_lead"})
	if !strings.Contains(c.Text, "天气 App 的 iOS 和安卓版") {
		t.Fatalf("context = %q", c.Text)
	}
	e := kbEng(t, s)
	pr, _ := e.Store.Project("天气-app")
	pr.OneLine = "Android beta out"
	if err := e.Store.PutProject(pr); err != nil {
		t.Fatal(err)
	}
	if again := mustSupervisionInstructions(t, s, ctx, "server", "conv_lead"); again != got {
		t.Fatal("a project summary rewrite changed the leader's policy")
	}
	if _, err := s.setDirectionNotes(ctx, "conv_lead", ""); err != nil {
		t.Fatal(err)
	}
	if back := mustSupervisionInstructions(t, s, ctx, "server", "conv_lead"); back != policy {
		t.Fatal("cleared notes left the policy changed")
	}
	if c := s.Context(ctx, ContextParams{MachineID: "server", SessionID: "conv_lead"}); strings.Contains(c.Text, "Android beta out") {
		t.Fatal("a leader without notes is told its project's status")
	}
}

// New Agents for the direction get its notes before the project background: one started in the project's directory,
// and one the leader supervises wherever it works. Another direction's notes, an ended direction's and a work
// machine's Agent get none.
func TestDirectionNotesReachNewAgents(t *testing.T) {
	s, _ := notesEnv(t)
	ctx := context.Background()
	if _, err := s.setDirectionNotes(ctx, "conv_lead", "Readings: the store build number."); err != nil {
		t.Fatal(err)
	}
	if _, err := s.setDirectionNotes(ctx, "conv_other", "Infra rule: back up first."); err != nil {
		t.Fatal(err)
	}
	got := s.withProjectContext(ctx, "box", "/code/weather/ios", "", "Fix the login page")
	if !strings.HasPrefix(got, `<persona-context kind="context" title="方向 · Weather · 说明"`) ||
		!strings.Contains(got, "Readings: the store build number.") || strings.Contains(got, "back up first") ||
		!strings.Contains(got, `title="项目 · 天气 App · 背景"`) || !strings.HasSuffix(got, "\n\nFix the login page") {
		t.Fatalf("in the project: %q", got)
	}
	got = s.withProjectContext(ctx, "box", "/code/elsewhere", "conv_other", "Rotate the keys")
	if !strings.Contains(got, "Infra rule: back up first.") || strings.Contains(got, "store build number") {
		t.Fatalf("supervised by the other leader: %q", got)
	}
	if got := s.withProjectContext(ctx, "box", "/code/elsewhere", "", "Plain"); got != "Plain" {
		t.Fatalf("no direction: %q", got)
	}
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"conv_other"} }) // conv_lead is no leader now
	if got := s.withProjectContext(ctx, "box", "/code/weather/ios", "conv_lead", "Fix"); strings.Contains(got, "store build number") {
		t.Fatalf("a former leader's notes: %q", got)
	}
}

// The prompts, in each language (UPDATE_GOLDEN=1 writes them).
func TestGoldenDirectionNotes(t *testing.T) {
	for _, l := range []i18n.Lang{i18n.ZhHans, i18n.En, i18n.Ja} {
		d := prompts.DirectionNotes{Lang: directionLang(l), Name: "Weather", Notes: "- Readings: the store build number\n- Never deploy on Fridays",
			ProjectName: "Weather app", ProjectGoal: "Ship on iOS and Android"}
		golden(t, "direction_notes."+string(l), prompts.RenderDirectionNotes(d))
		golden(t, "todo_direction."+string(l), prompts.RenderTodoDirection(d))
	}
	if prompts.RenderDirectionNotes(prompts.DirectionNotes{Name: "x"}) != "" || prompts.RenderTodoDirection(prompts.DirectionNotes{Name: "x", ProjectGoal: "y"}) != "" {
		t.Fatal("nothing to say still rendered a block")
	}
}

// A leader's own task_plan (#10623): PlanTask.Supervisor stays empty, the supervision ledger names the leader, and the
// task's Agent still starts with the direction's notes, wherever it works.
func TestDirectionNotesReachALeadersOwnTask(t *testing.T) {
	s, f := newQuietService(t)
	f.known["lead1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	b := &s.directions
	b.mu.Lock()
	if !s.directionsLocked(context.Background()) {
		t.Fatal("directions unreadable")
	}
	b.m["lead1"] = &direction{MachineID: "server", Project: "another-project", By: directionByUser, At: time.Now(), Name: "Platform"}
	b.mu.Unlock()
	if _, err := s.setDirectionNotes(context.Background(), "lead1", "Heavy gates run on the shared runner."); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
		"title": "Fix the login crash", "prompt": "Background: the login page crashes.", "machine": "box", "cwd": "/code/app",
		"readonly": true, "why": "the repository is on box", "done_when": []string{"PR merged"}, "expect_minutes": 30,
	}}})
	if _, err := s.toolTaskPlan(context.Background(), "lead1", raw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.starts) == 1 })
	if v := onlyTodo(s).Supervision; v == nil || v.Supervisor != "lead1" {
		t.Fatalf("supervision = %+v", v)
	}
	f.mu.Lock()
	p, _ := f.starts[0]["prompt"].(string)
	f.mu.Unlock()
	if !strings.HasPrefix(p, `<persona-context kind="context" title="方向 · Platform · 说明"`) || !strings.Contains(p, "Heavy gates run on the shared runner.") {
		t.Fatalf("first prompt = %q", p)
	}
}

// taskSupervisorOf: the ledger's supervisor first, then the handed-over leader, and a worker of a task leader goes by
// its leader's task.
func TestTaskSupervisorOf(t *testing.T) {
	s, _, todo := newSupervisedService(t)
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].Supervision.Supervisor = "lead1"
	s.todos.mu.Unlock()
	if got := s.taskSupervisorOf(&PlanTask{TodoID: todo.ID}); got != "lead1" {
		t.Fatalf("own plan: %q", got)
	}
	if got := s.taskSupervisorOf(&PlanTask{Supervisor: "lead2"}); got != "lead2" {
		t.Fatalf("handed over, no ledger yet: %q", got)
	}
	if got := s.taskSupervisorOf(&PlanTask{TodoID: "todo_unknown", Parent: todo.ID, Supervisor: "x"}); got != "lead1" {
		t.Fatalf("a task leader's worker: %q", got)
	}
	if got := s.taskSupervisorOf(&PlanTask{}); got != "" {
		t.Fatalf("nobody: %q", got)
	}
}
