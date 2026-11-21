package conversationlog

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestBoundPreservesNormalizedContentAndBoundsAllFields(t *testing.T) {
	base := proto.Item{ID: "block-1", Seq: 3, Role: "assistant", Kind: proto.KindToolResult, Text: "result"}
	for _, tc := range []struct {
		name     string
		item     proto.Item
		complete bool
	}{
		{"full", base, true},
		{"truncated", func() proto.Item { it := base; it.FullSize = 90000; return it }(), false},
		{"lazy attachment", proto.Item{ID: "image-1", Seq: 4, Kind: proto.KindImage, Image: &proto.Image{Mime: "image/png", Lazy: true}}, false},
		{"large multibyte output", func() proto.Item { it := base; it.Text = strings.Repeat("界", MaxItemBytes); return it }(), false},
		{"large tool input", proto.Item{ID: "tool-1", Seq: 5, Kind: proto.KindToolUse, ToolInput: json.RawMessage(`{"value":"` + strings.Repeat("x", MaxItemBytes) + `"}`)}, false},
		{"large nested metadata", func() proto.Item {
			it := base
			it.Source = &proto.MessageSource{Kind: "conversation", Title: strings.Repeat("x", MaxItemBytes)}
			return it
		}(), false},
		{"escaped previews and metadata", func() proto.Item {
			it := base
			escaped := strings.Repeat("\x00", 512)
			it.Text = strings.Repeat("\x00", MaxItemBytes)
			it.ToolName = strings.Repeat("\x00", 256)
			it.ToolUseID, it.ClientMsgID, it.From = escaped, escaped, escaped
			it.Source = &proto.MessageSource{Kind: "conversation", ConversationID: escaped, Title: escaped}
			it.Injected = &proto.Injected{Kind: "context", Title: escaped, Machine: escaped, TodoID: escaped, Line: it.Text}
			return it
		}(), false},
		{"inline attachment", proto.Item{ID: "image-2", Seq: 6, Kind: proto.KindImage, Image: &proto.Image{Mime: "image/png", Data: strings.Repeat("a", MaxItemBytes)}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, _ := json.Marshal(tc.item)
			got, err := Bound(tc.item)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(got.Item)
			if err != nil || len(raw) > MaxItemBytes || got.Complete != tc.complete || got.Item.ID != tc.item.ID || got.Item.Seq != tc.item.Seq || !utf8.ValidString(got.Item.Text) {
				t.Fatalf("bounded bytes=%d complete=%v reference=%s/%d err=%v", len(raw), got.Complete, got.Item.ID, got.Item.Seq, err)
			}
			after, _ := json.Marshal(tc.item)
			if string(before) != string(after) {
				t.Fatal("changed source item")
			}
			if got.Complete && string(raw) != string(before) {
				t.Fatal("changed a complete item")
			}
		})
	}
}

func TestCoverageDoesNotHideMissingRanges(t *testing.T) {
	ranges := MergeRanges(nil, Range{10, 20})
	ranges = MergeRanges(ranges, Range{0, 5})
	if Covers(ranges, 0, 20) {
		t.Fatal("gap is not covered")
	}
	ranges = MergeRanges(ranges, Range{5, 10})
	if len(ranges) != 1 || !Covers(ranges, 0, 20) {
		t.Fatalf("adjacent ranges: %+v", ranges)
	}
	ranges = MergeRanges(ranges, Range{3, 8})
	if len(ranges) != 1 || Covers(ranges, 0, 21) {
		t.Fatalf("overlap: %+v", ranges)
	}
	if !Covers(MergeRanges(nil, Range{0, 0}), 0, 0) {
		t.Fatal("successful empty history lost")
	}
}
