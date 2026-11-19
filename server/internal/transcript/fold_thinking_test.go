package transcript

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestFoldLatestThinkingPreservesRangeAndOriginal(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	latest := proto.Item{ID: "latest#1", Seq: 4, TS: ts.Add(4 * time.Second), Role: "assistant", Kind: proto.KindThinking,
		Text: "  Checking the new regression before continuing.  ", FullSize: 900,
		ToolName: "unrelated", ToolUseID: "unrelated", ToolInput: json.RawMessage(`{"payload":"not a preview"}`),
		IsError: true, Image: &proto.Image{Mime: "image/png", Data: "not a preview"},
		Fold: &proto.Fold{Items: 10}, Task: &proto.TaskNote{Title: "not a preview"}}
	run := []proto.Item{
		{ID: "old#0", Seq: 1, TS: ts, Role: "assistant", Kind: proto.KindThinking, Text: "Previous narration."},
		{ID: "call#0", Seq: 2, TS: ts.Add(time.Second), Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "call", ToolName: "Read"},
		{ID: "result#0", Seq: 3, TS: ts.Add(2 * time.Second), Role: "user", Kind: proto.KindToolResult, ToolUseID: "call", Text: "ok"},
		latest,
		{ID: "empty#0", Seq: 5, TS: ts.Add(5 * time.Second), Role: "assistant", Kind: proto.KindThinking},
		{ID: "blank#0", Seq: 6, TS: ts.Add(6 * time.Second), Role: "assistant", Kind: proto.KindThinking, Text: " \n\t "},
	}
	before, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	folded := FoldWork(run)
	if len(folded) != 1 || folded[0].Fold == nil {
		t.Fatalf("expected one fold, got %+v", folded)
	}
	item, fo := folded[0], folded[0].Fold
	if item.ID != "fold:old#0" || item.Seq != 1 || !item.TS.Equal(ts) || fo.Items != len(run) || fo.Tools != 1 || fo.Thoughts != 2 ||
		fo.ThroughSeq != 6 || fo.ThroughID != "blank#0" || !fo.Until.Equal(run[5].TS) {
		t.Fatalf("fold counts or range changed: %+v / %+v", item, fo)
	}
	want := &proto.Item{ID: latest.ID, Seq: latest.Seq, TS: latest.TS, Role: latest.Role, Kind: latest.Kind, Text: latest.Text, FullSize: latest.FullSize}
	if !reflect.DeepEqual(fo.LatestThinking, want) {
		t.Fatalf("latest nonempty thinking: got %+v, want %+v", fo.LatestThinking, want)
	}
	after, err := json.Marshal(run)
	if err != nil || string(after) != string(before) {
		t.Fatalf("folding mutated original items: %s / %s, error %v", before, after, err)
	}
	fo.LatestThinking.Text = "changed preview"
	if run[3].Text != latest.Text {
		t.Fatal("preview aliases the original item")
	}
}

func TestFoldWorkFoldsNarrationWithoutCallingItThinking(t *testing.T) {
	run := []proto.Item{
		{ID: "note#0", Seq: 1, Role: "assistant", Kind: proto.KindText, Text: "The focused tests pass.", Narration: true},
		{ID: "call#0", Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "call", ToolName: "Bash"},
		{ID: "result#0", Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "call", Text: "ok"},
		{ID: "final#0", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "All checks passed."},
	}
	folded := FoldWork(run)
	if len(folded) != 2 || folded[0].Fold == nil || folded[0].Fold.Items != 3 {
		t.Fatalf("commentary stays with its work: %+v", folded)
	}
	if fo := folded[0].Fold; fo.Thoughts != 0 || fo.LatestThinking != nil {
		t.Fatalf("assistant narration is not Reasoning: %+v", fo)
	}
	wire, err := json.Marshal(run[0])
	if err != nil || !strings.Contains(string(wire), `"narration":true`) || !strings.Contains(string(wire), `"kind":"text"`) {
		t.Fatalf("unfolded commentary keeps its text kind and narration marker: %s (%v)", wire, err)
	}
}

func TestFoldLatestThinkingPreviewBound(t *testing.T) {
	for _, tc := range []struct {
		name     string
		text     string
		fullSize int
		wantText string
		wantSize int
	}{
		{name: "short", text: "Checking the result.", wantText: "Checking the result."},
		{name: "exact limit", text: strings.Repeat("x", 512), wantText: strings.Repeat("x", 512)},
		{name: "ascii clipped", text: strings.Repeat("x", 513), wantText: strings.Repeat("x", 511) + "…", wantSize: 513},
		{name: "unicode clipped", text: strings.Repeat("🙂", 513), wantText: strings.Repeat("🙂", 511) + "…", wantSize: 513 * 4},
		{name: "short already clipped", text: "A clipped original.", fullSize: 30000, wantText: "A clipped original.", wantSize: 30000},
		{name: "long already clipped", text: strings.Repeat("界", 513), fullSize: 30000, wantText: strings.Repeat("界", 511) + "…", wantSize: 30000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := []proto.Item{
				{ID: "thought#0", Seq: 1, Role: "assistant", Kind: proto.KindThinking, Text: tc.text, FullSize: tc.fullSize},
				{ID: "empty#0", Seq: 2, Role: "assistant", Kind: proto.KindThinking},
				{ID: "blank#0", Seq: 3, Role: "assistant", Kind: proto.KindThinking, Text: "\n"},
			}
			got := FoldWork(run)[0].Fold.LatestThinking
			if got.Text != tc.wantText || got.FullSize != tc.wantSize || !utf8.ValidString(got.Text) || utf8.RuneCountInString(got.Text) > 512 {
				t.Fatalf("preview: %+v, want text %q / full_size %d", got, tc.wantText, tc.wantSize)
			}
			if run[0].Text != tc.text || run[0].FullSize != tc.fullSize {
				t.Fatal("clipping changed original thinking")
			}
		})
	}
}

func TestFoldWithoutThinkingPreviewOmitsJSONField(t *testing.T) {
	for _, run := range [][]proto.Item{
		{
			{ID: "call#0", Seq: 1, Kind: proto.KindToolUse, ToolUseID: "a"},
			{ID: "result#0", Seq: 2, Kind: proto.KindToolResult, ToolUseID: "a"},
			{ID: "blank#0", Seq: 3, Kind: proto.KindThinking, Text: "\n\t"},
		},
		{
			{ID: "a#0", Seq: 1, Kind: proto.KindThinking},
			{ID: "b#0", Seq: 2, Kind: proto.KindThinking, Text: " "},
			{ID: "c#0", Seq: 3, Kind: proto.KindThinking, Text: "\n"},
		},
	} {
		folded := FoldWork(run)
		if len(folded) != 1 || folded[0].Fold == nil || folded[0].Fold.LatestThinking != nil {
			t.Fatalf("unexpected preview: %+v", folded)
		}
		b, err := json.Marshal(folded[0])
		if err != nil || strings.Contains(string(b), "latest_thinking") {
			t.Fatalf("empty preview serialized: %s, error %v", b, err)
		}
	}
}
