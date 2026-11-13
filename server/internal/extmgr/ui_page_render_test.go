package extmgr

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/uistream"
)

// renderView is a native page of sections × items; changed gets another label.
func renderView(sections, items int, changed string) json.RawMessage {
	var b strings.Builder
	b.WriteString(`{"title":"Board","sections":[`)
	for s := 0; s < sections; s++ {
		if s > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"key":"s%d","items":[`, s)
		for i := 0; i < items; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			key := fmt.Sprintf("s%d-i%d", s, i)
			label := "Count " + key
			if key == changed {
				label += " (updated)"
			}
			// Not in sorted key order, as an extension writes it.
			fmt.Fprintf(&b, `{"type":"progress","key":%q,"label":%q,"ratio":0.5}`, key, label)
		}
		b.WriteString(`]}`)
	}
	b.WriteString(`]}`)
	return json.RawMessage(b.String())
}

func renderState(tree *uistream.Tree, view json.RawMessage, rootKey string) pageRenderState {
	return pageRenderState{id: "page-session", epoch: "epoch-1", tree: tree, slot: extension.UISlots[0],
		params: extension.UIParams{UIVersion: extension.UIVersion, Locale: "en"},
		native: pageNative{Generation: "g1", Version: 1, RootKey: rootKey, View: view}}
}

// The same page drawn again keeps the current tree and sends nothing; a fence,
// a changed page or another root still go through decoding and the diff.
func TestPagePrepareKeepsTheTreeForTheSamePage(t *testing.T) {
	h := &Manager{}
	first, frame, err := h.pagePrepare(renderState(nil, renderView(2, 3, ""), "page"), false, "")
	if err != nil || frame != nil || first == nil {
		t.Fatalf("first render: %v %s %v", first, frame, err)
	}
	again, frame, err := h.pagePrepare(renderState(first, renderView(2, 3, ""), "page"), false, "r1")
	if err != nil || frame != nil {
		t.Fatalf("the same page again: %s %v", frame, err)
	}
	if again != first {
		t.Fatal("the same page again decoded a new tree")
	}
	fenced, frame, err := h.pagePrepare(renderState(first, renderView(2, 3, ""), "page"), true, "r2")
	if err != nil || fenced == first || fenced.Version() != first.Version()+1 || !strings.Contains(string(frame), `"kind":"delta"`) {
		t.Fatalf("a fence: version %d, frame %s, %v", fenced.Version(), frame, err)
	}
	changed, frame, err := h.pagePrepare(renderState(first, renderView(2, 3, "s1-i2"), "page"), false, "r3")
	if err != nil || changed == first || changed.Version() != first.Version()+1 || !strings.Contains(string(frame), `(updated)`) {
		t.Fatalf("a changed page: %s %v", frame, err)
	}
	if _, _, err := h.pagePrepare(renderState(first, renderView(2, 3, ""), "other-root"), false, "r4"); err == nil {
		t.Fatal("the same view under another root key kept the tree")
	}
}

// BenchmarkPagePrepareSamePage draws an unchanged page again, as a page
// source does on every refresh that changes nothing.
func BenchmarkPagePrepareSamePage(b *testing.B) {
	for _, size := range []struct{ sections, items int }{{4, 8}, {10, 25}} {
		b.Run(fmt.Sprintf("%dx%d", size.sections, size.items), func(b *testing.B) {
			h := &Manager{}
			view := renderView(size.sections, size.items, "")
			tree, _, err := h.pagePrepare(renderState(nil, view, "page"), false, "")
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if next, frame, err := h.pagePrepare(renderState(tree, view, "page"), false, ""); err != nil || frame != nil || next.Version() != tree.Version() {
					b.Fatal(err)
				}
			}
		})
	}
}
