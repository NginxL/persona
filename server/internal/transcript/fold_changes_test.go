package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The cases the web reads too (web/src/views/chat/TurnChanges.test.tsx): a folded run counts its edits exactly as the
// chat counts the same calls unfolded, so the changed-files card says the same before and after a reload.
func TestFoldChangesSharedCases(t *testing.T) {
	raw, err := os.ReadFile("../../../spec/fixtures/fold-changes/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []struct {
			Name  string `json:"name"`
			Input struct {
				Items []proto.Item `json:"items"`
			} `json:"input"`
			Expected struct {
				Changes []proto.FoldChange `json:"changes"`
			} `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range doc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := foldChanges(c.Input.Items)
			if got == nil {
				t.Fatal("nil: a fold always carries its list")
			}
			if !reflect.DeepEqual(got, c.Expected.Changes) {
				t.Fatalf("got %+v, want %+v", got, c.Expected.Changes)
			}
		})
	}
}

// A fold carries what its run's edits changed; one whose run edited nothing still says so with an empty list, which an
// older node's fold (no list) does not.
func TestFoldWorkCarriesChanges(t *testing.T) {
	seq := 0
	call := func(name string, input string) []proto.Item {
		seq++
		id := fmt.Sprintf("c%d", seq)
		return []proto.Item{
			{ID: id + "u#0", Seq: seq, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: id, ToolName: name, ToolInput: json.RawMessage(input)},
			{ID: id + "r#0", Seq: seq, Role: "user", Kind: proto.KindToolResult, ToolUseID: id, Text: "ok"},
		}
	}
	text := func(s string) proto.Item {
		seq++
		return proto.Item{ID: fmt.Sprintf("t%d#0", seq), Seq: seq, Role: "assistant", Kind: proto.KindText, Text: s}
	}
	var page []proto.Item
	page = append(page, text("start"))
	page = append(page, call("Read", `{"file_path":"/w/a.go"}`)...)
	page = append(page, call("Edit", `{"file_path":"/w/a.go","old_string":"a\nb","new_string":"a\nc\nd"}`)...)
	page = append(page, text("then the checks"))
	page = append(page, call("Bash", `{"command":"go vet"}`)...)
	page = append(page, call("Bash", `{"command":"go test"}`)...)
	page = append(page, text("done"))
	folded := FoldWork(page)
	var folds []*proto.Fold
	for _, it := range folded {
		if it.Kind == proto.KindFold {
			folds = append(folds, it.Fold)
		}
	}
	if len(folds) != 2 {
		t.Fatalf("folds: %d, want 2: %+v", len(folds), folded)
	}
	if want := []proto.FoldChange{{Path: "/w/a.go", Add: 2, Del: 1, Exact: true}}; !reflect.DeepEqual(folds[0].Changes, want) {
		t.Fatalf("first fold: %+v, want %+v", folds[0].Changes, want)
	}
	b, _ := json.Marshal(folds[1])
	if !strings.Contains(string(b), `"changes":[]`) {
		t.Fatalf("a fold with no edits says so: %s", b)
	}
}

// Past the apps' line diff limits (web/src/lib/compare.ts diffLines) an edit counts as all of the old text removed and
// all of the new added, as the chat draws it there.
func TestLineChangesGiveUpLikeTheApps(t *testing.T) {
	lines := func(prefix string, n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("%s%d", prefix, i)
		}
		return strings.Join(out, "\n")
	}
	common := lines("same", 100)
	if add, del := lineChanges(common+"\n"+lines("old", 10), common+"\n"+lines("new", 12)); add != 12 || del != 10 {
		t.Fatalf("a small edit: +%d -%d, want +12 -10", add, del)
	}
	// 6,002 edits: more than the diff works out
	if add, del := lineChanges(common+"\n"+lines("old", 3001), common+"\n"+lines("new", 3001)); add != 3101 || del != 3101 {
		t.Fatalf("too many edits: +%d -%d, want everything: +3101 -3101", add, del)
	}
	// more than 60,000 lines in all
	if add, del := lineChanges(lines("x", 30000), lines("x", 30001)); add != 30001 || del != 30000 {
		t.Fatalf("too many lines: +%d -%d, want everything: +30001 -30000", add, del)
	}
	if add, del := lineChanges("", ""); add != 0 || del != 0 {
		t.Fatalf("nothing: +%d -%d", add, del)
	}
}

// An input clipJSON cut for good is known by clipParts' own note (clipNoteFormat), wherever the clipped string sits;
// the words in the middle of a text are not the note.
func TestClippedFollowsClipJSON(t *testing.T) {
	long := strings.Repeat("a line of a big new file\n", 4000)
	raw, _ := json.Marshal(map[string]any{"file_path": "/w/big.txt", "edits": []any{map[string]any{"new_string": long}}})
	var in map[string]any
	if err := json.Unmarshal(clipJSON(raw), &in); err != nil {
		t.Fatal(err)
	}
	if !clipped(in) {
		t.Fatal("clipJSON's note not recognized")
	}
	if !clipped(map[string]any{"content": "prefix" + ApproximateClipNote(12)}) {
		t.Fatal("the approximate clip note was not recognized")
	}
	if clipped(map[string]any{"content": "said: \n…（已截断，共 12 字节）, then more"}) || clipped(map[string]any{"content": long}) {
		t.Fatal("an unclipped text read as clipped")
	}
}

func TestStructuralClipMakesFoldChangesInexact(t *testing.T) {
	edits := make([]any, 2000)
	for i := range edits {
		edits[i] = map[string]any{"old_string": "a", "new_string": "b"}
	}
	raw, _ := json.Marshal(map[string]any{"file_path": "/home/user/project/a.go", "edits": edits})
	use := proto.Item{ID: "use#0", Kind: proto.KindToolUse, ToolUseID: "call", ToolName: "MultiEdit", ToolInput: clipJSON(raw)}
	result := proto.Item{ID: "result#0", Kind: proto.KindToolResult, ToolUseID: "call", Text: "ok"}
	changes := foldChanges([]proto.Item{use, result})
	if len(changes) != 1 || changes[0].Exact || changes[0].Add == 0 || changes[0].Del == 0 {
		t.Fatalf("structurally clipped changes = %+v, want nonzero inexact counts", changes)
	}
}
