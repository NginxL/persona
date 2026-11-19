package kb

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
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
