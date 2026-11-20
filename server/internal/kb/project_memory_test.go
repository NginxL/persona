package kb

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

func projectMemoryEngine(t *testing.T, dir string) *Engine {
	t.Helper()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{Store: st}
}

func putMemoryProject(t *testing.T, e *Engine, slug string) {
	t.Helper()
	if err := e.Store.PutProject(&Project{Slug: slug, Name: slug, Status: "active"}); err != nil {
		t.Fatal(err)
	}
}

// readProjectMemoryFile reads the durable JSONL itself, so the assertions do not rely on a read API hiding duplicate
// lines or not writing a field through.
func readProjectMemoryFile(t *testing.T, e *Engine, slug string) []ProjectMemoryRecord {
	t.Helper()
	raw, err := os.ReadFile(e.Store.Path(KindProject, slug, "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || !strings.HasSuffix(string(raw), "\n") {
		t.Fatalf("memory.jsonl is not newline-terminated JSONL: %q", raw)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	out := make([]ProjectMemoryRecord, len(lines))
	for i, line := range lines {
		if err := json.Unmarshal([]byte(line), &out[i]); err != nil {
			t.Fatalf("memory.jsonl line %d is not a record: %v; %q", i+1, err, line)
		}
	}
	return out
}

// A worker outcome is appended whole to its owning project's JSONL file, including the original conclusion and links;
// another project's outcome neither changes that file nor appears in its reads.
func TestProjectMemoryAppend(t *testing.T) {
	e := projectMemoryEngine(t, t.TempDir())
	putMemoryProject(t, e, "persona")
	putMemoryProject(t, e, "other")
	record := ProjectMemoryRecord{
		ID:         "todo-7/worker-2/turn-3/item-1",
		TodoID:     "todo-7",
		Agent:      "worker-2",
		Turn:       3,
		ItemIndex:  1,
		Conclusion: "Fixed the retry loop.",
		Links:      []string{"https://github.com/o/r/pull/42", "https://github.com/o/r/issues/10219"},
		Decision:   "keep",
		Reason:     "The original conclusion still describes the result.",
	}
	if err := e.AppendProjectMemory("persona", record); err != nil {
		t.Fatal(err)
	}
	if got := e.ProjectMemory("persona", 10); !reflect.DeepEqual(got, []ProjectMemoryRecord{record}) {
		t.Fatalf("ProjectMemory = %+v, want %+v", got, record)
	}
	if got := readProjectMemoryFile(t, e, "persona"); !reflect.DeepEqual(got, []ProjectMemoryRecord{record}) {
		t.Fatalf("memory.jsonl = %+v, want one complete record %+v", got, record)
	}

	other := ProjectMemoryRecord{ID: "todo-8/worker-1/turn-1/item-0", TodoID: "todo-8", Agent: "worker-1", Turn: 1, Conclusion: "Updated docs."}
	if err := e.AppendProjectMemory("other", other); err != nil {
		t.Fatal(err)
	}
	if got := e.ProjectMemory("other", 10); !reflect.DeepEqual(got, []ProjectMemoryRecord{other}) {
		t.Fatalf("other ProjectMemory = %+v, want %+v", got, other)
	}
	if got := readProjectMemoryFile(t, e, "persona"); !reflect.DeepEqual(got, []ProjectMemoryRecord{record}) {
		t.Fatalf("owning project's memory changed: %+v", got)
	}
}

// Repeating an outcome is a no-op both in one Engine and after a new Store and Engine have only the existing disk file
// to dedupe against. The file, not just the bounded view, retains one physical record.
func TestProjectMemoryDeduplicatesAcrossReopenedEngine(t *testing.T) {
	dir := t.TempDir()
	e := projectMemoryEngine(t, dir)
	putMemoryProject(t, e, "persona")
	record := ProjectMemoryRecord{
		ID: "todo-7/worker-2/turn-3/item-1", TodoID: "todo-7", Agent: "worker-2", Turn: 3, ItemIndex: 1,
		Conclusion: "Fixed the retry loop.", Links: []string{"https://github.com/o/r/pull/42"},
	}
	for range 2 {
		if err := e.AppendProjectMemory("persona", record); err != nil {
			t.Fatal(err)
		}
	}

	reopened := projectMemoryEngine(t, dir)
	replayed := record
	replayed.Conclusion = "A replay must not replace or duplicate the recorded ID."
	if err := reopened.AppendProjectMemory("persona", replayed); err != nil {
		t.Fatal(err)
	}
	if got := readProjectMemoryFile(t, reopened, "persona"); !reflect.DeepEqual(got, []ProjectMemoryRecord{record}) {
		t.Fatalf("memory.jsonl after replay and reopen = %+v, want one record", got)
	}
	if got := reopened.ProjectMemory("persona", 10); !reflect.DeepEqual(got, []ProjectMemoryRecord{record}) {
		t.Fatalf("ProjectMemory after replay and reopen = %+v, want one record", got)
	}
}

// Reads return the latest outcomes newest first, never more than the requested bound; a non-positive bound is empty and
// a bound beyond the file's length returns every record.
func TestProjectMemoryBoundedRead(t *testing.T) {
	e := projectMemoryEngine(t, t.TempDir())
	putMemoryProject(t, e, "persona")
	for i, id := range []string{"outcome-0", "outcome-1", "outcome-2", "outcome-3"} {
		record := ProjectMemoryRecord{ID: id, TodoID: "todo-7", Agent: "worker-2", Turn: 3, ItemIndex: i, Conclusion: id}
		if err := e.AppendProjectMemory("persona", record); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		limit int
		want  []string
	}{
		{limit: -1},
		{limit: 0},
		{limit: 1, want: []string{"outcome-3"}},
		{limit: 2, want: []string{"outcome-3", "outcome-2"}},
		{limit: 4, want: []string{"outcome-3", "outcome-2", "outcome-1", "outcome-0"}},
		{limit: 10, want: []string{"outcome-3", "outcome-2", "outcome-1", "outcome-0"}},
	} {
		got := e.ProjectMemory("persona", tc.limit)
		ids := make([]string, len(got))
		for i, record := range got {
			ids[i] = record.ID
		}
		if !slices.Equal(ids, tc.want) {
			t.Errorf("ProjectMemory(limit=%d) ids = %v, want %v", tc.limit, ids, tc.want)
		}
		if tc.limit >= 0 && len(got) > tc.limit {
			t.Errorf("ProjectMemory(limit=%d) returned %d records", tc.limit, len(got))
		}
	}
}

func TestProjectMemoryForDispatchSelectsEachKindAndKeepsConflicts(t *testing.T) {
	e := projectMemoryEngine(t, t.TempDir())
	putMemoryProject(t, e, "persona")
	append := func(record ProjectMemoryRecord) {
		t.Helper()
		if err := e.AppendProjectMemory("persona", record); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 6; i++ {
		append(ProjectMemoryRecord{ID: fmt.Sprintf("outcome-%d", i), Type: ProjectMemoryOutcome, Conclusion: fmt.Sprintf("result-%d", i)})
	}
	for i := 0; i < 7; i++ {
		record := ProjectMemoryRecord{ID: fmt.Sprintf("decision-%d", i), Type: ProjectMemoryDecision,
			Question: "shared question", Decision: fmt.Sprintf("choice-%d", i), Reason: fmt.Sprintf("reason-%d", i),
			RecordedAt: fmt.Sprintf("2026-11-20T04:0%d:00Z", i)}
		if i == 6 {
			record.SupersedesID = "decision-0"
		}
		append(record)
	}
	for i := 0; i < 4; i++ {
		append(ProjectMemoryRecord{ID: fmt.Sprintf("pitfall-%d", i), Type: ProjectMemoryPitfall, Conclusion: fmt.Sprintf("pit-%d", i)})
	}
	got, ok := e.ProjectMemoryForDispatch("persona", 5, 5, 3)
	if !ok {
		t.Fatal("usable memory was not recognized")
	}
	ids := func(records []ProjectMemoryRecord) []string {
		out := make([]string, len(records))
		for i, record := range records {
			out[i] = record.ID
		}
		return out
	}
	if !slices.Equal(ids(got.Outcomes), []string{"outcome-5", "outcome-4", "outcome-3", "outcome-2", "outcome-1"}) {
		t.Fatalf("outcomes = %v", ids(got.Outcomes))
	}
	if !slices.Equal(ids(got.Decisions), []string{"decision-6", "decision-5", "decision-4", "decision-3", "decision-2"}) {
		t.Fatalf("effective decisions = %v", ids(got.Decisions))
	}
	if got.Decisions[0].Question != got.Decisions[1].Question || got.Decisions[0].Decision == got.Decisions[1].Decision ||
		got.Decisions[0].RecordedAt == "" || got.Decisions[1].RecordedAt == "" {
		t.Fatalf("conflicting distinct IDs or their timestamps were merged: %+v", got.Decisions[:2])
	}
	if !slices.Equal(ids(got.Pitfalls), []string{"pitfall-3", "pitfall-2", "pitfall-1"}) {
		t.Fatalf("pitfalls = %v", ids(got.Pitfalls))
	}
}

func TestProjectMemoryForDispatchReadsLegacyDecision(t *testing.T) {
	e := projectMemoryEngine(t, t.TempDir())
	putMemoryProject(t, e, "persona")
	legacy := ProjectMemoryRecord{ID: "m1", Conclusion: "finished", Decision: "keep the cap", Reason: "bounded input"}
	if err := e.AppendProjectMemory("persona", legacy); err != nil {
		t.Fatal(err)
	}
	got, ok := e.ProjectMemoryForDispatch("persona", 5, 5, 3)
	if !ok || len(got.Outcomes) != 1 || len(got.Decisions) != 1 || got.Decisions[0].ID != "m1:legacy-decision" ||
		got.Decisions[0].Decision != "keep the cap" || got.Decisions[0].RecordedAt != "" {
		t.Fatalf("legacy projection = %+v, %v", got, ok)
	}
	if _, ok := e.ProjectMemoryForDispatch("missing", 5, 5, 3); ok {
		t.Fatal("a project without memory.jsonl must use the legacy prompt")
	}
}

func TestProjectSummaryIsRuneBounded(t *testing.T) {
	e := projectMemoryEngine(t, t.TempDir())
	putMemoryProject(t, e, "persona")
	path := e.Store.Path(KindProject, "persona", "summary.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("界", 8)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := e.Store.ProjectSummary("persona", 5); got != "界界界界…" || len([]rune(got)) != 5 {
		t.Fatalf("bounded summary = %q (%d runes)", got, len([]rune(got)))
	}
	if err := os.WriteFile(path, []byte("  short summary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := e.Store.ProjectSummary("persona", 100); got != "short summary" {
		t.Fatalf("short summary = %q", got)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", 20)+"meaningful"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := e.Store.ProjectSummary("persona", 5); got != "mean…" {
		t.Fatalf("leading whitespace hid or shortened the summary: %q", got)
	}
	if got := e.Store.ProjectSummary("missing", 100); got != "" {
		t.Fatalf("missing summary = %q", got)
	}
}

func TestProjectMemoryBatchDoesNotSpliceConcurrentExtractions(t *testing.T) {
	for i := 0; i < 20; i++ {
		e := projectMemoryEngine(t, t.TempDir())
		putMemoryProject(t, e, "persona")
		base := "todo/worker/turn/item"
		a := []ProjectMemoryRecord{
			{ID: base + "/decision/1", Type: ProjectMemoryDecision, Decision: "A1"},
			{ID: base + "/decision/2", Type: ProjectMemoryDecision, Decision: "A2"},
			{ID: base, Type: ProjectMemoryOutcome, Conclusion: "outcome A"},
		}
		b := []ProjectMemoryRecord{
			{ID: base + "/decision/1", Type: ProjectMemoryDecision, Decision: "B1"},
			{ID: base + "/pitfall/1", Type: ProjectMemoryPitfall, Conclusion: "pitfall B"},
			{ID: base, Type: ProjectMemoryOutcome, Conclusion: "outcome B"},
		}
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, batch := range [][]ProjectMemoryRecord{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs <- e.AppendProjectMemoryBatch("persona", batch)
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		records := e.ProjectMemory("persona", 10)
		if len(records) != 3 {
			t.Fatalf("iteration %d spliced concurrent batches: %+v", i, records)
		}
		switch records[0].Conclusion {
		case "outcome A":
			if records[1].Decision != "A2" || records[2].Decision != "A1" {
				t.Fatalf("iteration %d mixed A with another extraction: %+v", i, records)
			}
		case "outcome B":
			if records[1].Conclusion != "pitfall B" || records[2].Decision != "B1" {
				t.Fatalf("iteration %d mixed B with another extraction: %+v", i, records)
			}
		default:
			t.Fatalf("iteration %d has no complete outcome: %+v", i, records)
		}
	}
}

func TestProjectMemoryBatchRepairsTornTailBeforeRetry(t *testing.T) {
	e := projectMemoryEngine(t, t.TempDir())
	putMemoryProject(t, e, "persona")
	base := "todo/worker/turn/item"
	first := ProjectMemoryRecord{ID: base + "/decision/1", Type: ProjectMemoryDecision, Decision: "first"}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	path := e.Store.Path(KindProject, "persona", "memory.jsonl")
	torn := append(append(raw, '\n'), []byte(`{"id":"`+base+`/decision/2"`)...)
	if err := os.WriteFile(path, torn, 0o600); err != nil {
		t.Fatal(err)
	}
	batch := []ProjectMemoryRecord{
		first,
		{ID: base + "/decision/2", Type: ProjectMemoryDecision, Decision: "second"},
		{ID: base, Type: ProjectMemoryOutcome, Conclusion: "complete"},
	}
	if err := e.AppendProjectMemoryBatch("persona", batch); err != nil {
		t.Fatal(err)
	}
	records := e.ProjectMemory("persona", 10)
	if len(records) != 3 || records[0].ID != base || records[1].ID != base+"/decision/2" || records[2].ID != base+"/decision/1" {
		t.Fatalf("torn retry lost a record: %+v", records)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AppendProjectMemoryBatch("persona", batch); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("completed retry appended another batch:\nbefore %q\nafter %q", before, after)
	}
}
