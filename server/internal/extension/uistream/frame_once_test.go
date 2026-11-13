package uistream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The tree's own frame is validated again whenever it is not the raw frame
// DecodeSnapshot validated: here raw fits the frame bound, but the frame the
// tree encodes escapes each "<" as < and no longer does.
func TestDecodeSnapshotValidatesADifferentFrame(t *testing.T) {
	text := strings.Repeat("<", 100000) // each "<" encodes as six bytes
	raw := []byte(fmt.Sprintf(`{"protocol":1,"session":"session-a","epoch":"epoch-a","version":1,"root_key":"page","view":{"sections":[{"key":"s","items":[{"type":"code","key":"c","text":%q},{"type":"code","key":"d","text":%q}]}]}}`, text, text))
	raw = bytes.ReplaceAll(raw, []byte(`<`), []byte("<"))
	if len(raw) > FrameMost {
		t.Fatalf("raw frame %d bytes is over the bound itself", len(raw))
	}
	if err := validateRaw("UIStreamSnapshot", raw, FrameMost); err != nil {
		t.Fatalf("raw frame: %v", err)
	}
	if _, err := DecodeSnapshot(raw); !errors.Is(err, ErrSnapshotRequired) {
		t.Fatalf("a tree whose own frame exceeds the bound: %v", err)
	}
}

// A frame encoded the way the manager builds it (json.Marshal of a map) is the
// tree's own frame byte for byte, so it is validated once.
func TestManagerFrameIsTheTreesFrame(t *testing.T) {
	raw := managerFrame(t, benchView(3, 4, ""))
	tree, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	frame, _ := tree.MarshalJSON()
	if !bytes.Equal(frame, raw) {
		t.Fatalf("frames differ\n%s\n%s", raw, frame)
	}
}

func managerFrame(t testing.TB, view string) []byte {
	t.Helper()
	return managerFrameAt(t, view, 1)
}

func managerFrameAt(t testing.TB, view string, version int64) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(view), &v); err != nil {
		t.Fatal(err)
	}
	fitted, _ := json.Marshal(v)
	raw, err := json.Marshal(map[string]any{"protocol": 1, "session": "session-a", "epoch": "epoch-a", "version": version,
		"root_key": "page", "view": json.RawMessage(fitted)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// BenchmarkDecodeSnapshot decodes a page frame as the manager builds it.
func BenchmarkDecodeSnapshot(b *testing.B) {
	for _, size := range []struct{ sections, items int }{{4, 8}, {10, 25}} {
		b.Run(fmt.Sprintf("%dx%d", size.sections, size.items), func(b *testing.B) {
			raw := managerFrame(b, benchView(size.sections, size.items, ""))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := DecodeSnapshot(raw); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
