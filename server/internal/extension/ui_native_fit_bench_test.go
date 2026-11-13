package extension

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

// nativeView is a page of sections × items in the strict native vocabulary a
// page source sends on every render.
func nativeView(sections, items int) []byte {
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
			fmt.Fprintf(&b, `{"type":"progress","key":"s%d-i%d","label":"Count %d","ratio":0.5}`, s, i, i)
		}
		b.WriteString(`]}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func BenchmarkFitNative(b *testing.B) {
	slot := UISlots[0]
	for _, size := range []struct{ sections, items int }{{4, 8}, {10, 25}} {
		raw := nativeView(size.sections, size.items)
		b.Run(fmt.Sprintf("%dx%d", size.sections, size.items), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := FitNative(raw, slot, UIVersion, i18n.Lang("en")); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
