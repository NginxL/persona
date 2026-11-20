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
	env.s.recordWorkerOutcome(context.Background(), env.todo, "box", "s1", env.cwd, 7, 2, "report-call", true,
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

// Parallel tool calls in one assistant line share a Seq but not an item or tool-use ID. Different outcomes must not
// disappear, and WorkDone reuses the latest report's full identity instead of writing a third record.
func TestWorkerMemorySameSequenceReportsKeepDistinctItems(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	const first = "Fixed the first retry loop."
	const second = "Fixed the second retry loop."
	input := func(text string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"kind": "done", "text": text})
		return raw
	}
	items := []proto.Item{
		{ID: "prompt", Seq: 1, Kind: proto.KindText, Role: "user", Text: "Fix both retry loops."},
		{ID: "report-a", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "call-a",
			ToolName: "report_to_supervisor", ToolInput: input(first), TS: turnEnd.Add(-time.Second)},
		{ID: "report-b", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "call-b",
			ToolName: "report_to_supervisor", ToolInput: input(second), TS: turnEnd.Add(-time.Second)},
	}
	env.f.setItems(items...)
	env.s.goRecordWorkerReport(context.Background(), env.todo, "box", "s1", first, nil, turnEnd)
	env.s.tasks.outcomeRuns.Wait()
	if got := workerMemoryRecords(t, env.e, "explicit"); len(got) != 1 || got[0].Conclusion != first {
		t.Fatalf("first same-Seq report was not recorded once: %+v", got)
	}
	env.s.goRecordWorkerReport(context.Background(), env.todo, "box", "s1", second, nil, turnEnd)
	env.s.tasks.outcomeRuns.Wait()
	records := workerMemoryRecords(t, env.e, "explicit")
	if len(records) != 2 || records[0].ID == records[1].ID {
		t.Fatalf("same-Seq reports collided: %+v", records)
	}
	seen := map[string]bool{}
	for _, record := range records {
		seen[record.Conclusion] = true
	}
	if !seen[first] || !seen[second] {
		t.Fatalf("same-Seq reports lost content: %+v", records)
	}
	work := db.SessionWork{MachineID: "box", SessionID: "s1", TurnAt: turnEnd,
		Work: proto.WorkState{Phase: proto.WorkDone, Line: second}}
	env.s.goRecordWorkerDone(context.Background(), work, items)
	env.s.tasks.outcomeRuns.Wait()
	records = workerMemoryRecords(t, env.e, "explicit")
	seen = map[string]bool{}
	for _, record := range records {
		seen[record.Conclusion] = true
	}
	if len(records) != 2 || !seen[first] || !seen[second] {
		t.Fatalf("WorkDone duplicated or replaced a same-Seq report: %+v", records)
	}
}

// Exact duplicate handoffs are retries, not two outcomes. Only different report content needs a distinct durable item.
// Timestamps may differ between the two calls; the normalized report payload determines the durable semantic key.
func TestWorkerMemoryExactDuplicateReportsStayOne(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	items := []proto.Item{
		{ID: "prompt", Seq: 1, Kind: proto.KindText, Role: "user", Text: "Fix both retry loops."},
		{ID: "report-a", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "call-a",
			ToolName: "report_to_supervisor", ToolInput: workerMemoryReportInput(), TS: turnEnd},
		{ID: "report-b", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "call-b",
			ToolName: "report_to_supervisor", ToolInput: workerMemoryReportInput(), TS: turnEnd.Add(time.Second)},
	}
	env.f.setItems(items...)
	for _, at := range []time.Time{turnEnd, turnEnd.Add(time.Second)} {
		env.s.goRecordWorkerReport(context.Background(), env.todo, "box", "s1", workerMemoryConclusion,
			[]string{workerMemoryPR, workerMemoryIssue}, at)
		env.s.tasks.outcomeRuns.Wait()
	}
	assertOriginalWorkerMemory(t, workerMemoryRecords(t, env.e, "explicit"))
}

// Identical parallel report text and timestamps are disambiguated by their evidence links, not by which one happens to
// appear last in the transcript page.
func TestWorkerMemorySameSequenceIdenticalTextUsesEvidence(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	input := func(evidence string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"kind": "done", "text": workerMemoryConclusion, "evidence": []string{evidence}})
		return raw
	}
	items := []proto.Item{
		{ID: "prompt", Seq: 1, Kind: proto.KindText, Role: "user", Text: "Fix both retry loops."},
		{ID: "report-a", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "call-a",
			ToolName: "report_to_supervisor", ToolInput: input(workerMemoryPR), TS: turnEnd},
		{ID: "report-b", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "call-b",
			ToolName: "report_to_supervisor", ToolInput: input(workerMemoryIssue), TS: turnEnd},
	}
	env.f.setItems(items...)
	env.s.goRecordWorkerReport(context.Background(), env.todo, "box", "s1", workerMemoryConclusion, []string{workerMemoryPR}, turnEnd)
	env.s.tasks.outcomeRuns.Wait()
	env.s.goRecordWorkerReport(context.Background(), env.todo, "box", "s1", workerMemoryConclusion, []string{workerMemoryIssue}, turnEnd)
	env.s.tasks.outcomeRuns.Wait()
	records := workerMemoryRecords(t, env.e, "explicit")
	if len(records) != 2 {
		t.Fatalf("identical parallel reports collapsed: %+v", records)
	}
	links := map[string]bool{}
	for _, record := range records {
		if len(record.Links) == 1 {
			links[record.Links[0]] = true
		}
	}
	if !links[workerMemoryPR] || !links[workerMemoryIssue] {
		t.Fatalf("identical parallel reports lost their distinct evidence: %+v", records)
	}
	work := db.SessionWork{MachineID: "box", SessionID: "s1", TurnAt: turnEnd,
		Work: proto.WorkState{Phase: proto.WorkDone, Line: workerMemoryConclusion}}
	env.s.goRecordWorkerDone(context.Background(), work, items)
	env.s.tasks.outcomeRuns.Wait()
	if got := workerMemoryRecords(t, env.e, "explicit"); len(got) != 2 {
		t.Fatalf("WorkDone duplicated an identical parallel report: %+v", got)
	}
}

// Non-URL evidence is part of the accepted report too, so it can distinguish otherwise identical parallel calls.
func TestWorkerMemorySameSequenceIdenticalTextUsesNonURLEvidence(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	input := func(evidence string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"kind": "done", "text": workerMemoryConclusion, "evidence": []string{evidence}})
		return raw
	}
	items := []proto.Item{
		{ID: "prompt", Seq: 1, Kind: proto.KindText, Role: "user", Text: "Fix both retry loops."},
		{ID: "report-a", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "call-a",
			ToolName: "report_to_supervisor", ToolInput: input("test A passed"), TS: turnEnd},
		{ID: "report-b", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "call-b",
			ToolName: "report_to_supervisor", ToolInput: input("test B passed"), TS: turnEnd},
	}
	env.f.setItems(items...)
	env.s.goRecordWorkerReport(context.Background(), env.todo, "box", "s1", workerMemoryConclusion, []string{"test A passed"}, turnEnd)
	env.s.tasks.outcomeRuns.Wait()
	env.s.goRecordWorkerReport(context.Background(), env.todo, "box", "s1", workerMemoryConclusion, []string{"test B passed"}, turnEnd)
	env.s.tasks.outcomeRuns.Wait()
	records := workerMemoryRecords(t, env.e, "explicit")
	if len(records) != 2 || records[0].ID == records[1].ID {
		t.Fatalf("non-URL evidence did not distinguish parallel reports: %+v", records)
	}
}

// An ambiguous legacy prefix cannot prove which parallel item it recorded, so it must not suppress a new keyed outcome.
func TestWorkerMemoryParallelItemIgnoresAmbiguousLegacyID(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	const earlier = "The earlier parallel outcome."
	const current = "The current parallel outcome."
	legacy := kb.ProjectMemoryRecord{ID: workerMemoryID(env.todo.ID, "box", "s1", 1, 1), TodoID: env.todo.ID,
		Agent: "s1", Turn: 1, ItemIndex: 1, Conclusion: earlier}
	if err := env.e.AppendProjectMemory("explicit", legacy); err != nil {
		t.Fatal(err)
	}
	input := func(text string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"kind": "done", "text": text})
		return raw
	}
	env.f.setItems(
		proto.Item{ID: "prompt", Seq: 1, Kind: proto.KindText, Role: "user", Text: "Start"},
		proto.Item{ID: "report-a", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "call-a",
			ToolName: "report_to_supervisor", ToolInput: input(earlier), TS: turnEnd},
		proto.Item{ID: "report-b", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "call-b",
			ToolName: "report_to_supervisor", ToolInput: input(current), TS: turnEnd})
	env.s.goRecordWorkerReport(context.Background(), env.todo, "box", "s1", current, nil, turnEnd)
	env.s.tasks.outcomeRuns.Wait()
	records := workerMemoryRecords(t, env.e, "explicit")
	seen := map[string]bool{}
	for _, record := range records {
		seen[record.Conclusion] = true
	}
	if len(records) != 2 || !seen[earlier] || !seen[current] {
		t.Fatalf("ambiguous legacy ID suppressed a parallel outcome: %+v", records)
	}
}

// Records written before item keys were added still deduplicate the same outcome and do not invoke the model again.
func TestRecordWorkerOutcomeLegacyIDPreventsDuplicate(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	legacy := kb.ProjectMemoryRecord{ID: workerMemoryID(env.todo.ID, "box", "s1", 7, 2), TodoID: env.todo.ID,
		Agent: "s1", Turn: 7, ItemIndex: 2, Conclusion: workerMemoryConclusion}
	if err := env.e.AppendProjectMemory("explicit", legacy); err != nil {
		t.Fatal(err)
	}
	env.record(t)
	records := workerMemoryRecords(t, env.e, "explicit")
	if len(records) != 1 || records[0].ID != legacy.ID {
		t.Fatalf("legacy ID was appended again with a new key: %+v", records)
	}
	env.f.mu.Lock()
	asked := len(env.f.outcomeAsked)
	env.f.mu.Unlock()
	if asked != 0 {
		t.Fatalf("an already recorded legacy outcome called the model %d time(s)", asked)
	}
}

// The legacy writer selected only the last final text block, so earlier text in the same assistant line does not make
// its legacy ID ambiguous or cause a duplicate keyed append.
func TestWorkerMemoryEarlierTextDoesNotDisableLegacyDedupe(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	legacy := kb.ProjectMemoryRecord{ID: workerMemoryID(env.todo.ID, "box", "s1", 1, 1), TodoID: env.todo.ID,
		Agent: "s1", Turn: 1, ItemIndex: 1, Conclusion: workerMemoryConclusion}
	if err := env.e.AppendProjectMemory("explicit", legacy); err != nil {
		t.Fatal(err)
	}
	items := []proto.Item{
		{ID: "prompt", Seq: 1, Kind: proto.KindText, Role: "user", Text: "Start"},
		{ID: "earlier", Seq: 2, Kind: proto.KindText, Role: "assistant", Text: "Working"},
		{ID: "answer", Seq: 2, Kind: proto.KindText, Role: "assistant", Text: workerMemoryConclusion},
	}
	work := db.SessionWork{MachineID: "box", SessionID: "s1", TurnAt: turnEnd,
		Work: proto.WorkState{Phase: proto.WorkDone, Line: workerMemoryConclusion}}
	env.s.goRecordWorkerDone(context.Background(), work, items)
	env.s.tasks.outcomeRuns.Wait()
	records := workerMemoryRecords(t, env.e, "explicit")
	if len(records) != 1 || records[0].ID != legacy.ID {
		t.Fatalf("earlier text disabled unambiguous legacy dedupe: %+v", records)
	}
	env.f.mu.Lock()
	asked := len(env.f.outcomeAsked)
	env.f.mu.Unlock()
	if asked != 0 {
		t.Fatalf("legacy dedupe repeated the model call %d time(s)", asked)
	}
}

// Unrelated tools in the same assistant line were never outcome candidates, so they do not make a legacy report ID
// ambiguous or cause a duplicate keyed append.
func TestWorkerMemoryUnrelatedToolKeepsLegacyDedupe(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	legacy := kb.ProjectMemoryRecord{ID: workerMemoryID(env.todo.ID, "box", "s1", 1, 1), TodoID: env.todo.ID,
		Agent: "s1", Turn: 1, ItemIndex: 1, Conclusion: workerMemoryConclusion}
	if err := env.e.AppendProjectMemory("explicit", legacy); err != nil {
		t.Fatal(err)
	}
	env.f.setItems(
		proto.Item{ID: "prompt", Seq: 1, Kind: proto.KindText, Role: "user", Text: "Start"},
		proto.Item{ID: "report", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "report-call",
			ToolName: "report_to_supervisor", ToolInput: workerMemoryReportInput(), TS: turnEnd},
		proto.Item{ID: "bash", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "bash-call",
			ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"go test ./..."}`), TS: turnEnd})
	env.s.goRecordWorkerReport(context.Background(), env.todo, "box", "s1", workerMemoryConclusion,
		[]string{workerMemoryPR, workerMemoryIssue}, turnEnd)
	env.s.tasks.outcomeRuns.Wait()
	records := workerMemoryRecords(t, env.e, "explicit")
	if len(records) != 1 || records[0].ID != legacy.ID {
		t.Fatalf("unrelated tool disabled legacy dedupe: %+v", records)
	}
	env.f.mu.Lock()
	asked := len(env.f.outcomeAsked)
	env.f.mu.Unlock()
	if asked != 0 {
		t.Fatalf("legacy dedupe repeated the model call %d time(s)", asked)
	}
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
				env.f.mu.Lock()
				asked := len(env.f.outcomeAsked)
				env.f.mu.Unlock()
				if asked != 0 {
					t.Fatalf("unowned outcome called the model %d time(s), want zero", asked)
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

// WorkDone can win the write before the report path. The selected report's evidence must survive even when the final
// answer and work artifacts contain no links.
func TestWorkerMemoryDonePreservesReportEvidence(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	items := []proto.Item{
		{ID: "prompt", Seq: 10, Kind: proto.KindText, Role: "user", Text: "Fix the retry loop."},
		{ID: "report", Seq: 11, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "report-call",
			ToolName: "report_to_supervisor", ToolInput: workerMemoryReportInput()},
		{ID: "report-result", Seq: 12, Kind: proto.KindToolResult, Role: "tool", ToolUseID: "report-call", Text: "sent"},
		{ID: "answer", Seq: 13, Kind: proto.KindText, Role: "assistant", Text: "Finished without links in the reply."},
	}
	work := db.SessionWork{MachineID: "box", SessionID: "s1", TurnAt: turnEnd,
		Work: proto.WorkState{Phase: proto.WorkDone, Line: workerMemoryConclusion}}
	env.s.goRecordWorkerDone(context.Background(), work, items)
	env.s.tasks.outcomeRuns.Wait()
	assertOriginalWorkerMemory(t, workerMemoryRecords(t, env.e, "explicit"))
}

// A later unrelated turn must not inherit a todo that had already completed before that turn ended.
func TestWorkerMemoryDoneSkipsTurnAfterTodoCompletion(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	completedAt := turnEnd.Add(-time.Hour)
	env.s.todos.mu.Lock()
	env.s.todos.todos[env.todo.ID].DoneAt = &completedAt
	delete(env.s.todos.activeBySession, [2]string{"box", "s1"})
	env.s.todos.mu.Unlock()
	items := []proto.Item{
		{ID: "later-prompt", Seq: 20, Kind: proto.KindText, Role: "user", Text: "Unrelated later turn"},
		{ID: "later-answer", Seq: 21, Kind: proto.KindText, Role: "assistant", Text: "Unrelated later outcome"},
	}
	work := db.SessionWork{MachineID: "box", SessionID: "s1", TurnAt: turnEnd,
		Work: proto.WorkState{Phase: proto.WorkDone, Line: "Unrelated later outcome"}}
	env.s.goRecordWorkerDone(context.Background(), work, items)
	env.s.tasks.outcomeRuns.Wait()
	if got := workerMemoryRecords(t, env.e, "explicit"); len(got) != 0 {
		t.Fatalf("later unrelated turn wrote to the completed todo: %+v", got)
	}
}

// The todo captured at the WorkDone handoff remains authoritative even when the worker is already linked to a newer one.
func TestWorkerMemoryDoneUsesPinnedTodo(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	newer := env.todo
	newer.ID = "todo_newer"
	newer.Title = "A newer assignment"
	newer.Project = "other"
	env.s.runWorkerOutcome = func(run func()) {
		putTodo(env.s, newer)
		run()
	}
	items := []proto.Item{
		{ID: "prompt-1", Seq: 10, Kind: proto.KindText, Role: "user", Text: "First turn"},
		{ID: "answer-1", Seq: 11, Kind: proto.KindText, Role: "assistant", Text: workerMemoryConclusion},
	}
	work := db.SessionWork{MachineID: "box", SessionID: "s1", TurnAt: turnEnd,
		Work: proto.WorkState{Phase: proto.WorkDone, Line: workerMemoryConclusion}}
	env.s.goRecordWorkerDone(context.Background(), work, items)
	env.s.tasks.outcomeRuns.Wait()
	records := workerMemoryRecords(t, env.e, "explicit")
	if len(records) != 1 || records[0].TodoID != env.todo.ID {
		t.Fatalf("pinned todo did not own the WorkDone record: %+v", records)
	}
	if got := workerMemoryRecords(t, env.e, "other"); len(got) != 0 {
		t.Fatalf("a newer todo captured the old WorkDone record: %+v", got)
	}
}

// Directory fallback is captured with the todo. A later session cwd change cannot move the old outcome to a new project.
func TestWorkerMemoryDoneUsesPinnedCwd(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "")
	if _, err := env.e.Apply(kb.KindProject, "other", kb.Update{Dirs: []string{"box:/code/other"}}); err != nil {
		t.Fatal(err)
	}
	env.s.runWorkerOutcome = func(run func()) {
		env.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Cwd: "/code/other",
			Status: proto.StatusDone, UpdatedAt: turnEnd, LastText: workerMemoryConclusion})
		run()
	}
	items := []proto.Item{
		{ID: "prompt", Seq: 10, Kind: proto.KindText, Role: "user", Text: "First turn"},
		{ID: "answer", Seq: 11, Kind: proto.KindText, Role: "assistant", Text: workerMemoryConclusion},
	}
	work := db.SessionWork{MachineID: "box", SessionID: "s1", TurnAt: turnEnd,
		Work: proto.WorkState{Phase: proto.WorkDone, Line: workerMemoryConclusion}}
	env.s.goRecordWorkerDone(context.Background(), work, items)
	env.s.tasks.outcomeRuns.Wait()
	if got := workerMemoryRecords(t, env.e, "fallback"); len(got) != 1 {
		t.Fatalf("pinned cwd did not retain the fallback project: %+v", got)
	}
	if got := workerMemoryRecords(t, env.e, "other"); len(got) != 0 {
		t.Fatalf("later session cwd moved the old outcome: %+v", got)
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

// A final answer does not erase the report's stable identity; WorkDone can reuse it without a second write.
func TestWorkerReportIndexFindsCompletedTurnReport(t *testing.T) {
	items := []proto.Item{
		{Seq: 1, Kind: proto.KindText, Role: "user", Text: "Start"},
		{Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "report-call", ToolName: "report_to_supervisor", ToolInput: workerMemoryReportInput()},
		{Seq: 3, Kind: proto.KindToolResult, Role: "tool", ToolUseID: "report-call"},
		{Seq: 4, Kind: proto.KindText, Role: "assistant", Text: workerMemoryConclusion},
	}
	if got := workerReportIndex(items, workerMemoryConclusion, turnEnd); got != 1 {
		t.Fatalf("completed turn report was not found at index 1: %d", got)
	}
	if got := workerReportIndex(items, "", time.Time{}); got != 1 {
		t.Fatalf("WorkDone did not reuse report item: %d", got)
	}
}

// Capturing the todo's recorded cwd must not add a synchronous session RPC to the report response path.
func TestWorkerMemoryReportDoesNotReadSessionAtHandoff(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	env.f.setItems(
		proto.Item{ID: "prompt", Seq: 1, Kind: proto.KindText, Role: "user", Text: "Start"},
		proto.Item{ID: "report", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "report-call",
			ToolName: "report_to_supervisor", ToolInput: workerMemoryReportInput(), TS: turnEnd})
	reads := 0
	env.s.runWorkerOutcome = func(run func()) {
		env.f.mu.Lock()
		for _, call := range env.f.calls {
			if call.Method == DeviceSessionGet {
				reads++
			}
		}
		env.f.mu.Unlock()
		run()
	}
	env.s.goRecordWorkerReport(context.Background(), env.todo, "box", "s1", workerMemoryConclusion,
		[]string{workerMemoryPR, workerMemoryIssue}, turnEnd)
	env.s.tasks.outcomeRuns.Wait()
	if reads != 0 {
		t.Fatalf("report handoff made %d synchronous session.get call(s), want zero", reads)
	}
	assertOriginalWorkerMemory(t, workerMemoryRecords(t, env.e, "explicit"))
}

// WorkDone uses only evidence accepted by reportToSupervisor; it cannot resurrect a URL clipped or capped out of the
// live report, even though the transcript retains the original JSON.
func TestWorkerReportEvidenceLinksUsesAcceptedEvidence(t *testing.T) {
	input := func(evidence []string) proto.Item {
		raw, _ := json.Marshal(map[string]any{"kind": "done", "text": workerMemoryConclusion, "evidence": evidence})
		return proto.Item{Kind: proto.KindToolUse, Role: "assistant", ToolName: "report_to_supervisor", ToolInput: raw}
	}
	extra := []string{"one", "two", "three", "four", "five", "six", "seven", "eight", workerMemoryPR}
	if got := workerReportEvidenceLinks(input(extra)); len(got) != 0 {
		t.Fatalf("WorkDone retained a capped-out URL: %+v", got)
	}
	accepted := append([]string{workerMemoryPR}, extra[:7]...)
	if got := workerReportEvidenceLinks(input(accepted)); len(got) != 1 || got[0] != workerMemoryPR {
		t.Fatalf("WorkDone lost accepted evidence: %+v", got)
	}
}

// Live evidence has already been trimmed, clipped and capped by reportToSupervisor; apply the same acceptance rules to
// the transcript's original JSON before comparing it, or a valid report can become impossible to find.
func TestWorkerReportIndexNormalizesEvidence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		transcript []string
		live       []string
	}{
		{name: "caps at eight", transcript: []string{"https://example.com/1", "https://example.com/2", "https://example.com/3", "https://example.com/4", "https://example.com/5", "https://example.com/6", "https://example.com/7", "https://example.com/8", "https://example.com/9"},
			live: []string{"https://example.com/1", "https://example.com/2", "https://example.com/3", "https://example.com/4", "https://example.com/5", "https://example.com/6", "https://example.com/7", "https://example.com/8"}},
		{name: "trims and clips", transcript: []string{"  " + strings.Repeat("x", 299) + " https://example.com/after"},
			live: []string{clip(strings.Repeat("x", 299)+" https://example.com/after", 300)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"kind": "done", "text": workerMemoryConclusion, "evidence": tc.transcript})
			items := []proto.Item{
				{ID: "prompt", Seq: 1, Kind: proto.KindText, Role: "user", Text: "Start"},
				{ID: "report", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "report-call",
					ToolName: "report_to_supervisor", ToolInput: raw, TS: turnEnd},
			}
			if got := workerReportIndex(items, workerMemoryConclusion, turnEnd, tc.live); got != 1 {
				t.Fatalf("normalized evidence did not find the accepted report: %d", got)
			}
		})
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

// With triage enabled, a final answer without a committed WorkDone must not discard the already successful done report.
func TestWorkerMemoryReportWritesAfterFinalAnswerWithoutWorkDone(t *testing.T) {
	env := newWorkerMemoryEnv(t)
	env.setProject(t, "explicit")
	env.f.setItems(
		proto.Item{ID: "prompt", Seq: 1, Kind: proto.KindText, Role: "user", Text: "Start"},
		proto.Item{ID: "report", Seq: 2, Kind: proto.KindToolUse, Role: "assistant", ToolUseID: "report-call",
			ToolName: "report_to_supervisor", ToolInput: workerMemoryReportInput()},
		proto.Item{ID: "report-result", Seq: 3, Kind: proto.KindToolResult, Role: "tool", ToolUseID: "report-call"},
		proto.Item{ID: "answer", Seq: 4, Kind: proto.KindText, Role: "assistant", Text: workerMemoryConclusion})
	env.reportDone(t, turnEnd)
	assertOriginalWorkerMemory(t, workerMemoryRecords(t, env.e, "explicit"))
}
