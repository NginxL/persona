package db

import (
	"context"
	"slices"
	"testing"
)

// Information categories (#13246): a tagger only adds; a category the user took away stays away until the user adds
// it back; reads are by ref, removed ones included.
func TestInfoCategories(t *testing.T) {
	d, ctx := open(t), context.Background()
	const conv = InfoCategoryConversation
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	read := func(ref string) []string {
		t.Helper()
		got, err := d.InfoCategories(ctx, conv, []string{ref})
		must(err)
		var out []string
		for _, c := range got[ref] {
			if c.Ref != ref {
				t.Fatalf("ref %q under %q", c.Ref, ref)
			}
			s := c.Category + "/" + c.By
			if c.RemovedByUser {
				s += "/removed"
			}
			out = append(out, s)
		}
		return out
	}
	must(d.AddInfoCategories(ctx, conv, "qa-chat", []string{"finance", "emotion"}, "model", "qa-model"))
	must(d.AddInfoCategories(ctx, conv, "qa-chat", []string{"finance"}, "agent", ""))
	if got, want := read("qa-chat"), []string{"emotion/model", "finance/model"}; !slices.Equal(got, want) {
		t.Fatalf("tagged = %v, want %v", got, want)
	}
	must(d.RemoveInfoCategories(ctx, conv, "qa-chat", []string{"finance", "legal_id"}))
	must(d.AddInfoCategories(ctx, conv, "qa-chat", []string{"finance", "legal_id"}, "model", "qa-model"))
	if got, want := read("qa-chat"), []string{"emotion/model", "finance/user/removed", "legal_id/user/removed"}; !slices.Equal(got, want) {
		t.Fatalf("after the user took two away = %v, want %v", got, want)
	}
	must(d.AddInfoCategories(ctx, conv, "qa-chat", []string{"finance"}, InfoCategoryByUser, ""))
	if got, want := read("qa-chat"), []string{"emotion/model", "finance/user", "legal_id/user/removed"}; !slices.Equal(got, want) {
		t.Fatalf("after the user added one back = %v, want %v", got, want)
	}
	if got := read("qa-other"); len(got) != 0 {
		t.Fatalf("an untagged conversation = %v", got)
	}
	if got, err := d.InfoCategories(ctx, "todo", []string{"qa-chat"}); err != nil || len(got) != 0 {
		t.Fatalf("another kind = %v, %v", got, err)
	}
	must(d.AddInfoCategories(ctx, conv, "qa-chat", nil, "model", ""))
	must(d.RemoveInfoCategories(ctx, conv, "qa-chat", nil))
}
