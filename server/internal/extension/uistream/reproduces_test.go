package uistream

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Every delta Diff returns passes the check it no longer repeats: replayed by
// Apply, which checks the whole result, it gives the target's frame.
func TestDiffDeltasPassApplysCheck(t *testing.T) {
	for seed := int64(0); seed < 200; seed++ {
		a, b := randomTree(t, seed, 1), randomTree(t, seed+211, 2)
		d, _, err := Diff(a, b)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		got, err := Apply(a, d)
		if err != nil {
			t.Fatalf("seed %d: Apply rejects Diff's delta: %v", seed, err)
		}
		assertSame(t, got, b)
	}
}

// A replay that is not the target is refused, never taken for it.
func TestReproducesRefusesAnotherTarget(t *testing.T) {
	from := snapshot(t, benchView(2, 3, ""), 1)
	to := snapshot(t, benchView(2, 3, "s0-i1"), 2)
	other := snapshot(t, benchView(2, 3, "s1-i2"), 2)
	d, _, err := Diff(from, other)
	if err != nil {
		t.Fatal(err)
	}
	frame, _ := to.MarshalJSON()
	if err := reproduces(from, d, to, frame); err == nil || !strings.Contains(err.Error(), "did not reproduce") {
		t.Fatalf("a delta to another target: %v", err)
	}
	right, _, err := Diff(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if err := reproduces(from, right, to, frame); err != nil {
		t.Fatalf("Diff's own delta: %v", err)
	}
}

// A replay whose frame differs from the target's is checked in full: one that
// only the whole-tree check refuses (the composite frame bound) is rejected.
func TestReproducesChecksADifferentFrame(t *testing.T) {
	text := strings.Repeat("x", 190000)
	items := make([]any, 4)
	ops := make([]string, 4)
	for i := range items {
		items[i] = map[string]any{"type": "code", "key": fmt.Sprintf("a%d", i), "text": text}
		ops[i] = string(mustJSON(operation("insert", "parent", "s", "item", mustJSON(map[string]any{"type": "code", "key": fmt.Sprintf("b%d", i), "text": text}))))
	}
	a := snapshot(t, string(mustJSON(map[string]any{"sections": []any{map[string]any{"key": "s", "items": items}}})), 1)
	d := delta(t, "["+strings.Join(ops, ",")+"]")
	frame, _ := a.MarshalJSON()
	if err := reproduces(a, d, a, frame); !errors.Is(err, ErrSnapshotRequired) {
		t.Fatalf("an oversized replay: %v", err)
	}
}

// A target whose frame differs only outside the view still matches through
// the full check, as before.
func TestReproducesAcceptsTheSameViewUnderAnotherHeader(t *testing.T) {
	from := snapshot(t, benchView(2, 3, ""), 1)
	to := snapshot(t, benchView(2, 3, "s0-i1"), 2)
	d, _, err := Diff(from, to)
	if err != nil {
		t.Fatal(err)
	}
	twin := &Tree{header: to.header, root: to.root}
	twin.header.Protocol++
	frame, _ := twin.MarshalJSON()
	if err := reproduces(from, d, twin, frame); err != nil {
		t.Fatalf("same view, another header: %v", err)
	}
}
