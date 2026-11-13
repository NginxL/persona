package uistream

import (
	"bytes"
	"testing"
)

// Decodes holds only for a tree DecodeSnapshot made from its own frame, and
// only for that frame's exact bytes.
func TestDecodesOwnFrameOnly(t *testing.T) {
	raw := managerFrame(t, benchView(2, 3, ""))
	tree, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !tree.Decodes(raw) {
		t.Fatal("a tree does not decode from the frame it was decoded from")
	}
	if tree.Decodes(append(bytes.Clone(raw[:len(raw)-1]), ' ', '}')) {
		t.Fatal("a tree decodes from other bytes")
	}
	other := managerFrame(t, benchView(2, 3, "s1-i1"))
	if tree.Decodes(other) {
		t.Fatal("a tree decodes from another page's frame")
	}
	// A frame in another key order decodes to a tree whose own frame differs.
	loose := snapshot(t, benchView(2, 3, ""), 1)
	frame, _ := loose.MarshalJSON()
	if loose.Decodes(frame) {
		t.Fatal("a tree decoded from another encoding claims its own frame")
	}
	// A tree Apply made was never decoded from a frame.
	d, _, err := Diff(tree, mustDecode(t, managerFrameAt(t, benchView(2, 3, "s0-i2"), 2)))
	if err != nil {
		t.Fatal(err)
	}
	applied, err := Apply(tree, d)
	if err != nil {
		t.Fatal(err)
	}
	if frame, _ := applied.MarshalJSON(); applied.Decodes(frame) {
		t.Fatal("an applied tree claims a frame it was not decoded from")
	}
	var none *Tree
	if none.Decodes(raw) {
		t.Fatal("a nil tree decodes")
	}
}

func mustDecode(t testing.TB, raw []byte) *Tree {
	t.Helper()
	tree, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
