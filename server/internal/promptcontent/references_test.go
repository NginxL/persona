package promptcontent

import (
	"strings"
	"testing"
)

func TestFileReferencesOnlyUseLeadingTypedSelections(t *testing.T) {
	block := `<persona-context kind="file" path="notes &amp; more.txt" machine="box" cwd="/work" launcher="claude" runtime="">Display</persona-context>`
	refs, err := Files(block + "\nRead it")
	if err != nil || len(refs) != 1 || refs[0].Path != "notes & more.txt" {
		t.Fatalf("refs: %+v %v", refs, err)
	}
	if prefix, body := Split(block + "\nRead it"); prefix != block || body != "\nRead it" {
		t.Fatal("changed envelope")
	}
	for _, text := range []string{"Literal @notes", "Quoted: " + block} {
		refs, err := Files(text)
		if err != nil || len(refs) != 0 {
			t.Fatal("invented a selection")
		}
	}
	for _, text := range []string{strings.Replace(block, `machine="box"`, "", 1), strings.Replace(block, `path="notes &amp; more.txt"`, `path="one" path="two"`, 1), strings.Replace(block, `runtime=""`, `unknown="x"`, 1), strings.Repeat(block, 17)} {
		if _, err := Files(text); err == nil {
			t.Fatal("accepted invalid reference")
		}
	}
}
