package chatexport

import (
	"reflect"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestVisibleItemsDoesNotMutateInputSlice(t *testing.T) {
	narration := fixtureItem("a1", 4, 4, "assistant", proto.KindText, "checking files")
	narration.Narration = true
	items := []proto.Item{
		fixtureItem("u1", 1, 1, "user", proto.KindText, "Question"),
		fixtureItem("t1", 2, 2, "assistant", proto.KindThinking, "reasoning"),
		fixtureItem("r1", 3, 3, "assistant", proto.KindToolResult, "tool output"),
		narration,
		fixtureItem("f1", 5, 5, "assistant", proto.KindFold, ""),
		fixtureItem("a2", 6, 6, "assistant", proto.KindText, "Final answer"),
	}
	orig := slices.Clone(items)

	got := visibleItems(items, false, true)
	if len(got) != 2 || got[0].ID != "u1" || got[1].ID != "a2" {
		t.Fatalf("visibleItems = %+v, want [u1 a2]", got)
	}
	if !reflect.DeepEqual(items, orig) {
		t.Fatalf("visibleItems mutated caller slice:\n got %+v\nwant %+v", items, orig)
	}
}
