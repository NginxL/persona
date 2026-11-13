package uistream

import (
	"fmt"
	"strings"
	"testing"
)

// benchView builds a page of sections × items, the shape an extension page
// re-renders; changed picks the item whose label carries the new value.
func benchView(sections, items int, changed string) string {
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
			fmt.Fprintf(&b, `{"type":"progress","key":%q,"label":%q,"ratio":0.5}`, key, label)
		}
		b.WriteString(`]}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

// BenchmarkRenderDiff mirrors extmgr's pagePrepare: decode the next snapshot,
// then diff it against the current tree (which also replays the delta).
func BenchmarkRenderDiff(b *testing.B) {
	for _, size := range []struct{ sections, items int }{{4, 8}, {10, 25}} {
		b.Run(fmt.Sprintf("%dx%d", size.sections, size.items), func(b *testing.B) {
			from := snapshot(b, benchView(size.sections, size.items, ""), 1)
			raw := []byte(fmt.Sprintf(`{"protocol":1,"session":"session-a","epoch":"epoch-a","version":2,"root_key":"page","view":%s}`,
				benchView(size.sections, size.items, "s1-i3")))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				to, err := DecodeSnapshot(raw)
				if err != nil {
					b.Fatal(err)
				}
				d, _, err := Diff(from, to)
				if err != nil || d == nil || len(d.ops) != 1 {
					b.Fatalf("diff: %v %v", d, err)
				}
			}
		})
	}
}

// BenchmarkDiff is Diff alone, both trees already decoded: the part of a render
// that finds and replays the delta.
func BenchmarkDiff(b *testing.B) {
	for _, size := range []struct{ sections, items int }{{4, 8}, {10, 25}} {
		b.Run(fmt.Sprintf("%dx%d", size.sections, size.items), func(b *testing.B) {
			from := snapshot(b, benchView(size.sections, size.items, ""), 1)
			to := snapshot(b, benchView(size.sections, size.items, "s1-i3"), 2)
			b.ReportAllocs()
			for b.Loop() {
				if d, _, err := Diff(from, to); err != nil || d == nil || len(d.ops) != 1 {
					b.Fatalf("diff: %v %v", d, err)
				}
			}
		})
	}
}
