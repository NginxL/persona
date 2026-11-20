package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	workerMemoryConclusion = "Fixed the retry loop."
	workerMemoryPR         = "https://github.com/o/r/pull/42"
	workerMemoryIssue      = "https://github.com/o/r/issues/10219"
)

// workerMemoryEnv has a supervised worker on box/s1. The todo's title deliberately matches title-only, whose directory
// is elsewhere: a title is never enough to assign the worker's outcome to that project.
type workerMemoryEnv struct {
	s    *Service
	f    *fakeHub
	e    *kb.Engine
	todo Todo
	log  string
	cwd  string
}

func workerMemoryReportInput() json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"kind": "done", "text": workerMemoryConclusion,
		"evidence": []string{workerMemoryPR, workerMemoryIssue}})
	return raw
}

func newWorkerMemoryEnv(t *testing.T) *workerMemoryEnv {
	t.Helper()
	f := newFakeHub().withMachine()
	s := newKBService(t, f)
	s.work.switches = &TriageSwitches{Enabled: true}
	e := kbEng(t, s)
	for name, update := range map[string]kb.Update{
		"explicit":   {},
		"fallback":   {Dirs: []string{"box:/code/worker"}},
		"title-only": {},
		"other":      {},
	} {
		if _, err := e.Apply(kb.KindProject, name, update); err != nil {
			t.Fatal(err)
		}
	}
	todo := Todo{ID: "todo_worker", Title: "title-only", Origin: "conv1", Supervision: &Supervision{Supervisor: "conv1"},
		Agents: []TodoAgent{{MachineID: "box", SessionID: "s1", Cwd: "/code/worker"}}}
	putTodo(s, todo)
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Title: todo.Title, Cwd: "/code/worker",
		Status: proto.StatusDone, UpdatedAt: turnEnd, LastText: workerMemoryConclusion})
	f.setItems(
		proto.Item{ID: "prompt", Seq: 1, Kind: proto.KindText, Role: "user", Text: "Fix the retry loop.", TS: turnEnd.Add(-time.Minute)},
		proto.Item{ID: "report", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "report-call",
			ToolName: "report_to_supervisor", ToolInput: workerMemoryReportInput(), TS: turnEnd.Add(-time.Second)})
	if err := e.Store.AppendLog("explicit", turnEnd, kb.TodoLogSource+kb.TodoLogTitle(todo.Title), kb.TodoOutcomePrefix+workerMemoryConclusion+" "+workerMemoryPR); err != nil {
		t.Fatal(err)
	}
	return &workerMemoryEnv{s: s, f: f, e: e, todo: todoNow(s, todo.ID), log: e.Store.Path(kb.KindProject, "explicit", "log.md"),
		cwd: "/code/worker"}
}

func (env *workerMemoryEnv) reportDone(t *testing.T, now time.Time) {
	t.Helper()
	if _, err := env.s.reportToSupervisor(context.Background(), "box", "s1", workerMemoryReportInput(), now); err != nil {
		t.Fatal(err)
	}
	env.s.tasks.outcomeRuns.Wait()
}

func (env *workerMemoryEnv) triageDone(t *testing.T) {
	t.Helper()
	closeout := workerMemoryConclusion + "\n\n<closeout status=\"done\" merged=\"n/a\" deployed=\"n/a\">\n" +
		"did: " + workerMemoryConclusion + "\nartifact: pr " + workerMemoryPR + "\n</closeout>"
	env.f.setItems(
		proto.Item{ID: "prompt", Seq: 1, Kind: proto.KindText, Role: "user", Text: "Fix the retry loop.", TS: turnEnd.Add(-time.Minute)},
		proto.Item{ID: "report", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "report-call",
			ToolName: "report_to_supervisor", ToolInput: workerMemoryReportInput(), TS: turnEnd.Add(-time.Second)},
		proto.Item{ID: "report-result", Seq: 3, Kind: proto.KindToolResult, Role: "tool", ToolUseID: "report-call", Text: "sent"},
		proto.Item{ID: "answer", Seq: 4, Kind: proto.KindText, Role: "assistant", Text: closeout, TS: turnEnd})
	session := stopped("s1", closeout)
	session.Cwd = "/code/worker"
	env.f.setSessions("box", session)
	triage(t, env.s, "s1")
	if got := entryOf(t, env.s, "s1").Work; got.Phase != proto.WorkDone || got.By != "rule" {
		t.Fatalf("the deterministic done transition did not commit: %+v", got)
	}
	env.s.tasks.outcomeRuns.Wait()
}

func (env *workerMemoryEnv) setProject(t *testing.T, slug string) {
	t.Helper()
	env.s.todos.mu.Lock()
	env.s.todos.todos[env.todo.ID].Project = slug
	env.todo = copyTodoLocked(env.s.todos.todos[env.todo.ID])
	env.s.todos.mu.Unlock()
}

func (env *workerMemoryEnv) record(t *testing.T) {
	t.Helper()
	// Test-only contract for the shared completion path: both report_to_supervisor(done) and a WorkDone transition call
	// this method with these same fields. Turn and itemIndex come from that turn's transcript item.
	env.s.recordWorkerOutcome(context.Background(), env.todo, "box", "s1", env.cwd, 7, 2,
		workerMemoryConclusion, []string{workerMemoryPR, workerMemoryIssue})
}

func workerMemoryRecords(t *testing.T, e *kb.Engine, slug string) []kb.ProjectMemoryRecord {
	t.Helper()
	return e.ProjectMemory(slug, 20)
}

func assertOriginalWorkerMemory(t *testing.T, records []kb.ProjectMemoryRecord) {
	t.Helper()
	if len(records) != 1 {
		t.Fatalf("worker memory records = %+v, want exactly one", records)
	}
	got := records[0]
	if got.TodoID != "todo_worker" || got.Agent != "s1" {
		t.Fatalf("worker identity = %+v, want todo_worker/s1", got)
	}
	if got.Conclusion != workerMemoryConclusion || !slices.Equal(got.Links, []string{workerMemoryPR, workerMemoryIssue}) {
		t.Fatalf("original outcome was not preserved: %+v", got)
	}
	if got.Decision != "" || got.Reason != "" || got.Type != kb.ProjectMemoryOutcome ||
		got.Verification != kb.ProjectMemoryAgentReported || got.RecordedAt == "" {
		t.Fatalf("an unavailable model invented decision/reason or lost provenance: %+v", got)
	}
}

func assertDirectWorkerMemory(t *testing.T, records []kb.ProjectMemoryRecord) {
	t.Helper()
	assertOriginalWorkerMemory(t, records)
	if got := records[0]; got.Turn != 7 || got.ItemIndex != 2 {
		t.Fatalf("dedupe identity = %+v, want turn 7/item 2", got)
	}
}

func assertFileUnchanged(t *testing.T, path string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("%s changed:\nbefore:\n%s\nafter:\n%s", path, before, after)
	}
}

// Both live completion routes use the shared worker-outcome path. Replaying the same todo/agent/turn/item leaves one
// durable record, and writing worker memory does not replace or append the existing short conclusion in log.md.
func TestWorkerMemoryReportThenDoneWritesOnceAndPreservesLog(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	before, err := os.ReadFile(env.log)
	if err != nil {
		t.Fatal(err)
	}
	env.reportDone(t, turnEnd)
	assertOriginalWorkerMemory(t, workerMemoryRecords(t, env.e, "explicit"))
	env.triageDone(t)
	assertOriginalWorkerMemory(t, workerMemoryRecords(t, env.e, "explicit"))
	if got := workerMemoryRecords(t, env.e, "fallback"); len(got) != 0 {
		t.Fatalf("todo.Project lost to the directory fallback: %+v", got)
	}
	assertFileUnchanged(t, env.log, before)
}

// Ownership is explicit todo.Project first, then ProjectsForWork(machine,cwd). With neither, even a project whose name
// is the todo's title receives nothing.
func TestRecordWorkerOutcomeOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, project, want, rejected string
	}{
		{name: "todo project beats directory fallback", project: "explicit", want: "explicit", rejected: "fallback"},
		{name: "directory fallback", want: "fallback", rejected: "explicit"},
		{name: "no ownership never infers title", rejected: "title-only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newWorkerMemoryEnv(t)
			if tc.want == "" {
				env.cwd = "/code/unowned"
			}
			env.setProject(t, tc.project)
			env.record(t)
			if tc.want == "" {
				for _, slug := range []string{"explicit", "fallback", "title-only", "other"} {
					if got := workerMemoryRecords(t, env.e, slug); len(got) != 0 {
						t.Errorf("%s received an unowned outcome: %+v", slug, got)
					}
				}
				return
			}
			assertDirectWorkerMemory(t, workerMemoryRecords(t, env.e, tc.want))
			if got := workerMemoryRecords(t, env.e, tc.rejected); len(got) != 0 {
				t.Fatalf("%s also received the outcome: %+v", tc.rejected, got)
			}
		})
	}
}

// A model failure is a faithful fallback, not an empty outcome and not a license to invent a decision or rationale.
func TestRecordWorkerOutcomeModelFailurePreservesOriginal(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	env.f.mu.Lock()
	env.f.noModel = true
	env.f.outcome = ""
	env.f.replies = nil
	env.f.judge = ""
	env.f.mu.Unlock()
	env.record(t)
	assertDirectWorkerMemory(t, workerMemoryRecords(t, env.e, "explicit"))
}

// A successful extraction supplies every structured decision and pitfall while retaining the report's evidence links.
func TestRecordWorkerOutcomeModelDecision(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	env.f.mu.Lock()
	env.f.outcome = `{"outcome":"Bounded the retry loop.","links":["` + workerMemoryPR + `"],
		"decisions":[{"question":"How should retries work?","choice":"Keep a bound","reason":"It prevents duplicate work."},
		{"question":"When should work stop?","choice":"After the check","reason":"The evidence is complete."}],
		"pitfalls":["A retry used to duplicate work.","The old test missed the second decision."]}`
	env.f.mu.Unlock()
	env.record(t)
	records := workerMemoryRecords(t, env.e, "explicit")
	if len(records) != 5 {
		t.Fatalf("worker memory records = %+v, want outcome + two decisions + two pitfalls", records)
	}
	outcome := records[0]
	if outcome.Type != kb.ProjectMemoryOutcome || outcome.Conclusion != "Bounded the retry loop." ||
		outcome.Verification != kb.ProjectMemoryAgentReported || outcome.RecordedAt == "" {
		t.Fatalf("model outcome was not recorded with provenance: %+v", outcome)
	}
	if !slices.Equal(outcome.Links, []string{workerMemoryPR, workerMemoryIssue}) {
		t.Fatalf("model outcome lost existing links: %+v", outcome.Links)
	}
	decisions := []kb.ProjectMemoryRecord{records[3], records[4]}
	if decisions[0].Type != kb.ProjectMemoryDecision || decisions[0].Question != "When should work stop?" ||
		decisions[0].Decision != "After the check" || decisions[0].Reason != "The evidence is complete." ||
		decisions[1].Question != "How should retries work?" || decisions[1].Decision != "Keep a bound" {
		t.Fatalf("decisions were not retained newest first: %+v", decisions)
	}
	pitfalls := records[1:3]
	if pitfalls[0].Type != kb.ProjectMemoryPitfall || pitfalls[0].Conclusion != "The old test missed the second decision." ||
		pitfalls[1].Conclusion != "A retry used to duplicate work." {
		t.Fatalf("pitfalls were not retained: %+v", pitfalls)
	}
	for _, record := range records[1:] {
		if record.Verification != kb.ProjectMemoryAgentReported || record.RecordedAt != outcome.RecordedAt {
			t.Fatalf("typed record provenance = %+v", record)
		}
	}
}

// A failed append is warned about and does not consume the in-process dedupe identity. Removing the induced filesystem
// failure and completing the same worker item again persists one physical record, while log.md stays byte-identical.
func TestRecordWorkerOutcomeWriteFailureWarnsAndRetries(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	before, err := os.ReadFile(env.log)
	if err != nil {
		t.Fatal(err)
	}
	var logMu sync.Mutex
	var logs []string
	env.s.logf = func(format string, args ...any) {
		logMu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}
	memoryPath := env.e.Store.Path(kb.KindProject, "explicit", "memory.jsonl")
	if err := os.Mkdir(memoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	env.record(t)
	logMu.Lock()
	joined := strings.Join(logs, "\n")
	logMu.Unlock()
	if !strings.Contains(strings.ToLower(joined), "warn") || !strings.Contains(joined, "worker") ||
		!strings.Contains(joined, "memory") || !strings.Contains(joined, "todo_worker") {
		t.Fatalf("memory write failure was not logged as a warning with its todo: %q", joined)
	}
	if _, err := os.Stat(memoryPath); err != nil {
		t.Fatal(err)
	}
	assertFileUnchanged(t, env.log, before)

	if err := os.Remove(memoryPath); err != nil {
		t.Fatal(err)
	}
	env.record(t)
	assertDirectWorkerMemory(t, workerMemoryRecords(t, env.e, "explicit"))
	raw, err := os.ReadFile(memoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(raw), "\n"); lines != 1 {
		t.Fatalf("memory.jsonl has %d physical records after retry, want one: %q", lines, raw)
	}
	assertFileUnchanged(t, env.log, before)
}

// A delayed done recording uses the items triage captured, not the newer turn now listed by the node.
func TestWorkerMemoryDoneUsesCapturedTurn(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	captured := []proto.Item{
		{ID: "prompt-1", Seq: 10, Kind: proto.KindText, Role: "user", Text: "First turn", TS: turnEnd.Add(-time.Minute)},
		{ID: "answer-1", Seq: 11, Kind: proto.KindText, Role: "assistant", Text: workerMemoryConclusion, TS: turnEnd},
	}
	work := db.SessionWork{MachineID: "box", SessionID: "s1", TurnAt: turnEnd,
		Work: proto.WorkState{Phase: proto.WorkDone, Line: workerMemoryConclusion}}
	env.s.goRecordWorkerDone(context.Background(), work, captured)
	env.f.setItems(
		proto.Item{ID: "prompt-2", Seq: 20, Kind: proto.KindText, Role: "user", Text: "Later turn"},
		proto.Item{ID: "answer-2", Seq: 21, Kind: proto.KindText, Role: "assistant", Text: "A later outcome"})
	env.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Cwd: "/code/worker", Status: proto.StatusDone,
		UpdatedAt: turnEnd.Add(time.Hour), LastText: "A later outcome"})
	env.s.tasks.outcomeRuns.Wait()
	records := workerMemoryRecords(t, env.e, "explicit")
	if len(records) != 1 || records[0].Turn != 10 || records[0].ItemIndex != 1 || records[0].Conclusion != workerMemoryConclusion {
		t.Fatalf("done recording used a later turn: %+v", records)
	}
}

// The opening prompt and item offset are absolute even when the latest capped page starts after the prompt.
func TestWorkerIdentityStableAcrossCappedPage(t *testing.T) {
	prompt := proto.Item{ID: "prompt", Seq: 1, Kind: proto.KindText, Role: "user", Text: "Start"}
	capped := []proto.Item{{Seq: 2000}, {Seq: 2001}, {Seq: 2002}}
	older := func(before int) ([]proto.Item, bool, bool) {
		if before != 2000 {
			t.Fatalf("older page before = %d, want 2000", before)
		}
		return []proto.Item{prompt}, false, true
	}
	turn, index, ok := workerIdentity(capped, 2, older)
	if !ok || turn != 1 || index != 2001 {
		t.Fatalf("capped identity = %d/%d, %v; want 1/2001, true", turn, index, ok)
	}
	full := append([]proto.Item{prompt}, capped...)
	turn, index, ok = workerIdentity(full, len(full)-1, older)
	if !ok || turn != 1 || index != 2001 {
		t.Fatalf("full-page identity = %d/%d, %v; want the same 1/2001, true", turn, index, ok)
	}
}

// A released worker's late done transition cannot write to the todo's project after another agent owns the work.
func TestWorkerMemoryDoneSkipsReleasedAgent(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	now := time.Now()
	env.s.todos.mu.Lock()
	todo := env.s.todos.todos[env.todo.ID]
	todo.DoneAt = &now
	todo.Agents[0].ReleasedAt = &now
	delete(env.s.todos.activeBySession, [2]string{"box", "s1"})
	env.s.todos.mu.Unlock()
	items := []proto.Item{
		{Seq: 10, Kind: proto.KindText, Role: "user", Text: "First turn"},
		{Seq: 11, Kind: proto.KindText, Role: "assistant", Text: "Stale outcome"},
	}
	work := db.SessionWork{MachineID: "box", SessionID: "s1", TurnAt: turnEnd,
		Work: proto.WorkState{Phase: proto.WorkDone, Line: "Stale outcome"}}
	env.s.goRecordWorkerDone(context.Background(), work, items)
	env.s.tasks.outcomeRuns.Wait()
	if got := workerMemoryRecords(t, env.e, "explicit"); len(got) != 0 {
		t.Fatalf("released agent wrote project memory: %+v", got)
	}
}

// Once the report's turn has a final answer, its asynchronous report reader yields to the pinned WorkDone writer.
func TestWorkerReportIndexYieldsCompletedTurn(t *testing.T) {
	items := []proto.Item{
		{Seq: 1, Kind: proto.KindText, Role: "user", Text: "Start"},
		{Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "report-call", ToolName: "report_to_supervisor", ToolInput: workerMemoryReportInput()},
		{Seq: 3, Kind: proto.KindToolResult, Role: "tool", ToolUseID: "report-call"},
		{Seq: 4, Kind: proto.KindText, Role: "assistant", Text: workerMemoryConclusion},
	}
	if got := workerReportIndex(items, workerMemoryConclusion, turnEnd, true); got != -1 {
		t.Fatalf("pending report claimed a completed turn at %d", got)
	}
	if got := workerReportIndex(items, "", time.Time{}, false); got != 1 {
		t.Fatalf("WorkDone did not reuse report item: %d", got)
	}
}

// With triage disabled no WorkDone will arrive, so a completed report turn still writes through the report route.
func TestWorkerMemoryReportWritesWithTriageDisabled(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	env.s.work.switches = &TriageSwitches{Enabled: false}
	env.f.setItems(
		proto.Item{Seq: 1, Kind: proto.KindText, Role: "user", Text: "Start"},
		proto.Item{Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "report-call",
			ToolName: "report_to_supervisor", ToolInput: workerMemoryReportInput()},
		proto.Item{Seq: 3, Kind: proto.KindToolResult, Role: "tool", ToolUseID: "report-call"},
		proto.Item{Seq: 4, Kind: proto.KindText, Role: "assistant", Text: workerMemoryConclusion})
	env.reportDone(t, turnEnd)
	assertOriginalWorkerMemory(t, workerMemoryRecords(t, env.e, "explicit"))
}
